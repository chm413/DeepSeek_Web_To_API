package shared

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

const StatusClientClosedRequest = 499

type UpstreamErrorDetail struct {
	Status       int
	Message      string
	Code         string
	FinishReason string
	Stopped      bool
}

func CompletionErrorDetail(err error) UpstreamErrorDetail {
	return upstreamRequestErrorDetail(err, "completion")
}

func SessionErrorDetail(err error) UpstreamErrorDetail {
	return upstreamRequestErrorDetail(err, "session")
}

func PowErrorDetail(err error) UpstreamErrorDetail {
	return upstreamRequestErrorDetail(err, "pow")
}

func upstreamRequestErrorDetail(err error, op string) UpstreamErrorDetail {
	var healthErr *auth.AccountHealthError
	if errors.As(err, &healthErr) {
		if healthErr.State == account.HealthTemporarilyMuted {
			return UpstreamErrorDetail{
				Status:       http.StatusTooManyRequests,
				Message:      "Upstream account is temporarily muted; the account was skipped until the upstream mute expires.",
				Code:         "account_temporarily_muted",
				FinishReason: "account_temporarily_muted",
			}
		}
		if healthErr.State == account.HealthPermanentlyBanned {
			return UpstreamErrorDetail{
				Status:       http.StatusForbidden,
				Message:      "Upstream account is permanently banned and has been removed from rotation.",
				Code:         "account_permanently_banned",
				FinishReason: "account_permanently_banned",
			}
		}
	}
	var failure *dsclient.RequestFailure
	if errors.As(err, &failure) {
		if dsclient.IsSessionCapacityRateLimit(err) {
			return UpstreamErrorDetail{
				Status:       http.StatusRequestEntityTooLarge,
				Message:      "Upstream conversation reached its context or turn capacity; the account remains available and this session should be rotated.",
				Code:         "upstream_session_capacity",
				FinishReason: "upstream_session_capacity",
			}
		}
		switch failure.Kind {
		case dsclient.FailureClientCancelled:
			return UpstreamErrorDetail{
				Status:       StatusClientClosedRequest,
				Message:      "Client cancelled the request before upstream completion.",
				Code:         "client_cancelled",
				FinishReason: "context_cancelled",
				Stopped:      true,
			}
		case dsclient.FailureUpstreamTimeout:
			return UpstreamErrorDetail{
				Status:       http.StatusGatewayTimeout,
				Message:      prefixUpstreamMessage(op, "timed out", failure.Message),
				Code:         "upstream_timeout",
				FinishReason: "upstream_timeout",
			}
		case dsclient.FailureUpstreamNetwork:
			return UpstreamErrorDetail{
				Status:       http.StatusBadGateway,
				Message:      prefixUpstreamMessage(op, "network error", failure.Message),
				Code:         "upstream_network_error",
				FinishReason: "upstream_network_error",
			}
		case dsclient.FailureUpstreamStatus:
			status := failure.StatusCode
			if status <= 0 {
				status = http.StatusBadGateway
			}
			return UpstreamErrorDetail{
				Status:       status,
				Message:      prefixUpstreamHTTPMessage(op, status, failure.Message),
				Code:         "upstream_http_status",
				FinishReason: "upstream_http_status",
			}
		case dsclient.FailureManagedUnauthorized:
			return UpstreamErrorDetail{
				Status:       http.StatusUnauthorized,
				Message:      "Account token is invalid. Please re-login the account in admin.",
				Code:         "authentication_failed",
				FinishReason: "managed_unauthorized",
			}
		case dsclient.FailureDirectUnauthorized:
			return UpstreamErrorDetail{
				Status:       http.StatusUnauthorized,
				Message:      "Invalid token. If this should be a DeepSeek_Web_To_API key, add it to config.keys first.",
				Code:         "authentication_failed",
				FinishReason: "direct_unauthorized",
			}
		}
	}
	if errors.Is(err, context.Canceled) {
		return UpstreamErrorDetail{
			Status:       StatusClientClosedRequest,
			Message:      "Client cancelled the request before upstream completion.",
			Code:         "client_cancelled",
			FinishReason: "context_cancelled",
			Stopped:      true,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return UpstreamErrorDetail{
			Status:       http.StatusGatewayTimeout,
			Message:      prefixUpstreamMessage(op, "timed out", err.Error()),
			Code:         "upstream_timeout",
			FinishReason: "upstream_timeout",
		}
	}
	return UpstreamErrorDetail{
		Status:       http.StatusBadGateway,
		Message:      prefixUpstreamMessage(op, "failed", errorMessage(err, "request failed")),
		Code:         "upstream_request_failed",
		FinishReason: "upstream_request_failed",
	}
}

// IsSessionCapacityRateLimit distinguishes a conversation-specific upstream
// 429 from an account-wide rate limit. The latter may move to another account;
// the former must rotate or compress the current conversation instead.
func IsSessionCapacityRateLimit(err error) bool {
	return dsclient.IsSessionCapacityRateLimit(err)
}

func prefixUpstreamHTTPMessage(op string, status int, detail string) string {
	prefix := "Upstream " + strings.TrimSpace(op) + " failed"
	if status > 0 {
		prefix += " with HTTP " + strconv.Itoa(status)
		if text := http.StatusText(status); text != "" {
			prefix += " " + text
		}
	}
	if trimmed := strings.TrimSpace(detail); trimmed != "" {
		return prefix + ": " + trimmed
	}
	return prefix + "."
}

func prefixUpstreamMessage(op, reason, detail string) string {
	prefix := "Upstream " + strings.TrimSpace(op) + " " + strings.TrimSpace(reason)
	if trimmed := strings.TrimSpace(detail); trimmed != "" {
		return prefix + ": " + trimmed
	}
	return prefix + "."
}

func errorMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	if trimmed := strings.TrimSpace(err.Error()); trimmed != "" {
		return trimmed
	}
	return fallback
}
