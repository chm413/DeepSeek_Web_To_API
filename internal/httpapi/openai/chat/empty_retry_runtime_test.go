package chat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/stream"
)

type chatReadThenError struct {
	data []byte
	sent bool
}

func (r *chatReadThenError) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, r.data), io.ErrUnexpectedEOF
}

func (*chatReadThenError) Close() error { return nil }

func TestConsumeChatStreamAttemptMarksContextCancelledState(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	entry, err := historyStore.Start(chathistory.StartParams{
		CallerID:  "caller:test",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history failed: %v", err)
	}
	session := &chatHistorySession{
		store:       historyStore,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: "prompt",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	streamRuntime := newChatStreamRuntime(
		rec,
		http.NewResponseController(rec),
		true,
		"cid-cancelled",
		time.Now().Unix(),
		"deepseek-v4-flash",
		"prompt",
		false,
		false,
		true,
		false,
		nil,
		nil,
		false,
		false,
		false,
	)
	resp := makeOpenAISSEHTTPResponse(
		`data: {"p":"response/content","v":"hello"}`,
		`data: [DONE]`,
	)

	h := &Handler{}
	terminalWritten, retryable := h.consumeChatStreamAttempt(req, resp, streamRuntime, "text", false, session, true)
	if !terminalWritten || retryable {
		t.Fatalf("expected cancelled attempt to terminate without retry, got terminalWritten=%v retryable=%v", terminalWritten, retryable)
	}
	if got, want := streamRuntime.finalErrorCode, string(stream.StopReasonContextCancelled); got != want {
		t.Fatalf("expected cancelled final error code %q, got %q", want, got)
	}
	if streamRuntime.finalErrorMessage == "" {
		t.Fatalf("expected cancelled final error message to be preserved")
	}

	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Status != "stopped" {
		t.Fatalf("expected stopped status, got %#v", full)
	}
}

func TestConsumeChatStreamAttemptMarksUnexpectedEOFAsFailure(t *testing.T) {
	historyStore := newTestChatHistoryStore(t)
	entry, err := historyStore.Start(chathistory.StartParams{
		CallerID:  "caller:test",
		Model:     "deepseek-v4-flash",
		Stream:    true,
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history failed: %v", err)
	}
	session := &chatHistorySession{
		store:       historyStore,
		entryID:     entry.ID,
		startedAt:   time.Now(),
		lastPersist: time.Now(),
		finalPrompt: "prompt",
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	streamRuntime := newChatStreamRuntime(
		rec,
		http.NewResponseController(rec),
		true,
		"cid-unexpected-eof",
		time.Now().Unix(),
		"deepseek-v4-flash",
		"prompt",
		false,
		false,
		true,
		false,
		nil,
		nil,
		false,
		false,
		false,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: &chatReadThenError{data: []byte(
			"data: {\"response_message_id\":1}\n" +
				"data: {\"p\":\"response/content\",\"v\":\"partial\"}\n",
		)},
	}

	h := &Handler{}
	terminalWritten, retryable := h.consumeChatStreamAttempt(req, resp, streamRuntime, "text", false, session, true)
	if !terminalWritten || retryable {
		t.Fatalf("expected stream reader failure to terminate without retry, terminalWritten=%v retryable=%v", terminalWritten, retryable)
	}
	if streamRuntime.finalErrorStatus != http.StatusBadGateway || streamRuntime.finalErrorCode != "upstream_stream_error" {
		t.Fatalf("unexpected stream failure: status=%d code=%q", streamRuntime.finalErrorStatus, streamRuntime.finalErrorCode)
	}
	if !strings.Contains(rec.Body.String(), "upstream_stream_error") {
		t.Fatalf("stream error was not sent to the client: %q", rec.Body.String())
	}

	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	full, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get detail failed: %v", err)
	}
	if full.Status != "error" || full.StatusCode != http.StatusBadGateway || !strings.Contains(full.Error, "Upstream stream ended unexpectedly") {
		t.Fatalf("unexpected history result: %#v", full)
	}
}
