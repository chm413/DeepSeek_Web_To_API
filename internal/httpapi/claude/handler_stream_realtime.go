package claude

import (
	"io"
	"net/http"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/httpapi/historycapture"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
)

func (h *Handler) handleClaudeStreamRealtime(w http.ResponseWriter, r *http.Request, resp *http.Response, model string, messages []any, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, historySessions ...*historycapture.Session) claudeCompletionOutcome {
	historySession := firstHistorySession(historySessions)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		message := string(body)
		if historySession != nil {
			historySession.Error(resp.StatusCode, message, "error", "", "")
		}
		writeClaudeError(w, resp.StatusCode, message)
		return claudeCompletionOutcome{}
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_, canFlush := w.(http.Flusher)
	if !canFlush {
		config.Logger.Warn("[claude_stream] response writer does not support flush; streaming may be buffered")
	}

	streamRuntime := newClaudeStreamRuntime(
		w,
		rc,
		canFlush,
		model,
		messages,
		thinkingEnabled,
		searchEnabled,
		h.compatStripReferenceMarkers(),
		toolNames,
		toolsRaw,
		buildClaudePromptTokenText(messages, thinkingEnabled),
	)
	streamRuntime.sendMessageStart()

	initialType := "text"
	if thinkingEnabled {
		initialType = "thinking"
	}
	streamengine.ConsumeSSE(streamengine.ConsumeConfig{
		Context:             r.Context(),
		Body:                resp.Body,
		ThinkingEnabled:     thinkingEnabled,
		InitialType:         initialType,
		KeepAliveInterval:   claudeStreamPingInterval,
		IdleTimeout:         claudeStreamIdleTimeout,
		MaxKeepAliveNoInput: claudeStreamMaxKeepaliveCnt,
	}, streamengine.ConsumeHooks{
		OnKeepAlive: func() {
			streamRuntime.sendPing()
		},
		OnParsed: func(parsed sse.LineResult) streamengine.ParsedDecision {
			decision := streamRuntime.onParsed(parsed)
			if historySession != nil {
				historySession.Progress(streamRuntime.thinking.String(), streamRuntime.text.String())
			}
			return decision
		},
		OnFinalize: func(reason streamengine.StopReason, scannerErr error) {
			streamRuntime.onFinalize(reason, scannerErr)
			if historySession == nil {
				return
			}
			if string(reason) == "upstream_error" {
				historySession.Error(http.StatusBadGateway, streamRuntime.upstreamErr, "upstream_error", streamRuntime.thinking.String(), streamRuntime.text.String())
				return
			}
			if scannerErr != nil {
				historySession.Error(http.StatusBadGateway, scannerErr.Error(), "stream_error", streamRuntime.thinking.String(), streamRuntime.text.String())
				return
			}
			finalText := cleanVisibleOutput(streamRuntime.text.String(), streamRuntime.stripReferenceMarkers)
			historySession.Success(http.StatusOK, streamRuntime.thinking.String(), finalText, "end_turn", nil)
		},
		OnContextDone: func() {
			if historySession != nil {
				historySession.Stopped(streamRuntime.thinking.String(), streamRuntime.text.String(), string(streamengine.StopReasonContextCancelled))
			}
		},
	})
	if !streamRuntime.completed || streamRuntime.responseMessageID <= 0 || len(streamRuntime.finalContent) == 0 {
		return claudeCompletionOutcome{}
	}
	responseMessages := normalizeClaudeResponseMessages(streamRuntime.finalContent)
	return claudeCompletionOutcome{success: len(responseMessages) > 0, responseMessageID: streamRuntime.responseMessageID, responseMessages: responseMessages}
}
