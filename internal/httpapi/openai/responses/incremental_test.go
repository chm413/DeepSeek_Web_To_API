package responses

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
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

type responsesIncrementalAuthStub struct{}

type responsesBodyRecordingAuthStub struct {
	bodies      [][]byte
	sessionKeys []string
}

func (s *responsesBodyRecordingAuthStub) requestAuth() *auth.RequestAuth {
	return responsesIncrementalAuthStub{}.requestAuth()
}

func (s *responsesBodyRecordingAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s *responsesBodyRecordingAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s *responsesBodyRecordingAuthStub) DetermineWithSession(_ *http.Request, body []byte) (*auth.RequestAuth, error) {
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	return s.requestAuth(), nil
}

func (s *responsesBodyRecordingAuthStub) DetermineWithSessionKey(_ *http.Request, body []byte, sessionKey string) (*auth.RequestAuth, error) {
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	s.sessionKeys = append(s.sessionKeys, sessionKey)
	a := s.requestAuth()
	a.SessionKey = sessionKey
	return a, nil
}

func (*responsesBodyRecordingAuthStub) Release(_ *auth.RequestAuth) {}

type responsesRotationConfigStub struct{ responsesHistoryConfigStub }

func (responsesRotationConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.IncrementalMaxTurns = 2
	cfg.IncrementalRotationKeepRecent = 1
	return cfg
}

func (responsesIncrementalAuthStub) requestAuth() *auth.RequestAuth {
	return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller:responses-inc", AccountID: "account-1", SessionKey: "responses-session", TriedAccounts: map[string]bool{}}
}

func (s responsesIncrementalAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s responsesIncrementalAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s responsesIncrementalAuthStub) DetermineWithSession(_ *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (responsesIncrementalAuthStub) Release(_ *auth.RequestAuth) {}

type responsesIncrementalDSStub struct {
	createCalls int
	normal      []map[string]any
	pinned      []map[string]any
}

func (s *responsesIncrementalDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	s.createCalls++
	return "responses-remote-session", nil
}

func (*responsesIncrementalDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (*responsesIncrementalDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-1", Status: "uploaded"}, nil
}

func (s *responsesIncrementalDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normal = append(s.normal, cloneResponsesPayload(payload))
	return responsesIncrementalSSE(401, "first response"), nil
}

func (s *responsesIncrementalDSStub) CallCompletionPinned(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.pinned = append(s.pinned, cloneResponsesPayload(payload))
	return responsesIncrementalSSE(402, "second response"), nil
}

func (*responsesIncrementalDSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*responsesIncrementalDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func TestResponsesIncrementalReusesSessionAndSendsOnlyDelta(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	firstBody := map[string]any{"model": "deepseek-v4-flash", "input": "first request"}
	firstResponse := serveResponsesIncremental(t, h, firstBody)
	output, ok := firstResponse["output"].([]any)
	if !ok || len(output) == 0 {
		t.Fatalf("missing first response output: %#v", firstResponse)
	}
	secondInput := []any{map[string]any{"role": "user", "content": "first request"}}
	secondInput = append(secondInput, output...)
	secondInput = append(secondInput, map[string]any{"role": "user", "content": "second request"})
	serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": secondInput})

	if ds.createCalls != 1 || len(ds.normal) != 1 || len(ds.pinned) != 1 {
		t.Fatalf("unexpected calls: create=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	payload := ds.pinned[0]
	if payload["chat_session_id"] != "responses-remote-session" {
		t.Fatalf("unexpected session: %#v", payload["chat_session_id"])
	}
	if parent, ok := payload["parent_message_id"].(float64); !ok || int(parent) != 401 {
		t.Fatalf("unexpected parent: %#v", payload["parent_message_id"])
	}
	prompt, _ := payload["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing %q in prompt: %q", expected, prompt)
		}
	}
	for _, forbidden := range []string{"first request", "first response"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("unexpected replay of %q: %q", forbidden, prompt)
		}
	}
}

func TestResponsesIncrementalRotatesIntoFreshRootSession(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesRotationConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	history := []any{map[string]any{"role": "user", "content": "first request"}}
	first := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": history})
	firstOutput, _ := first["output"].([]any)
	history = append(history, firstOutput...)
	history = append(history, map[string]any{"role": "user", "content": "second request"})
	second := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": history})
	secondOutput, _ := second["output"].([]any)
	history = append(history, secondOutput...)
	history = append(history, map[string]any{"role": "user", "content": "third request"})
	serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": history})

	if ds.createCalls != 2 || len(ds.normal) != 2 || len(ds.pinned) != 1 {
		t.Fatalf("expected full, pinned, rollover calls; creates=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	rollover := ds.normal[1]
	if rollover["parent_message_id"] != nil {
		t.Fatalf("Responses rollover must start at root: %#v", rollover)
	}
	prompt, _ := rollover["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second response", "third request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Responses rollover prompt missing %q: %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "first request") {
		t.Fatalf("Responses rollover retained compacted history: %q", prompt)
	}
}

func TestResponsesPreviousResponseAuthenticatesExpandedCanonicalInput(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	authRecorder := &responsesBodyRecordingAuthStub{}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        authRecorder,
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	first := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": "first request"})
	responseID, _ := first["id"].(string)
	if responseID == "" {
		t.Fatalf("first response had no id: %#v", first)
	}
	serveResponsesIncremental(t, h, map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": responseID,
		"input":                "second request",
	})
	if len(authRecorder.bodies) != 2 {
		t.Fatalf("session auth bodies=%d, want 2", len(authRecorder.bodies))
	}
	if len(authRecorder.sessionKeys) != 1 || authRecorder.sessionKeys[0] != "responses-session" {
		t.Fatalf("previous_response_id did not inherit the original session key: %#v", authRecorder.sessionKeys)
	}
	expanded := string(authRecorder.bodies[1])
	for _, expected := range []string{"first request", "first response", "second request"} {
		if !strings.Contains(expanded, expected) {
			t.Fatalf("expanded session body missing %q: %s", expected, expanded)
		}
	}
}

func serveResponsesIncremental(t *testing.T, h *Handler, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func responsesIncrementalSSE(messageID int, text string) *http.Response {
	id, _ := json.Marshal(messageID)
	content, _ := json.Marshal(text)
	body := `data: {"response_message_id":` + string(id) + "}\n" +
		`data: {"p":"response/content","v":` + string(content) + "}\n" +
		"data: [DONE]\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func cloneResponsesPayload(payload map[string]any) map[string]any {
	b, _ := json.Marshal(payload)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
