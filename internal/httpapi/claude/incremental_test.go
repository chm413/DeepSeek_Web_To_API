package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	openaishared "DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

type claudeIncrementalAuthStub struct{}

type claudeRotationStoreStub struct{ streamStatusClaudeStoreStub }

func (claudeRotationStoreStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.IncrementalMaxTurns = 2
	cfg.IncrementalRotationKeepRecent = 1
	return cfg
}

func (claudeIncrementalAuthStub) requestAuth() *auth.RequestAuth {
	return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller:claude-inc", AccountID: "account-1", SessionKey: "claude-session", TriedAccounts: map[string]bool{}}
}

func (s claudeIncrementalAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s claudeIncrementalAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s claudeIncrementalAuthStub) DetermineWithSession(_ *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (claudeIncrementalAuthStub) Release(_ *auth.RequestAuth) {}

type claudeIncrementalDSStub struct {
	createCalls int
	normal      []map[string]any
	pinned      []map[string]any
}

func (s *claudeIncrementalDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	s.createCalls++
	return "claude-remote-session", nil
}

func (*claudeIncrementalDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (s *claudeIncrementalDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normal = append(s.normal, cloneClaudeIncrementalPayload(payload))
	return claudeIncrementalSSE(501, "first answer"), nil
}

func (s *claudeIncrementalDSStub) CallCompletionPinned(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.pinned = append(s.pinned, cloneClaudeIncrementalPayload(payload))
	return claudeIncrementalSSE(502, "second answer"), nil
}

func (*claudeIncrementalDSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*claudeIncrementalDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func TestClaudeIncrementalReusesSessionAndSendsOnlyDelta(t *testing.T) {
	ds := &claudeIncrementalDSStub{}
	h := &Handler{
		Store:       streamStatusClaudeStoreStub{},
		Auth:        claudeIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	first := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"thinking":   map[string]any{"type": "disabled"},
		"messages":   []any{map[string]any{"role": "user", "content": "first question"}},
	}
	firstResponse := serveClaudeIncremental(t, h, first)
	content, ok := firstResponse["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing first response content: %#v", firstResponse)
	}
	second := map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"thinking":   map[string]any{"type": "disabled"},
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"role": "assistant", "content": content},
			map[string]any{"role": "user", "content": "second question"},
		},
	}
	normalizedSecond, err := normalizeClaudeRequest(h.Store, cloneMap(second))
	if err != nil {
		t.Fatal(err)
	}
	applyClaudeDirectThinkingPolicy(&normalizedSecond, second)
	probeAuth := claudeIncrementalAuthStub{}.requestAuth()
	probe, hit := h.Incremental.Prepare(openaishared.IncrementalScope(probeAuth, normalizedSecond.Standard), normalizedSecond.Standard.Messages)
	if !hit {
		t.Fatalf("first Claude response was not recorded for incremental reuse; messages=%#v", normalizedSecond.Standard.Messages)
	}
	probe.Release()
	serveClaudeIncremental(t, h, second)

	if ds.createCalls != 1 || len(ds.normal) != 1 || len(ds.pinned) != 1 {
		t.Fatalf("unexpected calls: create=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	payload := ds.pinned[0]
	if payload["chat_session_id"] != "claude-remote-session" {
		t.Fatalf("unexpected session: %#v", payload["chat_session_id"])
	}
	if parent, ok := payload["parent_message_id"].(float64); !ok || int(parent) != 501 {
		t.Fatalf("unexpected parent: %#v", payload["parent_message_id"])
	}
	prompt, _ := payload["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second question"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing %q in prompt: %q", expected, prompt)
		}
	}
	for _, forbidden := range []string{"first question", "first answer"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("unexpected replay of %q: %q", forbidden, prompt)
		}
	}
}

func TestClaudeIncrementalRotatesIntoFreshRootSession(t *testing.T) {
	ds := &claudeIncrementalDSStub{}
	h := &Handler{
		Store:       claudeRotationStoreStub{},
		Auth:        claudeIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	base := map[string]any{"model": "claude-sonnet-4-5", "max_tokens": 1024, "thinking": map[string]any{"type": "disabled"}}
	firstMessages := []any{map[string]any{"role": "user", "content": "first question"}}
	firstReq := cloneMap(base)
	firstReq["messages"] = firstMessages
	first := serveClaudeIncremental(t, h, firstReq)
	firstContent, _ := first["content"].([]any)
	secondMessages := append([]any{}, firstMessages...)
	secondMessages = append(secondMessages,
		map[string]any{"role": "assistant", "content": firstContent},
		map[string]any{"role": "user", "content": "second question"})
	secondReq := cloneMap(base)
	secondReq["messages"] = secondMessages
	second := serveClaudeIncremental(t, h, secondReq)
	secondContent, _ := second["content"].([]any)
	thirdMessages := append([]any{}, secondMessages...)
	thirdMessages = append(thirdMessages,
		map[string]any{"role": "assistant", "content": secondContent},
		map[string]any{"role": "user", "content": "third question"})
	thirdReq := cloneMap(base)
	thirdReq["messages"] = thirdMessages
	serveClaudeIncremental(t, h, thirdReq)

	if ds.createCalls != 2 || len(ds.normal) != 2 || len(ds.pinned) != 1 {
		t.Fatalf("expected full, pinned, rollover calls; creates=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	rollover := ds.normal[1]
	if rollover["parent_message_id"] != nil {
		t.Fatalf("Claude rollover must start at root: %#v", rollover)
	}
	prompt, _ := rollover["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second answer", "third question"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Claude rollover prompt missing %q: %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "first question") {
		t.Fatalf("Claude rollover retained compacted history: %q", prompt)
	}
}

func serveClaudeIncremental(t *testing.T, h *Handler, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func claudeIncrementalSSE(messageID int, text string) *http.Response {
	id, _ := json.Marshal(messageID)
	content, _ := json.Marshal(text)
	body := `data: {"response_message_id":` + string(id) + "}\n" +
		`data: {"p":"response/content","v":` + string(content) + "}\n" +
		"data: [DONE]\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func cloneClaudeIncrementalPayload(payload map[string]any) map[string]any {
	b, _ := json.Marshal(payload)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
