package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	claudefmt "DeepSeek_Web_To_API/internal/format/claude"
	"DeepSeek_Web_To_API/internal/httpapi/historycapture"
	openaishared "DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/prompt"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/safetyllm"
	"DeepSeek_Web_To_API/internal/sse"
	"DeepSeek_Web_To_API/internal/toolcall"
	"DeepSeek_Web_To_API/internal/util"
)

type claudeSessionAuthResolver interface {
	DetermineWithSession(req *http.Request, body []byte) (*auth.RequestAuth, error)
}

type claudeCallerAuthResolver interface {
	DetermineCaller(req *http.Request) (*auth.RequestAuth, error)
}

func (h *Handler) handleDirectClaudeIfAvailable(w http.ResponseWriter, r *http.Request, store ConfigReader) bool {
	if h == nil || h.Auth == nil || h.DS == nil {
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, openaishared.GeneralMaxSize)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeClaudeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return true
		}
		writeClaudeError(w, http.StatusBadRequest, "invalid body")
		return true
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		writeClaudeError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	norm, err := normalizeClaudeRequest(store, cloneMap(req))
	if err != nil {
		writeClaudeError(w, http.StatusBadRequest, err.Error())
		return true
	}
	exposeThinking := applyClaudeDirectThinkingPolicy(&norm, req)
	incrementalBaseNorm := norm
	historySession := h.startQueuedDirectClaudeHistory(r, req, norm.Standard)
	promptLimit := config.DefaultPromptLimitSettings()
	if store != nil {
		promptLimit = store.PromptLimitSnapshot()
	}
	if dropped, ok := openaishared.CompressPromptBeforeCIF(promptLimit, &norm.Standard); ok {
		limit, expert := openaishared.PromptLimitForModel(promptLimit, norm.Standard.ResolvedModel)
		config.Logger.Info("[prompt_limit] compressed history",
			"surface", "anthropic.messages", "model", norm.Standard.ResolvedModel,
			"expert", expert, "limit_units", limit,
			"dropped_messages", dropped, "prompt_units", promptcompat.PromptUnits(norm.Standard.FinalPrompt))
	}
	a, err := h.determineDirectClaudeAuth(r, raw, req)
	if err != nil {
		if historySession != nil {
			historySession.Error(claudeAuthErrorStatus(err), err.Error(), "error", "", "")
		}
		writeClaudeError(w, claudeAuthErrorStatus(err), err.Error())
		return true
	}
	if historySession != nil {
		historySession.BindAuth(a)
	}
	dynamicLimitApplied := false
	promptLimit, dynamicLimitApplied, err = openaishared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "anthropic.messages", "error", err)
	}
	if dropped, ok := openaishared.CompressPromptBeforeCIF(promptLimit, &norm.Standard); ok {
		limit, expert := openaishared.PromptLimitForModel(promptLimit, norm.Standard.ResolvedModel)
		config.Logger.Info("[prompt_limit] recompressed history after dynamic lookup",
			"surface", "anthropic.messages", "model", norm.Standard.ResolvedModel,
			"expert", expert, "limit_units", limit, "dropped_messages", dropped,
			"prompt_units", promptcompat.PromptUnits(norm.Standard.FinalPrompt), "dynamic_upstream_limit", dynamicLimitApplied)
	}
	autoDeleteMode := "none"
	if h.Store != nil {
		autoDeleteMode = h.Store.AutoDeleteMode()
	}
	var sessionID string
	defer func() {
		// Issue #20: /v1/messages (Claude Code) was the most-affected path
		// of the auto-delete miss because Claude Code is the dominant
		// client on this surface. Mirror the chat handler's behavior.
		openaishared.AutoDeleteRemoteSession(r.Context(), h.DS, autoDeleteMode, a.AccountID, a.DeepSeekToken, sessionID)
		h.Auth.Release(a)
	}()

	r = r.WithContext(auth.WithAuth(r.Context(), a))
	if historySession == nil {
		historySession = historycapture.Start(h.ChatHistory, r, a, norm.Standard)
	}
	openaishared.LogIncrementalRequestContext("anthropic.messages", a, incrementalBaseNorm.Standard, len(raw))
	incrementalAttemptNorm := incrementalBaseNorm
	if h.tryIncrementalClaude(w, r, a, &incrementalAttemptNorm, exposeThinking, promptLimit, historySession, &sessionID) {
		return true
	}
	if incrementalAttemptNorm.Standard.IncrementalSessionRotated {
		norm.Standard = incrementalAttemptNorm.Standard
	}
	if openaishared.EnforcePromptLimit(promptLimit, norm.Standard) != "" && promptLimit.ProFlashCompressionEnable {
		compressed, ok, compressErr := openaishared.TryFlashCompressPrompt(r.Context(), h.DS, a, norm.Standard, promptLimit, autoDeleteMode)
		if compressErr != nil {
			config.Logger.Warn("[prompt_limit] Flash compression failed; returning original overflow",
				"surface", "anthropic.messages", "model", norm.Standard.ResolvedModel, "error", compressErr)
		} else if ok {
			norm.Standard = compressed
			config.Logger.Info("[prompt_limit] compressed Pro history with Flash",
				"surface", "anthropic.messages", "model", norm.Standard.ResolvedModel,
				"thinking", norm.Standard.Thinking, "prompt_units", promptcompat.PromptUnits(norm.Standard.FinalPrompt))
		}
	}
	if errMsg := openaishared.EnforcePromptLimit(promptLimit, norm.Standard); errMsg != "" {
		if historySession != nil {
			historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "prompt_too_large", "", "")
		}
		writeClaudeError(w, http.StatusRequestEntityTooLarge, errMsg)
		return true
	}

	// v1.0.14: LLM-based binary safety check on the assembled standard
	// prompt. Blocks return 403 with policy_blocked finish_reason; the
	// caller's deferred AutoDeleteRemoteSession still fires cleanly.
	if openaishared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, openaishared.PickAuditText(norm.Standard.LatestUserText, norm.Standard.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.Error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return true
	}

	sessionID, err = h.DS.CreateSession(r.Context(), a, 3)
	if err != nil {
		sessionDetail := openaishared.SessionErrorDetail(err)
		if sessionDetail.Stopped || sessionDetail.Status == http.StatusGatewayTimeout {
			writeClaudeSessionCallError(w, historySession, err)
			return true
		}
		if a.UseConfigToken {
			if historySession != nil {
				historySession.Error(http.StatusUnauthorized, "Account token is invalid. Please re-login the account in admin.", "error", "", "")
			}
			writeClaudeError(w, http.StatusUnauthorized, "Account token is invalid. Please re-login the account in admin.")
			return true
		}
		if historySession != nil {
			historySession.Error(http.StatusUnauthorized, "Invalid token. If this should be a DeepSeek_Web_To_API key, add it to config.keys first.", "error", "", "")
		}
		a.MarkDirectTokenInvalid()
		writeClaudeError(w, http.StatusUnauthorized, "Invalid token. If this should be a DeepSeek_Web_To_API key, add it to config.keys first.")
		return true
	}
	pow, err := h.DS.GetPow(r.Context(), a, 3)
	if err != nil {
		powDetail := openaishared.PowErrorDetail(err)
		if powDetail.Stopped || powDetail.Status == http.StatusGatewayTimeout {
			writeClaudePowCallError(w, historySession, err)
			return true
		}
		if historySession != nil {
			historySession.Error(http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).", "error", "", "")
		}
		if !a.UseConfigToken {
			a.MarkDirectTokenInvalid()
		}
		writeClaudeError(w, http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).")
		return true
	}
	payload := norm.Standard.CompletionPayload(sessionID)
	resp, err := h.DS.CallCompletion(r.Context(), a, payload, pow, 3)
	if err != nil {
		config.Logger.Warn("[claude] completion request failed", "stream", norm.Standard.Stream, "error", err)
		if !a.UseConfigToken && openaishared.CompletionErrorDetail(err).Status == http.StatusUnauthorized {
			a.MarkDirectTokenInvalid()
		}
		writeClaudeCompletionCallError(w, historySession, err, "", "")
		return true
	}
	var outcome claudeCompletionOutcome
	if norm.Standard.Stream {
		outcome = h.handleClaudeStreamRealtime(
			w,
			r,
			resp,
			norm.Standard.ResponseModel,
			norm.Standard.Messages,
			norm.Standard.Thinking,
			norm.Standard.Search,
			norm.Standard.ToolNames,
			norm.Standard.ToolsRaw,
			historySession,
		)
	} else {
		outcome = h.handleDirectClaudeNonStream(w, resp, norm, exposeThinking, historySession)
	}
	h.recordFullClaudeIncrementalState(a, incrementalBaseNorm.Standard, sessionID, outcome)
	return true
}

