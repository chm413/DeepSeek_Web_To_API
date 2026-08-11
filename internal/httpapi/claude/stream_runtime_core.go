package claude

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
	"DeepSeek_Web_To_API/internal/toolcall"
	"DeepSeek_Web_To_API/internal/toolstream"
)

type claudeStreamRuntime struct {
	w        http.ResponseWriter
	rc       *http.ResponseController
	canFlush bool

	model           string
	toolNames       []string
	messages        []any
	toolsRaw        any
	promptTokenText string

	thinkingEnabled       bool
	searchEnabled         bool
	bufferToolContent     bool
	stripReferenceMarkers bool

	messageID              string
	thinking               strings.Builder
	text                   strings.Builder
	rawText                strings.Builder
	toolSieve              toolstream.State
	toolCalls              []toolcall.ParsedToolCall
	leakedToolResultFilter shared.LeakedToolResultStreamFilter

	nextBlockIndex     int
	thinkingBlockOpen  bool
	thinkingBlockIndex int
	textBlockOpen      bool
	textBlockIndex     int
	ended              bool
	upstreamErr        string
	responseMessageID  int
	finalContent       []any
	completed          bool
}

func newClaudeStreamRuntime(
	w http.ResponseWriter,
	rc *http.ResponseController,
	canFlush bool,
	model string,
	messages []any,
	thinkingEnabled bool,
	searchEnabled bool,
	stripReferenceMarkers bool,
	toolNames []string,
	toolsRaw any,
	promptTokenText string,
) *claudeStreamRuntime {
	return &claudeStreamRuntime{
		w:                     w,
		rc:                    rc,
		canFlush:              canFlush,
		model:                 model,
		messages:              messages,
		thinkingEnabled:       thinkingEnabled,
		searchEnabled:         searchEnabled,
		bufferToolContent:     len(toolNames) > 0,
		stripReferenceMarkers: stripReferenceMarkers,
		toolNames:             toolNames,
		toolsRaw:              toolsRaw,
		promptTokenText:       promptTokenText,
		messageID:             fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		thinkingBlockIndex:    -1,
		textBlockIndex:        -1,
	}
}

func (s *claudeStreamRuntime) onParsed(parsed sse.LineResult) streamengine.ParsedDecision {
	if !parsed.Parsed {
		return streamengine.ParsedDecision{}
	}
	if parsed.ResponseMessageID > 0 {
		s.responseMessageID = parsed.ResponseMessageID
	}
	if parsed.ErrorMessage != "" {
		s.upstreamErr = parsed.ErrorMessage
		return streamengine.ParsedDecision{Stop: true, StopReason: streamengine.StopReason("upstream_error")}
	}
	if parsed.Stop {
		return streamengine.ParsedDecision{Stop: true}
	}

	contentSeen := false
	for _, p := range parsed.Parts {
		cleanedText := cleanVisibleOutput(p.Text, s.stripReferenceMarkers)
		if s.bufferToolContent {
			cleanedText = cleanVisibleOutputPreservingToolMarkup(p.Text, s.stripReferenceMarkers)
		}
		if cleanedText == "" {
			continue
		}
		if p.Type != "thinking" && s.searchEnabled && sse.IsCitation(cleanedText) {
			continue
		}
		contentSeen = true

		if p.Type == "thinking" {
			if !s.thinkingEnabled {
				continue
			}
			trimmed := sse.TrimContinuationOverlap(s.thinking.String(), cleanedText)
			if trimmed == "" {
				continue
			}
			s.thinking.WriteString(trimmed)
			s.closeTextBlock()
			if !s.thinkingBlockOpen {
				s.thinkingBlockIndex = s.nextBlockIndex
				s.nextBlockIndex++
				s.send("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": s.thinkingBlockIndex,
					"content_block": map[string]any{
						"type":     "thinking",
						"thinking": "",
					},
				})
				s.thinkingBlockOpen = true
			}
			s.send("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": s.thinkingBlockIndex,
				"delta": map[string]any{
					"type":     "thinking_delta",
					"thinking": trimmed,
				},
			})
			continue
		}

		rawTrimmed := sse.TrimContinuationOverlap(s.rawText.String(), p.Text)
		if rawTrimmed == "" {
			continue
		}
		s.rawText.WriteString(rawTrimmed)
		if s.bufferToolContent {
			s.processToolStreamEvents(toolstream.ProcessChunk(&s.toolSieve, rawTrimmed, s.toolNames))
			continue
		}
		s.emitTextDelta(rawTrimmed)
	}

	return streamengine.ParsedDecision{ContentSeen: contentSeen}
}

func (s *claudeStreamRuntime) processToolStreamEvents(events []toolstream.Event) {
	for _, evt := range events {
		if len(evt.ToolCalls) > 0 {
			s.toolCalls = append(s.toolCalls, evt.ToolCalls...)
			continue
		}
		if evt.Content != "" {
			s.emitTextDelta(evt.Content)
		}
	}
}

func (s *claudeStreamRuntime) emitTextDelta(raw string) bool {
	raw = s.leakedToolResultFilter.Filter(raw)
	if raw == "" {
		return false
	}
	cleanedText := cleanVisibleOutput(raw, s.stripReferenceMarkers)
	if cleanedText == "" {
		return false
	}
	if s.searchEnabled && sse.IsCitation(cleanedText) {
		return false
	}
	trimmed := sse.TrimContinuationOverlap(s.text.String(), cleanedText)
	if trimmed == "" {
		return false
	}
	s.text.WriteString(trimmed)
	s.closeThinkingBlock()
	if !s.textBlockOpen {
		s.textBlockIndex = s.nextBlockIndex
		s.nextBlockIndex++
		s.send("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": s.textBlockIndex,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
		s.textBlockOpen = true
	}
	s.send("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.textBlockIndex,
		"delta": map[string]any{
			"type": "text_delta",
			"text": trimmed,
		},
	})
	return true
}
