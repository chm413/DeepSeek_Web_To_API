package responses

import (
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

// mergePreviousResponseInput expands the local response snapshot for
// previous_response_id. Only visible input/output items are reconstructed;
// opaque reasoning and encrypted compaction state are filtered by the normal
// Responses item converter instead of being fabricated.
func (h *Handler) mergePreviousResponseInput(owner string, req map[string]any) error {
	previousID := strings.TrimSpace(responseString(req["previous_response_id"]))
	if previousID == "" {
		return nil
	}
	store := h.getResponseStore()
	previousState, ok := store.getInputState(owner, previousID)
	if !ok {
		return fmt.Errorf("previous_response_id %q was not found or has expired", previousID)
	}
	previousResponse, ok := store.get(owner, previousID)
	if !ok {
		return fmt.Errorf("previous_response_id %q was not found or has expired", previousID)
	}

	// Responses clients commonly send tools only on the root request. Restore
	// the exact prior contract when a follow-up omits either field, while
	// respecting an explicit tools/tool_choice field in the new request.
	inheritedTools := inheritStoredToolContract(req,
		previousState.HasTools, previousState.Tools,
		previousState.HasToolChoice, previousState.ToolChoice)
	combined := cloneAnySlice(previousState.Messages)
	if output, ok := previousResponse["output"].([]any); ok {
		if visible := promptcompat.NormalizeResponsesInputAsMessages(output); len(visible) > 0 {
			combined = append(combined, visible...)
		}
	}
	current := promptcompat.ResponsesMessagesFromRequest(req)
	if len(current) > 0 {
		combined = append(combined, current...)
	}
	if len(combined) == 0 {
		return fmt.Errorf("previous_response_id %q has no reconstructable input", previousID)
	}
	req["input"] = combined
	delete(req, "messages")
	if inheritedTools {
		config.Logger.Info("[responses_state] inherited tool contract from previous response",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(previousID),
			"tools_present", previousState.HasTools,
			"tool_choice_present", previousState.HasToolChoice,
		)
	}
	return nil
}

func inheritStoredToolContract(req map[string]any, hasTools bool, tools any, hasToolChoice bool, toolChoice any) bool {
	if req == nil {
		return false
	}
	inherited := false
	if hasTools {
		if _, present := req["tools"]; !present {
			req["tools"] = cloneAnyValue(tools)
			inherited = true
		}
	}
	if hasToolChoice {
		if _, present := req["tool_choice"]; !present {
			req["tool_choice"] = cloneAnyValue(toolChoice)
			inherited = true
		}
	}
	return inherited
}

func responseString(value any) string {
	text, _ := value.(string)
	return text
}
