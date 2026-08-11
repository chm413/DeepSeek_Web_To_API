package chat

import (
	"encoding/json"
	"net/http"
	"strings"

	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
	"DeepSeek_Web_To_API/internal/toolstream"
)

type chatStreamRuntime struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	canFlush bool

	completionID  string
	created       int64
	model         string
	finalPrompt   string
	refFileTokens int
	toolNames     []string
	toolsRaw      any

	thinkingEnabled       bool
	exposeReasoning       bool
	searchEnabled         bool
	stripReferenceMarkers bool
	requireToolCall       bool

	firstChunkSent       bool
	bufferToolContent    bool
	emitEarlyToolDeltas  bool
	toolCallsEmitted     bool
	toolCallsDoneEmitted bool

	toolSieve             toolstream.State
	streamToolCallIDs     map[int]string
	streamToolNames       map[int]string
	rawThinking           strings.Builder
	thinking              strings.Builder
	toolDetectionThinking strings.Builder
	rawText               strings.Builder
	text                  strings.Builder
	responseMessageID     int
	finalToolCalls        []map[string]any

	finalThinking     string
	finalText         string
	finalFinishReason string
	finalUsage        map[string]any
	finalErrorStatus  int
	finalErrorMessage string
	finalErrorCode    string
}

type chatDeltaBatch struct {
	runtime *chatStreamRuntime
	field   string
	text    strings.Builder
}

func (b *chatDeltaBatch) append(field, text string) {
	if text == "" {
		return
	}
	if b.field != "" && b.field != field {
		b.flush()
	}
	b.field = field
	b.text.WriteString(text)
}

func (b *chatDeltaBatch) flush() {
	if b.field == "" || b.text.Len() == 0 {
		return
	}
	b.runtime.sendDelta(map[string]any{b.field: b.text.String()})
	b.field = ""
	b.text.Reset()
}

func newChatStreamRuntime(
	w http.ResponseWriter,
	rc *http.ResponseController,
	canFlush bool,
	completionID string,
	created int64,
	model string,
	finalPrompt string,
	thinkingEnabled bool,
	exposeReasoning bool,
	searchEnabled bool,
	stripReferenceMarkers bool,
	toolNames []string,
	toolsRaw any,
	requireToolCall bool,
	bufferToolContent bool,
	emitEarlyToolDeltas bool,
) *chatStreamRuntime {
	return &chatStreamRuntime{
		w:                     w,
		rc:                    rc,
		canFlush:              canFlush,
		completionID:          completionID,
		created:               created,
		model:                 model,
		finalPrompt:           finalPrompt,
		toolNames:             toolNames,
		toolsRaw:              toolsRaw,
		thinkingEnabled:       thinkingEnabled,
		exposeReasoning:       exposeReasoning,
		searchEnabled:         searchEnabled,
		stripReferenceMarkers: stripReferenceMarkers,
		requireToolCall:       requireToolCall,
		bufferToolContent:     bufferToolContent,
		emitEarlyToolDeltas:   emitEarlyToolDeltas,
		streamToolCallIDs:     map[int]string{},
		streamToolNames:       map[int]string{},
	}
}

func (s *chatStreamRuntime) sendKeepAlive() {
	if !s.canFlush {
		return
	}
	_, _ = s.w.Write([]byte(": keep-alive\n\n"))
	_ = s.rc.Flush()
}

func (s *chatStreamRuntime) sendChunk(v any) {
	b, _ := json.Marshal(v)
	_, _ = s.w.Write([]byte("data: "))
	_, _ = s.w.Write(b)
	_, _ = s.w.Write([]byte("\n\n"))
	if s.canFlush {
		_ = s.rc.Flush()
	}
}

func (s *chatStreamRuntime) sendDelta(delta map[string]any) {
	if len(delta) == 0 {
		return
	}
	if !s.firstChunkSent {
		delta["role"] = "assistant"
		s.firstChunkSent = true
	}
	s.sendChunk(openaifmt.BuildChatStreamChunk(
		s.completionID,
		s.created,
		s.model,
		[]map[string]any{openaifmt.BuildChatStreamDeltaChoice(0, delta)},
		nil,
	))
}

func (s *chatStreamRuntime) sendDone() {
	_, _ = s.w.Write([]byte("data: [DONE]\n\n"))
	if s.canFlush {
		_ = s.rc.Flush()
	}
}

func (s *chatStreamRuntime) sendFailedChunk(status int, message, code string) {
	s.finalErrorStatus = status
	s.finalErrorMessage = message
	s.finalErrorCode = code
	s.sendChunk(map[string]any{
		"status_code": status,
		"error": map[string]any{
			"message": message,
			"type":    openAIErrorType(status),
			"code":    code,
			"param":   nil,
		},
	})
	s.sendDone()
}

func (s *chatStreamRuntime) markContextCancelled() {
	s.finalErrorStatus = 499
	s.finalErrorMessage = "request context cancelled"
	s.finalErrorCode = string(streamengine.StopReasonContextCancelled)
	s.finalThinking = s.thinking.String()
	s.finalText = cleanVisibleOutput(s.text.String(), s.stripReferenceMarkers)
	s.finalFinishReason = string(streamengine.StopReasonContextCancelled)
}

