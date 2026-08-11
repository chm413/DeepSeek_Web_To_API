package shared

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sse"
)

const proFlashCompressionInstruction = `Summarize the conversation below for a later Pro-model request.
Return only a concise factual summary. Preserve user requirements, decisions,
constraints, unresolved questions, tool results, file references, and exact
identifiers that are needed to answer the latest user message. Do not answer
the user and do not add commentary about summarization.`

// ProFlashCompressionCaller is the minimal upstream surface needed by the
// real Flash summarization path.
type ProFlashCompressionCaller interface {
	SessionDeleter
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error)
}

// TryFlashCompressPrompt uses a real Flash completion to summarize the older
// turns of an oversized Pro request. It is opt-in because it costs an upstream
// request and can change the prompt's wording. If the Flash request cannot
// produce a bounded summary, the caller must retain the original overflow and
// return the normal downstream 413 rather than silently truncating it.
func TryFlashCompressPrompt(ctx context.Context, ds ProFlashCompressionCaller, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings, autoDeleteMode string) (promptcompat.StandardRequest, bool, error) {
	if ds == nil || a == nil || !cfg.Enabled || !cfg.ProFlashCompressionEnable || !promptcompat.IsExpertModel(promptcompat.EffectiveModel(req)) {
		return req, false, nil
	}
	if promptcompat.PromptUnits(req.FinalPrompt) <= promptcompat.LimitForModel(cfg, promptcompat.EffectiveModel(req)) {
		return req, false, nil
	}

	lastUser := lastUserMessageIndex(req.Messages)
	if lastUser <= 0 {
		return req, false, fmt.Errorf("pro prompt overflow has no prior conversation turns to summarize")
	}

	prior := cloneAnySlice(req.Messages[:lastUser])
	flashReq := req
	flashReq.RequestedModel = "deepseek-v4-flash"
	flashReq.ResolvedModel = "deepseek-v4-flash"
	flashReq.ResponseModel = "deepseek-v4-flash"
	flashReq.Messages = prior
	flashReq.Thinking = false
	flashReq.ToolsRaw = nil
	flashReq.ToolNames = nil
	flashReq.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(prior, nil, "", promptcompat.DefaultToolChoicePolicy(), false)
	if promptcompat.PromptUnits(flashReq.FinalPrompt) > cfg.MaxCharsDefault {
		var changed bool
		// Pro-to-Flash compression is itself an explicit operator opt-in;
		// allow its bounded preparation pass even when silent auto-compression
		// is disabled globally.
		flashCfg := cfg
		flashCfg.AutoCompressEnable = true
		flashReq, changed = promptcompat.CompressToFit(flashCfg, flashReq)
		if !changed || promptcompat.PromptUnits(flashReq.FinalPrompt) > cfg.MaxCharsDefault {
			return req, false, fmt.Errorf("flash compression input is above the Flash limit")
		}
	}
	summaryPrompt := proFlashCompressionInstruction + "\n\n--- CONVERSATION ---\n" + flashReq.FinalPrompt
	if promptcompat.PromptUnits(summaryPrompt) > cfg.MaxCharsDefault {
		return req, false, fmt.Errorf("flash compression instruction plus history is above the Flash limit")
	}

	sessionID, err := ds.CreateSession(ctx, a, 3)
	if err != nil {
		return req, false, fmt.Errorf("create Flash compression session: %w", err)
	}
	defer AutoDeleteRemoteSession(ctx, ds, autoDeleteMode, a.AccountID, a.DeepSeekToken, sessionID)
	pow, err := ds.GetPow(ctx, a, 3)
	if err != nil {
		return req, false, fmt.Errorf("get Flash compression PoW: %w", err)
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
		return req, false, fmt.Errorf("flash compression completion: %w", err)
	}
	if resp == nil {
		return req, false, fmt.Errorf("flash compression returned no response")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			config.Logger.Warn("[prompt_limit] failed to close Flash compression response", "error", closeErr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return req, false, fmt.Errorf("flash compression returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	result := sse.CollectStream(resp, false, true)
	if strings.TrimSpace(result.Text) == "" {
		return req, false, fmt.Errorf("flash compression returned an empty summary")
	}

	summary := strings.TrimSpace(result.Text)
	currentTurn := cloneAnySlice(req.Messages[lastUser:])
	compressedMessages := make([]any, 0, len(currentTurn)+1)
	compressedMessages = append(compressedMessages, map[string]any{"role": "system", "content": "Conversation summary:\n" + summary})
	compressedMessages = append(compressedMessages, currentTurn...)
	compressedPrompt, toolNames := promptcompat.BuildOpenAIPrompt(compressedMessages, req.ToolsRaw, "", req.ToolChoice, req.Thinking)
	target := cfg.ProFlashCompressionTarget
	if target <= 0 || target > cfg.MaxCharsExpert {
		target = cfg.MaxCharsExpert
	}
	if promptcompat.PromptUnits(compressedPrompt) > target {
		return req, false, fmt.Errorf("flash compression summary remains above the Pro target")
	}
	req.Messages = compressedMessages
	req.FinalPrompt = compressedPrompt
	if len(toolNames) > 0 {
		req.ToolNames = toolNames
	}
	return req, true, nil
}

func lastUserMessageIndex(messages []any) int {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(fmt.Sprintf("%v", msg["role"])), "user") {
			return i
		}
	}
	return -1
}

func cloneAnySlice(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}
