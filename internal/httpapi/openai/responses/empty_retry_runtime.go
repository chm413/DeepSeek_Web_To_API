package responses

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/httpapi/historycapture"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
	"DeepSeek_Web_To_API/internal/toolcall"
)

type responsesNonStreamResult struct {
	rawThinking           string
	rawText               string
	thinking              string
	toolDetectionThinking string
	text                  string
	contentFilter         bool
	parsed                toolcall.ToolCallParseResult
	body                  map[string]any
	responseMessageID     int
}

type responsesCompletionOutcome struct {
	success           bool
	responseMessageID int
	responseMessages  []any
}

func (h *Handler) handleResponsesNonStreamWithRetry(w http.ResponseWriter, ctx context.Context, a *auth.RequestAuth, resp *http.Response, payload map[string]any, pow, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, outputPrefix []any, traceID string, historySession *historycapture.Session, activeSessionID *string) responsesCompletionOutcome {
	attempts := 0
	currentResp := resp
	usagePrompt := finalPrompt
	accumulatedThinking := ""
	accumulatedRawThinking := ""
	accumulatedToolDetectionThinking := ""
	for {
		result, ok := h.collectResponsesNonStreamAttempt(w, currentResp, responseID, model, usagePrompt, refFileTokens, thinkingEnabled, searchEnabled, toolNames, toolsRaw, historySession)
		if !ok {
			return responsesCompletionOutcome{}
		}
		accumulatedThinking += sse.TrimContinuationOverlap(accumulatedThinking, result.thinking)
		accumulatedRawThinking += sse.TrimContinuationOverlap(accumulatedRawThinking, result.rawThinking)
		accumulatedToolDetectionThinking += sse.TrimContinuationOverlap(accumulatedToolDetectionThinking, result.toolDetectionThinking)
		result.thinking = accumulatedThinking
		result.rawThinking = accumulatedRawThinking
		result.toolDetectionThinking = accumulatedToolDetectionThinking
		result.parsed = detectAssistantToolCalls(result.rawText, result.text, result.rawThinking, result.toolDetectionThinking, toolNames)
		result.body = openaifmt.BuildResponseObjectWithToolCalls(responseID, model, usagePrompt, result.thinking, result.text, result.parsed.Calls, toolsRaw)
		prependResponsesOutputItems(result.body, outputPrefix)
		addRefFileTokensToUsage(result.body, refFileTokens)

		if !shouldRetryResponsesNonStream(result, attempts) {
			if h.finishResponsesNonStreamResult(w, result, attempts, owner, responseID, toolChoice, traceID, historySession, usagePrompt, refFileTokens) {
				return responsesOutcomeFromBody(result.body, result.responseMessageID)
			}
			return responsesCompletionOutcome{}
		}

		attempts++
		retryPayload := clonePayloadForEmptyOutputRetry(payload, result.responseMessageID)
		config.Logger.Info("[openai_empty_retry] attempting synthetic retry", "surface", "responses", "stream", false, "retry_attempt", attempts, "parent_message_id", shared.PayloadParentMessageID(retryPayload))
		retryPow, prepared := h.prepareResponsesEmptyOutputRetry(ctx, a, payload, retryPayload, pow, attempts, false, historySession, activeSessionID)
		if !prepared {
			if h.finishResponsesNonStreamResult(w, result, attempts, owner, responseID, toolChoice, traceID, historySession, usagePrompt, refFileTokens) {
				return responsesOutcomeFromBody(result.body, result.responseMessageID)
			}
			return responsesCompletionOutcome{}
		}
		var nextResp *http.Response
		var err error
		if shared.IsPinnedCompletionPayload(payload) {
			nextResp, err = shared.CallPinnedCompletion(ctx, h.DS, a, retryPayload, retryPow)
		} else {
			nextResp, err = h.DS.CallCompletion(ctx, a, retryPayload, retryPow, 3)
		}
		if err != nil {
			writeCompletionCallError(w, historySession, err, result.thinking, result.text)
			config.Logger.Warn("[openai_empty_retry] retry request failed", "surface", "responses", "stream", false, "retry_attempt", attempts, "error", err)
			return responsesCompletionOutcome{}
		}
		usagePrompt = usagePromptWithEmptyOutputRetry(usagePrompt, attempts)
		currentResp = nextResp
	}
}

