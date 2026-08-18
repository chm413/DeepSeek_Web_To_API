package promptcompat

import (
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/util"
)

type ConfigReader interface {
	ModelAliases() map[string]string
	CompatWideInputStrictOutput() bool
}

func NormalizeOpenAIChatRequest(store ConfigReader, req map[string]any, traceID string) (StandardRequest, error) {
	model, _ := req["model"].(string)
	messagesRaw, _ := req["messages"].([]any)
	if strings.TrimSpace(model) == "" || len(messagesRaw) == 0 {
		return StandardRequest{}, fmt.Errorf("request must include 'model' and 'messages'")
	}
	messagesRaw = repairOpenAIToolMessages(messagesRaw)
	resolvedModel, ok := config.ResolveModel(store, model)
	if !ok {
		return StandardRequest{}, fmt.Errorf("model %q is not available", model)
	}
	defaultThinkingEnabled, searchEnabled, _ := config.GetModelConfig(resolvedModel)
	explicitThinkingEnabled, hasThinkingOverride := util.ResolveThinkingOverride(req)
	thinkingEnabled := util.ResolveThinkingEnabled(req, defaultThinkingEnabled)
	if config.IsNoThinkingModel(resolvedModel) {
		thinkingEnabled = false
	}
	exposeReasoning := hasThinkingOverride && explicitThinkingEnabled && thinkingEnabled
	responseModel := strings.TrimSpace(model)
	if responseModel == "" {
		responseModel = resolvedModel
	}
	toolPolicy, err := parseToolChoicePolicy(req["tool_choice"], req["tools"])
	if err != nil {
		return StandardRequest{}, err
	}
	finalPrompt, toolNames := BuildOpenAIPrompt(messagesRaw, req["tools"], traceID, toolPolicy, thinkingEnabled)
	if !toolPolicy.IsNone() {
		toolNames = ensureToolDetectionEnabled(toolNames, req["tools"])
		toolPolicy.Allowed = namesToSet(toolNames)
	}
	passThrough := collectOpenAIChatPassThrough(req)
	refFileIDs := CollectOpenAIRefFileIDs(req)

	return StandardRequest{
		Surface:                 "openai_chat",
		RequestedModel:          strings.TrimSpace(model),
		ResolvedModel:           resolvedModel,
		ResponseModel:           responseModel,
		Messages:                messagesRaw,
		LatestUserText:          ExtractLatestUserText(messagesRaw),
		ToolsRaw:                req["tools"],
		HasTools:                hasRequestField(req, "tools"),
		HasToolChoice:           hasRequestField(req, "tool_choice"),
		ToolChoiceRaw:           req["tool_choice"],
		FinalPrompt:             finalPrompt,
		IncrementalFormatPrompt: BuildOpenAIIncrementalFormatPrompt(req["tools"], toolPolicy),
		ToolNames:               toolNames,
		ToolChoice:              toolPolicy,
		Stream:                  util.ToBool(req["stream"]),
		Thinking:                thinkingEnabled,
		ExposeReasoning:         exposeReasoning,
		Search:                  searchEnabled,
		RefFileIDs:              refFileIDs,
		PassThrough:             passThrough,
	}, nil
}

