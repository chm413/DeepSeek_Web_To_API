package responses

import (
	"DeepSeek_Web_To_API/internal/toolcall"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/httpapi/historycapture"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/safetyllm"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
)

func (h *Handler) GetResponseByID(w http.ResponseWriter, r *http.Request) {
	a, err := h.Auth.DetermineCaller(r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "response_id"))
	if id == "" {
		writeOpenAIError(w, http.StatusBadRequest, "response_id is required.")
		return
	}
	owner := responseStoreOwner(a)
	if owner == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	st := h.getResponseStore()
	item, ok := st.get(owner, id)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "Response not found.")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) Responses(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, openAIGeneralMaxSize)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json")
		return
	}

	callerAuth, err := h.Auth.DetermineCaller(r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	owner := responseStoreOwner(callerAuth)
	previousResponseID := strings.TrimSpace(responseString(req["previous_response_id"]))
	inheritedSessionKey := ""
	if previousResponseID != "" {
		inheritedSessionKey, _ = h.getResponseStore().getSessionKey(owner, previousResponseID)
	}
	compactTriggered := removeCompactionTriggers(req)
	if err := h.expandLocalCompactionState(owner, req); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if compactTriggered {
		h.serveLocalCompaction(w, r, rawBody, req, true)
		return
	}
	traceID := requestTraceID(r)
	historyStdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, req, traceID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	historyStdReq = shared.ApplyThinkingInjection(h.Store, historyStdReq)
	historySession := historycapture.StartWithStatus(h.ChatHistory, r, callerAuth, historyStdReq, "queued")

	// Authenticate against the expanded canonical request. For a
	// previous_response_id turn the wire body contains only the new input;
	// using it for session affinity would hash a different first user message
	// on every turn and prevent the retained upstream branch from being found.
	sessionBody := rawBody
	if expandedBody, marshalErr := json.Marshal(req); marshalErr == nil {
		sessionBody = expandedBody
	}
	var a *auth.RequestAuth
	if inheritedSessionKey != "" {
		if resolver, ok := h.Auth.(interface {
			DetermineWithSessionKey(req *http.Request, body []byte, sessionKey string) (*auth.RequestAuth, error)
		}); ok {
			a, err = resolver.DetermineWithSessionKey(r, sessionBody, inheritedSessionKey)
		} else {
			a, err = h.Auth.DetermineWithSession(r, sessionBody)
			if a != nil {
				a.SessionKey = inheritedSessionKey
			}
		}
	} else {
		a, err = h.Auth.DetermineWithSession(r, sessionBody)
	}
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		if historySession != nil {
			historySession.Error(status, detail, "error", "", "")
		}
		writeOpenAIError(w, status, detail)
		return
	}
	if historySession != nil {
		historySession.BindAuth(a)
	}
	var sessionID string
	defer func() {
		// Issue #20: /v1/responses must honor the WebUI auto-delete toggle
		// the same way /v1/chat/completions does. The session id may still
		// be empty if CreateSession failed below — the helper handles that.
		shared.AutoDeleteRemoteSession(r.Context(), h.DS, h.Store.AutoDeleteMode(), a.AccountID, a.DeepSeekToken, sessionID)
		h.Auth.Release(a)
	}()
	r = r.WithContext(auth.WithAuth(r.Context(), a))
	owner = responseStoreOwner(a)
	if owner == "" {
		if historySession != nil {
			historySession.Error(http.StatusUnauthorized, "unauthorized", "error", "", "")
		}
		writeOpenAIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := h.preprocessInlineFileInputs(r.Context(), a, req); err != nil {
		if historySession != nil {
			historySession.Error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIInlineFileError(w, err)
		return
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, req, traceID)
	if err != nil {
		if historySession != nil {
			historySession.Error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	stdReq = shared.ApplyThinkingInjection(h.Store, stdReq)
	shared.LogIncrementalRequestContext("responses", a, stdReq, len(rawBody))
	incrementalBaseReq := stdReq
	promptLimit := h.Store.PromptLimitSnapshot()
	if h.tryIncrementalResponses(w, r, a, owner, &stdReq, promptLimit, traceID, historySession, &sessionID) {
		return
	}
	// Trim history BEFORE the current-input-file step: CIF folds the whole
	// transcript into one message, so compressing afterwards has nothing
	// left to drop. See shared.CompressPromptBeforeCIF.
	dynamicLimitApplied := false
	promptLimit, dynamicLimitApplied, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses", "error", err)
	}
	var compactThresholdApplied bool
	promptLimit, compactThresholdApplied = shared.ApplyResponsesCompactThreshold(req, promptLimit, stdReq.ResolvedModel)
	if compactThresholdApplied {
		config.Logger.Info("[prompt_limit] applied Responses compact_threshold", "surface", "responses", "model", stdReq.ResolvedModel)
	}
	if !stdReq.IncrementalSessionRotated {
		if dropped, ok := shared.CompressPromptBeforeCIF(promptLimit, &stdReq); ok {
			config.Logger.Info("[prompt_limit] compressed history before CIF",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"dropped_messages", dropped, "prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "dynamic_upstream_limit", dynamicLimitApplied, "compact_threshold", compactThresholdApplied)
		}
	}
	if errMsg := shared.EnforcePromptLimitBeforeCIF(promptLimit, stdReq, h.Store.RemoteFileUploadEnabled()); errMsg != "" {
		config.Logger.Info("[prompt_limit] rejected before CIF",
			"surface", "responses", "model", stdReq.ResolvedModel,
			"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "auto_compress_enabled", promptLimit.AutoCompressEnable)
		if historySession != nil {
			historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "error", "prompt_too_large", "")
		}
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
		return
	}
	cifStartedAt := time.Now()
	stdReq, err = h.applyCurrentInputFile(r.Context(), a, stdReq)
	cifDuration := time.Since(cifStartedAt)
	if err != nil {
		status, message := mapCurrentInputFileError(err)
		if historySession != nil {
			historySession.Error(status, message, "error", "", "")
		}
		writeOpenAIError(w, status, message)
		return
	}
	recordCurrentInputMetrics(stdReq, cifDuration)
	if historySession != nil {
		historySession.UpdateCurrentInputState(stdReq)
	}

	// v1.0.14: LLM-based binary safety check.
	if shared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, shared.PickAuditText(stdReq.LatestUserText, stdReq.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.Error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return
	}

	// Final gate: CIF inlines the whole transcript into one user message, so
	// the assembled prompt can exceed the ceiling even after the pre-CIF
	// compression above. Nothing further can be trimmed at this point, so a
	// still-oversized prompt is a 413 rather than a silent upstream failure.
	if shared.EnforcePromptLimit(promptLimit, stdReq) != "" && promptLimit.ProFlashCompressionEnable {
		compressed, ok, compressErr := shared.TryFlashCompressPrompt(r.Context(), h.DS, a, stdReq, promptLimit, h.Store.AutoDeleteMode())
		if compressErr != nil {
			config.Logger.Warn("[prompt_limit] Flash compression failed; returning original overflow",
				"surface", "responses", "model", stdReq.ResolvedModel, "error", compressErr)
		} else if ok {
			stdReq = compressed
			config.Logger.Info("[prompt_limit] compressed Pro history with Flash",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"thinking", stdReq.Thinking, "prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt))
		}
	}
	if errMsg := shared.EnforcePromptLimit(promptLimit, stdReq); errMsg != "" {
		if historySession != nil {
			historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "prompt_too_large", "", "")
		}
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
		return
	}

	sessionID, err = h.DS.CreateSession(r.Context(), a, 3)
	if err != nil {
		handleCreateSessionError(w, historySession, a, err)
		return
	}
	pow, err := h.DS.GetPow(r.Context(), a, 3)
	if err != nil {
		handlePowError(w, historySession, a, err)
		return
	}
	payload := stdReq.CompletionPayload(sessionID)
	resp, err := h.DS.CallCompletion(r.Context(), a, payload, pow, 3)
	if err != nil {
		if !a.UseConfigToken && shared.CompletionErrorDetail(err).Status == http.StatusUnauthorized {
			a.MarkDirectTokenInvalid()
		}
		writeCompletionCallError(w, historySession, err, "", "")
		return
	}

	responseID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	h.getResponseStore().putInput(owner, responseID, stdReq.Messages)
	h.getResponseStore().putSessionKey(owner, responseID, a.SessionKey)
	refFileTokens := stdReq.RefFileTokens
	var outcome responsesCompletionOutcome
	if stdReq.Stream {
		outcome = h.handleResponsesStreamWithRetry(w, r, a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, refFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, traceID, historySession)
	} else {
		outcome = h.handleResponsesNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, refFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, traceID, historySession)
	}
	h.recordFullResponsesIncrementalState(a, incrementalBaseReq, sessionID, outcome)
}

func (h *Handler) tryIncrementalResponses(w http.ResponseWriter, r *http.Request, a *auth.RequestAuth, owner string, stdReq *promptcompat.StandardRequest, promptLimit config.PromptLimitSettings, traceID string, historySession *historycapture.Session, activeSessionID *string) bool {
	if stdReq == nil {
		return false
	}
	lease, incrementalPrompt, ok := shared.PrepareIncrementalRequestWithSettings(h.Incremental, h.DS, h.Store.AutoDeleteMode(), a, *stdReq, stdReq.Messages, promptLimit)
	if !ok {
		return false
	}
	defer func() {
		if lease != nil {
			lease.Invalidate()
		}
	}()
	if lease.Rotate {
		dropped, applied := shared.ApplyIncrementalSessionRotation(stdReq, lease, promptLimit)
		if !applied {
			return false
		}
		config.Logger.Info("[incremental] rotating upstream session",
			"surface", "responses", "session_key", a.SessionKey,
			"previous_session_id", lease.SessionID, "completed_turns", lease.TurnCount,
			"configured_max_turns", promptLimit.IncrementalMaxTurns,
			"dropped_messages", dropped, "rotation_prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
			"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt))
		lease.Invalidate()
		lease = nil
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		return false
	}
	fullPrompt := stdReq.FinalPrompt
	stdReq.FinalPrompt = incrementalPrompt
	if activeSessionID != nil {
		*activeSessionID = lease.SessionID
	}
	config.Logger.Info("[incremental] reused upstream session",
		"surface", "responses", "session_key", a.SessionKey,
		"parent_message_id", lease.ParentMessageID,
		"retained_messages", len(stdReq.Messages)-len(lease.DeltaMessages),
		"delta_messages", len(lease.DeltaMessages),
		"full_prompt_units", promptcompat.PromptUnits(fullPrompt),
		"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt),
		"incremental_prompt_units", promptcompat.PromptUnits(incrementalPrompt))

	var err error
	promptLimit, _, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses.incremental", "error", err)
	}
	if shared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, shared.PickAuditText(stdReq.LatestUserText, stdReq.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.Error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return true
	}
	if errMsg := shared.EnforcePromptLimit(promptLimit, *stdReq); errMsg != "" {
		if historySession != nil {
			historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "prompt_too_large", "", "")
		}
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
		return true
	}
	pow, err := h.DS.GetPow(r.Context(), a, 3)
	if err != nil {
		handlePowError(w, historySession, a, err)
		return true
	}
	payload := stdReq.CompletionPayload(lease.SessionID)
	payload["parent_message_id"] = lease.ParentMessageID
	resp, err := shared.CallPinnedCompletion(r.Context(), h.DS, a, payload, pow)
	if err != nil {
		config.Logger.Warn("[incremental] pinned completion failed; falling back to full replay",
			"surface", "responses", "session_key", a.SessionKey, "error", err)
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		stdReq.FinalPrompt = fullPrompt
		return false
	}
	responseID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	h.getResponseStore().putInput(owner, responseID, stdReq.Messages)
	h.getResponseStore().putSessionKey(owner, responseID, a.SessionKey)
	var outcome responsesCompletionOutcome
	if stdReq.Stream {
		outcome = h.handleResponsesStreamWithRetry(w, r, a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, stdReq.RefFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, traceID, historySession)
	} else {
		outcome = h.handleResponsesNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, stdReq.RefFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, traceID, historySession)
	}
	if outcome.success {
		lease.Complete(shared.IncrementalScope(a, *stdReq), stdReq.Messages, outcome.responseMessages, lease.SessionID, outcome.responseMessageID)
		lease = nil
	}
	return true
}