func (h *Handler) collectResponsesNonStreamAttempt(w http.ResponseWriter, resp *http.Response, responseID, model, usagePrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, historySession *historycapture.Session) (responsesNonStreamResult, bool) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(body))
		if historySession != nil {
			historySession.Error(resp.StatusCode, message, "error", "", "")
		}
		writeOpenAIError(w, resp.StatusCode, message)
		return responsesNonStreamResult{}, false
	}
	result := sse.CollectStream(resp, thinkingEnabled, false)
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	sanitizedThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	sanitizedText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if searchEnabled {
		sanitizedText = replaceCitationMarkersWithLinks(sanitizedText, result.CitationLinks)
	}
	textParsed := detectAssistantToolCalls(result.Text, sanitizedText, result.Thinking, result.ToolDetectionThinking, toolNames)
	responseObj := openaifmt.BuildResponseObjectWithToolCalls(responseID, model, usagePrompt, sanitizedThinking, sanitizedText, textParsed.Calls, toolsRaw)
	addRefFileTokensToUsage(responseObj, refFileTokens)
	return responsesNonStreamResult{
		rawThinking:           result.Thinking,
		rawText:               result.Text,
		thinking:              sanitizedThinking,
		toolDetectionThinking: result.ToolDetectionThinking,
		text:                  sanitizedText,
		contentFilter:         result.ContentFilter,
		parsed:                textParsed,
		body:                  responseObj,
		responseMessageID:     result.ResponseMessageID,
	}, true
}

func (h *Handler) finishResponsesNonStreamResult(w http.ResponseWriter, result responsesNonStreamResult, attempts int, owner, responseID string, toolChoice promptcompat.ToolChoicePolicy, traceID string, historySession *historycapture.Session, usagePrompt string, refFileTokens int) bool {
	if len(result.parsed.Calls) == 0 && writeUpstreamEmptyOutputError(w, result.text, result.thinking, result.contentFilter) {
		if historySession != nil {
			status, message, code := upstreamEmptyOutputDetail(result.contentFilter, result.text, result.thinking)
			historySession.Error(status, message, code, result.thinking, result.text)
		}
		config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "responses", "stream", false, "retry_attempts", attempts, "success_source", "none", "content_filter", result.contentFilter)
		return false
	}
	logResponsesToolPolicyRejection(traceID, toolChoice, result.parsed, "text")
	if toolChoice.IsRequired() && len(result.parsed.Calls) == 0 {
		if historySession != nil {
			historySession.Error(http.StatusUnprocessableEntity, "tool_choice requires at least one valid tool call.", "tool_choice_violation", result.thinking, result.text)
		}
		writeOpenAIErrorWithCode(w, http.StatusUnprocessableEntity, "tool_choice requires at least one valid tool call.", "tool_choice_violation")
		return false
	}
	h.getResponseStore().put(owner, responseID, result.body)
	if historySession != nil {
		historySession.Success(http.StatusOK, result.thinking, result.text, "stop", openaifmt.BuildChatUsageForModel("", usagePrompt, result.thinking, result.text, refFileTokens))
	}
	writeJSON(w, http.StatusOK, result.body)
	source := "first_attempt"
	if attempts > 0 {
		source = "synthetic_retry"
	}
	config.Logger.Info("[openai_empty_retry] completed", "surface", "responses", "stream", false, "retry_attempts", attempts, "success_source", source)
	return true
}

func responsesOutcomeFromBody(body map[string]any, responseMessageID int) responsesCompletionOutcome {
	if responseMessageID <= 0 || body == nil {
		return responsesCompletionOutcome{}
	}
	messages := promptcompat.NormalizeResponsesInputAsMessages(body["output"])
	return responsesCompletionOutcome{success: len(messages) > 0, responseMessageID: responseMessageID, responseMessages: messages}
}

// shouldRetryResponsesNonStream — see chat counterpart for rationale.
// Thinking-only responses (text empty, reasoning trace present) flow
// through as success and are NOT retried.
func shouldRetryResponsesNonStream(result responsesNonStreamResult, attempts int) bool {
	return emptyOutputRetryEnabled() &&
		attempts < emptyOutputRetryMaxAttempts() &&
		!result.contentFilter &&
		len(result.parsed.Calls) == 0 &&
		strings.TrimSpace(result.text) == "" &&
		strings.TrimSpace(result.thinking) == ""
}