func NormalizeOpenAIResponsesRequest(store ConfigReader, req map[string]any, traceID string) (StandardRequest, error) {
	model, _ := req["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return StandardRequest{}, fmt.Errorf("request must include 'model'")
	}
	resolvedModel, ok := config.ResolveModel(store, model)
	if !ok {
		return StandardRequest{}, fmt.Errorf("model %q is not available", model)
	}
	defaultThinkingEnabled, searchEnabled, _ := config.GetModelConfig(resolvedModel)
	explicitThinkingEnabled, hasThinkingOverride := util.ResolveThinkingOverride(req)
	thinkingEnabled := util.ResolveThinkingEnabled(req, defaultThinkingEnabled)
	if config.IsNoThinkingModel(resolvedModel) {
		thinkingEnabled = false
	}
	exposeReasoning := hasThinkingOverride && explicitThinkingEnabled && thinkingEnabled

	// Keep width-control as an explicit policy hook even if current default is true.
	allowWideInput := true
	if store != nil {
		allowWideInput = store.CompatWideInputStrictOutput()
	}
	var messagesRaw []any
	if allowWideInput {
		messagesRaw = ResponsesMessagesFromRequest(req)
	} else if msgs, ok := req["messages"].([]any); ok && len(msgs) > 0 {
		messagesRaw = msgs
	}
	if len(messagesRaw) == 0 {
		return StandardRequest{}, fmt.Errorf("request must include 'input' or 'messages'")
	}
	messagesRaw = repairOpenAIToolMessages(messagesRaw)
	toolPolicy, err := parseToolChoicePolicy(req["tool_choice"], req["tools"])
	if err != nil {
		return StandardRequest{}, err
	}
	finalPrompt, toolNames := BuildOpenAIPrompt(messagesRaw, req["tools"], traceID, toolPolicy, thinkingEnabled)
	if !toolPolicy.IsNone() {
		toolNames = ensureToolDetectionEnabled(toolNames, req["tools"])
		toolPolicy.Allowed = namesToSet(toolNames)
	}
	passThrough := collectOpenAIChatPassThrough(req)
	refFileIDs := CollectOpenAIRefFileIDs(req)

	return StandardRequest{
		Surface:                 "openai_responses",
		RequestedModel:          model,
		ResolvedModel:           resolvedModel,
		ResponseModel:           model,
		Messages:                messagesRaw,
		LatestUserText:          ExtractLatestUserText(messagesRaw),
		ToolsRaw:                req["tools"],
		HasTools:                hasRequestField(req, "tools"),
		HasToolChoice:           hasRequestField(req, "tool_choice"),
		ToolChoiceRaw:           req["tool_choice"],
		FinalPrompt:             finalPrompt,
		IncrementalFormatPrompt: BuildOpenAIIncrementalFormatPrompt(req["tools"], toolPolicy),
		ToolNames:               toolNames,
		ToolChoice:              toolPolicy,
		Stream:                  util.ToBool(req["stream"]),
		Thinking:                thinkingEnabled,
		ExposeReasoning:         exposeReasoning,
		Search:                  searchEnabled,
		RefFileIDs:              refFileIDs,
		PassThrough:             passThrough,
	}, nil
}

func hasRequestField(req map[string]any, key string) bool {
	if req == nil {
		return false
	}
	_, ok := req[key]
	return ok
}

// ExtractLatestUserText scans a normalized OpenAI/Claude messages array
// (the same []any shape used everywhere in promptcompat) for the most
// recent message with role=="user" and returns its content as plain text.
// Used by v1.0.19 LLM safety review to avoid auditing the gateway's own
// system prompts / history / injection banners — only the human's last
// turn matters for content moderation.
//
// Handles three content shapes per OpenAI/Anthropic spec:
//   - string content: returned verbatim
//   - []any of {type:"text", text:"..."} / {type:"input_text", ...} blocks:
//     concatenated with newlines
//   - mixed (e.g. text + image_url): only text blocks are included
//
// Returns "" when no user message is found (audit will skip below
// MinInputChars threshold, which is the safe degradation).
func ExtractLatestUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asMessageString(msg["role"])))
		if role != "user" {
			continue
		}
		return latestUserTextFromContent(msg["content"])
	}
	return ""
}

func latestUserTextFromContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			t := strings.ToLower(asMessageString(pm["type"]))
			if t == "text" || t == "input_text" || t == "" {
				if txt := asMessageString(pm["text"]); txt != "" {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func asMessageString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func ensureToolDetectionEnabled(toolNames []string, toolsRaw any) []string {
	if len(toolNames) > 0 {
		return toolNames
	}
	tools, _ := toolsRaw.([]any)
	if len(tools) == 0 {
		return toolNames
	}
	// Keep stream sieve/tool buffering enabled even when client tool schemas
	// are malformed or lack explicit names; parsed tool payload names are no
	// longer filtered by this list.
	return []string{"__any_tool__"}
}

func collectOpenAIChatPassThrough(req map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{
		"temperature",
		"top_p",
		"max_tokens",
		"max_completion_tokens",
		"presence_penalty",
		"frequency_penalty",
		"stop",
	} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	return out
}

func parseToolChoicePolicy(toolChoiceRaw any, toolsRaw any) (ToolChoicePolicy, error) {
	policy := DefaultToolChoicePolicy()
	declaredNames := extractDeclaredToolNames(toolsRaw)
	declaredSet := namesToSet(declaredNames)
	if len(declaredNames) > 0 {
		policy.Allowed = declaredSet
	}

	if toolChoiceRaw == nil {
		return policy, nil
	}

	switch v := toolChoiceRaw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "auto":
			policy.Mode = ToolChoiceAuto
		case "none":
			policy.Mode = ToolChoiceNone
			policy.Allowed = nil
		case "required":
			policy.Mode = ToolChoiceRequired
		default:
			return ToolChoicePolicy{}, fmt.Errorf("unsupported tool_choice: %q", v)
		}
	case map[string]any:
		allowedOverride, hasAllowedOverride, err := parseAllowedToolNames(v["allowed_tools"])
		if err != nil {
			return ToolChoicePolicy{}, err
		}
		if hasAllowedOverride {
			filtered := make([]string, 0, len(allowedOverride))
			for _, name := range allowedOverride {
				if _, ok := declaredSet[name]; !ok {
					return ToolChoicePolicy{}, fmt.Errorf("tool_choice.allowed_tools contains undeclared tool %q", name)
				}
				filtered = append(filtered, name)
			}
			policy.Allowed = namesToSet(filtered)
		}

		typ := strings.ToLower(strings.TrimSpace(asString(v["type"])))
		switch typ {
		case "", "auto":
			if hasFunctionSelector(v) {
				name, err := parseForcedToolName(v)
				if err != nil {
					return ToolChoicePolicy{}, err
				}
				policy.Mode = ToolChoiceForced
				policy.ForcedName = name
				policy.Allowed = namesToSet([]string{name})
			} else {
				policy.Mode = ToolChoiceAuto
			}
		case "none":
			policy.Mode = ToolChoiceNone
			policy.Allowed = nil
		case "required":
			policy.Mode = ToolChoiceRequired
		case "function":
			name, err := parseForcedToolName(v)
			if err != nil {
				return ToolChoicePolicy{}, err
			}
			policy.Mode = ToolChoiceForced
			policy.ForcedName = name
			policy.Allowed = namesToSet([]string{name})
		default:
			return ToolChoicePolicy{}, fmt.Errorf("unsupported tool_choice.type: %q", typ)
		}
	default:
		return ToolChoicePolicy{}, fmt.Errorf("tool_choice must be a string or object")
	}

	if policy.Mode == ToolChoiceRequired || policy.Mode == ToolChoiceForced {
		if len(declaredNames) == 0 {
			return ToolChoicePolicy{}, fmt.Errorf("tool_choice=%s requires non-empty tools", policy.Mode)
		}
	}
	if policy.Mode == ToolChoiceForced {
		if _, ok := declaredSet[policy.ForcedName]; !ok {
			return ToolChoicePolicy{}, fmt.Errorf("tool_choice forced function %q is not declared in tools", policy.ForcedName)
		}
	}
	if len(policy.Allowed) == 0 && (policy.Mode == ToolChoiceRequired || policy.Mode == ToolChoiceForced) {
		return ToolChoicePolicy{}, fmt.Errorf("tool_choice policy resolved to empty allowed tool set")
	}
	return policy, nil
}

func parseForcedToolName(v map[string]any) (string, error) {
	if name := strings.TrimSpace(asString(v["name"])); name != "" {
		return name, nil
	}
	if fn, ok := v["function"].(map[string]any); ok {
		if name := strings.TrimSpace(asString(fn["name"])); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("tool_choice function requires name")
}

func parseAllowedToolNames(raw any) ([]string, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	collectName := func(v any) string {
		if name := strings.TrimSpace(asString(v)); name != "" {
			return name
		}
		if m, ok := v.(map[string]any); ok {
			if name := strings.TrimSpace(asString(m["name"])); name != "" {
				return name
			}
			if fn, ok := m["function"].(map[string]any); ok {
				if name := strings.TrimSpace(asString(fn["name"])); name != "" {
					return name
				}
			}
		}
		return ""
	}

	names := []string{}
	switch x := raw.(type) {
	case []any:
		for _, item := range x {
			name := collectName(item)
			if name == "" {
				return nil, true, fmt.Errorf("tool_choice.allowed_tools contains invalid item")
			}
			names = append(names, name)
		}
	case []string:
		for _, item := range x {
			name := strings.TrimSpace(item)
			if name == "" {
				return nil, true, fmt.Errorf("tool_choice.allowed_tools contains empty name")
			}
			names = append(names, name)
		}
	default:
		return nil, true, fmt.Errorf("tool_choice.allowed_tools must be an array")
	}

	if len(names) == 0 {
		return nil, true, fmt.Errorf("tool_choice.allowed_tools must not be empty")
	}
	return names, true, nil
}

func hasFunctionSelector(v map[string]any) bool {
	if strings.TrimSpace(asString(v["name"])) != "" {
		return true
	}
	if fn, ok := v["function"].(map[string]any); ok {
		return strings.TrimSpace(asString(fn["name"])) != ""
	}
	return false
}

func extractDeclaredToolNames(toolsRaw any) []string {
	tools, ok := toolsRaw.([]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(tools))
	seen := map[string]struct{}{}
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if len(fn) == 0 {
			fn = tool
		}
		name := strings.TrimSpace(asString(fn["name"]))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func namesToSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		out[trimmed] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
