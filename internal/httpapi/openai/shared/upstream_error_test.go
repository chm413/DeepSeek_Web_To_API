package shared

import (
	"context"
	"net/http"
	"strings"
	"testing"

	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

func TestCompletionErrorDetailMapsClientCancelToStopped499(t *testing.T) {
	t.Parallel()

	detail := CompletionErrorDetail(&dsclient.RequestFailure{
		Op:      "completion",
		Kind:    dsclient.FailureClientCancelled,
		Message: "client cancelled request",
		Cause:   context.Canceled,
	})

	if !detail.Stopped || detail.Status != StatusClientClosedRequest || detail.Code != "client_cancelled" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	if detail.FinishReason != "context_cancelled" {
		t.Fatalf("unexpected finish reason: %q", detail.FinishReason)
	}
}

func TestCompletionErrorDetailPreservesUpstreamHTTPStatus(t *testing.T) {
	t.Parallel()

	detail := CompletionErrorDetail(&dsclient.RequestFailure{
		Op:         "completion",
		Kind:       dsclient.FailureUpstreamStatus,
		StatusCode: http.StatusServiceUnavailable,
		Message:    `{"error":"busy"}`,
	})

	if detail.Status != http.StatusServiceUnavailable || detail.Code != "upstream_http_status" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	if !strings.Contains(detail.Message, "HTTP 503") || !strings.Contains(detail.Message, "busy") {
		t.Fatalf("expected status and body in message, got %q", detail.Message)
	}
}

func TestCompletionErrorDetailMapsSessionCapacitySeparately(t *testing.T) {
	t.Parallel()

	detail := CompletionErrorDetail(&dsclient.RequestFailure{
		Op:             "completion",
		Kind:           dsclient.FailureUpstreamStatus,
		StatusCode:     http.StatusTooManyRequests,
		RateLimitScope: dsclient.RateLimitScopeSessionCapacity,
		Message:        "maximum conversation turns reached",
	})
	if detail.Status != http.StatusRequestEntityTooLarge || detail.Code != "upstream_session_capacity" {
		t.Fatalf("unexpected session capacity detail: %#v", detail)
	}
}
