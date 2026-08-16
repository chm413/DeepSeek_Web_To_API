package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

type FailureKind string

// RateLimitScope distinguishes a whole-account cooldown from a single
// conversation that has exhausted its upstream context or turn budget.
type RateLimitScope string

const (
	RateLimitScopeAccount         RateLimitScope = "account"
	RateLimitScopeSessionCapacity RateLimitScope = "session_capacity"
)

const (
	FailureUnknown             FailureKind = ""
	FailureDirectUnauthorized  FailureKind = "direct_unauthorized"
	FailureManagedUnauthorized FailureKind = "managed_unauthorized"
	FailureClientCancelled     FailureKind = "client_cancelled"
	FailureUpstreamTimeout     FailureKind = "upstream_timeout"
	FailureUpstreamNetwork     FailureKind = "upstream_network_error"
	FailureUpstreamStatus      FailureKind = "upstream_http_status"
)

type RequestFailure struct {
	Op             string
	Kind           FailureKind
	Message        string
	StatusCode     int
	RateLimitScope RateLimitScope
	Cause          error
}

func (e *RequestFailure) Error() string {
	if e == nil {
		return ""
	}
	op := strings.TrimSpace(e.Op)
	message := strings.TrimSpace(e.Message)
	switch {
	case op != "" && e.StatusCode > 0 && message != "":
		return fmt.Sprintf("%s failed with HTTP %d: %s", op, e.StatusCode, message)
	case op != "" && e.StatusCode > 0:
		return fmt.Sprintf("%s failed with HTTP %d", op, e.StatusCode)
	case op != "" && message != "":
		return fmt.Sprintf("%s: %s", op, message)
	case op != "":
		return op + " failed"
	case message != "":
		return message
	default:
		return "request failed"
	}
}

func (e *RequestFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsManagedUnauthorizedError(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureManagedUnauthorized
}

func IsDirectUnauthorizedError(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureDirectUnauthorized
}

func IsClientCancelledError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureClientCancelled
}

func IsUpstreamTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.Kind == FailureUpstreamTimeout
}

// IsSessionCapacityRateLimit reports a 429 caused by one upstream
// conversation's context or turn capacity, not account-wide throughput.
func IsSessionCapacityRateLimit(err error) bool {
	var failure *RequestFailure
	return errors.As(err, &failure) && failure.RateLimitScope == RateLimitScopeSessionCapacity
}

func requestContextFailure(op string, ctx context.Context, cause error) *RequestFailure {
	if ctx != nil {
		if err := ctx.Err(); errors.Is(err, context.Canceled) {
			return &RequestFailure{Op: op, Kind: FailureClientCancelled, Message: "client cancelled request", Cause: firstError(cause, err)}
		}
		if err := ctx.Err(); errors.Is(err, context.DeadlineExceeded) {
			return &RequestFailure{Op: op, Kind: FailureUpstreamTimeout, Message: "request timed out", Cause: firstError(cause, err)}
		}
	}
	if errors.Is(cause, context.Canceled) {
		return &RequestFailure{Op: op, Kind: FailureClientCancelled, Message: "client cancelled request", Cause: cause}
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return &RequestFailure{Op: op, Kind: FailureUpstreamTimeout, Message: "request timed out", Cause: cause}
	}
	return nil
}

func transportFailure(op string, ctx context.Context, err error) *RequestFailure {
	if failure := requestContextFailure(op, ctx, err); failure != nil {
		return failure
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &RequestFailure{Op: op, Kind: FailureUpstreamTimeout, Message: err.Error(), Cause: err}
	}
	return &RequestFailure{Op: op, Kind: FailureUpstreamNetwork, Message: err.Error(), Cause: err}
}

func requestFailureKind(err error) FailureKind {
	var failure *RequestFailure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return FailureUnknown
}

func requestFailureWithOp(op string, err error) error {
	var failure *RequestFailure
	if !errors.As(err, &failure) {
		return err
	}
	next := *failure
	next.Op = op
	return &next
}

func firstError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