func (s *chatStreamRuntime) resetStreamToolCallState() {
	s.streamToolCallIDs = map[int]string{}
	s.streamToolNames = map[int]string{}
}

func (s *chatStreamRuntime) finalize(finishReason string, deferEmptyOutput bool) bool {
	s.finalErrorStatus = 0
	s.finalErrorMessage = ""
	s.finalErrorCode = ""
	finalThinking := s.thinking.String()
	finalToolDetectionThinking := s.toolDetectionThinking.String()
	finalText := cleanVisibleOutput(s.text.String(), s.stripReferenceMarkers)
	s.finalThinking = finalThinking
	s.finalText = finalText
	detected := detectAssistantToolCalls(s.rawText.String(), finalText, s.rawThinking.String(), finalToolDetectionThinking, s.toolNames)
	if len(detected.Calls) > 0 && !s.toolCallsDoneEmitted {
		finishReason = "tool_calls"
		formattedCalls := formatFinalStreamToolCallsWithStableIDs(detected.Calls, s.streamToolCallIDs, s.toolsRaw)
		s.finalToolCalls = append(s.finalToolCalls, formattedCalls...)
		delta := map[string]any{
			"tool_calls": formattedCalls,
		}
		if !s.firstChunkSent {
			delta["role"] = "assistant"
			s.firstChunkSent = true
		}
		s.sendChunk(openaifmt.BuildChatStreamChunk(
			s.completionID,
			s.created,
			s.model,
			[]map[string]any{openaifmt.BuildChatStreamDeltaChoice(0, delta)},
			nil,
		))
		s.toolCallsEmitted = true
		s.toolCallsDoneEmitted = true
	} else if s.bufferToolContent {
		batch := chatDeltaBatch{runtime: s}
		for _, evt := range toolstream.Flush(&s.toolSieve, s.toolNames) {
			if len(evt.ToolCalls) > 0 {
				batch.flush()
				finishReason = "tool_calls"
				s.toolCallsEmitted = true
				s.toolCallsDoneEmitted = true
				formattedCalls := formatFinalStreamToolCallsWithStableIDs(evt.ToolCalls, s.streamToolCallIDs, s.toolsRaw)
				s.finalToolCalls = append(s.finalToolCalls, formattedCalls...)
				tcDelta := map[string]any{"tool_calls": formattedCalls}
				if !s.firstChunkSent {
					tcDelta["role"] = "assistant"
					s.firstChunkSent = true
				}
				s.sendChunk(openaifmt.BuildChatStreamChunk(
					s.completionID,
					s.created,
					s.model,
					[]map[string]any{openaifmt.BuildChatStreamDeltaChoice(0, tcDelta)},
					nil,
				))
				s.resetStreamToolCallState()
			}
			if evt.Content == "" || s.requireToolCall {
				continue
			}
			cleaned := cleanVisibleOutput(evt.Content, s.stripReferenceMarkers)
			if cleaned == "" || (s.searchEnabled && sse.IsCitation(cleaned)) {
				continue
			}
			batch.append("content", cleaned)
		}
		batch.flush()
	}

	if len(detected.Calls) > 0 || s.toolCallsEmitted {
		finishReason = "tool_calls"
	}
	if s.requireToolCall && len(detected.Calls) == 0 && !s.toolCallsEmitted {
		s.sendFailedChunk(http.StatusUnprocessableEntity, "tool_choice requires at least one valid tool call.", "tool_choice_violation")
		return true
	}
	if len(detected.Calls) == 0 && !s.toolCallsEmitted && strings.TrimSpace(finalText) == "" {
		status, message, code := upstreamEmptyOutputDetail(finishReason == "content_filter", finalText, finalThinking)
		if deferEmptyOutput {
			s.finalErrorStatus = status
			s.finalErrorMessage = message
			s.finalErrorCode = code
			return false
		}
		s.sendFailedChunk(status, message, code)
		return true
	}
	usage := openaifmt.BuildChatUsageForModel(s.model, s.finalPrompt, finalThinking, finalText, s.refFileTokens)
	s.finalFinishReason = finishReason
	s.finalUsage = usage
	s.sendChunk(openaifmt.BuildChatStreamChunk(
		s.completionID,
		s.created,
		s.model,
		[]map[string]any{openaifmt.BuildChatStreamFinishChoice(0, finishReason)},
		usage,
	))
	s.sendDone()
	return true
}

