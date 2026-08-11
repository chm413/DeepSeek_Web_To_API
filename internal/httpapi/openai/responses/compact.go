package responses

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

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
	item, stdReq, err := h.buildLocalCompaction(r, a, owner, req)
	if err != nil {
		status := http.StatusBadRequest
		if compactErr, ok := err.(localCompactionError); ok {
			status = compactErr.status
		}
		writeOpenAIError(w, status, err.Error())
		return
	}

	if !responsesV2 {
		writeJSON(w, http.StatusOK, map[string]any{"output": []any{item}})
		return
	}

	responseID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	responseObj := openaifmt.BuildResponseObjectFromItems(
		responseID,
		stdReq.ResponseModel,
		stdReq.FinalPrompt,
		"",
		"",
		[]any{item},
		"",
	)
	h.getResponseStore().putInput(owner, responseID, stdReq.Messages)
	h.getResponseStore().put(owner, responseID, responseObj)
	if stdReq.Stream {
		writeLocalCompactionStream(w, responseObj, item)
		return
	}
	writeJSON(w, http.StatusOK, responseObj)
}

type localCompactionError struct {
	status int
	msg    string
}

func (e localCompactionError) Error() string { return e.msg }

type missingLocalCompactionStateError struct{}

func (missingLocalCompactionStateError) Error() string {
	return "local compaction state was not found or has expired"
}

func (h *Handler) buildLocalCompaction(r *http.Request, a *auth.RequestAuth, owner string, req map[string]any) (map[string]any, promptcompat.StandardRequest, error) {
	// Instructions are sent separately by the Responses client on every turn.
	// Storing them inside the opaque state would prepend them a second time when
	// the handle is expanded in a later request.
	compactReq := cloneAnyMap(req)
	delete(compactReq, "instructions")
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, compactReq, requestTraceID(r))
	if err != nil {
		return nil, promptcompat.StandardRequest{}, localCompactionError{status: http.StatusBadRequest, msg: err.Error()}
	}

	limit := shared.PromptLimitSnapshot(h.Store)
	limit, dynamicApplied, err := shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, limit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses_compact", "error", err)
	}
	limit, thresholdApplied := shared.ApplyResponsesCompactThreshold(compactReq, limit, stdReq.ResolvedModel)
	// The standalone compact endpoint is itself an explicit request. Keep it
	// functional even when automatic over-limit history dropping is disabled.
	limit.AutoCompressEnable = true
	before := len(stdReq.Messages)
	stdReq, compacted := promptcompat.CompressToFit(limit, stdReq)
	if compacted {
		config.Logger.Info("[responses_compact] locally compacted Responses history",
			"model", stdReq.ResolvedModel,
			"dropped_messages", before-len(stdReq.Messages),
			"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
			"dynamic_upstream_limit", dynamicApplied,
			"compact_threshold", thresholdApplied,
		)
	}
	if errMsg := shared.EnforcePromptLimit(limit, stdReq); errMsg != "" {
		return nil, promptcompat.StandardRequest{}, localCompactionError{status: http.StatusRequestEntityTooLarge, msg: errMsg}
	}

	handle := h.getResponseStore().putCompaction(owner, stdReq.Messages)
	if handle == "" {
		return nil, promptcompat.StandardRequest{}, localCompactionError{status: http.StatusInternalServerError, msg: "failed to store local compaction state"}
	}
	return map[string]any{
		"type":              "compaction",
		"encrypted_content": handle,
	}, stdReq, nil
}

func writeLocalCompactionStream(w http.ResponseWriter, responseObj, item map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	responseID, _ := responseObj["id"].(string)
	model, _ := responseObj["model"].(string)
	itemID := "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	send("response.output_item.added", openaifmt.BuildResponsesOutputItemAddedPayload(responseID, itemID, 0, item))
	send("response.output_item.done", openaifmt.BuildResponsesOutputItemDonePayload(responseID, itemID, 0, item))
	send("response.completed", openaifmt.BuildResponsesCompletedPayload(responseObj))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *Handler) expandLocalCompactionState(owner string, req map[string]any) error {
	if req == nil {
		return nil
	}
	for _, key := range []string{"input", "messages"} {
		raw, ok := req[key]
		if !ok {
			continue
		}
		expanded, changed, err := h.expandLocalCompactionValue(owner, raw)
		if err != nil {
			return err
		}
		if changed {
			req[key] = expanded
		}
	}
	return nil
}

func (h *Handler) expandLocalCompactionValue(owner string, raw any) (any, bool, error) {
	if item, ok := raw.(map[string]any); ok {
		if messages, handled, err := h.expandLocalCompactionItem(owner, item); handled || err != nil {
			return messages, handled, err
		}
		return raw, false, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return raw, false, nil
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		messages, handled, err := h.expandLocalCompactionItem(owner, item)
		if err != nil {
			if _, missing := err.(missingLocalCompactionStateError); !missing {
				return nil, false, err
			}
			// Preserve the newly-added tail when an old process-local handle is
			// gone so the client can establish a fresh compact state.
			tail, _, tailErr := h.expandLocalCompactionValue(owner, items[index+1:])
			if tailErr != nil {
				return nil, false, tailErr
			}
			tailItems, _ := tail.([]any)
			if len(tailItems) == 0 {
				return nil, false, err
			}
			config.Logger.Warn("[responses_compact] recovered from missing local state using fresh input",
				"owner_fingerprint", responseStateFingerprint(owner),
				"fresh_items", len(tailItems),
				"fresh_context_bytes", responseStateSize(tailItems),
			)
			return tailItems, true, nil
		}
		if !handled {
			continue
		}
		// Codex v2 keeps a small retained window before the compaction item
		// and appends fresh user input after it. The local compacted window
		// already contains the retained history, so replace everything through
		// the handle and keep only the later, newly-added items. This avoids
		// duplicating the retained messages on the next completion.
		tail, _, err := h.expandLocalCompactionValue(owner, items[index+1:])
		if err != nil {
			return nil, false, err
		}
		out := cloneAnySlice(messages)
		if tailItems, ok := tail.([]any); ok {
			out = append(out, tailItems...)
		}
		return out, true, nil
	}
	return raw, false, nil
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
