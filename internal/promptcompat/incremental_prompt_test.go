package promptcompat

import (
	"strings"
	"testing"
)

func TestBuildIncrementalPromptAlwaysIncludesFormatAndDelta(t *testing.T) {
	formatPrompt := BuildOpenAIIncrementalFormatPrompt(nil, DefaultToolChoicePolicy())
	prompt := BuildIncrementalPrompt([]any{map[string]any{"role": "user", "content": "new input"}}, formatPrompt, false)
	if !strings.Contains(prompt, "Incremental response format requirements") {
		t.Fatalf("missing forced format prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "new input") {
		t.Fatalf("missing delta input: %q", prompt)
	}
}

func TestBuildIncrementalFormatPromptRepeatsToolContract(t *testing.T) {
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup",
			"description": "lookup data",
			"parameters":  map[string]any{"type": "object"},
		},
	}}
	prompt := BuildOpenAIIncrementalFormatPrompt(tools, DefaultToolChoicePolicy())
	for _, expected := range []string{"Incremental response format requirements", "Tool: lookup", "lookup data"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing %q in incremental format prompt: %q", expected, prompt)
		}
	}
}
