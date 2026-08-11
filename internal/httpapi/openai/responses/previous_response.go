package responses

import (
	"fmt"
	"strings"

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
	previousMessages, ok := store.getInput(owner, previousID)
	if !ok {
		return fmt.Errorf("previous_response_id %q was not found or has expired", previousID)
	}
	previousResponse, ok := store.get(owner, previousID)
	if !ok {
		return fmt.Errorf("previous_response_id %q was not found or has expired", previousID)
	}

	combined := cloneAnySlice(previousMessages)
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
	return nil
}

func responseString(value any) string {
	text, _ := value.(string)
	return text
}
