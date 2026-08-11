package promptcompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"DeepSeek_Web_To_API/internal/toolcall"
)

func injectToolPrompt(messages []map[string]any, tools []any, policy ToolChoicePolicy) ([]map[string]any, []string) {
	toolPrompt, names := buildOpenAIToolPrompt(tools, policy)
	if toolPrompt == "" {
		return messages, names
	}

	for i := range messages {
		if messages[i]["role"] == "system" {
			old, _ := messages[i]["content"].(string)
			messages[i]["content"] = strings.TrimSpace(old + "\n\n" + toolPrompt)
			return messages, names
		}
	}
	messages = append([]map[string]any{{"role": "system", "content": toolPrompt}}, messages...)
	return messages, names
}

func buildOpenAIToolPrompt(tools []any, policy ToolChoicePolicy) (string, []string) {
	if policy.IsNone() {
		return "", nil
	}
	toolSchemas := make([]string, 0, len(tools))
	names := make([]string, 0, len(tools))
	isAllowed := func(name string) bool {
		if strings.TrimSpace(name) == "" {
			return false
		}
		if len(policy.Allowed) == 0 {
			return true
		}
		_, ok := policy.Allowed[name]
		return ok
	}

	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, desc, schema := toolcall.ExtractToolMeta(tool)
		name = strings.TrimSpace(name)
		if !isAllowed(name) {
			continue
		}
		names = append(names, name)
		if desc == "" {
			desc = "No description available"
		}
		b, _ := json.Marshal(schema)
		toolSchemas = append(toolSchemas, fmt.Sprintf("Tool: %s\nDescription: %s\nParameters: %s", name, desc, string(b)))
	}
	if len(toolSchemas) == 0 {
		return "", names
	}
	toolPrompt := "You have access to these tools:\n\n" + strings.Join(toolSchemas, "\n\n") + "\n\n" + toolcall.BuildToolCallInstructions(names)
	if hasReadLikeTool(names) {
		toolPrompt += "\n\nRead-tool cache guard: If a Read/read_file-style tool result says the file is unchanged, already available in history, should be referenced from previous context, or otherwise provides no file body, treat that result as missing content. Do not repeatedly call the same read request for that missing body. Request a full-content read if the tool supports it, or tell the user that the file contents need to be provided again."
	}
	if policy.Mode == ToolChoiceRequired {
		toolPrompt += "\n7) For this response, you MUST call at least one tool from the allowed list."
	}
	if policy.Mode == ToolChoiceForced && strings.TrimSpace(policy.ForcedName) != "" {
		toolPrompt += "\n7) For this response, you MUST call exactly this tool name: " + strings.TrimSpace(policy.ForcedName)
		toolPrompt += "\n8) Do not call any other tool."
	}

	return toolPrompt, names
}

func BuildOpenAIIncrementalFormatPrompt(toolsRaw any, policy ToolChoicePolicy) string {
	const base = "Incremental response format requirements:\n1) Continue from the existing parent message and use the role blocks below only as new input.\n2) Return only the assistant response for the newest input.\n3) Do not repeat role labels, protocol wrappers, or prior conversation text unless the user explicitly asks for them.\n4) Follow every tool/output constraint below exactly."
	tools, _ := toolsRaw.([]any)
	toolPrompt, _ := buildOpenAIToolPrompt(tools, policy)
	if strings.TrimSpace(toolPrompt) == "" {
		return base
	}
	return base + "\n\n" + toolPrompt
}

func hasReadLikeTool(names []string) bool {
	for _, name := range names {
		switch normalizeToolNameForGuard(name) {
		case "read", "readfile":
			return true
		}
	}
	return false
}

func normalizeToolNameForGuard(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
