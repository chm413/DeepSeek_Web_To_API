package settings

import (
	"fmt"
	"strconv"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
)

func boolFrom(v any) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.ToLower(strings.TrimSpace(x)) == "true"
	default:
		return false
	}
}

func int64From(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func parseSettingsUpdateRequest(req map[string]any) (*config.AdminConfig, *config.RuntimeConfig, *config.CompatConfig, *config.ResponsesConfig, *config.EmbeddingsConfig, *config.CacheConfig, *config.AutoDeleteConfig, *config.CurrentInputFileConfig, *config.ThinkingInjectionConfig, *config.SafetyConfig, map[string]string, error) {
	var (
		adminCfg        *config.AdminConfig
		runtimeCfg      *config.RuntimeConfig
		compatCfg       *config.CompatConfig
		respCfg         *config.ResponsesConfig
		embCfg          *config.EmbeddingsConfig
		cacheCfg        *config.CacheConfig
		autoDeleteCfg   *config.AutoDeleteConfig
		currentInputCfg *config.CurrentInputFileConfig
		thinkingInjCfg  *config.ThinkingInjectionConfig
		safetyCfg       *config.SafetyConfig
		aliasMap        map[string]string
	)

	if raw, ok := req["admin"].(map[string]any); ok {
		cfg := &config.AdminConfig{}
		if v, exists := raw["jwt_expire_hours"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("admin.jwt_expire_hours", n, 1, 720, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.JWTExpireHours = n
		}
		adminCfg = cfg
	}

	if raw, ok := req["runtime"].(map[string]any); ok {
		cfg := &config.RuntimeConfig{}
		if v, exists := raw["account_max_inflight"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("runtime.account_max_inflight", n, 1, 256, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.AccountMaxInflight = n
		}
		if v, exists := raw["account_max_queue"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("runtime.account_max_queue", n, 1, 200000, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.AccountMaxQueue = n
		}
		if v, exists := raw["global_max_inflight"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("runtime.global_max_inflight", n, 1, 200000, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.GlobalMaxInflight = n
		}
		if v, exists := raw["token_refresh_interval_hours"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("runtime.token_refresh_interval_hours", n, 1, 720, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.TokenRefreshIntervalHours = n
		}
		if v, exists := raw["account_health_check_interval_minutes"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("runtime.account_health_check_interval_minutes", n, 1, 1440, false); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.AccountHealthCheckIntervalMinutes = n
		}
		if cfg.AccountMaxInflight > 0 && cfg.GlobalMaxInflight > 0 && cfg.GlobalMaxInflight < cfg.AccountMaxInflight {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, fmt.Errorf("runtime.global_max_inflight must be >= runtime.account_max_inflight")
		}
		runtimeCfg = cfg
	}

	if raw, ok := req["compat"].(map[string]any); ok {
		cfg := &config.CompatConfig{}
		if v, exists := raw["wide_input_strict_output"]; exists {
			b := boolFrom(v)
			cfg.WideInputStrictOutput = &b
		}
		if v, exists := raw["strip_reference_markers"]; exists {
			b := boolFrom(v)
			cfg.StripReferenceMarkers = &b
		}
		compatCfg = cfg
	}

	if raw, ok := req["responses"].(map[string]any); ok {
		cfg := &config.ResponsesConfig{}
		if v, exists := raw["store_ttl_seconds"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("responses.store_ttl_seconds", n, 30, 86400, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.StoreTTLSeconds = n
		}
		respCfg = cfg
	}

	if raw, ok := req["embeddings"].(map[string]any); ok {
		cfg := &config.EmbeddingsConfig{}
		if v, exists := raw["provider"]; exists {
			p := strings.TrimSpace(fmt.Sprintf("%v", v))
			if err := config.ValidateTrimmedString("embeddings.provider", p, false); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.Provider = p
		}
		embCfg = cfg
	}

	if raw, ok := req["cache"].(map[string]any); ok {
		if responseRaw, ok := raw["response"].(map[string]any); ok {
			cfg := &config.CacheConfig{}
			if v, exists := responseRaw["dir"]; exists {
				cfg.Response.Dir = strings.TrimSpace(fmt.Sprintf("%v", v))
			}
			if v, exists := responseRaw["memory_ttl_seconds"]; exists {
				n := intFrom(v)
				if err := config.ValidateIntRange("cache.response.memory_ttl_seconds", n, 1, 86400, true); err != nil {
					return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
				}
				cfg.Response.MemoryTTLSeconds = n
			}
			if v, exists := responseRaw["disk_ttl_seconds"]; exists {
				n := intFrom(v)
				if err := config.ValidateIntRange("cache.response.disk_ttl_seconds", n, 1, 604800, true); err != nil {
					return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
				}
				cfg.Response.DiskTTLSeconds = n
			}
			if v, exists := responseRaw["max_body_bytes"]; exists {
				n := int64From(v)
				if err := config.ValidateInt64Range("cache.response.max_body_bytes", n, 1, 1<<30, true); err != nil {
					return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
				}
				cfg.Response.MaxBodyBytes = n
			}
			if v, exists := responseRaw["memory_max_bytes"]; exists {
				n := int64From(v)
				if err := config.ValidateInt64Range("cache.response.memory_max_bytes", n, 1, 1<<40, true); err != nil {
					return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
				}
				cfg.Response.MemoryMaxBytes = n
			}
			if v, exists := responseRaw["disk_max_bytes"]; exists {
				n := int64From(v)
				if err := config.ValidateInt64Range("cache.response.disk_max_bytes", n, 1, 1<<42, true); err != nil {
					return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
				}
				cfg.Response.DiskMaxBytes = n
			}
			if v, exists := responseRaw["semantic_key"]; exists {
				b := boolFrom(v)
				cfg.Response.SemanticKey = &b
			}
			if err := config.ValidateCacheConfig(*cfg); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cacheCfg = cfg
		}
	}

	if raw, ok := req["model_aliases"].(map[string]any); ok {
		if aliasMap == nil {
			aliasMap = map[string]string{}
		}
		for k, v := range raw {
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(fmt.Sprintf("%v", v))
			if key == "" || val == "" {
				continue
			}
			aliasMap[key] = val
		}
	}

	if raw, ok := req["auto_delete"].(map[string]any); ok {
		cfg := &config.AutoDeleteConfig{}
		if v, exists := raw["mode"]; exists {
			mode := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
			if err := config.ValidateAutoDeleteMode(mode); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			if mode == "" {
				mode = "none"
			}
			cfg.Mode = mode
		}
		if v, exists := raw["sessions"]; exists {
			cfg.Sessions = boolFrom(v)
		}
		autoDeleteCfg = cfg
	}

	if raw, ok := req["current_input_file"].(map[string]any); ok {
		cfg := &config.CurrentInputFileConfig{}
		if v, exists := raw["enabled"]; exists {
			enabled := boolFrom(v)
			cfg.Enabled = &enabled
		}
		if v, exists := raw["min_chars"]; exists {
			n := intFrom(v)
			if err := config.ValidateIntRange("current_input_file.min_chars", n, 0, 100000000, true); err != nil {
				return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
			}
			cfg.MinChars = n
		}
		if err := config.ValidateCurrentInputFileConfig(*cfg); err != nil {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
		}
		currentInputCfg = cfg
	}

	if raw, ok := req["thinking_injection"].(map[string]any); ok {
		cfg := &config.ThinkingInjectionConfig{}
		if v, exists := raw["enabled"]; exists {
			b := boolFrom(v)
			cfg.Enabled = &b
		}
		if v, exists := raw["prompt"]; exists {
			cfg.Prompt = strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		thinkingInjCfg = cfg
	}

	if raw, ok := req["safety"].(map[string]any); ok {
		cfg := &config.SafetyConfig{}
		if v, exists := raw["enabled"]; exists {
			b := boolFrom(v)
			cfg.Enabled = &b
		}
		if v, exists := raw["block_message"]; exists {
			cfg.BlockMessage = strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		cfg.BlockedIPs = stringSliceFrom(raw["blocked_ips"])
		cfg.AllowedIPs = stringSliceFrom(raw["allowed_ips"])
		cfg.BlockedConversationIDs = stringSliceFrom(raw["blocked_conversation_ids"])
		cfg.BannedContent = stringSliceFrom(raw["banned_content"])
		cfg.BannedRegex = stringSliceFrom(raw["banned_regex"])
		cfg.DisabledBuiltinRules = stringSliceFrom(raw["disabled_builtin_rules"])
		if jailRaw, ok := raw["jailbreak"].(map[string]any); ok {
			if v, exists := jailRaw["enabled"]; exists {
				b := boolFrom(v)
				cfg.Jailbreak.Enabled = &b
			}
			cfg.Jailbreak.Patterns = stringSliceFrom(jailRaw["patterns"])
		}
		if autoRaw, ok := raw["auto_ban"].(map[string]any); ok {
			if v, exists := autoRaw["enabled"]; exists {
				b := boolFrom(v)
				cfg.AutoBan.Enabled = &b
			}
			if v, exists := autoRaw["threshold"]; exists {
				cfg.AutoBan.Threshold = intFrom(v)
			}
			if v, exists := autoRaw["window_seconds"]; exists {
				cfg.AutoBan.WindowSeconds = intFrom(v)
			}
		}
		if llmRaw, ok := raw["llm_check"].(map[string]any); ok {
			if v, exists := llmRaw["enabled"]; exists {
				b := boolFrom(v)
				cfg.LLMCheck.Enabled = &b
			}
			if v, exists := llmRaw["model"]; exists {
				cfg.LLMCheck.Model = strings.TrimSpace(fmt.Sprintf("%v", v))
			}
			if v, exists := llmRaw["timeout_ms"]; exists {
				cfg.LLMCheck.TimeoutMs = intFrom(v)
			}
			if v, exists := llmRaw["fail_open"]; exists {
				b := boolFrom(v)
				cfg.LLMCheck.FailOpen = &b
			}
			if v, exists := llmRaw["cache_ttl_seconds"]; exists {
				cfg.LLMCheck.CacheTTLSeconds = intFrom(v)
			}
			if v, exists := llmRaw["cache_max_entries"]; exists {
				cfg.LLMCheck.CacheMaxEntries = intFrom(v)
			}
			if v, exists := llmRaw["min_input_chars"]; exists {
				cfg.LLMCheck.MinInputChars = intFrom(v)
			}
			if v, exists := llmRaw["max_input_chars"]; exists {
				cfg.LLMCheck.MaxInputChars = intFrom(v)
			}
			if v, exists := llmRaw["max_concurrent"]; exists {
				cfg.LLMCheck.MaxConcurrent = intFrom(v)
			}
		}
		if err := config.ValidateSafetyConfig(*cfg); err != nil {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, err
		}
		safetyCfg = cfg
	}

	return adminCfg, runtimeCfg, compatCfg, respCfg, embCfg, cacheCfg, autoDeleteCfg, currentInputCfg, thinkingInjCfg, safetyCfg, aliasMap, nil
}

func parsePromptLimitUpdate(req map[string]any) (*config.PromptLimitConfig, error) {
	raw, ok := req["prompt_limit"].(map[string]any)
	if !ok {
		return nil, nil
	}
	cfg := &config.PromptLimitConfig{}
	if v, exists := raw["enabled"]; exists {
		b := boolFrom(v)
		cfg.Enabled = &b
	}
	if v, exists := raw["max_chars_default"]; exists {
		n := intFrom(v)
		if err := config.ValidateIntRange("prompt_limit.max_chars_default", n, 1, 10000000, true); err != nil {
			return nil, err
		}
		cfg.MaxCharsDefault = n
	}
	if v, exists := raw["max_chars_expert"]; exists {
		n := intFrom(v)
		if err := config.ValidateIntRange("prompt_limit.max_chars_expert", n, 1, 1000000, true); err != nil {
			return nil, err
		}
		cfg.MaxCharsExpert = n
	}
	if v, exists := raw["auto_compress_enabled"]; exists {
		b := boolFrom(v)
		cfg.AutoCompressEnabled = &b
	}
	if v, exists := raw["compress_keep_recent"]; exists {
		n := intFrom(v)
		if err := config.ValidateIntRange("prompt_limit.compress_keep_recent", n, 1, 100, true); err != nil {
			return nil, err
		}
		cfg.CompressKeepRecent = n
	}
	if v, exists := raw["compress_keep_system"]; exists {
		b := boolFrom(v)
		cfg.CompressKeepSystem = &b
	}
	if v, exists := raw["pro_flash_compression_enabled"]; exists {
		b := boolFrom(v)
		cfg.ProFlashCompressionEnabled = &b
	}
	if v, exists := raw["pro_flash_compression_target_chars"]; exists {
		n := intFrom(v)
		if err := config.ValidateIntRange("prompt_limit.pro_flash_compression_target_chars", n, 1, 1000000, true); err != nil {
			return nil, err
		}
		cfg.ProFlashCompressionTargetChars = n
	}
	if v, exists := raw["incremental_max_turns"]; exists {
		n := intFrom(v)
		if err := config.ValidateIntRange("prompt_limit.incremental_max_turns", n, 0, 1000000, true); err != nil {
			return nil, err
		}
		cfg.IncrementalMaxTurns = &n
	}
	if v, exists := raw["incremental_rotation_keep_recent"]; exists {
		n := intFrom(v)
		if err := config.ValidateIntRange("prompt_limit.incremental_rotation_keep_recent", n, 1, 100, true); err != nil {
			return nil, err
		}
		cfg.IncrementalRotationKeepRecent = n
	}
	return cfg, nil
}

func stringSliceFrom(value any) []string {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text := strings.TrimSpace(fmt.Sprintf("%v", item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text := strings.TrimSpace(item)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		lines := strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == ',' })
		out := make([]string, 0, len(lines))
		for _, item := range lines {
			text := strings.TrimSpace(item)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
