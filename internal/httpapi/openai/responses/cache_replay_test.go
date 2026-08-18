package responses

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/responsecache"
)

func TestOnProtocolResponseCacheHitStoresNonStreamResponse(t *testing.T) {
	store, resolver := newDirectTokenResolver(t)
	h := &Handler{Store: store, Auth: resolver}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer token-a")

	h.OnProtocolResponseCacheHit(req, responsecache.Entry{
		Status: http.StatusOK,
		Body:   []byte(`{"id":"resp_cached","object":"response","status":"completed"}`),
	}, "memory")

	owner := responseStoreOwner(authForToken(t, resolver, "token-a"))
	got, ok := h.getResponseStore().get(owner, "resp_cached")
	if !ok {
		t.Fatal("expected cached response to be stored")
	}
	if got["status"] != "completed" {
		t.Fatalf("unexpected stored response: %#v", got)
	}
}

func TestOnProtocolResponseCacheHitStoresStreamCompletedResponse(t *testing.T) {
	store, resolver := newDirectTokenResolver(t)
	h := &Handler{Store: store, Auth: resolver}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer token-a")
	body := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream_cached\",\"object\":\"response\",\"status\":\"completed\"}}\n\n" +
		"data: [DONE]\n\n")

	h.OnProtocolResponseCacheHit(req, responsecache.Entry{
		Status: http.StatusOK,
		Body:   body,
	}, "disk")

	owner := responseStoreOwner(authForToken(t, resolver, "token-a"))
	if _, ok := h.getResponseStore().get(owner, "resp_stream_cached"); !ok {
		t.Fatal("expected cached stream response to be stored")
	}
}

func TestOnProtocolResponseCacheHitStoresToolContractSnapshot(t *testing.T) {
	store, resolver := newDirectTokenResolver(t)
	h := &Handler{Store: store, Auth: resolver}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "input":"inspect the issue",
  "tools":[{"type":"function","function":{"name":"lookup_issue","description":"Find an issue","parameters":{"type":"object"}}}],
  "tool_choice":"auto"
}`))
	req.Header.Set("Authorization", "Bearer token-a")

	h.OnProtocolResponseCacheHit(req, responsecache.Entry{
		Status: http.StatusOK,
		Body:   []byte(`{"id":"resp_cached_tools","object":"response","status":"completed","output":[]}`),
	}, "memory")

	owner := responseStoreOwner(authForToken(t, resolver, "token-a"))
	state, ok := h.getResponseStore().getInputState(owner, "resp_cached_tools")
	if !ok || !state.HasTools || !state.HasToolChoice {
		t.Fatalf("expected cached request tool contract, got %#v", state)
	}
	followUp := map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": "resp_cached_tools",
		"input":                "continue",
	}
	if err := h.mergePreviousResponseInput(owner, followUp); err != nil {
		t.Fatalf("merge cached previous response: %v", err)
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(store, followUp, "")
	if err != nil {
		t.Fatalf("normalize cached follow-up: %v", err)
	}
	if !strings.Contains(stdReq.IncrementalFormatPrompt, "Tool: lookup_issue") {
		t.Fatalf("cached tool contract was not restored: %q", stdReq.IncrementalFormatPrompt)
	}
}

func TestOnProtocolResponseCacheHitIgnoresOtherPaths(t *testing.T) {
	store, resolver := newDirectTokenResolver(t)
	h := &Handler{Store: store, Auth: resolver}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer token-a")

	h.OnProtocolResponseCacheHit(req, responsecache.Entry{
		Status: http.StatusOK,
		Body:   []byte(`{"id":"resp_cached","object":"response"}`),
	}, "memory")

	owner := responseStoreOwner(authForToken(t, resolver, "token-a"))
	if _, ok := h.getResponseStore().get(owner, "resp_cached"); ok {
		t.Fatal("expected non-responses path to be ignored")
	}
}
