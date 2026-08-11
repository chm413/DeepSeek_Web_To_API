package claude

import (
	"strconv"
	"strings"
)

// applyClaudeContextManagement applies the portable, client-requested edits
// that can be represented in the normalized message history. Provider-owned
// encrypted state is never synthesized here; unsupported edits remain
// transport-only and are simply omitted from the DeepSeek prompt.
func applyClaudeContextManagement(req map[string]any) {
	if req == nil {
		return
	}
	management, _ := req["context_management"].(map[string]any)
	edits, _ := management["edits"].([]any)
	if len(edits) == 0 {
		return
	}
	messages, _ := req["messages"].([]any)
	for _, raw := range edits {
		edit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(asStringField(edit["type"])))
		switch {
		case strings.HasPrefix(typ, "clear_thinking"):
			messages = clearClaudeThinking(messages, edit["keep"])
		}
	}
	req["messages"] = messages
}

func clearClaudeThinking(messages []any, keepRaw any) []any {
	keep := thinkingKeepCount(keepRaw)
	if keep < 0 {
		return messages
	}

	seen := 0
	out := cloneAnySlice(messages)
	for i := len(out) - 1; i >= 0; i-- {
		msg, ok := out[i].(map[string]any)
		if !ok || !strings.EqualFold(strings.TrimSpace(asStringField(msg["role"])), "assistant") {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		copied := cloneMap(msg)
		filtered := make([]any, 0, len(blocks))
		for blockIndex := len(blocks) - 1; blockIndex >= 0; blockIndex-- {
			block, ok := blocks[blockIndex].(map[string]any)
			if !ok || !strings.EqualFold(strings.TrimSpace(asStringField(block["type"])), "thinking") {
				filtered = append([]any{blocks[blockIndex]}, filtered...)
				continue
			}
			if seen < keep {
				filtered = append([]any{blocks[blockIndex]}, filtered...)
				seen++
			}
		}
		copied["content"] = filtered
		out[i] = copied
	}
	return out
}

func thinkingKeepCount(raw any) int {
	switch v := raw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "all":
			return -1
		case "none", "":
			return 0
		default:
			n, err := strconv.Atoi(v)
			if err == nil && n >= 0 {
				return n
			}
		}
	case float64:
		if v >= 0 {
			return int(v)
		}
	case int:
		if v >= 0 {
			return v
		}
	}
	return 0
}
