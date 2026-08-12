package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

// ResolveDynamicPromptLimits overlays provider-advertised hard ceilings onto
// local limits. A provider value replaces an empirical fallback so newly
// advertised capacity (including the 1M range) can be used, while an explicit
// operator ceiling remains a safety cap and can only be lowered by upstream.
func ResolveDynamicPromptLimits(ctx context.Context, ds any, a *auth.RequestAuth, cfg config.PromptLimitSettings) (config.PromptLimitSettings, bool, error) {
	if !cfg.Enabled || ds == nil || a == nil {
		return cfg, false, nil
	}
	provider, ok := ds.(DynamicPromptLimitProvider)
	if !ok {
		return cfg, false, nil
	}
	limits, err := provider.GetModelInputLimits(ctx, a)
	if err != nil {
		return cfg, false, fmt.Errorf("dynamic prompt limit lookup: %w", err)
	}
	if limits.Default > 0 {
		if !cfg.MaxCharsDefaultConfigured || cfg.MaxCharsDefault <= 0 || limits.Default < cfg.MaxCharsDefault {
			cfg.MaxCharsDefault = limits.Default
		}
	}
	if limits.Expert > 0 {
		if !cfg.MaxCharsExpertConfigured || cfg.MaxCharsExpert <= 0 || limits.Expert < cfg.MaxCharsExpert {
			cfg.MaxCharsExpert = limits.Expert
		}
	}
	if cfg.ProFlashCompressionTarget > cfg.MaxCharsExpert {
		cfg.ProFlashCompressionTarget = cfg.MaxCharsExpert
	}
	config.Logger.Info("[prompt_limit] resolved dynamic upstream limits",
		"provider_default_units", limits.Default,
		"provider_expert_units", limits.Expert,
		"effective_default_units", cfg.MaxCharsDefault,
		"effective_expert_units", cfg.MaxCharsExpert,
		"default_operator_ceiling", cfg.MaxCharsDefaultConfigured,
		"expert_operator_ceiling", cfg.MaxCharsExpertConfigured,
		"auto_compress_enabled", cfg.AutoCompressEnable)
	return cfg, true, nil
}

// ResponsesCompactThreshold reads the official Responses API shape:
//
//	"context_management": [{"type":"compaction","compact_threshold":200000}]
//
// compact_threshold is a rendered-token count. It must not be interpreted as
// a fraction of DeepSeek Web's UTF-16 input_character_limit.
func ResponsesCompactThreshold(req map[string]any) (int, bool, error) {
	raw, exists := req["context_management"]
	if !exists || raw == nil {
		return 0, false, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return 0, false, fmt.Errorf("context_management must be an array")
	}
	threshold := 0
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return 0, false, fmt.Errorf("context_management entries must be objects")
		}
		if !strings.EqualFold(strings.TrimSpace(fmt.Sprintf("%v", item["type"])), "compaction") {
			continue
		}
		rawThreshold, exists := item["compact_threshold"]
		if !exists || rawThreshold == nil {
			continue
		}
		value, ok := positiveJSONInteger(rawThreshold)
		if !ok {
			return 0, false, fmt.Errorf("context_management.compact_threshold must be a positive integer token count")
		}
		if threshold == 0 || value < threshold {
			threshold = value
		}
	}
	return threshold, threshold > 0, nil
}

func positiveJSONInteger(value any) (int, bool) {
	var n int64
	switch v := value.(type) {
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, false
		}
		n = parsed
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		n = int64(v)
	case float32:
		if v != float32(int64(v)) {
			return 0, false
		}
		n = int64(v)
	case int:
		n = int64(v)
	case int64:
		n = v
	default:
		return 0, false
	}
	maxInt := int64(^uint(0) >> 1)
	if n <= 0 || n > maxInt {
		return 0, false
	}
	return int(n), true
}
