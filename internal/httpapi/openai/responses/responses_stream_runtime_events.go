package responses

import (
	"encoding/json"
	"strings"

	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/sse"
	"DeepSeek_Web_To_API/internal/toolstream"

	"github.com/google/uuid"
)

func (s *responsesStreamRuntime) nextSequence() int {
	s.sequence++
	return s.sequence
}

func (s *responsesStreamRuntime) sendEvent(event string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	if _, ok := payload["sequence_number"]; !ok {
		payload["sequence_number"] = s.nextSequence()
	}
	b, _ := json.Marshal(payload)
	_, _ = s.w.Write([]byte("event: " + event + "\n"))
	_, _ = s.w.Write([]byte("data: "))
	_, _ = s.w.Write(b)
	_, _ = s.w.Write([]byte("\n\n"))
	if s.canFlush {
		_ = s.rc.Flush()
	}
}

func (s *responsesStreamRuntime) sendCreated() {
	s.start()
}

// start commits the Responses stream only after there is a deliverable output
// event. An upstream stream that ends with no content can then be surfaced as
// an ordinary HTTP error instead of a response.failed SSE terminal event.
func (s *responsesStreamRuntime) start() {
	if s == nil || s.started {
		return
	}
	s.started = true
	s.sendEvent("response.created", openaifmt.BuildResponsesCreatedPayload(s.responseID, s.model))
	s.emitOutputPrefix()
}

func (s *responsesStreamRuntime) hasStarted() bool {
	return s != nil && s.started
}

func (s *responsesStreamRuntime) emitOutputPrefix() {
	for _, rawItem := range s.outputPrefix {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemID := strings.TrimSpace(responseString(item["id"]))
		if itemID == "" {
			itemID = "item_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			item["id"] = itemID
		}
		outputIndex := s.allocateOutputIndex()
		s.sendEvent("response.output_item.added", openaifmt.BuildResponsesOutputItemAddedPayload(s.responseID, itemID, outputIndex, item))
		s.sendEvent("response.output_item.done", openaifmt.BuildResponsesOutputItemDonePayload(s.responseID, itemID, outputIndex, item))
	}
}

func (s *responsesStreamRuntime) sendDone() {
	_, _ = s.w.Write([]byte("data: [DONE]\n\n"))
	if s.canFlush {
		_ = s.rc.Flush()
	}
}

func (s *responsesStreamRuntime) processToolStreamEvents(events []toolstream.Event, emitContent bool, resetAfterToolCalls bool) {
	for _, evt := range events {
		if emitContent && evt.Content != "" {
			cleaned := cleanVisibleOutput(evt.Content, s.stripReferenceMarkers)
			if cleaned != "" && (!s.searchEnabled || !sse.IsCitation(cleaned)) {
				s.emitTextDelta(cleaned)
			}
		}
		if len(evt.ToolCallDeltas) > 0 {
			if !s.emitEarlyToolDeltas {
				continue
			}
			filtered := filterIncrementalToolCallDeltasByAllowed(evt.ToolCallDeltas, s.functionNames)
			if len(filtered) == 0 {
				continue
			}
			s.emitFunctionCallDeltaEvents(filtered)
		}
		if len(evt.ToolCalls) > 0 {
			s.emitFunctionCallDoneEvents(evt.ToolCalls)
			if resetAfterToolCalls {
				s.resetStreamToolCallState()
			}
		}
	}
}
