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

func TestMergePreviousResponseInputInheritsToolContractWhenOmitted(t *testing.T) {
	h := &Handler{responses: newResponseStore(0)}
	owner := "caller:test"
	responseID := "resp_tool_contract"
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "lookup_issue",
			"description": "Find an issue by identifier.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string"}},
			},
		},
	}}
	toolChoice := map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "lookup_issue"},
	}
	h.responses.putInputState(owner, responseID, []any{
		map[string]any{"role": "user", "content": "inspect issue ABC-123"},
	}, tools, true, toolChoice, true)
	h.responses.put(owner, responseID, map[string]any{
		"id":     responseID,
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "I will inspect it."}}}},
	})
	req := map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": responseID,
		"input":                "continue",
	}

	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		t.Fatalf("merge previous response: %v", err)
	}
	if _, ok := req["tools"].([]any); !ok {
		t.Fatalf("expected inherited tools, got %#v", req["tools"])
	}
	if _, ok := req["tool_choice"].(map[string]any); !ok {
		t.Fatalf("expected inherited tool_choice, got %#v", req["tool_choice"])
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(responsesHistoryConfigStub{}, req, "")
	if err != nil {
		t.Fatalf("normalize inherited request: %v", err)
	}
	if !stdReq.ToolChoice.IsRequired() || stdReq.ToolChoice.ForcedName != "lookup_issue" {
		t.Fatalf("tool choice was not restored: %#v", stdReq.ToolChoice)
	}
	for _, expected := range []string{"Tool: lookup_issue", "Find an issue", "MUST call exactly this tool name"} {
		if !strings.Contains(stdReq.IncrementalFormatPrompt, expected) {
			t.Fatalf("missing %q from inherited incremental contract: %q", expected, stdReq.IncrementalFormatPrompt)
		}
	}
}

func TestMergePreviousResponseInputHonorsExplicitToolDisable(t *testing.T) {
	h := &Handler{responses: newResponseStore(0)}
	owner := "caller:test"
	responseID := "resp_tool_disable"
	h.responses.putInputState(owner, responseID, []any{
		map[string]any{"role": "user", "content": "first"},
	}, []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}, true, "required", true)
	h.responses.put(owner, responseID, map[string]any{"id": responseID, "output": []any{}})
	req := map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": responseID,
		"input":                "continue without tools",
		"tools":                []any{},
		"tool_choice":          "none",
	}

	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		t.Fatalf("merge previous response: %v", err)
	}
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("explicit empty tools was overwritten: %#v", req["tools"])
	}
	if req["tool_choice"] != "none" {
		t.Fatalf("explicit tool_choice was overwritten: %#v", req["tool_choice"])
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(responsesHistoryConfigStub{}, req, "")
	if err != nil {
		t.Fatalf("normalize explicitly disabled request: %v", err)
	}
	if !stdReq.ToolChoice.IsNone() {
		t.Fatalf("expected explicit tool disable, got %#v", stdReq.ToolChoice)
	}
	if strings.Contains(stdReq.IncrementalFormatPrompt, "Tool: lookup") {
		t.Fatalf("inherited tool leaked into explicit-disable prompt: %q", stdReq.IncrementalFormatPrompt)
	}
}

func TestMergePreviousResponseInputPreservesExplicitNullTools(t *testing.T) {
	h := &Handler{responses: newResponseStore(0)}
	owner := "caller:test"
	responseID := "resp_tool_null"
	h.responses.putInputState(owner, responseID, []any{
		map[string]any{"role": "user", "content": "first"},
	}, []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}, true, "auto", true)
	h.responses.put(owner, responseID, map[string]any{"id": responseID, "output": []any{}})
	req := map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": responseID,
		"input":                "continue",
		"tools":                nil,
	}
	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		t.Fatalf("merge previous response: %v", err)
	}
	if value, present := req["tools"]; !present || value != nil {
		t.Fatalf("explicit null tools was overwritten: present=%v value=%#v", present, value)
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(responsesHistoryConfigStub{}, req, "")
	if err != nil {
		t.Fatalf("normalize explicitly null tools request: %v", err)
	}
	if strings.Contains(stdReq.IncrementalFormatPrompt, "Tool: lookup") {
		t.Fatalf("inherited tool leaked into explicit-null prompt: %q", stdReq.IncrementalFormatPrompt)
	}
}
