package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

// capturingDSStub records the completion payload the handler actually sends
// upstream, so a test can assert on the prompt that WOULD reach DeepSeek —
// the only place the compression result is observable end-to-end.
type capturingDSStub struct {
	lastPayload map[string]any
}

func (m *capturingDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}
func (m *capturingDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}
func (m *capturingDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id", Status: "uploaded"}, nil
}
func (m *capturingDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	m.lastPayload = payload
	return makeOpenAISSEHTTPResponse(`data: {"p":"response/content","v":"ok"}`, `data: [DONE]`), nil
}
func (m *capturingDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}
func (m *capturingDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error { return nil }

// buildOversizedBody returns a chat request of `turns` user+assistant pairs,
// each message padChars long, for the given model. CIF is left disabled by the
// caller so the compression path (not the CIF inline path) is what runs.
func buildOversizedBody(t *testing.T, model string, turns, padChars int) string {
	t.Helper()
	msgs := []map[string]any{{"role": "system", "content": "you are a precise assistant"}}
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": strings.Repeat("u", padChars)},
			map[string]any{"role": "assistant", "content": strings.Repeat("a", padChars)},
		)
	}
	body := map[string]any{"model": model, "messages": msgs, "stream": false}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return string(raw)
}

func promptLimitStore(expert, def int, autoCompress bool) mockOpenAIConfig {
	s := config.DefaultPromptLimitSettings()
	s.Enabled = true
	s.MaxCharsExpert = expert
	s.MaxCharsDefault = def
	s.AutoCompressEnable = autoCompress
	return mockOpenAIConfig{wideInput: true, promptLimit: &s}
}

// TestExpertPromptAutoCompressedReachesUpstreamUnderLimit is the end-to-end
// proof I owed: drive a >150k expert request through the real ChatCompletions
// entry point and assert the payload that reaches the DS client is actually
// smaller than the raw request AND within the expert ceiling. Before this test
// the compression code had only isolated unit coverage — it was never observed
// firing through the handler.
func TestExpertPromptAutoCompressedReachesUpstreamUnderLimit(t *testing.T) {
	// 30 turns * 2 msgs * 5000 chars ≈ 300k: over the 150k expert ceiling,
	// under the 380k default ceiling. Compression keeps the recent turns only.
	body := buildOversizedBody(t, "deepseek-v4-pro", 30, 5000)
	rawLen := len(body)

	ds := &capturingDSStub{}
	h := &Handler{
		Store:       promptLimitStore(150000, 380000, true),
		Auth:        streamStatusAuthStub{},
		DS:          ds,
		ChatHistory: newTestChatHistoryStore(t),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after compression, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ds.lastPayload == nil {
		t.Fatal("completion never reached the DS client — request failed before upstream")
	}
	prompt, _ := ds.lastPayload["prompt"].(string)
	if prompt == "" {
		t.Fatal("upstream payload carried no prompt")
	}
	if len(prompt) > 150000 {
		t.Fatalf("compressed prompt %d still exceeds expert limit 150000", len(prompt))
	}
	if len(prompt) >= rawLen {
		t.Fatalf("prompt not compressed: upstream=%d raw request=%d", len(prompt), rawLen)
	}
	if mt, _ := ds.lastPayload["model_type"].(string); mt != "expert" {
		t.Fatalf("expected model_type=expert, got %q", mt)
	}
}

// TestExpertPromptRejectedWhenAutoCompressDisabled pins the enforce backstop:
// with auto-compression off, a >150k expert prompt must be refused with 413
// rather than silently forwarded to fail upstream.
func TestExpertPromptRejectedWhenAutoCompressDisabled(t *testing.T) {
	body := buildOversizedBody(t, "deepseek-v4-pro", 30, 5000)

	ds := &capturingDSStub{}
	h := &Handler{
		Store:       promptLimitStore(150000, 380000, false),
		Auth:        streamStatusAuthStub{},
		DS:          ds,
		ChatHistory: newTestChatHistoryStore(t),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 with auto-compress off, got %d body=%s", rec.Code, rec.Body.String())
	}
	if ds.lastPayload != nil {
		t.Fatal("oversized prompt must not reach upstream when rejected")
	}
}

// TestSameSizeFlashSucceedsWhereExpertWouldCompress reproduces the reported
// symptom directly: the identical ~300k conversation that trips the expert
// ceiling is accepted verbatim on the flash/default tier, because its ceiling
// is higher. This is what "expert mode limits long text" actually means.
func TestSameSizeFlashSucceedsWhereExpertWouldCompress(t *testing.T) {
	body := buildOversizedBody(t, "deepseek-v4-flash", 30, 5000)
	rawLen := len(body)

	ds := &capturingDSStub{}
	h := &Handler{
		Store:       promptLimitStore(150000, 380000, true),
		Auth:        streamStatusAuthStub{},
		DS:          ds,
		ChatHistory: newTestChatHistoryStore(t),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on flash tier, got %d body=%s", rec.Code, rec.Body.String())
	}
	prompt, _ := ds.lastPayload["prompt"].(string)
	// Flash ceiling is 380k; a ~300k prompt is under it, so NO compression
	// should have happened — the full conversation reaches upstream intact.
	if len(prompt) < rawLen/2 {
		t.Fatalf("flash prompt unexpectedly compressed: upstream=%d raw=%d", len(prompt), rawLen)
	}
	if mt, _ := ds.lastPayload["model_type"].(string); mt != "default" {
		t.Fatalf("expected model_type=default, got %q", mt)
	}
}
