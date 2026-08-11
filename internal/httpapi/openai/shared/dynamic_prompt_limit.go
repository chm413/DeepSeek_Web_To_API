package shared

import (
	"context"
	"fmt"
	"strconv"
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

// ApplyResponsesCompactThreshold turns OpenAI Responses' per-request
// compact_threshold into a temporary local compression budget. It does not
// emit a provider-owned encrypted compaction item; it only asks the existing
// history compressor to compact earlier than the hard upstream ceiling.
func ApplyResponsesCompactThreshold(req map[string]any, cfg config.PromptLimitSettings, model string) (config.PromptLimitSettings, bool) {
	if !cfg.Enabled {
		return cfg, false
	}
	management, _ := req["context_management"].(map[string]any)
	if management == nil {
		return cfg, false
	}
	threshold, ok := numericFraction(management["compact_threshold"])
	if !ok || threshold <= 0 || threshold >= 1 {
		return cfg, false
	}
	limit := configLimitForModel(cfg, model)
	target := int(float64(limit) * threshold)
	if target <= 0 || target >= limit {
		return cfg, false
	}
	if isExpertModel(model) {
		cfg.MaxCharsExpert = target
	} else {
		cfg.MaxCharsDefault = target
	}
	// A client-provided compact_threshold is an explicit compaction request;
	// it is allowed even when silent over-limit auto-compression is disabled.
	cfg.AutoCompressEnable = true
	if cfg.ProFlashCompressionTarget > target && isExpertModel(model) {
		cfg.ProFlashCompressionTarget = target
	}
	return cfg, true
}

func numericFraction(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func configLimitForModel(cfg config.PromptLimitSettings, model string) int {
	if isExpertModel(model) {
		return cfg.MaxCharsExpert
	}
	return cfg.MaxCharsDefault
}

func isExpertModel(model string) bool {
	modelType, ok := config.GetModelType(model)
	return ok && modelType == "expert"
}
