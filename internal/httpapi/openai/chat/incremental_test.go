package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	geminiapi "DeepSeek_Web_To_API/internal/httpapi/gemini"
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

type incrementalAuthStub struct{}

func (incrementalAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller:test", SessionKey: "session:key", TriedAccounts: map[string]bool{}}, nil
}

func (s incrementalAuthStub) DetermineCaller(r *http.Request) (*auth.RequestAuth, error) {
	return s.Determine(r)
}

func (s incrementalAuthStub) DetermineWithSession(r *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.Determine(r)
}

func (incrementalAuthStub) Release(_ *auth.RequestAuth) {}

type incrementalDSStub struct {
	createCalls    int
	normalPayloads []map[string]any
	pinnedPayloads []map[string]any
	pinnedErr      error
}

func (s *incrementalDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	s.createCalls++
	return fmt.Sprintf("remote-session-%d", s.createCalls), nil
}

func (*incrementalDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (*incrementalDSStub) GetPowPinned(_ context.Context, _ *auth.RequestAuth) (string, error) {
	return "pow", nil
}

func (*incrementalDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-1", Status: "uploaded"}, nil
}

func (s *incrementalDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normalPayloads = append(s.normalPayloads, clonePayloadForTest(payload))
	return incrementalResponse(101, "first answer"), nil
}

func (s *incrementalDSStub) CallCompletionPinned(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.pinnedPayloads = append(s.pinnedPayloads, clonePayloadForTest(payload))
	if s.pinnedErr != nil {
		return nil, s.pinnedErr
	}
	return incrementalResponse(202, "second answer"), nil
}

func (*incrementalDSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func TestChatIncrementalPinnedFailureFallsBackToFullReplay(t *testing.T) {
	ds := &incrementalDSStub{pinnedErr: errors.New("stored upstream branch expired")}
	h := &Handler{
		Store:       mockOpenAIConfig{},
		Auth:        incrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}

	serveIncrementalChat(t, h, map[string]any{
		"model":  "deepseek-v4-flash",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
		},
	})
	serveIncrementalChat(t, h, map[string]any{
		"model":  "deepseek-v4-flash",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second question"},
		},
	})

	if ds.createCalls != 2 {
		t.Fatalf("expected fallback to create a fresh upstream session, got %d creates", ds.createCalls)
	}
	if len(ds.pinnedPayloads) != 1 || len(ds.normalPayloads) != 2 {
		t.Fatalf("unexpected completion calls: normal=%d pinned=%d", len(ds.normalPayloads), len(ds.pinnedPayloads))
	}
	fallback := ds.normalPayloads[1]
	if fallback["chat_session_id"] != "remote-session-2" {
		t.Fatalf("expected fresh fallback session, got %#v", fallback["chat_session_id"])
	}
	if fallback["parent_message_id"] != nil {
		t.Fatalf("full replay must start at the root, got parent %#v", fallback["parent_message_id"])
	}
	prompt, _ := fallback["prompt"].(string)
	for _, expected := range []string{"first question", "first answer", "second question"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("full replay fallback missing %q: %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "Incremental response format requirements") {
		t.Fatalf("full replay fallback unexpectedly used the incremental wrapper: %q", prompt)
	}
}

func TestGeminiProxyUsesChatIncrementalLane(t *testing.T) {
	ds := &incrementalDSStub{}
	chatHandler := &Handler{
		Store:       mockOpenAIConfig{},
		Auth:        incrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	geminiHandler := &geminiapi.Handler{
		Store:  mockOpenAIConfig{},
		Auth:   incrementalAuthStub{},
		OpenAI: chatHandler,
	}
	router := chi.NewRouter()
	geminiapi.RegisterRoutes(router, geminiHandler)

	serveGemini := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected Gemini status %d: %s", rec.Code, rec.Body.String())
		}
		return rec
	}
	first := serveGemini(`{"contents":[{"role":"user","parts":[{"text":"first question"}]}]}`)
	if contentType := first.Header().Get("Content-Type"); !strings.Contains(contentType, "charset=utf-8") {
		t.Fatalf("Gemini response must explicitly declare UTF-8, got %q", contentType)
	}
	serveGemini(`{"contents":[{"role":"user","parts":[{"text":"first question"}]},{"role":"model","parts":[{"thought":true,"text":"private reasoning that must not be replayed"},{"text":"first answer"}]},{"role":"user","parts":[{"text":"second question"}]}]}`)

	if ds.createCalls != 1 || len(ds.normalPayloads) != 1 || len(ds.pinnedPayloads) != 1 {
		t.Fatalf("expected Gemini to reuse the Chat incremental lane, creates=%d normal=%d pinned=%d", ds.createCalls, len(ds.normalPayloads), len(ds.pinnedPayloads))
	}
	prompt, _ := ds.pinnedPayloads[0]["prompt"].(string)
	if !strings.Contains(prompt, "Incremental response format requirements") || !strings.Contains(prompt, "second question") {
		t.Fatalf("Gemini incremental prompt missing forced format or delta: %q", prompt)
	}
	if strings.Contains(prompt, "first question") || strings.Contains(prompt, "first answer") {
		t.Fatalf("Gemini incremental prompt replayed prior transcript: %q", prompt)
	}
}

