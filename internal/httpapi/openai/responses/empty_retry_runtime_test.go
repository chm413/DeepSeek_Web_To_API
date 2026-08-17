package responses

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/stream"
)

type responsesEmptyRetryAuthStub struct{}

type responsesReadThenError struct {
	data []byte
	sent bool
}

func (r *responsesReadThenError) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, r.data), io.ErrUnexpectedEOF
}

func (*responsesReadThenError) Close() error { return nil }

func (responsesEmptyRetryAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return nil, nil
}

func (responsesEmptyRetryAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return nil, nil
}

func (responsesEmptyRetryAuthStub) DetermineWithSession(_ *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return nil, nil
}

func (responsesEmptyRetryAuthStub) Release(_ *auth.RequestAuth) {}

func (responsesEmptyRetryAuthStub) SwitchAccount(_ context.Context, a *auth.RequestAuth) bool {
	a.AccountID = "retry-account"
	a.DeepSeekToken = "retry-token"
	return true
}

type responsesEmptyRetryDSStub struct{}

func (responsesEmptyRetryDSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "session-" + a.AccountID, nil
}

func (responsesEmptyRetryDSStub) GetPow(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "pow-" + a.AccountID, nil
}

func (responsesEmptyRetryDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return nil, nil
}

func (responsesEmptyRetryDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return nil, nil
}

func (responsesEmptyRetryDSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return nil, nil
}

func (responsesEmptyRetryDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func makeResponsesOpenAISSEHTTPResponse(lines ...string) *http.Response {
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

func TestConsumeResponsesStreamAttemptMarksContextCancelledState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	streamRuntime := newResponsesStreamRuntime(
		rec,
		http.NewResponseController(rec),
		true,
		"resp-cancelled",
		"deepseek-v4-flash",
		"prompt",
		false,
		false,
		true,
		nil,
		nil,
		false,
		false,
		promptcompat.DefaultToolChoicePolicy(),
		"",
		nil,
	)
	resp := makeResponsesOpenAISSEHTTPResponse(
		`data: {"p":"response/content","v":"hello"}`,
		`data: [DONE]`,
	)

	h := &Handler{}
	terminalWritten, retryable := h.consumeResponsesStreamAttempt(req, resp, streamRuntime, "text", false, true, nil)
	if !terminalWritten || retryable {
		t.Fatalf("expected cancelled attempt to terminate without retry, got terminalWritten=%v retryable=%v", terminalWritten, retryable)
	}
	if !streamRuntime.failed {
		t.Fatalf("expected cancelled response stream to be marked failed")
	}
	if got, want := streamRuntime.finalErrorCode, string(stream.StopReasonContextCancelled); got != want {
		t.Fatalf("expected cancelled final error code %q, got %q", want, got)
	}
	if streamRuntime.finalErrorMessage == "" {
		t.Fatalf("expected cancelled final error message to be preserved")
	}
	if writeUnstartedResponsesStreamError(rec, streamRuntime) {
		t.Fatal("client-cancelled streams must not write a trailing HTTP error")
	}
}

func TestConsumeResponsesStreamAttemptMarksUnexpectedEOFAsFailure(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rec := httptest.NewRecorder()
	streamRuntime := newResponsesStreamRuntime(
		rec,
		http.NewResponseController(rec),
		true,
		"resp-unexpected-eof",
		"deepseek-v4-flash",
		"prompt",
		false,
		false,
		true,
		nil,
		nil,
		false,
		false,
		promptcompat.DefaultToolChoicePolicy(),
		"",
		nil,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: &responsesReadThenError{data: []byte(
			"data: {\"response_message_id\":1}\n" +
				"data: {\"p\":\"response/content\",\"v\":\"partial\"}\n",
		)},
	}

	h := &Handler{}
	terminalWritten, retryable := h.consumeResponsesStreamAttempt(req, resp, streamRuntime, "text", false, true, nil)
	if !terminalWritten || retryable {
		t.Fatalf("expected stream reader failure to terminate without retry, terminalWritten=%v retryable=%v", terminalWritten, retryable)
	}
	if !streamRuntime.failed || streamRuntime.finalErrorStatus != http.StatusBadGateway || streamRuntime.finalErrorCode != "upstream_stream_error" {
		t.Fatalf("unexpected stream failure: failed=%v status=%d code=%q", streamRuntime.failed, streamRuntime.finalErrorStatus, streamRuntime.finalErrorCode)
	}
	if !streamRuntime.hasStarted() {
		t.Fatal("partial upstream content must start the SSE response")
	}
	if writeUnstartedResponsesStreamError(rec, streamRuntime) {
		t.Fatal("started streams must not append an HTTP error after SSE data")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event: response.failed") || !strings.Contains(rec.Body.String(), "upstream_stream_error") {
		t.Fatalf("partial stream failure was not sent as response.failed: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestPrepareResponsesEmptyOutputRetryUpdatesActiveSession(t *testing.T) {
	h := &Handler{Auth: responsesEmptyRetryAuthStub{}, DS: responsesEmptyRetryDSStub{}}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "initial-account",
		DeepSeekToken:  "initial-token",
		TriedAccounts:  map[string]bool{},
	}
	base := map[string]any{"chat_session_id": "session-initial-account", "prompt": "request"}
	retry := clonePayloadForEmptyOutputRetry(base, 101)
	activeSessionID := "session-initial-account"
	pow, ok := h.prepareResponsesEmptyOutputRetry(context.Background(), a, base, retry, "pow-initial", 1, false, nil, &activeSessionID)
	if !ok {
		t.Fatal("expected managed retry preparation to succeed")
	}
	if activeSessionID != "session-retry-account" || base["chat_session_id"] != activeSessionID || retry["chat_session_id"] != activeSessionID {
		t.Fatalf("retry session state was not propagated: active=%q base=%#v retry=%#v", activeSessionID, base, retry)
	}
	if pow != "pow-retry-account" || a.AccountID != "retry-account" {
		t.Fatalf("retry did not use switched account: pow=%q account=%q", pow, a.AccountID)
	}
}
