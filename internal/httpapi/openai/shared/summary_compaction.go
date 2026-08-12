package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sse"
	"DeepSeek_Web_To_API/internal/util"
)

const summaryCompactionMarker = "ds2api_summary_v1"

var ErrSummaryCompactionNotReducible = errors.New("summary compaction is not safely reducible")

const summaryCompactionInstruction = `Compress the conversation into a durable handoff summary for a later model request.
Return only the summary, with no preamble and no markdown fence.
Preserve all active user requirements, system-level constraints mentioned in the conversation, decisions, unresolved work, exact identifiers, file paths, errors, API details, and tool results needed to continue correctly.
Merge any existing compacted summary into the new summary instead of describing or nesting it.
Do not answer the latest user request. Do not invent facts. Keep the result concise and within %d UTF-16 code units.`

type SummaryCompactionStats struct {
	BeforeMessages       int
	AfterMessages        int
	BeforeStateBytes     int
	AfterStateBytes      int
	BeforePromptUnits    int
	AfterPromptUnits     int
	SourcePromptUnits    int
	SummaryUnits         int
	SummaryInputTokens   int
	SummaryOutputTokens  int
	UsedThinkingFallback bool
	RetainedTurns        int
	Attempts             int
	Duration             time.Duration
}

// TrySummaryCompactPrompt performs a real server-side Flash summary. Older
// history and any previous generated summary are replaced by one rolling
// summary while leading client system messages and recent complete user turns
// remain verbatim. The original request is returned on every failure.
func TrySummaryCompactPrompt(ctx context.Context, ds ProFlashCompressionCaller, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings, targetUnits int) (promptcompat.StandardRequest, SummaryCompactionStats, bool, error) {
	stats := SummaryCompactionStats{
		BeforeMessages:    len(req.Messages),
		BeforeStateBytes:  summaryStateSize(req.Messages),
		BeforePromptUnits: promptcompat.PromptUnits(req.FinalPrompt),
	}
	if ds == nil || a == nil {
		return req, stats, false, fmt.Errorf("summary compaction upstream is unavailable")
	}
	if targetUnits <= 0 {
		targetUnits = promptcompat.LimitForModel(cfg, promptcompat.EffectiveModel(req))
	}
	meaningfulTarget := 0
	if stats.BeforePromptUnits >= 4096 {
		meaningfulTarget = stats.BeforePromptUnits * 75 / 100
	}
	if meaningfulTarget > 0 && (targetUnits <= 0 || targetUnits > meaningfulTarget) {
		targetUnits = meaningfulTarget
	}
	if targetUnits <= 0 {
		return req, stats, false, fmt.Errorf("summary compaction target is unavailable")
	}

	keepRecent := cfg.KeepRecentTurns
	if keepRecent < 1 {
		keepRecent = 1
	}
	var permanent, older, recent []any
	var ok bool
	for {
		permanent, older, recent, ok = splitSummaryWindow(req.Messages, cfg.KeepSystemMessage, keepRecent)
		if !ok {
			return req, stats, false, fmt.Errorf("%w: at least two complete user turns are required", ErrSummaryCompactionNotReducible)
		}
		base := append(cloneSummaryMessages(permanent), recent...)
		basePrompt, _ := promptcompat.BuildOpenAIPrompt(base, req.ToolsRaw, "", req.ToolChoice, req.Thinking)
		if promptcompat.PromptUnits(basePrompt) <= targetUnits*3/4 || keepRecent == 1 {
			break
		}
		keepRecent /= 2
		if keepRecent < 1 {
			keepRecent = 1
		}
	}

	base := append(cloneSummaryMessages(permanent), recent...)
	basePrompt, _ := promptcompat.BuildOpenAIPrompt(base, req.ToolsRaw, "", req.ToolChoice, req.Thinking)
	baseUnits := promptcompat.PromptUnits(basePrompt)
	maxSummaryUnits := targetUnits - baseUnits - 64
	if maxSummaryUnits < 32 {
		return req, stats, false, fmt.Errorf("%w: latest retained turn leaves no room for a compact summary", ErrSummaryCompactionNotReducible)
	}
	if maxSummaryUnits > 32768 {
		maxSummaryUnits = 32768
	}

	sourcePrompt, _ := promptcompat.BuildOpenAIPrompt(older, nil, "", promptcompat.DefaultToolChoicePolicy(), false)
	flashLimit := cfg.MaxCharsDefault
	if flashLimit <= 0 {
		flashLimit = promptcompat.LimitForModel(cfg, "deepseek-v4-flash")
	}
	if promptcompat.PromptUnits(sourcePrompt) > flashLimit {
		flashReq := req
		flashReq.RequestedModel = "deepseek-v4-flash"
		flashReq.ResolvedModel = "deepseek-v4-flash"
		flashReq.ResponseModel = "deepseek-v4-flash"
		flashReq.Messages = older
		flashReq.ToolsRaw = nil
		flashReq.ToolNames = nil
		flashReq.Thinking = false
		flashReq.FinalPrompt = sourcePrompt
		flashCfg := cfg
		flashCfg.AutoCompressEnable = true
		flashReq, _ = promptcompat.CompressToFit(flashCfg, flashReq)
		sourcePrompt = flashReq.FinalPrompt
		if promptcompat.PromptUnits(sourcePrompt) > flashLimit {
			return req, stats, false, fmt.Errorf("summary source exceeds the Flash input limit")
		}
	}
	stats.SourcePromptUnits = promptcompat.PromptUnits(sourcePrompt)
	summaryRequirements := fmt.Sprintf(summaryCompactionInstruction, maxSummaryUnits)
	summaryPrompt := "--- CONVERSATION TO COMPACT ---\n" + sourcePrompt +
		"\n\n--- COMPACTION OUTPUT REQUIREMENTS ---\n" + summaryRequirements
	if promptcompat.PromptUnits(summaryPrompt) > flashLimit {
		return req, stats, false, fmt.Errorf("summary instruction and source exceed the Flash input limit")
	}
	stats.SummaryInputTokens = util.CountPromptTokens(summaryPrompt, "deepseek-v4-flash")

	startedAt := time.Now()
	var summary string
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		stats.Attempts = attempt
		sessionID, err := ds.CreateSession(ctx, a, 3)
		if err != nil {
			lastErr = fmt.Errorf("create summary session: %w", err)
			continue
		}
		func() {
			defer AutoDeleteRemoteSession(ctx, ds, "single", a.AccountID, a.DeepSeekToken, sessionID)
			pow, err := ds.GetPow(ctx, a, 3)
			if err != nil {
				lastErr = fmt.Errorf("get summary PoW: %w", err)
				return
			}
			resp, err := ds.CallCompletion(ctx, a, map[string]any{
				"chat_session_id":   sessionID,
				"model_type":        "default",
				"parent_message_id": nil,
				"prompt":            summaryPrompt,
				"ref_file_ids":      []any{},
				"thinking_enabled":  false,
				"search_enabled":    false,
			}, pow, 3)
			if err != nil {
				lastErr = fmt.Errorf("summary completion: %w", err)
				return
			}
			if resp == nil {
				lastErr = fmt.Errorf("summary completion returned no response")
				return
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("summary completion returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
				return
			}
			result := sse.CollectStream(resp, false, true)
			visibleSummary := strings.TrimSpace(result.Text)
			hiddenSummary := strings.TrimSpace(result.ToolDetectionThinking)
			config.Logger.Info("[responses_compact] summary upstream response collected",
				"attempt", attempt,
				"visible_output_units", promptcompat.PromptUnits(visibleSummary),
				"hidden_output_units", promptcompat.PromptUnits(hiddenSummary),
				"response_message_id", result.ResponseMessageID,
				"content_filter", result.ContentFilter)
			if result.ContentFilter {
				lastErr = fmt.Errorf("summary completion was blocked by the upstream content filter")
				return
			}
			summary = visibleSummary
			if summary == "" && hiddenSummary != "" {
				summary = hiddenSummary
				stats.UsedThinkingFallback = true
				config.Logger.Warn("[responses_compact] summary used hidden-output fallback",
					"attempt", attempt,
					"summary_units", promptcompat.PromptUnits(summary),
					"response_message_id", result.ResponseMessageID)
			}
			if summary == "" {
				lastErr = fmt.Errorf("summary completion returned empty output")
			}
		}()
		if summary != "" {
			break
		}
	}
	stats.Duration = time.Since(startedAt)
	if summary == "" {
		return req, stats, false, lastErr
	}
	stats.SummaryUnits = promptcompat.PromptUnits(summary)
	stats.SummaryOutputTokens = util.CountOutputTokens(summary, "deepseek-v4-flash")
	if stats.SummaryUnits > maxSummaryUnits {
		return req, stats, false, fmt.Errorf("summary output exceeds its %d-unit budget", maxSummaryUnits)
	}

	compactedMessages := cloneSummaryMessages(permanent)
	compactedMessages = append(compactedMessages, map[string]any{
		"role":                      "system",
		"content":                   "Compacted conversation summary [" + summaryCompactionMarker + "]:\n" + summary,
		"ds2api_compaction_summary": summaryCompactionMarker,
	})
	compactedMessages = append(compactedMessages, recent...)
	compactedPrompt, toolNames := promptcompat.BuildOpenAIPrompt(compactedMessages, req.ToolsRaw, "", req.ToolChoice, req.Thinking)
	stats.AfterMessages = len(compactedMessages)
	stats.AfterStateBytes = summaryStateSize(compactedMessages)
	stats.AfterPromptUnits = promptcompat.PromptUnits(compactedPrompt)
	stats.RetainedTurns = countSummaryUserTurns(recent)
	if stats.AfterStateBytes >= stats.BeforeStateBytes || stats.AfterPromptUnits >= stats.BeforePromptUnits {
		return req, stats, false, fmt.Errorf("%w: result did not reduce the stored context", ErrSummaryCompactionNotReducible)
	}
	if stats.AfterPromptUnits > targetUnits {
		return req, stats, false, fmt.Errorf("summary result exceeds the %d-unit target", targetUnits)
	}

	req.Messages = compactedMessages
	req.FinalPrompt = compactedPrompt
	req.ToolNames = toolNames
	req.LatestUserText = promptcompat.ExtractLatestUserText(compactedMessages)
	return req, stats, true, nil
}

