package responses

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

// Compact implements the legacy Responses compact endpoint. The returned
// encrypted_content field is an opaque, process-local handle rather than a
// provider-owned ciphertext. It is accepted only from the caller that created
// it and only until the normal Responses store TTL expires.
func (h *Handler) Compact(w http.ResponseWriter, r *http.Request) {
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
	if err := h.expandLocalCompactionState(owner, req); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	h.serveLocalCompaction(w, r, rawBody, req, false)
}

// serveLocalCompaction runs the local history reduction shared by the legacy
// compact endpoint and the current Responses v2 compaction trigger. Resolving
// a session-scoped auth object lets the normal dynamic upstream limit lookup
// use the selected account without opening a DeepSeek chat session.
func (h *Handler) serveLocalCompaction(w http.ResponseWriter, r *http.Request, rawBody []byte, req map[string]any, responsesV2 bool) {
	a, err := h.Auth.DetermineWithSession(r, rawBody)
	if err != nil {
		status := http.StatusUnauthorized
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		writeOpenAIError(w, status, err.Error())
		return
	}
	defer h.Auth.Release(a)

	owner := responseStoreOwner(a)
	if owner == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	output, stdReq, summaryStats, err := h.buildLocalCompaction(r, a, owner, req, len(rawBody))
	if err != nil {
		status := http.StatusBadRequest
		code := ""
		if compactErr, ok := err.(localCompactionError); ok {
			status = compactErr.status
			code = compactErr.code
		}
		writeOpenAIErrorWithCode(w, status, err.Error(), code)
		return
	}

	if !responsesV2 {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         "resp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			"created_at": time.Now().Unix(),
			"object":     "response.compaction",
			"output":     output,
			"usage": map[string]any{
				"input_tokens": summaryStats.SummaryInputTokens,
				"input_tokens_details": map[string]any{
					"cached_tokens":      0,
					"cache_write_tokens": 0,
				},
				"output_tokens": summaryStats.SummaryOutputTokens,
				"output_tokens_details": map[string]any{
					"reasoning_tokens": 0,
				},
				"total_tokens": summaryStats.SummaryInputTokens + summaryStats.SummaryOutputTokens,
			},
		})
		return
	}

	responseID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	responseObj := openaifmt.BuildResponseObjectFromItems(
		responseID,
		stdReq.ResponseModel,
		stdReq.FinalPrompt,
		"",
		"",
		output,
		"",
	)
	h.getResponseStore().putInput(owner, responseID, stdReq.Messages)
	h.getResponseStore().put(owner, responseID, responseObj)
	if stdReq.Stream {
		writeLocalCompactionStream(w, responseObj, output)
		return
	}
	writeJSON(w, http.StatusOK, responseObj)
}

type localCompactionError struct {
	status int
	msg    string
	code   string
}

func (e localCompactionError) Error() string { return e.msg }

type missingLocalCompactionStateError struct{}

func (missingLocalCompactionStateError) Error() string {
	return "local compaction state was not found or has expired"
}

type recoveredLocalCompaction struct {
	Handle string
}

