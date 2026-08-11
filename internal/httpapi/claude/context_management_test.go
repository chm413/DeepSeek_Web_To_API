package claude

import (
	"strings"
	"testing"
)

func TestClaudeContextManagementClearThinkingNone(t *testing.T) {
	req := map[string]any{
		"context_management": map[string]any{
			"edits": []any{map[string]any{"type": "clear_thinking_20251015", "keep": "none"}},
		},
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "remove me"},
					map[string]any{"type": "text", "text": "keep me"},
				},
			},
			map[string]any{"role": "user", "content": "continue"},
		},
	}

	applyClaudeContextManagement(req)
	normalized := normalizeClaudeMessages(req["messages"].([]any))
	for _, msg := range normalized {
		m, _ := msg.(map[string]any)
		content := asStringField(m["content"])
		if strings.Contains(content, "remove me") {
			t.Fatalf("thinking block was not cleared: %#v", normalized)
		}
	}
}

func TestClaudeContextManagementKeepAllPreservesThinking(t *testing.T) {
	req := map[string]any{
		"context_management": map[string]any{
			"edits": []any{map[string]any{"type": "clear_thinking_20251015", "keep": "all"}},
		},
		"messages": []any{
			map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "thinking", "thinking": "preserve me"}},
			},
			map[string]any{"role": "user", "content": "continue"},
		},
	}

	applyClaudeContextManagement(req)
	normalized := normalizeClaudeMessages(req["messages"].([]any))
	found := false
	for _, msg := range normalized {
		m, _ := msg.(map[string]any)
		found = found || strings.Contains(asStringField(m["content"]), "preserve me")
	}
	if !found {
		t.Fatalf("keep=all unexpectedly removed thinking: %#v", normalized)
	}
}
