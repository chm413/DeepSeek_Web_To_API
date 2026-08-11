package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

// claudeAutoDeleteStoreStub lets each subtest dial in the AutoDeleteMode
// independently of the shared streamStatusClaudeStoreStub (which forces
// "none").
type claudeAutoDeleteStoreStub struct {
	mode string
}

func (claudeAutoDeleteStoreStub) ModelAliases() map[string]string   { return nil }
func (claudeAutoDeleteStoreStub) CompatStripReferenceMarkers() bool { return true }
func (s claudeAutoDeleteStoreStub) AutoDeleteMode() string          { return s.mode }
func (claudeAutoDeleteStoreStub) SafetyBlockMessage() string        { return "" }
func (claudeAutoDeleteStoreStub) PromptLimitSnapshot() config.PromptLimitSettings {
	return config.DefaultPromptLimitSettings()
}

// claudeAutoDeleteDSStub mirrors directClaudeDSStub but counts the delete
// invocations so the test can assert the auto-delete defer fired once.
type claudeAutoDeleteDSStub struct {
	resp        *http.Response
	singleCalls int
	allCalls    int
	seenSession string
}

func (s *claudeAutoDeleteDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-direct", nil
}

func (s *claudeAutoDeleteDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (s *claudeAutoDeleteDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return s.resp, nil
}

func (s *claudeAutoDeleteDSStub) DeleteSessionForToken(_ context.Context, _ string, sessionID string) (*dsclient.DeleteSessionResult, error) {
	s.singleCalls++
	s.seenSession = sessionID
	return &dsclient.DeleteSessionResult{SessionID: sessionID, Success: true}, nil
}

func (s *claudeAutoDeleteDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	s.allCalls++
	return nil
}

// Issue #20 regression: /v1/messages (Claude / Claude Code) must honor the
// WebUI auto-delete toggle the same way /v1/chat/completions does.
func TestClaudeMessagesAutoDeleteSingleMode(t *testing.T) {
	ds := &claudeAutoDeleteDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"hi"}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store: claudeAutoDeleteStoreStub{mode: "single"},
		Auth:  &directClaudeAuthStub{},
		DS:    ds,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ds.singleCalls != 1 {
		t.Fatalf("expected 1 single-session delete on /v1/messages, got %d", ds.singleCalls)
	}
	if ds.seenSession != "session-direct" {
		t.Fatalf("expected delete to target session-direct, got %q", ds.seenSession)
	}
}

func TestClaudeMessagesAutoDeleteAllMode(t *testing.T) {
	ds := &claudeAutoDeleteDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"hi"}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store: claudeAutoDeleteStoreStub{mode: "all"},
		Auth:  &directClaudeAuthStub{},
		DS:    ds,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ds.allCalls != 1 {
		t.Fatalf("expected 1 delete-all on /v1/messages, got %d", ds.allCalls)
	}
}

func TestClaudeMessagesAutoDeleteNoneMode(t *testing.T) {
	ds := &claudeAutoDeleteDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"hi"}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store: claudeAutoDeleteStoreStub{mode: "none"},
		Auth:  &directClaudeAuthStub{},
		DS:    ds,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ds.singleCalls != 0 || ds.allCalls != 0 {
		t.Fatalf("expected zero deletes when mode=none, got single=%d all=%d", ds.singleCalls, ds.allCalls)
	}
}
