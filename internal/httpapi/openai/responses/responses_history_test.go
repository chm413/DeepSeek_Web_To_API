package responses

import (
	"DeepSeek_Web_To_API/internal/config"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

type responsesHistoryConfigStub struct{}

func (responsesHistoryConfigStub) ModelAliases() map[string]string { return nil }
func (responsesHistoryConfigStub) CompatWideInputStrictOutput() bool {
	return true
}
func (responsesHistoryConfigStub) CompatStripReferenceMarkers() bool   { return true }
func (responsesHistoryConfigStub) ToolcallMode() string                { return "" }
func (responsesHistoryConfigStub) ToolcallEarlyEmitConfidence() string { return "" }
func (responsesHistoryConfigStub) ResponsesStoreTTLSeconds() int       { return 0 }
func (responsesHistoryConfigStub) EmbeddingsProvider() string          { return "" }
func (responsesHistoryConfigStub) AutoDeleteMode() string              { return "none" }
func (responsesHistoryConfigStub) AutoDeleteSessions() bool            { return false }
func (responsesHistoryConfigStub) HistorySplitEnabled() bool           { return false }
func (responsesHistoryConfigStub) HistorySplitTriggerAfterTurns() int  { return 1 }
func (responsesHistoryConfigStub) CurrentInputFileEnabled() bool       { return false }
func (responsesHistoryConfigStub) CurrentInputFileMinChars() int       { return 0 }
func (responsesHistoryConfigStub) ThinkingInjectionEnabled() bool      { return false }
func (responsesHistoryConfigStub) ThinkingInjectionPrompt() string     { return "" }
func (responsesHistoryConfigStub) SafetyBlockMessage() string          { return "" }
func (responsesHistoryConfigStub) RemoteFileUploadEnabled() bool       { return true }
func (responsesHistoryConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	s := config.DefaultPromptLimitSettings()
	s.Enabled = true
	return s
}

type responsesHistoryCurrentInputConfigStub struct {
	responsesHistoryConfigStub
}

func (responsesHistoryCurrentInputConfigStub) CurrentInputFileEnabled() bool { return true }
func (responsesHistoryCurrentInputConfigStub) CurrentInputFileMinChars() int { return 0 }

type responsesHistoryAuthStub struct{}

func (responsesHistoryAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{
		UseConfigToken: false,
		DeepSeekToken:  "token",
		CallerID:       "caller:responses",
		AccountID:      "acc-responses",
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (s responsesHistoryAuthStub) DetermineWithSession(req *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.Determine(req)
}

func (s responsesHistoryAuthStub) DetermineCaller(req *http.Request) (*auth.RequestAuth, error) {
	return s.Determine(req)
}

func (responsesHistoryAuthStub) Release(_ *auth.RequestAuth) {}

type responsesHistoryDSStub struct {
	resp *http.Response
}

func (s responsesHistoryDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-responses", nil
}

func (s responsesHistoryDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (s responsesHistoryDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id", Status: "uploaded"}, nil
}

func (s responsesHistoryDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return s.resp, nil
}

func (s responsesHistoryDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (s responsesHistoryDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func makeResponsesHistorySSEHTTPResponse(lines ...string) *http.Response {
	body := strings.Join(lines, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestResponsesNonStreamCapturesChatHistory(t *testing.T) {
	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS: responsesHistoryDSStub{resp: makeResponsesHistorySSEHTTPResponse(
			`data: {"p":"response/content","v":"responses answer"}`,
			`data: [DONE]`,
		)},
		ChatHistory: historyStore,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-v4-flash","input":"remember me"}`))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	item := singleResponsesHistoryItem(t, historyStore)
	if item.Status != "success" || item.Content != "responses answer" || item.Stream {
		t.Fatalf("unexpected captured response item: %#v", item)
	}
	if item.UserInput != "remember me" || item.AccountID != "acc-responses" {
		t.Fatalf("unexpected captured metadata: %#v", item)
	}
}

func TestResponsesStreamCapturesChatHistory(t *testing.T) {
	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS: responsesHistoryDSStub{resp: makeResponsesHistorySSEHTTPResponse(
			`data: {"p":"response/content","v":"stream "}`,
			`data: {"p":"response/content","v":"answer"}`,
			`data: [DONE]`,
		)},
		ChatHistory: historyStore,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-v4-flash","input":"stream remember","stream":true}`))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	item := singleResponsesHistoryItem(t, historyStore)
	if item.Status != "success" || item.Content != "stream answer" || !item.Stream {
		t.Fatalf("unexpected captured stream item: %#v", item)
	}
}

func TestResponsesCurrentInputFilePersistsOriginalMessages(t *testing.T) {
	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	h := &Handler{
		Store: responsesHistoryCurrentInputConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS: responsesHistoryDSStub{resp: makeResponsesHistorySSEHTTPResponse(
			`data: {"p":"response/content","v":"responses answer"}`,
			`data: [DONE]`,
		)},
		ChatHistory: historyStore,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-v4-flash","input":[{"role":"system","content":"system instructions"},{"role":"user","content":"first user turn"},{"role":"assistant","content":"previous answer"},{"role":"user","content":"latest user turn"}]}`))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	item := singleResponsesHistoryItem(t, historyStore)
	if item.UserInput != "latest user turn" {
		t.Fatalf("expected latest original user input to be persisted, got %#v", item.UserInput)
	}
	if len(item.Messages) != 4 {
		t.Fatalf("expected original response input messages to be persisted, got %#v", item.Messages)
	}
	if strings.Contains(item.Messages[len(item.Messages)-1].Content, "Answer the latest user request directly.") {
		t.Fatalf("expected history to keep original messages rather than neutral prompt, got %#v", item.Messages)
	}
}

func singleResponsesHistoryItem(t *testing.T, store *chathistory.Store) chathistory.Entry {
	t.Helper()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	item, err := store.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get history item failed: %v", err)
	}
	return item
}