func (s *chatStreamRuntime) onParsed(parsed sse.LineResult) streamengine.ParsedDecision {
	if !parsed.Parsed {
		return streamengine.ParsedDecision{}
	}
	if parsed.ResponseMessageID > 0 {
		s.responseMessageID = parsed.ResponseMessageID
	}
	if parsed.ContentFilter {
		if strings.TrimSpace(s.text.String()) == "" {
			return streamengine.ParsedDecision{Stop: true, StopReason: streamengine.StopReason("content_filter")}
		}
		return streamengine.ParsedDecision{Stop: true, StopReason: streamengine.StopReasonHandlerRequested}
	}
	if parsed.ErrorMessage != "" {
		return streamengine.ParsedDecision{Stop: true, StopReason: streamengine.StopReason("content_filter")}
	}
	if parsed.Stop {
		return streamengine.ParsedDecision{Stop: true, StopReason: streamengine.StopReasonHandlerRequested}
	}

	contentSeen := false
	toolCallJustCompleted := false
	batch := chatDeltaBatch{runtime: s}
	for _, p := range parsed.ToolDetectionThinkingParts {
		trimmed := sse.TrimContinuationOverlap(s.toolDetectionThinking.String(), p.Text)
		if trimmed != "" {
			s.toolDetectionThinking.WriteString(trimmed)
		}
	}
	for _, p := range parsed.Parts {
		if p.Type == "thinking" {
			rawTrimmed := sse.TrimContinuationOverlap(s.rawThinking.String(), p.Text)
			if rawTrimmed != "" {
				s.rawThinking.WriteString(rawTrimmed)
				contentSeen = true
			}
			if s.thinkingEnabled {
				cleanedText := cleanVisibleOutput(rawTrimmed, s.stripReferenceMarkers)
				if cleanedText == "" {
					continue
				}
				trimmed := sse.TrimContinuationOverlap(s.thinking.String(), cleanedText)
				if trimmed == "" {
					continue
				}
				s.thinking.WriteString(trimmed)
				if s.exposeReasoning {
					batch.append("reasoning_content", trimmed)
				}
			}
		} else {
			rawTrimmed := sse.TrimContinuationOverlap(s.rawText.String(), p.Text)
			if rawTrimmed == "" {
				continue
			}
			s.rawText.WriteString(rawTrimmed)
			contentSeen = true
			cleanedText := cleanVisibleOutput(rawTrimmed, s.stripReferenceMarkers)
			if s.searchEnabled && sse.IsCitation(cleanedText) {
				continue
			}
			trimmed := sse.TrimContinuationOverlap(s.text.String(), cleanedText)
			if trimmed != "" {
				s.text.WriteString(trimmed)
			}
			if !s.bufferToolContent {
				if trimmed == "" {
					continue
				}
				batch.append("content", trimmed)
			} else {
				events := toolstream.ProcessChunk(&s.toolSieve, rawTrimmed, s.toolNames)
				for _, evt := range events {
					if len(evt.ToolCallDeltas) > 0 {
						if !s.emitEarlyToolDeltas {
							continue
						}
						filtered := filterIncrementalToolCallDeltasByAllowed(evt.ToolCallDeltas, s.streamToolNames)
						if len(filtered) == 0 {
							continue
						}
						formatted := formatIncrementalStreamToolCallDeltas(filtered, s.streamToolCallIDs)
						if len(formatted) == 0 {
							continue
						}
						batch.flush()
						tcDelta := map[string]any{
							"tool_calls": formatted,
						}
						s.toolCallsEmitted = true
						s.sendDelta(tcDelta)
						continue
					}
					if len(evt.ToolCalls) > 0 {
						batch.flush()
						s.toolCallsEmitted = true
						s.toolCallsDoneEmitted = true
						formattedCalls := formatFinalStreamToolCallsWithStableIDs(evt.ToolCalls, s.streamToolCallIDs, s.toolsRaw)
						s.finalToolCalls = append(s.finalToolCalls, formattedCalls...)
						tcDelta := map[string]any{"tool_calls": formattedCalls}
						s.sendDelta(tcDelta)
						s.resetStreamToolCallState()
						toolCallJustCompleted = true
						continue
					}
					if evt.Content != "" && !s.requireToolCall {
						cleaned := cleanVisibleOutput(evt.Content, s.stripReferenceMarkers)
						if cleaned == "" || (s.searchEnabled && sse.IsCitation(cleaned)) {
							continue
						}
						batch.append("content", cleaned)
					}
				}
			}
		}
	}
	batch.flush()
	if toolCallJustCompleted {
		// Once a complete tool_call block has been delivered, stop consuming
		// upstream SSE immediately and let finalize() emit the
		// finish_reason="tool_calls" chunk + [DONE]. This avoids a known
		// stream-cut symptom where DeepSeek occasionally fails to send the
		// trailing [DONE] after a tool_call, leaving the engine to wait for
		// idle/no-content timeouts (or client cancellation) and skip the
		// final framing chunk. See Meow-Calculations/DeepSeek_Web_To_API#4.
		return streamengine.ParsedDecision{ContentSeen: contentSeen, Stop: true, StopReason: streamengine.StopReasonHandlerRequested}
	}
	return streamengine.ParsedDecision{ContentSeen: contentSeen}
}

func (s *chatStreamRuntime) responseMessages() []any {
	if s == nil || s.finalErrorMessage != "" || s.responseMessageID <= 0 {
		return nil
	}
	message := map[string]any{"role": "assistant", "content": s.finalText}
	if s.exposeReasoning && strings.TrimSpace(s.finalThinking) != "" {
		message["reasoning_content"] = s.finalThinking
	}
	if len(s.finalToolCalls) > 0 {
		message["content"] = nil
		message["tool_calls"] = s.finalToolCalls
	}
	return []any{message}
}
