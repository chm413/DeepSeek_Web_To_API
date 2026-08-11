package claude

import (
	"encoding/json"
	"fmt"
	"time"

	streamengine "DeepSeek_Web_To_API/internal/stream"
	"DeepSeek_Web_To_API/internal/toolcall"
	"DeepSeek_Web_To_API/internal/toolstream"
	"DeepSeek_Web_To_API/internal/util"
)

func (s *claudeStreamRuntime) closeThinkingBlock() {
	if !s.thinkingBlockOpen {
		return
	}
	s.send("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.thinkingBlockIndex,
	})
	s.thinkingBlockOpen = false
	s.thinkingBlockIndex = -1
}

func (s *claudeStreamRuntime) closeTextBlock() {
	if !s.textBlockOpen {
		return
	}
	s.send("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.textBlockIndex,
	})
	s.textBlockOpen = false
	s.textBlockIndex = -1
}

func (s *claudeStreamRuntime) finalize(stopReason string) {
	if s.ended {
		return
	}
	s.ended = true

	if s.bufferToolContent {
		s.processToolStreamEvents(toolstream.Flush(&s.toolSieve, s.toolNames))
	}

	s.closeThinkingBlock()
	s.closeTextBlock()

	finalThinking := s.thinking.String()
	finalText := cleanVisibleOutput(s.text.String(), s.stripReferenceMarkers)
	s.finalContent = nil
	if finalThinking != "" {
		s.finalContent = append(s.finalContent, map[string]any{"type": "thinking", "thinking": finalThinking})
	}
	if finalText != "" {
		s.finalContent = append(s.finalContent, map[string]any{"type": "text", "text": finalText})
	}
	toolParseText := cleanVisibleOutputPreservingToolMarkup(s.rawText.String(), s.stripReferenceMarkers)
	if s.bufferToolContent {
		detected := s.toolCalls
		if len(detected) == 0 {
			detected = toolcall.ParseStandaloneToolCalls(toolParseText, s.toolNames)
		}
		if len(detected) == 0 && toolParseText == "" && finalThinking != "" {
			detected = toolcall.ParseStandaloneToolCalls(finalThinking, s.toolNames)
		}
		if len(detected) > 0 {
			s.emitToolUseBlocks(detected, &stopReason)
		}
	}

	outputTokens := util.CountOutputTokens(finalThinking, s.model) + util.CountOutputTokens(finalText, s.model)
	s.send("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": outputTokens,
		},
	})
	s.send("message_stop", map[string]any{"type": "message_stop"})
	s.completed = true
}

func (s *claudeStreamRuntime) emitToolUseBlocks(calls []toolcall.ParsedToolCall, stopReason *string) {
	if len(calls) == 0 {
		return
	}
	detected := toolcall.NormalizeParsedToolCallsForSchemas(calls, s.toolsRaw)
	if stopReason != nil {
		*stopReason = "tool_use"
	}
	idSeed := time.Now().UnixNano()
	for i, tc := range detected {
		idx := s.nextBlockIndex + i
		toolID := fmt.Sprintf("toolu_%d_%d", idSeed, idx)
		s.finalContent = append(s.finalContent, map[string]any{
			"type":  "tool_use",
			"id":    toolID,
			"name":  tc.Name,
			"input": tc.Input,
		})
		s.send("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": idx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    toolID,
				"name":  tc.Name,
				"input": map[string]any{},
			},
		})

		inputBytes, _ := json.Marshal(tc.Input)
		s.send("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": string(inputBytes),
			},
		})

		s.send("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": idx,
		})
	}
	s.nextBlockIndex += len(detected)
}

func (s *claudeStreamRuntime) onFinalize(reason streamengine.StopReason, scannerErr error) {
	if string(reason) == "upstream_error" {
		s.sendError(s.upstreamErr)
		return
	}
	if scannerErr != nil {
		s.sendError(scannerErr.Error())
		return
	}
	s.finalize("end_turn")
}