type claudeCompletionOutcome struct {
	success           bool
	responseMessageID int
	responseMessages  []any
}

func (h *Handler) tryIncrementalClaude(w http.ResponseWriter, r *http.Request, a *auth.RequestAuth, norm *claudeNormalizedRequest, exposeThinking bool, promptLimit config.PromptLimitSettings, historySession *historycapture.Session, activeSessionID *string) bool {
	if norm == nil {
		return false
	}
	autoDeleteMode := "none"
	if h.Store != nil {
		autoDeleteMode = h.Store.AutoDeleteMode()
	}
	lease, incrementalPrompt, ok := openaishared.PrepareIncrementalRequestWithSettings(h.Incremental, h.DS, autoDeleteMode, a, norm.Standard, norm.Standard.Messages, promptLimit)
	if !ok {
		return false
	}
	defer func() {
		if lease != nil {
			lease.Invalidate()
		}
	}()
	if lease.Rotate {
		dropped, applied := openaishared.ApplyIncrementalSessionRotation(&norm.Standard, lease, promptLimit)
		if !applied {
			return false
		}
		config.Logger.Info("[incremental] rotating upstream session",
			"surface", "anthropic.messages", "session_key", a.SessionKey,
			"previous_session_id", lease.SessionID, "completed_turns", lease.TurnCount,
			"configured_max_turns", promptLimit.IncrementalMaxTurns,
			"dropped_messages", dropped, "rotation_prompt_units", promptcompat.PromptUnits(norm.Standard.FinalPrompt),
			"format_prompt_units", promptcompat.PromptUnits(norm.Standard.IncrementalFormatPrompt))
		lease.Invalidate()
		lease = nil
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		return false
	}
	fullPrompt := norm.Standard.FinalPrompt
	norm.Standard.FinalPrompt = incrementalPrompt
	if activeSessionID != nil {
		*activeSessionID = lease.SessionID
	}
	config.Logger.Info("[incremental] reused upstream session",
		"surface", "anthropic.messages", "session_key", a.SessionKey,
		"parent_message_id", lease.ParentMessageID,
		"match_mode", lease.MatchMode,
		"retained_messages", len(norm.Standard.Messages)-len(lease.DeltaMessages),
		"delta_messages", len(lease.DeltaMessages),
		"full_prompt_units", promptcompat.PromptUnits(fullPrompt),
		"format_prompt_units", promptcompat.PromptUnits(norm.Standard.IncrementalFormatPrompt),
		"incremental_prompt_units", promptcompat.PromptUnits(incrementalPrompt))
	if errMsg := openaishared.EnforcePromptLimit(promptLimit, norm.Standard); errMsg != "" {
		if historySession != nil {
			historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "prompt_too_large", "", "")
		}
		writeClaudeError(w, http.StatusRequestEntityTooLarge, errMsg)
		return true
	}
	if openaishared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, openaishared.PickAuditText(norm.Standard.LatestUserText, norm.Standard.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.Error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return true
	}
	pow, err := h.DS.GetPow(r.Context(), a, 3)
	if err != nil {
		writeClaudePowCallError(w, historySession, err)
		return true
	}
	payload := norm.Standard.CompletionPayload(lease.SessionID)
	payload["parent_message_id"] = lease.ParentMessageID
	resp, err := openaishared.CallPinnedCompletion(r.Context(), h.DS, a, payload, pow)
	if err != nil {
		config.Logger.Warn("[incremental] pinned completion failed; falling back to full replay",
			"surface", "anthropic.messages", "session_key", a.SessionKey, "error", err)
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		norm.Standard.FinalPrompt = fullPrompt
		return false
	}
	var outcome claudeCompletionOutcome
	if norm.Standard.Stream {
		outcome = h.handleClaudeStreamRealtime(w, r, resp, norm.Standard.ResponseModel, norm.Standard.Messages, norm.Standard.Thinking, norm.Standard.Search, norm.Standard.ToolNames, norm.Standard.ToolsRaw, historySession)
	} else {
		outcome = h.handleDirectClaudeNonStream(w, resp, *norm, exposeThinking, historySession)
	}
	if outcome.success {
		lease.Complete(openaishared.IncrementalScope(a, norm.Standard), norm.Standard.Messages, outcome.responseMessages, lease.SessionID, outcome.responseMessageID)
		lease = nil
	}
	return true
}

