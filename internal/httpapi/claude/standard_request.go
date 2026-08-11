package claude

import (
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/prompt"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/util"
)

type claudeNormalizedRequest struct {
	Standard           promptcompat.StandardRequest
	NormalizedMessages []any
}

func normalizeClaudeRequest(store ConfigReader, req map[string]any) (claudeNormalizedRequest, error) {
	model, _ := req["model"].(string)
	messagesRaw, _ := req["messages"].([]any)
	if strings.TrimSpace(model) == "" || len(messagesRaw) == 0 {
		return claudeNormalizedRequest{}, fmt.Errorf("request must include 'model' and 'messages'")
	}
	if _, ok := req["max_tokens"]; !ok {
		req["max_tokens"] = 8192
	}
	normalizedMessages := normalizeClaudeMessages(messagesRaw)
	payload := cloneMap(req)
	applyClaudeContextManagement(payload)
	// Context edits operate on the original Anthropic content blocks. Re-read
	// the edited message array before converting it to the DeepSeek shape.
	if edited, ok := payload["messages"].([]any); ok {
		normalizedMessages = normalizeClaudeMessages(edited)
	}
	if systemText := claudeSystemText(req["system"]); systemText != "" {
		payload["system"] = systemText
	} else {
		delete(payload, "system")
	}
	payload["messages"] = normalizedMessages
	toolsRequested, _ := req["tools"].([]any)
	// Anthropic Messages API allows clients to declare MCP servers via
	// "mcp_servers". The upstream DeepSeek web channel cannot natively dispatch
	// MCP, but we still expand each server's advertised tools into virtual
	// entries so the model sees them in the system prompt and emits standard
	// tool_use blocks. The client SDK is responsible for routing those calls
	// to its MCP server.
	if mcpTools := expandMCPServersAsTools(req["mcp_servers"]); len(mcpTools) > 0 {
		toolsRequested = append(toolsRequested, mcpTools...)
		payload["tools"] = toolsRequested
	}
	payload["messages"] = injectClaudeToolPrompt(payload, normalizedMessages, toolsRequested)

	dsPayload := convertClaudeToDeepSeek(payload, store)
	dsModel, _ := dsPayload["model"].(string)
	_, searchEnabled, ok := config.GetModelConfig(dsModel)
	if !ok {
		searchEnabled = false
	}
	thinkingEnabled := util.ResolveThinkingEnabled(req, false)
	if config.IsNoThinkingModel(dsModel) {
		thinkingEnabled = false
	}
	dsMessages, _ := dsPayload["messages"].([]any)
	finalPrompt := prompt.MessagesPrepareWithThinking(toMessageMaps(dsMessages), thinkingEnabled)
	toolNames := extractClaudeToolNames(toolsRequested)
	if len(toolNames) == 0 && len(toolsRequested) > 0 {
		toolNames = []string{"__any_tool__"}
	}
	incrementalFormatPrompt := promptcompat.BuildOpenAIIncrementalFormatPrompt(nil, promptcompat.DefaultToolChoicePolicy())
	if toolPrompt := strings.TrimSpace(buildClaudeToolPrompt(toolsRequested)); toolPrompt != "" {
		incrementalFormatPrompt += "\n\n" + toolPrompt
	}

	return claudeNormalizedRequest{
		Standard: promptcompat.StandardRequest{
			Surface:                 "anthropic_messages",
			RequestedModel:          strings.TrimSpace(model),
			ResolvedModel:           dsModel,
			ResponseModel:           strings.TrimSpace(model),
			Messages:                dsMessages,
			LatestUserText:          promptcompat.ExtractLatestUserText(dsMessages),
			PromptTokenText:         finalPrompt,
			ToolsRaw:                toolsRequested,
			FinalPrompt:             finalPrompt,
			IncrementalFormatPrompt: incrementalFormatPrompt,
			ToolNames:               toolNames,
			Stream:                  util.ToBool(req["stream"]),
			Thinking:                thinkingEnabled,
			Search:                  searchEnabled,
		},
		NormalizedMessages: normalizedMessages,
	}, nil
}

