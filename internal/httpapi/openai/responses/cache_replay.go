package responses

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

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
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var req map[string]any
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	if _, err := h.expandLocalCompactionStateWithRecovery(owner, req); err != nil {
		return
	}
	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		return
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, req, shared.RequestTraceID(r))
	if err != nil {
		return
	}
	stdReq = shared.ApplyThinkingInjection(h.Store, stdReq)
	h.getResponseStore().putInputState(owner, responseID, stdReq.Messages,
		stdReq.ToolsRaw, stdReq.HasTools, stdReq.ToolChoiceRaw, stdReq.HasToolChoice)
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