func (h *Handler) buildLocalCompaction(r *http.Request, a *auth.RequestAuth, owner string, req map[string]any, wireRequestBytes int) ([]any, promptcompat.StandardRequest, shared.SummaryCompactionStats, error) {
	// Instructions are sent separately by the Responses client on every turn.
	// Storing them inside the opaque state would prepend them a second time when
	// the handle is expanded in a later request.
	compactReq := cloneAnyMap(req)
	delete(compactReq, "instructions")
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, compactReq, requestTraceID(r))
	if err != nil {
		return nil, promptcompat.StandardRequest{}, shared.SummaryCompactionStats{}, localCompactionError{status: http.StatusBadRequest, msg: err.Error()}
	}

	limit := shared.PromptLimitSnapshot(h.Store)
	limit, dynamicApplied, err := shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, limit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses_compact", "error", err)
	}
	hardLimit := limit
	_, thresholdApplied, thresholdErr := shared.ResponsesCompactThreshold(compactReq)
	if thresholdErr != nil {
		return nil, promptcompat.StandardRequest{}, shared.SummaryCompactionStats{}, localCompactionError{status: http.StatusBadRequest, msg: thresholdErr.Error()}
	}
	// The standalone compact endpoint is itself an explicit request. Keep it
	// functional even when automatic over-limit history dropping is disabled.
	limit.AutoCompressEnable = true
	beforeMessages := len(stdReq.Messages)
	beforeStateBytes := responseStateSize(stdReq.Messages)
	beforePromptUnits := promptcompat.PromptUnits(stdReq.FinalPrompt)

	targetUnits := promptcompat.LimitForModel(limit, promptcompat.EffectiveModel(stdReq))
	if targetUnits > 0 {
		targetUnits = targetUnits * 80 / 100
	}
	var summaryStats shared.SummaryCompactionStats
	stdReq, summaryStats, compacted, compactErr := shared.TrySummaryCompactPrompt(r.Context(), h.DS, a, stdReq, hardLimit, targetUnits)
	if compactErr != nil || !compacted {
		if compactErr == nil {
			compactErr = shared.ErrSummaryCompactionNotReducible
		}
		detail := summaryCompactionErrorDetail(compactErr)
		config.Logger.Warn("[responses_compact] summary compaction failed",
			"model", stdReq.ResolvedModel,
			"wire_request_bytes", wireRequestBytes,
			"before_messages", beforeMessages,
			"before_state_bytes", beforeStateBytes,
			"before_prompt_units", beforePromptUnits,
			"summary_attempts", summaryStats.Attempts,
			"summary_duration_ms", summaryStats.Duration.Milliseconds(),
			"error", compactErr,
		)
		return nil, promptcompat.StandardRequest{}, summaryStats, localCompactionError{status: detail.Status, msg: detail.Message, code: detail.Code}
	}
	afterStateBytes := responseStateSize(stdReq.Messages)
	afterPromptUnits := promptcompat.PromptUnits(stdReq.FinalPrompt)
	if afterStateBytes >= beforeStateBytes || afterPromptUnits >= beforePromptUnits {
		config.Logger.Error("[responses_compact] rejected compact state that did not shrink",
			"model", stdReq.ResolvedModel,
			"wire_request_bytes", wireRequestBytes,
			"before_messages", beforeMessages,
			"after_messages", len(stdReq.Messages),
			"before_state_bytes", beforeStateBytes,
			"after_state_bytes", afterStateBytes,
			"before_prompt_units", beforePromptUnits,
			"after_prompt_units", afterPromptUnits,
		)
		return nil, promptcompat.StandardRequest{}, summaryStats, localCompactionError{
			status: http.StatusUnprocessableEntity,
			msg:    "local compaction did not reduce the stored context",
		}
	}
	if errMsg := shared.EnforcePromptLimit(limit, stdReq); errMsg != "" {
		return nil, promptcompat.StandardRequest{}, summaryStats, localCompactionError{status: http.StatusRequestEntityTooLarge, msg: errMsg}
	}
	config.Logger.Info("[responses_compact] compacted Responses history",
		"model", stdReq.ResolvedModel,
		"wire_request_bytes", wireRequestBytes,
		"before_messages", beforeMessages,
		"after_messages", len(stdReq.Messages),
		"dropped_messages", beforeMessages-len(stdReq.Messages),
		"before_state_bytes", beforeStateBytes,
		"after_state_bytes", afterStateBytes,
		"state_reduction_percent", reductionPercent(beforeStateBytes, afterStateBytes),
		"before_prompt_units", beforePromptUnits,
		"after_prompt_units", afterPromptUnits,
		"prompt_reduction_percent", reductionPercent(beforePromptUnits, afterPromptUnits),
		"summary_source_units", summaryStats.SourcePromptUnits,
		"summary_output_units", summaryStats.SummaryUnits,
		"summary_input_tokens", summaryStats.SummaryInputTokens,
		"summary_output_tokens", summaryStats.SummaryOutputTokens,
		"summary_used_hidden_output", summaryStats.UsedThinkingFallback,
		"summary_retained_turns", summaryStats.RetainedTurns,
		"summary_attempts", summaryStats.Attempts,
		"summary_duration_ms", summaryStats.Duration.Milliseconds(),
		"dynamic_upstream_limit", dynamicApplied,
		"compact_threshold", thresholdApplied,
	)

	_, retained, splitOK := shared.SplitSummaryCompactionWindow(stdReq.Messages)
	if !splitOK {
		return nil, promptcompat.StandardRequest{}, summaryStats, localCompactionError{status: http.StatusInternalServerError, msg: "failed to split compacted context window"}
	}
	handle := h.getResponseStore().putCompaction(owner, stdReq.Messages)
	if handle == "" {
		return nil, promptcompat.StandardRequest{}, summaryStats, localCompactionError{status: http.StatusInternalServerError, msg: "failed to store local compaction state"}
	}
	item := newLocalCompactionItem(handle)
	output := responsesCompactedWindowItems(retained)
	output = append(output, item)
	return output, stdReq, summaryStats, nil
}

