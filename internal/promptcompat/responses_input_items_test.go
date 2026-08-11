package promptcompat

import (
	"strings"
	"testing"
)

func TestNormalizeResponsesInputItemPreservesAssistantReasoningContent(t *testing.T) {
	item := map[string]any{
		"role":              "assistant",
		"reasoning_content": "hidden reasoning",
		"tool_calls": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":      "search",
					"arguments": `{"q":"docs"}`,
				},
			},
		},
	}

	got := normalizeResponsesInputItem(item)
	if got == nil {
		t.Fatal("expected assistant item to be preserved")
	}
	if got["role"] != "assistant" {
		t.Fatalf("unexpected role: %#v", got["role"])
	}
	if got["reasoning_content"] != "hidden reasoning" {
		t.Fatalf("expected reasoning_content preserved, got %#v", got["reasoning_content"])
	}
}

func TestNormalizeResponsesInputItemAssistantMessageWithReasoningBlocks(t *testing.T) {
	item := map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "reasoning", "text": "internal chain"},
			map[string]any{"type": "output_text", "text": "visible answer"},
		},
	}

	got := normalizeResponsesInputItem(item)
	if got == nil {
		t.Fatal("expected assistant message item to be preserved")
	}
	content, _ := got["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected content blocks preserved, got %#v", got["content"])
	}
}

func TestResponsesEncryptedCompactionStateDoesNotLeakIntoPrompt(t *testing.T) {
	items := []any{
		map[string]any{"type": "compaction", "encrypted_content": "secret-opaque-state"},
		map[string]any{"type": "context_compaction", "encrypted_content": "context-opaque-state"},
		map[string]any{"type": "compaction_trigger", "encrypted_content": "trigger-opaque-state"},
		map[string]any{"type": "reasoning", "encrypted_content": "hidden-reasoning-state"},
		map[string]any{"type": "message", "role": "user", "content": "continue"},
	}

	messages := NormalizeResponsesInputAsMessages(items)
	prompt, _ := BuildOpenAIPrompt(messages, nil, "", DefaultToolChoicePolicy(), false)
	for _, forbidden := range []string{"secret-opaque-state", "context-opaque-state", "trigger-opaque-state", "hidden-reasoning-state", "encrypted_content"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("opaque Responses state leaked into prompt: %q", prompt)
		}
	}
	if !strings.Contains(prompt, "continue") {
		t.Fatalf("visible user input was lost: %q", prompt)
	}
}

func TestTopLevelResponsesCompactionTriggerDoesNotFallbackToText(t *testing.T) {
	item := map[string]any{
		"type":              "compaction_trigger",
		"encrypted_content": "must-not-reach-prompt",
		"text":              "must-not-reach-prompt",
	}
	if messages := NormalizeResponsesInputAsMessages(item); len(messages) != 0 {
		t.Fatalf("expected top-level trigger to be omitted, got %#v", messages)
	}
}

func TestResponsesVisibleCompactionSummaryIsPreserved(t *testing.T) {
	items := []any{
		map[string]any{
			"type": "compaction",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": "Earlier decisions and constraints."},
			},
		},
		map[string]any{"type": "message", "role": "user", "content": "continue"},
	}

	messages := NormalizeResponsesInputAsMessages(items)
	prompt, _ := BuildOpenAIPrompt(messages, nil, "", DefaultToolChoicePolicy(), false)
	if !strings.Contains(prompt, "Earlier decisions and constraints.") {
		t.Fatalf("visible compaction summary was not preserved: %q", prompt)
	}
}