func (h *Handler) handleResponsesStreamWithRetry(w http.ResponseWriter, r *http.Request, a *auth.RequestAuth, resp *http.Response, payload map[string]any, pow, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, outputPrefix []any, traceID string, historySession *historycapture.Session, activeSessionID *string) responsesCompletionOutcome {
	streamRuntime, initialType, ok := h.prepareResponsesStreamRuntime(w, resp, owner, responseID, model, finalPrompt, refFileTokens, thinkingEnabled, searchEnabled, toolNames, toolsRaw, toolChoice, outputPrefix, traceID)
	if !ok {
		if historySession != nil {
			historySession.Error(resp.StatusCode, "upstream response error", "error", "", "")
		}
		return responsesCompletionOutcome{}
	}
	attempts := 0
	currentResp := resp
	for {
		terminalWritten, retryable := h.consumeResponsesStreamAttempt(r, currentResp, streamRuntime, initialType, thinkingEnabled, attempts < emptyOutputRetryMaxAttempts(), historySession)
		if terminalWritten {
			recordResponsesStreamHistory(streamRuntime, historySession)
			logResponsesStreamTerminal(streamRuntime, attempts)
			return responsesOutcomeFromBody(streamRuntime.completedObject, streamRuntime.responseMessageID)
		}
		if !retryable || !emptyOutputRetryEnabled() || attempts >= emptyOutputRetryMaxAttempts() {
			streamRuntime.finalize("stop", false)
			recordResponsesStreamHistory(streamRuntime, historySession)
			config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "responses", "stream", true, "retry_attempts", attempts, "success_source", "none", "error_code", streamRuntime.finalErrorCode)
			return responsesCompletionOutcome{}
		}
		attempts++
		retryPayload := clonePayloadForEmptyOutputRetry(payload, streamRuntime.responseMessageID)
		config.Logger.Info("[openai_empty_retry] attempting synthetic retry", "surface", "responses", "stream", true, "retry_attempt", attempts, "parent_message_id", shared.PayloadParentMessageID(retryPayload))
		retryPow, prepared := h.prepareResponsesEmptyOutputRetry(r.Context(), a, payload, retryPayload, pow, attempts, true, historySession, activeSessionID)
		if !prepared {
			streamRuntime.finalize("stop", false)
			recordResponsesStreamHistory(streamRuntime, historySession)
			config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "responses", "stream", true, "retry_attempts", attempts, "success_source", "none", "error_code", streamRuntime.finalErrorCode)
			return responsesCompletionOutcome{}
		}
		var nextResp *http.Response
		var err error
		if shared.IsPinnedCompletionPayload(payload) {
			nextResp, err = shared.CallPinnedCompletion(r.Context(), h.DS, a, retryPayload, retryPow)
		} else {
			nextResp, err = h.DS.CallCompletion(r.Context(), a, retryPayload, retryPow, 3)
		}
		if err != nil {
			failResponsesStreamCompletionError(streamRuntime, historySession, err)
			config.Logger.Warn("[openai_empty_retry] retry request failed", "surface", "responses", "stream", true, "retry_attempt", attempts, "error", err)
			return responsesCompletionOutcome{}
		}
		if nextResp.StatusCode != http.StatusOK {
			defer func() { _ = nextResp.Body.Close() }()
			body, _ := io.ReadAll(nextResp.Body)
			streamRuntime.failResponse(nextResp.StatusCode, strings.TrimSpace(string(body)), "error")
			recordResponsesStreamHistory(streamRuntime, historySession)
			return responsesCompletionOutcome{}
		}
		streamRuntime.finalPrompt = usagePromptWithEmptyOutputRetry(finalPrompt, attempts)
		currentResp = nextResp
	}
}

func (h *Handler) prepareResponsesEmptyOutputRetry(ctx context.Context, a *auth.RequestAuth, basePayload, retryPayload map[string]any, originalPow string, retryAttempt int, stream bool, historySession *historycapture.Session, activeSessionID *string) (string, bool) {
	var bindAuth func(*auth.RequestAuth)
	if historySession != nil {
		bindAuth = historySession.BindAuth
	}
	return shared.PrepareEmptyOutputRetry(ctx, h.Auth, h.DS, a, basePayload, retryPayload, originalPow, "responses", stream, retryAttempt, bindAuth, activeSessionID)
}

func (h *Handler) prepareResponsesStreamRuntime(w http.ResponseWriter, resp *http.Response, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, outputPrefix []any, traceID string) (*responsesStreamRuntime, string, bool) {
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		writeOpenAIError(w, resp.StatusCode, strings.TrimSpace(string(body)))
		return nil, "", false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_, canFlush := w.(http.Flusher)
	initialType := "text"
	if thinkingEnabled {
		initialType = "thinking"
	}
	streamRuntime := newResponsesStreamRuntime(
		w, rc, canFlush, responseID, model, finalPrompt, thinkingEnabled, searchEnabled,
		h.compatStripReferenceMarkers(), toolNames, toolsRaw, len(toolNames) > 0,
		h.toolcallFeatureMatchEnabled() && h.toolcallEarlyEmitHighConfidence(),
		toolChoice, traceID, func(obj map[string]any) {
			h.getResponseStore().put(owner, responseID, obj)
		},
	)
	streamRuntime.refFileTokens = refFileTokens
	streamRuntime.outputPrefix = cloneAnySlice(outputPrefix)
	streamRuntime.sendCreated()
	streamRuntime.emitOutputPrefix()
	return streamRuntime, initialType, true
}