func (h *Handler) recordFullResponsesIncrementalState(a *auth.RequestAuth, stdReq promptcompat.StandardRequest, sessionID string, outcome responsesCompletionOutcome) {
	if h == nil || h.Incremental == nil || h.Store == nil || !outcome.success || !strings.EqualFold(strings.TrimSpace(h.Store.AutoDeleteMode()), "none") {
		return
	}
	scope := shared.IncrementalScope(a, stdReq)
	h.Incremental.Record(scope, stdReq.Messages, outcome.responseMessages, sessionID, outcome.responseMessageID)
	config.Logger.Info("[incremental] recorded upstream branch",
		"surface", "responses", "session_key", scope.SessionKey,
		"variant", scope.Variant,
		"parent_message_id", outcome.responseMessageID,
		"request_messages", len(stdReq.Messages), "response_messages", len(outcome.responseMessages),
		"full_prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
		"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt))
}

func (h *Handler) handleResponsesNonStream(w http.ResponseWriter, resp *http.Response, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, traceID string) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeOpenAIError(w, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	result := sse.CollectStream(resp, thinkingEnabled, true)
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	sanitizedThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	sanitizedText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if searchEnabled {
		sanitizedText = replaceCitationMarkersWithLinks(sanitizedText, result.CitationLinks)
	}
	textParsed := detectAssistantToolCalls(result.Text, sanitizedText, result.Thinking, result.ToolDetectionThinking, toolNames)
	if len(textParsed.Calls) == 0 && writeUpstreamEmptyOutputError(w, sanitizedText, sanitizedThinking, result.ContentFilter) {
		return
	}
	logResponsesToolPolicyRejection(traceID, toolChoice, textParsed, "text")

	callCount := len(textParsed.Calls)
	if toolChoice.IsRequired() && callCount == 0 {
		writeOpenAIErrorWithCode(w, http.StatusUnprocessableEntity, "tool_choice requires at least one valid tool call.", "tool_choice_violation")
		return
	}

	responseObj := openaifmt.BuildResponseObjectWithToolCalls(responseID, model, finalPrompt, sanitizedThinking, sanitizedText, textParsed.Calls, toolsRaw)
	addRefFileTokensToUsage(responseObj, refFileTokens)
	h.getResponseStore().put(owner, responseID, responseObj)
	writeJSON(w, http.StatusOK, responseObj)
}

func (h *Handler) handleResponsesStream(w http.ResponseWriter, r *http.Request, resp *http.Response, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, traceID string) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeOpenAIError(w, resp.StatusCode, strings.TrimSpace(string(body)))
		return
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
	bufferToolContent := len(toolNames) > 0
	emitEarlyToolDeltas := h.toolcallFeatureMatchEnabled() && h.toolcallEarlyEmitHighConfidence()
	stripReferenceMarkers := h.compatStripReferenceMarkers()

	streamRuntime := newResponsesStreamRuntime(
		w,
		rc,
		canFlush,
		responseID,
		model,
		finalPrompt,
		thinkingEnabled,
		searchEnabled,
		stripReferenceMarkers,
		toolNames,
		toolsRaw,
		bufferToolContent,
		emitEarlyToolDeltas,
		toolChoice,
		traceID,
		func(obj map[string]any) {
			h.getResponseStore().put(owner, responseID, obj)
		},
	)
	streamRuntime.refFileTokens = refFileTokens
	streamRuntime.sendCreated()

	streamengine.ConsumeSSE(streamengine.ConsumeConfig{
		Context:             r.Context(),
		Body:                resp.Body,
		ThinkingEnabled:     thinkingEnabled,
		InitialType:         initialType,
		KeepAliveInterval:   time.Duration(dsprotocol.KeepAliveTimeout) * time.Second,
		IdleTimeout:         time.Duration(dsprotocol.StreamIdleTimeout) * time.Second,
		MaxKeepAliveNoInput: dsprotocol.MaxKeepaliveCount,
	}, streamengine.ConsumeHooks{
		OnParsed: streamRuntime.onParsed,
		OnFinalize: func(reason streamengine.StopReason, _ error) {
			if string(reason) == "content_filter" {
				streamRuntime.finalize("content_filter", false)
				return
			}
			streamRuntime.finalize("stop", false)
		},
	})
}

func logResponsesToolPolicyRejection(traceID string, policy promptcompat.ToolChoicePolicy, parsed toolcall.ToolCallParseResult, channel string) {
	rejected := filteredRejectedToolNamesForLog(parsed.RejectedToolNames)
	if !parsed.RejectedByPolicy || len(rejected) == 0 {
		return
	}
	config.Logger.Warn(
		"[responses] rejected tool calls by policy",
		"trace_id", strings.TrimSpace(traceID),
		"channel", channel,
		"tool_choice_mode", policy.Mode,
		"rejected_tool_names", strings.Join(rejected, ","),
	)
}

func filteredRejectedToolNamesForLog(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		switch strings.ToLower(trimmed) {
		case "", "tool_name":
			continue
		default:
			out = append(out, trimmed)
		}
	}
	return out
}
