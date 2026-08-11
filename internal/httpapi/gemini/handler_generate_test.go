package gemini

import "testing"

func TestStripGeminiThoughtParts(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{
				"role": "model",
				"parts": []any{
					map[string]any{"thought": true, "text": "private reasoning"},
					map[string]any{"text": "visible answer"},
					map[string]any{
						"isThought": true,
						"text":      "private tool reasoning",
						"functionCall": map[string]any{
							"name": "lookup",
							"args": map[string]any{"query": "status"},
						},
					},
				},
			},
		},
	}

	if got := stripGeminiThoughtParts(req); got != 2 {
		t.Fatalf("dropped thought parts = %d, want 2", got)
	}
	contents := req["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("parts after sanitization = %#v, want visible text and function call", parts)
	}
	if text, _ := parts[0].(map[string]any)["text"].(string); text != "visible answer" {
		t.Fatalf("visible text = %q, want preserved answer", text)
	}
	toolPart, ok := parts[1].(map[string]any)
	if !ok || toolPart["functionCall"] == nil {
		t.Fatalf("tool-call thought part was not retained: %#v", parts[1])
	}
	for _, key := range []string{"thought", "isThought", "is_thought", "text"} {
		if _, exists := toolPart[key]; exists {
			t.Fatalf("retained tool part still has transient key %q: %#v", key, toolPart)
		}
	}
}