func (h *Handler) recordFullClaudeIncrementalState(a *auth.RequestAuth, stdReq promptcompat.StandardRequest, sessionID string, outcome claudeCompletionOutcome) {
	if h == nil || h.Incremental == nil || h.Store == nil || !outcome.success || !strings.EqualFold(strings.TrimSpace(h.Store.AutoDeleteMode()), "none") {
		return
	}
	scope := openaishared.IncrementalScope(a, stdReq)
	h.Incremental.Record(scope, stdReq.Messages, outcome.responseMessages, sessionID, outcome.responseMessageID)
	config.Logger.Info("[incremental] recorded upstream branch",
		"surface", "anthropic.messages", "session_key", scope.SessionKey,
		"variant", scope.Variant,
		"parent_message_id", outcome.responseMessageID,
		"request_messages", len(stdReq.Messages), "response_messages", len(outcome.responseMessages),
		"full_prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
		"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt))
}

func (h *Handler) startQueuedDirectClaudeHistory(r *http.Request, req map[string]any, stdReq promptcompat.StandardRequest) *historycapture.Session {
	resolver, ok := h.Auth.(claudeCallerAuthResolver)
	if !ok {
		return nil
	}
	reqForCaller := r.Clone(r.Context())
	reqForCaller.Header = r.Header.Clone()
	reqForCaller.Header.Set(auth.SessionAffinityHeader, claudeSessionAffinityScope(r, req))
	callerAuth, err := resolver.DetermineCaller(reqForCaller)
	if err != nil {
		return nil
	}
	return historycapture.StartWithStatus(h.ChatHistory, r, callerAuth, stdReq, "queued")
}

func (h *Handler) determineDirectClaudeAuth(r *http.Request, raw []byte, req map[string]any) (*auth.RequestAuth, error) {
	reqForAuth := r.Clone(r.Context())
	reqForAuth.Header = r.Header.Clone()
	reqForAuth.Header.Set(auth.SessionAffinityHeader, claudeSessionAffinityScope(r, req))
	if resolver, ok := h.Auth.(claudeSessionAuthResolver); ok {
		return resolver.DetermineWithSession(reqForAuth, raw)
	}
	return h.Auth.Determine(reqForAuth)
}

func claudeAuthErrorStatus(err error) int {
	if errors.Is(err, auth.ErrNoAccount) {
		return http.StatusTooManyRequests
	}
	return http.StatusUnauthorized
}

func applyClaudeDirectThinkingPolicy(norm *claudeNormalizedRequest, original map[string]any) bool {
	if norm == nil {
		return false
	}
	enabled, hasOverride := util.ResolveThinkingOverride(original)
	if !hasOverride {
		enabled = !norm.Standard.Stream
	}
	if config.IsNoThinkingModel(norm.Standard.ResolvedModel) {
		enabled = false
		hasOverride = false
	}
	norm.Standard.Thinking = enabled
	norm.Standard.ExposeReasoning = hasOverride && enabled
	norm.Standard.FinalPrompt = prompt.MessagesPrepareWithThinking(toMessageMaps(norm.Standard.Messages), enabled)
	return norm.Standard.ExposeReasoning
}

func (h *Handler) handleDirectClaudeNonStream(w http.ResponseWriter, resp *http.Response, norm claudeNormalizedRequest, exposeThinking bool, historySessions ...*historycapture.Session) claudeCompletionOutcome {
	historySession := firstHistorySession(historySessions)
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(body))
		if historySession != nil {
			historySession.Error(resp.StatusCode, message, "error", "", "")
		}
		writeClaudeError(w, resp.StatusCode, message)
		return claudeCompletionOutcome{}
	}

	result := sse.CollectStream(resp, norm.Standard.Thinking, true)
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	finalThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	finalText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if norm.Standard.Search {
		finalText = openaishared.ReplaceCitationMarkersWithLinks(finalText, result.CitationLinks)
	}
	if shouldWriteClaudeEmptyOutputError(finalText, finalThinking, result.ContentFilter, norm.Standard.ToolNames) {
		status, message, _ := openaishared.UpstreamEmptyOutputDetail(result.ContentFilter, finalText, finalThinking)
		if historySession != nil {
			historySession.Error(status, message, "empty_output", finalThinking, finalText)
		}
		writeClaudeError(w, status, message)
		return claudeCompletionOutcome{}
	}

	body := claudefmt.BuildMessageResponse(
		fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		norm.Standard.ResponseModel,
		norm.Standard.Messages,
		finalThinking,
		finalText,
		norm.Standard.ToolNames,
		norm.Standard.ToolsRaw,
	)
	raw, err := json.Marshal(body)
	if err != nil {
		if historySession != nil {
			historySession.Error(http.StatusInternalServerError, "failed to encode response", "error", finalThinking, finalText)
		}
		writeClaudeError(w, http.StatusInternalServerError, "failed to encode response")
		return claudeCompletionOutcome{}
	}
	if historySession != nil {
		historySession.Success(http.StatusOK, finalThinking, finalText, "end_turn", nil)
	}
	if !exposeThinking {
		raw = stripClaudeThinkingBlocks(raw)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	responseMessages := normalizeClaudeResponseMessages(body["content"])
	return claudeCompletionOutcome{success: result.ResponseMessageID > 0 && len(responseMessages) > 0, responseMessageID: result.ResponseMessageID, responseMessages: responseMessages}
}

func normalizeClaudeResponseMessages(content any) []any {
	raw, err := json.Marshal(map[string]any{"role": "assistant", "content": content})
	if err != nil {
		return nil
	}
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil
	}
	return normalizeClaudeMessages([]any{message})
}

func firstHistorySession(historySessions []*historycapture.Session) *historycapture.Session {
	if len(historySessions) == 0 {
		return nil
	}
	return historySessions[0]
}

func shouldWriteClaudeEmptyOutputError(finalText, finalThinking string, contentFilter bool, toolNames []string) bool {
	if !openaishared.ShouldWriteUpstreamEmptyOutputError(finalText, finalThinking) {
		return false
	}
	if len(toolcall.ParseToolCalls(finalText, toolNames)) > 0 {
		return false
	}
	if len(toolcall.ParseToolCalls(finalThinking, toolNames)) > 0 {
		return false
	}
	return true
}
