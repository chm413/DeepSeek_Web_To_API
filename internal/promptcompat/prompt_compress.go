package promptcompat

import (
	"strconv"
	"strings"
	"unicode/utf16"

	"DeepSeek_Web_To_API/internal/config"
)

// PromptUnits returns the length used by DeepSeek Web's input_character_limit.
// The web client reads JavaScript String.length, which counts UTF-16 code
// units rather than UTF-8 bytes or Unicode code points. Keeping this explicit
// prevents non-BMP characters (for example emoji) from bypassing the limit.
func PromptUnits(value string) int {
	if value == "" {
		return 0
	}
	return len(utf16.Encode([]rune(value)))
}

// IsExpertModel reports whether the model maps to the upstream expert tier,
// which carries the tighter character ceiling. Driven by config.GetModelType so
// the model table stays the single source of truth.
func IsExpertModel(model string) bool {
	modelType, ok := config.GetModelType(model)
	return ok && modelType == "expert"
}

// EffectiveModel picks the id to classify against: the resolved upstream model
// when known, else whatever the client asked for.
func EffectiveModel(req StandardRequest) string {
	if m := strings.TrimSpace(req.ResolvedModel); m != "" {
		return m
	}
	return strings.TrimSpace(req.RequestedModel)
}

// LimitForModel returns the character ceiling for a model, or 0 when limits are
// disabled entirely.
func LimitForModel(cfg config.PromptLimitSettings, model string) int {
	if !cfg.Enabled {
		return 0
	}
	if IsExpertModel(model) {
		return cfg.MaxCharsExpert
	}
	return cfg.MaxCharsDefault
}

// LimitExceededMessage renders the client-facing 413 detail. An empty string
// means the prompt fits (or limits are off).
func LimitExceededMessage(cfg config.PromptLimitSettings, prompt, model string) string {
	limit := LimitForModel(cfg, model)
	units := PromptUnits(prompt)
	if limit <= 0 || units <= limit {
		return ""
	}
	tier := "default"
	if IsExpertModel(model) {
		tier = "expert"
	}
	return "prompt size " + strconv.Itoa(units) +
		" UTF-16 code units exceeds the " + tier + " model limit of " + strconv.Itoa(limit) +
		" by " + strconv.Itoa(units-limit) +
		" units; shorten the conversation or raise prompt_limit.max_chars_" + tier
}

// isToolResultRole reports whether a normalized role carries a tool RESULT,
// which is only valid when preceded by the assistant tool_calls that produced
// it.
func isToolResultRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "tool", "function":
		return true
	default:
		return false
	}
}

func messageRole(item any) string {
	msg, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(asString(msg["role"])))
}

// dropLeadingOrphanToolResults trims tool-result messages from the front of a
// window. Cutting history at an arbitrary index can leave a `tool` message
// whose originating assistant tool_calls was dropped; that orphan is a
// malformed exchange and some clients reject it outright, so we discard the
// leading run rather than emit it.
func dropLeadingOrphanToolResults(messages []any) []any {
	i := 0
	for i < len(messages) && isToolResultRole(messageRole(messages[i])) {
		i++
	}
	return messages[i:]
}

// CompressMessages keeps the leading system message (when keepSystem) plus the
// most recent keepRecent turns, where a turn is approximated as a user +
// assistant pair. Returns the trimmed slice and whether anything was dropped.
//
// The input is []any of map[string]any, matching StandardRequest.Messages.
func CompressMessages(messages []any, keepSystem bool, keepRecent int) ([]any, bool) {
	if len(messages) == 0 {
		return messages, false
	}

	var systemMsg any
	nonSystem := make([]any, 0, len(messages))
	for _, item := range messages {
		if keepSystem && systemMsg == nil && messageRole(item) == "system" {
			systemMsg = item
			continue
		}
		nonSystem = append(nonSystem, item)
	}

	window := keepRecent * 2 // ~2 messages per turn
	if window < 2 {
		window = 2
	}
	if len(nonSystem) <= window {
		return messages, false
	}

	kept := dropLeadingOrphanToolResults(nonSystem[len(nonSystem)-window:])
	if len(kept) == 0 {
		// Everything in the window was an orphan tool result; keep the final
		// message so we never ship an empty conversation.
		kept = nonSystem[len(nonSystem)-1:]
	}

	out := make([]any, 0, len(kept)+1)
	if systemMsg != nil {
		out = append(out, systemMsg)
	}
	out = append(out, kept...)
	return out, true
}

// CompressToFit trims conversation history until the rebuilt prompt fits the
// model's ceiling, progressively halving the retained-turn count. It must run
// BEFORE the current-input-file (CIF) stage: CIF collapses Messages into a
// single inlined transcript, after which there is no per-turn structure left to
// drop.
//
// Returns the updated request and whether any history was dropped. When even
// the minimum window overflows, the request comes back as compressed as we
// could make it — the caller then decides via LimitExceededMessage whether to
// reject.
func CompressToFit(cfg config.PromptLimitSettings, req StandardRequest) (StandardRequest, bool) {
	if !cfg.Enabled || !cfg.AutoCompressEnable {
		return req, false
	}
	limit := LimitForModel(cfg, EffectiveModel(req))
	if limit <= 0 || PromptUnits(req.FinalPrompt) <= limit {
		return req, false
	}

	original := req.Messages
	changed := false
	for keep := cfg.KeepRecentTurns; keep >= 1; keep /= 2 {
		compressed, ok := CompressMessages(original, cfg.KeepSystemMessage, keep)
		if !ok {
			continue
		}
		prompt, toolNames := BuildOpenAIPrompt(compressed, req.ToolsRaw, "", req.ToolChoice, req.Thinking)
		req.Messages = compressed
		req.FinalPrompt = prompt
		if len(toolNames) > 0 {
			req.ToolNames = toolNames
		}
		changed = true
		if PromptUnits(prompt) <= limit {
			return req, true
		}
	}
	return req, changed
}
