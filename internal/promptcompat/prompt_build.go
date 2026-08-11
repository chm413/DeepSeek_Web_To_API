package promptcompat

import (
	"DeepSeek_Web_To_API/internal/prompt"
	"strings"
)

func buildOpenAIFinalPrompt(messagesRaw []any, toolsRaw any, traceID string, thinkingEnabled bool) (string, []string) {
	return BuildOpenAIPrompt(messagesRaw, toolsRaw, traceID, DefaultToolChoicePolicy(), thinkingEnabled)
}

func BuildOpenAIPrompt(messagesRaw []any, toolsRaw any, traceID string, toolPolicy ToolChoicePolicy, thinkingEnabled bool) (string, []string) {
	messages := NormalizeOpenAIMessagesForPrompt(messagesRaw, traceID)
	toolNames := []string{}
	if tools, ok := toolsRaw.([]any); ok && len(tools) > 0 {
		messages, toolNames = injectToolPrompt(messages, tools, toolPolicy)
	}
	return prompt.MessagesPrepareWithThinking(messages, thinkingEnabled), toolNames
}

// BuildOpenAIPromptForAdapter exposes the OpenAI-compatible prompt building flow so
// other protocol adapters (for example Gemini) can reuse the same tool/history
// normalization logic and remain behavior-compatible with chat/completions.
func BuildOpenAIPromptForAdapter(messagesRaw []any, toolsRaw any, traceID string, thinkingEnabled bool) (string, []string) {
	return buildOpenAIFinalPrompt(messagesRaw, toolsRaw, traceID, thinkingEnabled)
}

func BuildIncrementalPrompt(messagesRaw []any, formatPrompt string, thinkingEnabled bool) string {
	messages := NormalizeOpenAIMessagesForPrompt(messagesRaw, "")
	formatPrompt = strings.TrimSpace(formatPrompt)
	if formatPrompt == "" {
		formatPrompt = BuildOpenAIIncrementalFormatPrompt(nil, DefaultToolChoicePolicy())
	}
	messages = append([]map[string]any{{"role": "system", "content": formatPrompt}}, messages...)
	return prompt.MessagesPrepareWithThinking(messages, thinkingEnabled)
}