func prependResponsesOutputItems(response map[string]any, prefix []any) {
	if response == nil || len(prefix) == 0 {
		return
	}
	output, _ := response["output"].([]any)
	combined := make([]any, 0, len(prefix)+len(output))
	combined = append(combined, cloneAnySlice(prefix)...)
	combined = append(combined, output...)
	response["output"] = combined
}

func (h *Handler) consumeResponsesStreamAttempt(r *http.Request, resp *http.Response, streamRuntime *responsesStreamRuntime, initialType string, thinkingEnabled bool, allowDeferEmpty bool, historySession *historycapture.Session) (bool, bool) {
	defer func() { _ = resp.Body.Close() }()
	finalReason := "stop"
	streamengine.ConsumeSSE(streamengine.ConsumeConfig{
		Context:             r.Context(),
		Body:                resp.Body,
		ThinkingEnabled:     thinkingEnabled,
		InitialType:         initialType,
		KeepAliveInterval:   time.Duration(dsprotocol.KeepAliveTimeout) * time.Second,
		IdleTimeout:         time.Duration(dsprotocol.StreamIdleTimeout) * time.Second,
		MaxKeepAliveNoInput: dsprotocol.MaxKeepaliveCount,
	}, streamengine.ConsumeHooks{
		OnParsed: func(parsed sse.LineResult) streamengine.ParsedDecision {
			decision := streamRuntime.onParsed(parsed)
			if historySession != nil {
				historySession.Progress(streamRuntime.thinking.String(), streamRuntime.text.String())
			}
			return decision
		},
		OnFinalize: func(reason streamengine.StopReason, _ error) {
			if string(reason) == "content_filter" {
				finalReason = "content_filter"
			}
		},
		OnContextDone: func() {
			streamRuntime.markContextCancelled()
			if historySession != nil {
				historySession.Stopped(streamRuntime.thinking.String(), streamRuntime.text.String(), string(streamengine.StopReasonContextCancelled))
			}
		},
	})
	if streamRuntime.finalErrorCode == string(streamengine.StopReasonContextCancelled) {
		return true, false
	}
	terminalWritten := streamRuntime.finalize(finalReason, allowDeferEmpty && finalReason != "content_filter")
	if terminalWritten {
		return true, false
	}
	return false, true
}

func recordResponsesStreamHistory(streamRuntime *responsesStreamRuntime, historySession *historycapture.Session) {
	if historySession == nil || streamRuntime == nil {
		return
	}
	// See chat counterpart — context-cancelled streams already wrote a
	// stopped history record from OnContextDone; do not overwrite with
	// an error record. CJackHwang/ds2api 0bca6e2c.
	if streamRuntime.finalErrorCode == string(streamengine.StopReasonContextCancelled) {
		return
	}
	if streamRuntime.finalErrorMessage != "" {
		historySession.Error(streamRuntime.finalErrorStatus, streamRuntime.finalErrorMessage, streamRuntime.finalErrorCode, streamRuntime.thinking.String(), streamRuntime.text.String())
		return
	}
	historySession.Success(
		http.StatusOK,
		streamRuntime.finalThinking,
		streamRuntime.finalText,
		streamRuntime.finalFinishReason,
		streamRuntime.finalUsage,
	)
}

func logResponsesStreamTerminal(streamRuntime *responsesStreamRuntime, attempts int) {
	source := "first_attempt"
	if attempts > 0 {
		source = "synthetic_retry"
	}
	if streamRuntime.finalErrorCode == string(streamengine.StopReasonContextCancelled) {
		config.Logger.Info("[openai_empty_retry] terminal cancelled", "surface", "responses", "stream", true, "retry_attempts", attempts, "error_code", streamRuntime.finalErrorCode)
		return
	}
	if streamRuntime.failed {
		config.Logger.Info("[openai_empty_retry] terminal empty output", "surface", "responses", "stream", true, "retry_attempts", attempts, "success_source", "none", "error_code", streamRuntime.finalErrorCode)
		return
	}
	config.Logger.Info("[openai_empty_retry] completed", "surface", "responses", "stream", true, "retry_attempts", attempts, "success_source", source)
}