func summaryCompactionErrorDetail(err error) shared.UpstreamErrorDetail {
	if errors.Is(err, shared.ErrSummaryCompactionNotReducible) {
		return shared.UpstreamErrorDetail{
			Status:       http.StatusUnprocessableEntity,
			Message:      err.Error(),
			Code:         "compaction_not_reducible",
			FinishReason: "compaction_not_reducible",
		}
	}
	return shared.CompletionErrorDetail(err)
}

func newLocalCompactionItem(handle string) map[string]any {
	if strings.TrimSpace(handle) == "" {
		return nil
	}
	return map[string]any{
		"id":                "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":              "compaction",
		"encrypted_content": handle,
	}
}

func localCompactionOutputPrefix(item map[string]any) []any {
	if item == nil {
		return nil
	}
	return []any{item}
}

func writeLocalCompactionStream(w http.ResponseWriter, responseObj map[string]any, output []any) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	responseID, _ := responseObj["id"].(string)
	model, _ := responseObj["model"].(string)
	sequence := 0
	send := func(event string, payload map[string]any) {
		sequence++
		payload["sequence_number"] = sequence
		body, _ := json.Marshal(payload)
		_, _ = w.Write([]byte("event: " + event + "\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(body)
		_, _ = w.Write([]byte("\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	send("response.created", openaifmt.BuildResponsesCreatedPayload(responseID, model))
	for outputIndex, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemID := strings.TrimSpace(responseString(item["id"]))
		if itemID == "" {
			itemID = "item_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			item["id"] = itemID
		}
		send("response.output_item.added", openaifmt.BuildResponsesOutputItemAddedPayload(responseID, itemID, outputIndex, item))
		send("response.output_item.done", openaifmt.BuildResponsesOutputItemDonePayload(responseID, itemID, outputIndex, item))
	}
	send("response.completed", openaifmt.BuildResponsesCompletedPayload(responseObj))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func responsesCompactedWindowItems(messages []any) []any {
	output := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(responseString(message["role"])))
		content := strings.TrimSpace(promptcompat.NormalizeOpenAIContentForPrompt(message["content"]))
		switch role {
		case "tool", "function":
			callID := strings.TrimSpace(responseString(message["tool_call_id"]))
			if callID == "" {
				callID = strings.TrimSpace(responseString(message["call_id"]))
			}
			output = append(output, map[string]any{
				"id":      "fco_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
				"type":    "function_call_output",
				"call_id": callID,
				"output":  content,
			})
		case "assistant":
			if content != "" {
				output = append(output, compactedMessageItem(role, "output_text", content))
			}
			if calls, ok := message["tool_calls"].([]any); ok {
				for _, rawCall := range calls {
					call, _ := rawCall.(map[string]any)
					function, _ := call["function"].(map[string]any)
					name := strings.TrimSpace(responseString(function["name"]))
					if name == "" {
						name = strings.TrimSpace(responseString(call["name"]))
					}
					if name == "" {
						continue
					}
					callID := strings.TrimSpace(responseString(call["id"]))
					if callID == "" {
						callID = "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
					}
					arguments := strings.TrimSpace(responseString(function["arguments"]))
					if arguments == "" {
						arguments = strings.TrimSpace(responseString(call["arguments"]))
					}
					if arguments == "" {
						arguments = "{}"
					}
					output = append(output, map[string]any{
						"id":        "fc_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
						"type":      "function_call",
						"call_id":   callID,
						"name":      name,
						"arguments": arguments,
						"status":    "completed",
					})
				}
			}
		case "user", "system", "developer":
			if content != "" {
				output = append(output, compactedMessageItem(role, "input_text", content))
			}
		}
	}
	return output
}

func compactedMessageItem(role, contentType, content string) map[string]any {
	return map[string]any{
		"id":     "msg_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":   "message",
		"role":   role,
		"status": "completed",
		"content": []any{map[string]any{
			"type": contentType,
			"text": content,
		}},
	}
}

func (h *Handler) expandLocalCompactionState(owner string, req map[string]any) error {
	_, err := h.expandLocalCompactionStateWithRecovery(owner, req)
	return err
}

func (h *Handler) expandLocalCompactionStateWithRecovery(owner string, req map[string]any) (recoveredLocalCompaction, error) {
	if req == nil {
		return recoveredLocalCompaction{}, nil
	}
	var recovered recoveredLocalCompaction
	for _, key := range []string{"input", "messages"} {
		raw, ok := req[key]
		if !ok {
			continue
		}
		expanded, changed, handle, err := h.expandLocalCompactionValue(owner, raw)
		if err != nil {
			return recoveredLocalCompaction{}, err
		}
		if changed {
			req[key] = expanded
		}
		if recovered.Handle == "" {
			recovered.Handle = handle
		}
	}
	return recovered, nil
}

func (h *Handler) expandLocalCompactionValue(owner string, raw any) (any, bool, string, error) {
	if item, ok := raw.(map[string]any); ok {
		if messages, handled, err := h.expandLocalCompactionItem(owner, item); handled || err != nil {
			return messages, handled, "", err
		}
		return raw, false, "", nil
	}
	items, ok := raw.([]any)
	if !ok {
		return raw, false, "", nil
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		messages, handled, err := h.expandLocalCompactionItem(owner, item)
		if err != nil {
			if _, missing := err.(missingLocalCompactionStateError); !missing {
				return nil, false, "", err
			}
			// Preserve the newly-added tail when an old process-local handle is
			// gone so the client can establish a fresh compact state.
			tail, _, _, tailErr := h.expandLocalCompactionValue(owner, items[index+1:])
			if tailErr != nil {
				return nil, false, "", tailErr
			}
			tailItems, _ := tail.([]any)
			if len(tailItems) == 0 {
				return nil, false, "", err
			}
			handle := strings.TrimSpace(responseString(item["encrypted_content"]))
			config.Logger.Warn("[responses_compact] recovered from missing local state using fresh input",
				"owner_fingerprint", responseStateFingerprint(owner),
				"fresh_items", len(tailItems),
				"fresh_context_bytes", responseStateSize(tailItems),
			)
			return tailItems, true, handle, nil
		}
		if !handled {
			continue
		}
		// Codex v2 keeps a small retained window before the compaction item
		// and appends fresh user input after it. The local compacted window
		// already contains the retained history, so replace everything through
		// the handle and keep only the later, newly-added items. This avoids
		// duplicating the retained messages on the next completion.
		tail, _, _, err := h.expandLocalCompactionValue(owner, items[index+1:])
		if err != nil {
			return nil, false, "", err
		}
		out := cloneAnySlice(messages)
		tailItems, _ := tail.([]any)
		if len(tailItems) > 0 {
			out = append(out, tailItems...)
		}
		config.Logger.Info("[responses_compact] expanded local state",
			"owner_fingerprint", responseStateFingerprint(owner),
			"handle_fingerprint", responseStateFingerprint(responseString(item["encrypted_content"])),
			"stored_messages", len(messages),
			"stored_context_bytes", responseStateSize(messages),
			"fresh_tail_items", len(tailItems),
			"fresh_tail_bytes", responseStateSize(tailItems),
			"expanded_messages", len(out),
			"expanded_context_bytes", responseStateSize(out),
		)
		return out, true, "", nil
	}
	return raw, false, "", nil
}

func (h *Handler) recoverExpiredCompaction(stdReq *promptcompat.StandardRequest, recovered recoveredLocalCompaction, limit config.PromptLimitSettings) (int, bool) {
	if recovered.Handle == "" || stdReq == nil {
		return 0, false
	}
	before := len(stdReq.Messages)
	candidate := *stdReq
	candidate.FinalPrompt = promptcompat.BuildIncrementalPrompt(candidate.Messages, candidate.IncrementalFormatPrompt, candidate.Thinking)
	if shared.EnforcePromptLimit(limit, candidate) == "" {
		*stdReq = candidate
		return 0, true
	}

	keepRecent := limit.KeepRecentTurns
	if keepRecent < 1 {
		keepRecent = 1
	}
	for keep := keepRecent; keep >= 1; keep /= 2 {
		messages, changed := promptcompat.CompressMessages(stdReq.Messages, limit.KeepSystemMessage, keep)
		if !changed {
			continue
		}
		candidate = *stdReq
		candidate.Messages = messages
		candidate.FinalPrompt = promptcompat.BuildIncrementalPrompt(messages, candidate.IncrementalFormatPrompt, candidate.Thinking)
		if shared.EnforcePromptLimit(limit, candidate) == "" {
			*stdReq = candidate
			return before - len(messages), true
		}
	}
	return 0, false
}

func (h *Handler) expandLocalCompactionItem(owner string, item map[string]any) ([]any, bool, error) {
	typ := strings.ToLower(strings.TrimSpace(responseString(item["type"])))
	switch typ {
	case "compaction", "compaction_summary", "context_compaction":
	default:
		return nil, false, nil
	}
	handle := strings.TrimSpace(responseString(item["encrypted_content"]))
	if !strings.HasPrefix(handle, localCompactionHandlePrefix) {
		return nil, false, nil
	}
	messages, ok := h.getResponseStore().getCompaction(owner, handle)
	if !ok {
		return nil, false, missingLocalCompactionStateError{}
	}
	return messages, true, nil
}

func reductionPercent(before, after int) int {
	if before <= 0 || after >= before {
		return 0
	}
	return (before - after) * 100 / before
}

func removeCompactionTriggers(req map[string]any) bool {
	if req == nil {
		return false
	}
	found := false
	for _, key := range []string{"input", "messages"} {
		raw, ok := req[key]
		if !ok {
			continue
		}
		cleaned, removed := removeCompactionTriggerValue(raw)
		if removed {
			req[key] = cleaned
			found = true
		}
	}
	return found
}

func removeCompactionTriggerValue(raw any) (any, bool) {
	if item, ok := raw.(map[string]any); ok {
		if strings.EqualFold(strings.TrimSpace(responseString(item["type"])), "compaction_trigger") {
			return []any{}, true
		}
		return raw, false
	}
	items, ok := raw.([]any)
	if !ok {
		return raw, false
	}
	out := make([]any, 0, len(items))
	removed := false
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && strings.EqualFold(strings.TrimSpace(responseString(m["type"])), "compaction_trigger") {
			removed = true
			continue
		}
		out = append(out, item)
	}
	return out, removed
}
