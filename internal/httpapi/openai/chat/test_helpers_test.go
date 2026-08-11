package chat

import (
	"DeepSeek_Web_To_API/internal/config"
	"context"
	"io"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

type mockOpenAIConfig struct {
	aliases             map[string]string
	wideInput           bool
	autoDeleteMode      string
	toolMode            string
	earlyEmit           string
	responsesTTL        int
	embedProv           string
	historySplitEnabled bool
	historySplitTurns   int
	currentInputEnabled bool
	currentInputMin     int
	thinkingInjection   *bool
	thinkingPrompt      string
	promptLimit         *config.PromptLimitSettings
}

func (m mockOpenAIConfig) ModelAliases() map[string]string { return m.aliases }
func (m mockOpenAIConfig) CompatWideInputStrictOutput() bool {
	return m.wideInput
}
func (m mockOpenAIConfig) CompatStripReferenceMarkers() bool   { return true }
func (m mockOpenAIConfig) ToolcallMode() string                { return m.toolMode }
func (m mockOpenAIConfig) ToolcallEarlyEmitConfidence() string { return m.earlyEmit }
func (m mockOpenAIConfig) ResponsesStoreTTLSeconds() int       { return m.responsesTTL }
func (m mockOpenAIConfig) EmbeddingsProvider() string          { return m.embedProv }
func (m mockOpenAIConfig) AutoDeleteMode() string {
	if m.autoDeleteMode == "" {
		return "none"
	}
	return m.autoDeleteMode
}
func (m mockOpenAIConfig) AutoDeleteSessions() bool   { return false }
func (m mockOpenAIConfig) SafetyBlockMessage() string { return "" }
func (m mockOpenAIConfig) HistorySplitEnabled() bool  { return m.historySplitEnabled }
func (m mockOpenAIConfig) HistorySplitTriggerAfterTurns() int {
	if m.historySplitTurns <= 0 {
		return 1
	}
	return m.historySplitTurns
}
func (m mockOpenAIConfig) CurrentInputFileEnabled() bool { return m.currentInputEnabled }
func (m mockOpenAIConfig) RemoteFileUploadEnabled() bool { return true }
func (m mockOpenAIConfig) CurrentInputFileMinChars() int {
	return m.currentInputMin
}
func (m mockOpenAIConfig) PromptLimitSnapshot() config.PromptLimitSettings {
	if m.promptLimit != nil {
		return *m.promptLimit
	}
	s := config.DefaultPromptLimitSettings()
	s.Enabled = true
	return s
}
func (m mockOpenAIConfig) ThinkingInjectionEnabled() bool {
	if m.thinkingInjection == nil {
		return false
	}
	return *m.thinkingInjection
}
func (m mockOpenAIConfig) ThinkingInjectionPrompt() string { return m.thinkingPrompt }

// Prompt-limit knobs default to the production defaults so existing tests
// exercise the same path as a stock deployment.

type streamStatusAuthStub struct{}

func (streamStatusAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{
		UseConfigToken: false,
		DeepSeekToken:  "direct-token",
		CallerID:       "caller:test",
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (streamStatusAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return (&streamStatusAuthStub{}).Determine(nil)
}

func (s streamStatusAuthStub) DetermineWithSession(req *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.Determine(req)
}

func (streamStatusAuthStub) Release(_ *auth.RequestAuth) {}

type streamStatusDSStub struct {
	resp *http.Response
}

func (m streamStatusDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}

func (m streamStatusDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (m streamStatusDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id", Filename: "file.txt", Bytes: 1, Status: "uploaded"}, nil
}

func (m streamStatusDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return m.resp, nil
}

func (m streamStatusDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (m streamStatusDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func makeOpenAISSEHTTPResponse(lines ...string) *http.Response {
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

type inlineUploadDSStub struct {
	uploadCalls    []dsclient.UploadFileRequest
	lastCtx        context.Context
	completionReq  map[string]any
	createSession  string
	uploadErr      error
	completionResp *http.Response
}

func (m *inlineUploadDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	if strings.TrimSpace(m.createSession) == "" {
		return "session-id", nil
	}
	return m.createSession, nil
}

func (m *inlineUploadDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (m *inlineUploadDSStub) UploadFile(ctx context.Context, _ *auth.RequestAuth, req dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	m.lastCtx = ctx
	m.uploadCalls = append(m.uploadCalls, req)
	if m.uploadErr != nil {
		return nil, m.uploadErr
	}
	return &dsclient.UploadFileResult{
		ID:       "file-inline-1",
		Filename: req.Filename,
		Bytes:    int64(len(req.Data)),
		Status:   "uploaded",
		Purpose:  req.Purpose,
	}, nil
}

func (m *inlineUploadDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	m.completionReq = payload
	if m.completionResp != nil {
		return m.completionResp, nil
	}
	return makeOpenAISSEHTTPResponse(
		`data: {"p":"response/content","v":"ok"}`,
		`data: [DONE]`,
	), nil
}

func (m *inlineUploadDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (m *inlineUploadDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}
