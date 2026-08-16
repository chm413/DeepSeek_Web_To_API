package stream

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestClassifyTerminalFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     StopReason
		scannerErr error
		wantStatus int
		wantCode   string
		wantOK     bool
	}{
		{
			name:       "reader error",
			reason:     StopReasonUpstreamCompleted,
			scannerErr: io.ErrUnexpectedEOF,
			wantStatus: http.StatusBadGateway,
			wantCode:   "upstream_stream_error",
			wantOK:     true,
		},
		{
			name:       "no content timeout",
			reason:     StopReasonNoContentTimeout,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "upstream_stream_timeout",
			wantOK:     true,
		},
		{
			name:       "idle timeout",
			reason:     StopReasonIdleTimeout,
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "upstream_stream_timeout",
			wantOK:     true,
		},
		{
			name:   "normal completion",
			reason: StopReasonUpstreamCompleted,
			wantOK: false,
		},
		{
			name:   "handler stop",
			reason: StopReasonHandlerRequested,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failure, ok := ClassifyTerminalFailure(tt.reason, tt.scannerErr)
			if ok != tt.wantOK {
				t.Fatalf("ClassifyTerminalFailure() ok=%v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if failure.Status != tt.wantStatus || failure.Code != tt.wantCode {
				t.Fatalf("ClassifyTerminalFailure() = %#v, want status=%d code=%q", failure, tt.wantStatus, tt.wantCode)
			}
			if failure.Message == "" {
				t.Fatal("terminal failure must provide a client-safe message")
			}
		})
	}

	if failure, ok := ClassifyTerminalFailure(StopReasonIdleTimeout, errors.New("sensitive upstream URL")); !ok || failure.Code != "upstream_stream_error" {
		t.Fatalf("scanner error must take precedence over timeout: %#v ok=%v", failure, ok)
	}
}
