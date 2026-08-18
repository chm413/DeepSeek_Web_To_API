package responses

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/responsecache"
)

func (h *Handler) OnProtocolResponseCacheHit(r *http.Request, entry responsecache.Entry, _ string) {
	if h == nil || h.Auth == nil || r == nil || r.URL == nil || strings.TrimSpace(r.URL.Path) != "/v1/responses" {
		return
	}
	a, err := h.Auth.DetermineCaller(r)
	if err != nil {
		return
	}
	owner := responseStoreOwner(a)
	if owner == "" {
		return
	}
	obj := cachedResponseObject(entry.Body)
	if obj == nil {
		return
	}
	id, _ := obj["id"].(string)
	if strings.TrimSpace(id) == "" {
		return
	}
	h.getResponseStore().put(owner, id, obj)
	config.Logger.Info("[responses_cache] replay hit",
		"owner_fingerprint", responseStateFingerprint(owner),
		"response_id_fingerprint", responseStateFingerprint(id),
		"cached_body_bytes", len(entry.Body),
		"stream_object", bytes.Contains(entry.Body, []byte("data:")),
	)
	h.recordCachedResponseInput(owner, id, r)
}

// recordCachedResponseInput keeps a cache hit usable as a previous_response_id
// parent. The response cache has the current request body, while the response
// object alone cannot recover its canonical input or tool contract.
func (h *Handler) recordCachedResponseInput(owner, responseID string, r *http.Request) {
	if h == nil || r == nil || r.Body == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(responseID) == "" {
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		config.Logger.Warn("[responses_cache] unable to restore cached input",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(responseID),
			"stage", "read_request_body", "error", err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req map[string]any
	if json.Unmarshal(raw, &req) != nil {
		config.Logger.Warn("[responses_cache] unable to restore cached input",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(responseID),
			"stage", "decode_request_body", "wire_request_bytes", len(raw))
		return
	}
	if _, err := h.expandLocalCompactionStateWithRecovery(owner, req); err != nil {
		config.Logger.Warn("[responses_cache] unable to restore cached input",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(responseID),
			"stage", "expand_compaction", "wire_request_bytes", len(raw), "error", err)
		return
	}
	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		config.Logger.Warn("[responses_cache] unable to restore cached input",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(responseID),
			"stage", "merge_previous_response", "wire_request_bytes", len(raw), "error", err)
		return
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, req, shared.RequestTraceID(r))
	if err != nil {
		config.Logger.Warn("[responses_cache] unable to restore cached input",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(responseID),
			"stage", "normalize_request", "wire_request_bytes", len(raw), "error", err)
		return
	}
	stdReq = shared.ApplyThinkingInjection(h.Store, stdReq)
	h.getResponseStore().putInputState(owner, responseID, stdReq.Messages,
		stdReq.ToolsRaw, stdReq.HasTools, stdReq.ToolChoiceRaw, stdReq.HasToolChoice)
	config.Logger.Info("[responses_cache] restored canonical input",
		"owner_fingerprint", responseStateFingerprint(owner),
		"response_id_fingerprint", responseStateFingerprint(responseID),
		"wire_request_bytes", len(raw),
		"messages", len(stdReq.Messages),
		"context_bytes", responseStateSize(stdReq.Messages),
		"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
		"tools_present", stdReq.HasTools,
		"tool_count", responseToolCount(stdReq.ToolsRaw),
		"tool_choice_present", stdReq.HasToolChoice,
		"tool_contract_fingerprint", responseToolContractFingerprint(
			stdReq.ToolsRaw, stdReq.HasTools, stdReq.ToolChoiceRaw, stdReq.HasToolChoice),
	)
}

func cachedResponseObject(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err == nil {
		if id, _ := obj["id"].(string); strings.TrimSpace(id) != "" {
			return obj
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	maxToken := len(body) + 1
	if maxToken < 64*1024 {
		maxToken = 64 * 1024
	}
	scanner.Buffer(make([]byte, 0, 1024), maxToken)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		resp, ok := event["response"].(map[string]any)
		if !ok {
			continue
		}
		if id, _ := resp["id"].(string); strings.TrimSpace(id) != "" {
			return resp
		}
	}
	return nil
}