func (*incrementalDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error { return nil }

func TestChatIncrementalReusesSessionAndSendsFormatPlusDelta(t *testing.T) {
	ds := &incrementalDSStub{}
	h := &Handler{
		Store:       mockOpenAIConfig{},
		Auth:        incrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}

	first := map[string]any{
		"model":  "deepseek-v4-flash",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
		},
	}
	serveIncrementalChat(t, h, first)
	second := map[string]any{
		"model":  "deepseek-v4-flash",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second question"},
		},
	}
	serveIncrementalChat(t, h, second)

	if ds.createCalls != 1 {
		t.Fatalf("expected one upstream session creation, got %d", ds.createCalls)
	}
	if len(ds.normalPayloads) != 1 || len(ds.pinnedPayloads) != 1 {
		t.Fatalf("unexpected completion calls: normal=%d pinned=%d", len(ds.normalPayloads), len(ds.pinnedPayloads))
	}
	payload := ds.pinnedPayloads[0]
	if payload["chat_session_id"] != "remote-session-1" {
		t.Fatalf("unexpected reused session: %#v", payload["chat_session_id"])
	}
	parent, ok := payload["parent_message_id"].(float64)
	if !ok || int(parent) != 101 {
		t.Fatalf("expected parent_message_id=101, got %#v", payload["parent_message_id"])
	}
	prompt, _ := payload["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second question"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("incremental prompt missing %q: %q", expected, prompt)
		}
	}
	for _, forbidden := range []string{"first question", "first answer"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("incremental prompt unexpectedly replayed %q: %q", forbidden, prompt)
		}
	}
}

func TestChatIncrementalRotatesAfterConfiguredTurnLimit(t *testing.T) {
	ds := &incrementalDSStub{}
	promptLimit := config.DefaultPromptLimitSettings()
	promptLimit.IncrementalMaxTurns = 2
	promptLimit.IncrementalRotationKeepRecent = 1
	h := &Handler{
		Store:       mockOpenAIConfig{promptLimit: &promptLimit},
		Auth:        incrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}

	serveIncrementalChat(t, h, map[string]any{
		"model": "deepseek-v4-flash", "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": "first question"}},
	})
	serveIncrementalChat(t, h, map[string]any{
		"model": "deepseek-v4-flash", "stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second question"},
		},
	})
	serveIncrementalChat(t, h, map[string]any{
		"model": "deepseek-v4-flash", "stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second question"},
			map[string]any{"role": "assistant", "content": "second answer"},
			map[string]any{"role": "user", "content": "third question"},
		},
	})

	if ds.createCalls != 2 || len(ds.normalPayloads) != 2 || len(ds.pinnedPayloads) != 1 {
		t.Fatalf("expected full, pinned, rollover calls; creates=%d normal=%d pinned=%d", ds.createCalls, len(ds.normalPayloads), len(ds.pinnedPayloads))
	}
	rollover := ds.normalPayloads[1]
	if rollover["chat_session_id"] != "remote-session-2" || rollover["parent_message_id"] != nil {
		t.Fatalf("rollover must start a fresh root session: %#v", rollover)
	}
	prompt, _ := rollover["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second answer", "third question"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("rollover prompt missing %q: %q", expected, prompt)
		}
	}
	for _, dropped := range []string{"first question", "first answer", "second question"} {
		if strings.Contains(prompt, dropped) {
			t.Fatalf("rollover prompt retained compacted history %q: %q", dropped, prompt)
		}
	}
}

func serveIncrementalChat(t *testing.T, h *Handler, body map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
}

func incrementalResponse(messageID int, text string) *http.Response {
	body := strings.Join([]string{
		`data: {"response_message_id":` + jsonNumber(messageID) + `}`,
		`data: {"p":"response/content","v":` + mustJSONString(text) + `}`,
		`data: [DONE]`,
	}, "\n") + "\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func clonePayloadForTest(payload map[string]any) map[string]any {
	b, _ := json.Marshal(payload)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonNumber(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func mustJSONString(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