func splitSummaryWindow(messages []any, keepSystem bool, keepRecent int) (permanent, older, recent []any, ok bool) {
	if keepRecent < 1 {
		keepRecent = 1
	}
	index := 0
	for index < len(messages) && summaryMessageRole(messages[index]) == "system" {
		if isGeneratedSummary(messages[index]) {
			older = append(older, messages[index])
		} else if keepSystem {
			permanent = append(permanent, messages[index])
		} else {
			older = append(older, messages[index])
		}
		index++
	}
	body := messages[index:]
	turnStarts := make([]int, 0, keepRecent+1)
	for i, item := range body {
		if summaryMessageRole(item) == "user" {
			turnStarts = append(turnStarts, i)
		}
	}
	if len(turnStarts) <= 1 {
		return nil, nil, nil, false
	}
	if keepRecent >= len(turnStarts) {
		keepRecent = len(turnStarts) / 2
		if keepRecent < 1 {
			keepRecent = 1
		}
	}
	start := turnStarts[len(turnStarts)-keepRecent]
	older = append(older, body[:start]...)
	recent = append(recent, body[start:]...)
	return permanent, older, recent, len(older) > 0 && len(recent) > 0
}

func summaryMessageRole(item any) string {
	message, _ := item.(map[string]any)
	return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", message["role"])))
}

func isGeneratedSummary(item any) bool {
	message, _ := item.(map[string]any)
	if strings.TrimSpace(fmt.Sprintf("%v", message["ds2api_compaction_summary"])) == summaryCompactionMarker {
		return true
	}
	return strings.Contains(fmt.Sprintf("%v", message["content"]), "["+summaryCompactionMarker+"]")
}

// SplitSummaryCompactionWindow separates a compacted request into the opaque
// base carried by the local compaction item and the recent items that remain
// visible in the canonical next context window.
func SplitSummaryCompactionWindow(messages []any) (base, retained []any, ok bool) {
	for index, item := range messages {
		if !isGeneratedSummary(item) {
			continue
		}
		base = cloneSummaryMessages(messages[:index+1])
		retained = cloneSummaryMessages(messages[index+1:])
		return base, retained, len(base) > 0
	}
	return nil, nil, false
}

func countSummaryUserTurns(messages []any) int {
	count := 0
	for _, item := range messages {
		if summaryMessageRole(item) == "user" {
			count++
		}
	}
	return count
}

func cloneSummaryMessages(messages []any) []any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]any, len(messages))
	copy(out, messages)
	return out
}

func summaryStateSize(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(raw)
}
