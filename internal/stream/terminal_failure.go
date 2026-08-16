package stream

import (
	"net/http"
)

// TerminalFailure is the client-safe result of an abnormal upstream stream
// termination. The raw reader error remains available to caller logs through
// ConsumeSSE's OnFinalize hook and is intentionally not exposed to clients.
type TerminalFailure struct {
	Status  int
	Code    string
	Message string
}

// ClassifyTerminalFailure turns a scanner failure or engine timeout into a
// structured terminal error. A normal upstream completion and an explicit
// handler stop return ok=false.
func ClassifyTerminalFailure(reason StopReason, scannerErr error) (failure TerminalFailure, ok bool) {
	if scannerErr != nil {
		return TerminalFailure{
			Status:  http.StatusBadGateway,
			Code:    "upstream_stream_error",
			Message: "Upstream stream ended unexpectedly; the request did not complete.",
		}, true
	}
	switch reason {
	case StopReasonNoContentTimeout:
		return TerminalFailure{
			Status:  http.StatusGatewayTimeout,
			Code:    "upstream_stream_timeout",
			Message: "Upstream stream timed out before it produced content.",
		}, true
	case StopReasonIdleTimeout:
		return TerminalFailure{
			Status:  http.StatusGatewayTimeout,
			Code:    "upstream_stream_timeout",
			Message: "Upstream stream became idle before it completed.",
		}, true
	default:
		return TerminalFailure{}, false
	}
}