func injectClaudeToolPrompt(payload map[string]any, normalizedMessages []any, tools []any) []any {
	if len(tools) == 0 {
		return normalizedMessages
	}
	toolPrompt := strings.TrimSpace(buildClaudeToolPrompt(tools))
	if toolPrompt == "" {
		return normalizedMessages
	}

	// Prefer top-level Anthropic-style system prompt when available.
	if systemText, ok := payload["system"].(string); ok && strings.TrimSpace(systemText) != "" {
		payload["system"] = mergeSystemPrompt(systemText, toolPrompt)
		return normalizedMessages
	}

	messages := cloneAnySlice(normalizedMessages)
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "system") {
			continue
		}
		copied := cloneMap(msg)
		copied["content"] = mergeSystemPrompt(strings.TrimSpace(fmt.Sprintf("%v", copied["content"])), toolPrompt)
		messages[i] = copied
		return messages
	}

	return append([]any{map[string]any{"role": "system", "content": toolPrompt}}, messages...)
}

func mergeSystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

// expandMCPServersAsTools converts the Anthropic-style "mcp_servers" array into
// a slice of virtual tool descriptors that look like ordinary Claude tools
// ({"name", "description"} only). Real MCP dispatch is delegated to the
// downstream client; here we only ensure the model is aware of the tool names
// so it can emit valid tool_use blocks.
//
// Each server is an object such as:
//
//	{
//	  "type": "url",
//	  "url":  "https://example/mcp",
//	  "name": "github",
//	  "tool_configuration": { "allowed_tools": ["github_repo_search", ...] }
//	}
//
// The resulting tool name uses the form "<server>.<tool>" so the client can
// route the dispatch back to the correct MCP server. When a server lists no
// tools we still emit a single placeholder so the system prompt mentions the
// server.
func expandMCPServersAsTools(raw any) []any {
	servers, ok := raw.([]any)
	if !ok || len(servers) == 0 {
		return nil
	}
	out := make([]any, 0, len(servers))
	for _, item := range servers {
		server, ok := item.(map[string]any)
		if !ok {
			continue
		}
		serverName := strings.TrimSpace(asStringField(server["name"]))
		if serverName == "" {
			serverName = strings.TrimSpace(asStringField(server["url"]))
		}
		if serverName == "" {
			continue
		}
		// Skip servers explicitly disabled by tool_configuration.enabled = false.
		if cfg, ok := server["tool_configuration"].(map[string]any); ok {
			if enabled, present := cfg["enabled"].(bool); present && !enabled {
				continue
			}
		}
		toolNames := mcpAdvertisedToolNames(server)
		if len(toolNames) == 0 {
			out = append(out, map[string]any{
				"name":        sanitizeToolName(serverName),
				"description": fmt.Sprintf("MCP server %q (no tool list provided; pass arguments through as-is).", serverName),
			})
			continue
		}
		for _, toolName := range toolNames {
			toolName = strings.TrimSpace(toolName)
			if toolName == "" {
				continue
			}
			out = append(out, map[string]any{
				"name":        sanitizeToolName(serverName + "." + toolName),
				"description": fmt.Sprintf("MCP tool %q exposed by server %q.", toolName, serverName),
			})
		}
	}
	return out
}

func mcpAdvertisedToolNames(server map[string]any) []string {
	names := []string{}
	if cfg, ok := server["tool_configuration"].(map[string]any); ok {
		if list, ok := cfg["allowed_tools"].([]any); ok {
			for _, v := range list {
				if s := strings.TrimSpace(asStringField(v)); s != "" {
					names = append(names, s)
				}
			}
		}
	}
	if list, ok := server["tools"].([]any); ok {
		for _, v := range list {
			switch t := v.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					names = append(names, s)
				}
			case map[string]any:
				if s := strings.TrimSpace(asStringField(t["name"])); s != "" {
					names = append(names, s)
				}
			}
		}
	}
	return names
}

func asStringField(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

func sanitizeToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "mcp_tool"
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "mcp_tool"
	}
	return out
}

func cloneAnySlice(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}
