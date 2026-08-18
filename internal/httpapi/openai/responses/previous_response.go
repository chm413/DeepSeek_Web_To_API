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
		config.Logger.Warn("[responses_state] previous response input unavailable",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(previousID),
			"stage", "input_snapshot",
		)
		return fmt.Errorf("previous_response_id %q was not found or has expired", previousID)
	}
	previousResponse, ok := store.get(owner, previousID)
	if !ok {
		config.Logger.Warn("[responses_state] previous response object unavailable",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(previousID),
			"stage", "response_object",
		)
		return fmt.Errorf("previous_response_id %q was not found or has expired", previousID)
	}

	// Responses clients commonly send tools only on the root request. Restore
	// the exact prior contract when a follow-up omits either field, while
	// respecting an explicit tools/tool_choice field in the new request.
	_, explicitTools := req["tools"]
	_, explicitToolChoice := req["tool_choice"]
	inheritance := inheritStoredToolContract(req,
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
	config.Logger.Info("[responses_state] merged previous response",
		"owner_fingerprint", responseStateFingerprint(owner),
		"response_id_fingerprint", responseStateFingerprint(previousID),
		"stored_messages", len(previousState.Messages),
		"stored_context_bytes", responseStateSize(previousState.Messages),
		"output_items", responseOutputItemCount(previousResponse["output"]),
		"current_input_items", len(current),
		"merged_messages", len(combined),
		"merged_context_bytes", responseStateSize(combined),
		"tools_explicit", explicitTools,
		"tools_inherited", inheritance.Tools,
		"stored_tools_present", previousState.HasTools,
		"stored_tool_count", responseToolCount(previousState.Tools),
		"tool_choice_explicit", explicitToolChoice,
		"tool_choice_inherited", inheritance.ToolChoice,
		"stored_tool_choice_present", previousState.HasToolChoice,
		"tool_contract_fingerprint", responseToolContractFingerprint(
			requestValue(req, "tools"), requestFieldPresent(req, "tools"),
			requestValue(req, "tool_choice"), requestFieldPresent(req, "tool_choice")),
	)
	return nil
}

type toolContractInheritance struct {
	Tools      bool
	ToolChoice bool
}

func inheritStoredToolContract(req map[string]any, hasTools bool, tools any, hasToolChoice bool, toolChoice any) toolContractInheritance {
	if req == nil {
		return toolContractInheritance{}
	}
	inherited := toolContractInheritance{}
	if hasTools {
		if _, present := req["tools"]; !present {
			req["tools"] = cloneAnyValue(tools)
			inherited.Tools = true
		}
	}
	if hasToolChoice {
		if _, present := req["tool_choice"]; !present {
			req["tool_choice"] = cloneAnyValue(toolChoice)
			inherited.ToolChoice = true
		}
	}
	return inherited
}

func requestFieldPresent(req map[string]any, key string) bool {
	if req == nil {
		return false
	}
	_, ok := req[key]
	return ok
}

func requestValue(req map[string]any, key string) any {
	if req == nil {
		return nil
	}
	return req[key]
}

func responseOutputItemCount(value any) int {
	return responseStateItemCount(value)
}

func responseString(value any) string {
	text, _ := value.(string)
	return text
}
