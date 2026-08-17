package responses

import (
	"net/http"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/httpapi/historycapture"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	streamengine "DeepSeek_Web_To_API/internal/stream"
)

func writeCompletionCallError(w http.ResponseWriter, historySession *historycapture.Session, err error, thinking, content string) {
	detail := shared.CompletionErrorDetail(err)
	writeUpstreamCallError(w, historySession, detail, thinking, content)
}

func handleCreateSessionError(w http.ResponseWriter, historySession *historycapture.Session, a *auth.RequestAuth, err error) {
	detail := shared.SessionErrorDetail(err)
	if detail.Code != "authentication_failed" {
		writeUpstreamCallError(w, historySession, detail, "", "")
		return
	}
	if a != nil && a.UseConfigToken {
		message := "Account token is invalid. Please re-login the account in admin."
		if historySession != nil {
			historySession.Error(http.StatusUnauthorized, message, "error", "", "")
		}
		writeOpenAIError(w, http.StatusUnauthorized, message)
		return
	}
	if a != nil {
		a.MarkDirectTokenInvalid()
	}
	message := "Invalid token. If this should be a DeepSeek_Web_To_API key, add it to config.keys first."
	if historySession != nil {
		historySession.Error(http.StatusUnauthorized, message, "error", "", "")
	}
	writeOpenAIError(w, http.StatusUnauthorized, message)
}

func handlePowError(w http.ResponseWriter, historySession *historycapture.Session, a *auth.RequestAuth, err error) {
	detail := shared.PowErrorDetail(err)
	if detail.Code != "authentication_failed" {
		writeUpstreamCallError(w, historySession, detail, "", "")
		return
	}
	if a != nil && !a.UseConfigToken {
		a.MarkDirectTokenInvalid()
	}
	message := "Failed to get PoW (invalid token or unknown error)."
	if historySession != nil {
		historySession.Error(http.StatusUnauthorized, message, "error", "", "")
	}
	writeOpenAIError(w, http.StatusUnauthorized, message)
}

func writeUpstreamCallError(w http.ResponseWriter, historySession *historycapture.Session, detail shared.UpstreamErrorDetail, thinking, content string) {
	if historySession != nil {
		if detail.Stopped {
			historySession.Stopped(thinking, content, detail.FinishReason)
		} else {
			historySession.Error(detail.Status, detail.Message, detail.FinishReason, thinking, content)
		}
	}
	writeOpenAIErrorWithCode(w, detail.Status, detail.Message, detail.Code)
}

func failResponsesStreamCompletionError(streamRuntime *responsesStreamRuntime, historySession *historycapture.Session, err error) {
	detail := shared.CompletionErrorDetail(err)
	if detail.Stopped {
		if historySession != nil {
			historySession.Stopped(streamRuntime.thinking.String(), streamRuntime.text.String(), detail.FinishReason)
		}
		return
	}
	streamRuntime.failResponse(detail.Status, detail.Message, detail.Code)
	recordResponsesStreamHistory(streamRuntime, historySession)
}

// writeUnstartedResponsesStreamError preserves an HTTP error when the upstream
// produced no deliverable stream event. Once a stream has started, the
// Responses protocol requires response.failed instead because its HTTP status
// is already committed.
func writeUnstartedResponsesStreamError(w http.ResponseWriter, streamRuntime *responsesStreamRuntime) bool {
	if streamRuntime == nil || streamRuntime.hasStarted() || streamRuntime.finalErrorMessage == "" {
		return false
	}
	if streamRuntime.finalErrorCode == string(streamengine.StopReasonContextCancelled) {
		return false
	}
	status := streamRuntime.finalErrorStatus
	if status < http.StatusBadRequest {
		status = http.StatusBadGateway
	}
	config.Logger.Warn("[responses_stream] returning pre-stream upstream failure as HTTP error",
		"trace_id", streamRuntime.traceID,
		"status", status,
		"code", streamRuntime.finalErrorCode)
	writeOpenAIErrorWithCode(w, status, streamRuntime.finalErrorMessage, streamRuntime.finalErrorCode)
	return true
}
