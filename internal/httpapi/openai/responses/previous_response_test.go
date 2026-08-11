package responses

import (
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/promptcompat"
)

func TestMergePreviousResponseInputReconstructsVisibleHistory(t *testing.T) {
	h := &Handler{responses: newResponseStore(0)}
	owner := "caller:test"
	responseID := "resp_previous"
	h.responses.putInput(owner, responseID, []any{
		map[string]any{"role": "user", "content": "first question"},
	})
	h.responses.put(owner, responseID, map[string]any{
		"id": responseID,
		"output": []any{
			map[string]any{"type": "reasoning", "encrypted_content": "opaque-secret"},
			map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "previous answer"}},
			},
		},
	})
	req := map[string]any{
		"previous_response_id": responseID,
		"input":                "follow up",
	}

	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		t.Fatalf("merge previous response: %v", err)
	}
	messages := promptcompat.NormalizeResponsesInputAsMessages(req["input"])
	prompt, _ := promptcompat.BuildOpenAIPrompt(messages, nil, "", promptcompat.DefaultToolChoicePolicy(), false)
	for _, expected := range []string{"first question", "previous answer", "follow up"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing %q from reconstructed prompt: %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "opaque-secret") {
		t.Fatalf("opaque reasoning leaked into reconstructed prompt: %q", prompt)
	}
}

func TestMergePreviousResponseInputRejectsMissingSnapshot(t *testing.T) {
	h := &Handler{responses: newResponseStore(0)}
	err := h.mergePreviousResponseInput("caller:test", map[string]any{
		"previous_response_id": "resp_missing",
		"input":                "follow up",
	})
	if err == nil {
		t.Fatal("expected missing previous response error")
	}
}
