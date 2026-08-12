package settings

import (
	"net/http"
	"strings"

	authn "DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

func (h *Handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	snap := h.Store.Snapshot()
	recommended := defaultRuntimeRecommended(len(snap.Accounts), h.Store.RuntimeAccountMaxInflight())
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"admin": map[string]any{
			"has_password_hash":        strings.TrimSpace(snap.Admin.PasswordHash) != "",
			"jwt_expire_hours":         h.Store.AdminJWTExpireHours(),
			"jwt_valid_after_unix":     snap.Admin.JWTValidAfterUnix,
			"default_password_warning": authn.UsingDefaultAdminKey(h.Store),
		},
		"runtime": map[string]any{
			"account_max_inflight":                  h.Store.RuntimeAccountMaxInflight(),
			"account_max_queue":                     h.Store.RuntimeAccountMaxQueue(recommended),
			"global_max_inflight":                   h.Store.RuntimeGlobalMaxInflight(recommended),
			"token_refresh_interval_hours":          h.Store.RuntimeTokenRefreshIntervalHours(),
			"account_health_check_interval_minutes": h.Store.RuntimeAccountHealthCheckIntervalMinutes(),
		},
		"compat": map[string]any{
			"wide_input_strict_output": h.Store.CompatWideInputStrictOutput(),
			"strip_reference_markers":  h.Store.CompatStripReferenceMarkers(),
		},
		"responses": map[string]any{
			"store_ttl_seconds": h.Store.ResponsesStoreTTLSeconds(),
		},
		"embeddings": map[string]any{
			"provider": h.Store.EmbeddingsProvider(),
		},
		"cache":       h.responseCacheSettings(),
		"safety":      h.safetyResponse(snap.Safety),
		"auto_delete": snap.AutoDelete,
		"current_input_file": map[string]any{
			"enabled":   h.Store.CurrentInputFileEnabled(),
			"min_chars": h.Store.CurrentInputFileMinChars(),
		},
		"thinking_injection": map[string]any{
			"enabled":        h.Store.ThinkingInjectionEnabled(),
			"prompt":         h.Store.ThinkingInjectionPrompt(),
			"default_prompt": promptcompat.DefaultThinkingInjectionPrompt,
		},
		"prompt_limit":  promptLimitResponse(h.Store.PromptLimitSnapshot()),
		"model_aliases": snap.ModelAliases,
		"env_backed":    h.Store.IsEnvBacked(),
	})
}

func promptLimitResponse(s config.PromptLimitSettings) map[string]any {
	return map[string]any{
		"enabled":                            s.Enabled,
		"max_chars_default":                  s.MaxCharsDefault,
		"max_chars_expert":                   s.MaxCharsExpert,
		"max_chars_default_configured":       s.MaxCharsDefaultConfigured,
		"max_chars_expert_configured":        s.MaxCharsExpertConfigured,
		"auto_compress_enabled":              s.AutoCompressEnable,
		"compress_keep_recent":               s.KeepRecentTurns,
		"compress_keep_system":               s.KeepSystemMessage,
		"pro_flash_compression_enabled":      s.ProFlashCompressionEnable,
		"pro_flash_compression_target_chars": s.ProFlashCompressionTarget,
		"summary_compaction_enabled":         s.SummaryCompactionEnable,
		"summary_compaction_threshold":       s.SummaryCompactionThreshold,
		"incremental_max_turns":              s.IncrementalMaxTurns,
		"incremental_rotation_keep_recent":   s.IncrementalRotationKeepRecent,
	}
}

// safetyResponse merges the legacy SafetyConfig with the dedicated SQLite
// stores so the admin UI sees the same view that the runtime guard is
// using. SQLite is the source of truth for blocked/allowed IPs, blocked
// conversation IDs, banned content, banned regex, and jailbreak patterns
// when the corresponding store is wired; the legacy config still appears
// as a fallback for setups that have not yet migrated.
func (h *Handler) safetyResponse(legacy config.SafetyConfig) map[string]any {
	enabled := false
	if legacy.Enabled != nil {
		enabled = *legacy.Enabled
	}
	jailbreakEnabled := false
	if legacy.Jailbreak.Enabled != nil {
		jailbreakEnabled = *legacy.Jailbreak.Enabled
	}
	autoBan := legacy.AutoBan
	autoBanEnabled := true
	if autoBan.Enabled != nil {
		autoBanEnabled = *autoBan.Enabled
	}
	autoBanThreshold := autoBan.Threshold
	if autoBanThreshold <= 0 {
		autoBanThreshold = 3
	}
	autoBanWindow := autoBan.WindowSeconds
	if autoBanWindow <= 0 {
		autoBanWindow = 600
	}

	blockedIPs := append([]string(nil), legacy.BlockedIPs...)
	allowedIPs := append([]string(nil), legacy.AllowedIPs...)
	blockedConv := append([]string(nil), legacy.BlockedConversationIDs...)
	bannedContent := append([]string(nil), legacy.BannedContent...)
	bannedRegex := append([]string(nil), legacy.BannedRegex...)
	jailPatterns := append([]string(nil), legacy.Jailbreak.Patterns...)

	if h.SafetyIPs != nil {
		if b, a, c, err := h.SafetyIPs.Snapshot(); err == nil {
			blockedIPs = mergeUnique(blockedIPs, b)
			allowedIPs = mergeUnique(allowedIPs, a)
			blockedConv = mergeUnique(blockedConv, c)
		}
	}
	if h.SafetyWords != nil {
		if content, regex, jail, err := h.SafetyWords.Snapshot(); err == nil {
			bannedContent = mergeUnique(bannedContent, content)
			bannedRegex = mergeUnique(bannedRegex, regex)
			jailPatterns = mergeUnique(jailPatterns, jail)
		}
	}

	return map[string]any{
		"enabled":                  enabled,
		"block_message":            legacy.BlockMessage,
		"blocked_ips":              blockedIPs,
		"allowed_ips":              allowedIPs,
		"blocked_conversation_ids": blockedConv,
		"banned_content":           bannedContent,
		"banned_regex":             bannedRegex,
		"disabled_builtin_rules":   append([]string(nil), legacy.DisabledBuiltinRules...),
		"jailbreak": map[string]any{
			"enabled":  jailbreakEnabled,
			"patterns": jailPatterns,
		},
		"auto_ban": map[string]any{
			"enabled":        autoBanEnabled,
			"threshold":      autoBanThreshold,
			"window_seconds": autoBanWindow,
		},
		"llm_check": map[string]any{
			"enabled":           legacy.LLMCheck.Enabled != nil && *legacy.LLMCheck.Enabled,
			"model":             legacy.LLMCheck.Model,
			"timeout_ms":        legacy.LLMCheck.TimeoutMs,
			"fail_open":         legacy.LLMCheck.FailOpen == nil || *legacy.LLMCheck.FailOpen,
			"cache_ttl_seconds": legacy.LLMCheck.CacheTTLSeconds,
			"cache_max_entries": legacy.LLMCheck.CacheMaxEntries,
			"min_input_chars":   legacy.LLMCheck.MinInputChars,
			"max_input_chars":   legacy.LLMCheck.MaxInputChars,
			"max_concurrent":    legacy.LLMCheck.MaxConcurrent,
		},
	}
}

// mergeUnique appends b into a without duplicates while preserving order
// (a entries first, b entries appended in order).
func mergeUnique(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, v := range a {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range b {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func (h *Handler) responseCacheSettings() map[string]any {
	response := map[string]any{
		"dir":                h.Store.ResponseCacheDir(),
		"memory_ttl_seconds": int(h.Store.ResponseCacheMemoryTTL().Seconds()),
		"memory_max_bytes":   h.Store.ResponseCacheMemoryMaxBytes(),
		"disk_ttl_seconds":   int(h.Store.ResponseCacheDiskTTL().Seconds()),
		"disk_max_bytes":     h.Store.ResponseCacheDiskMaxBytes(),
		"max_body_bytes":     h.Store.ResponseCacheMaxBodyBytes(),
		"semantic_key":       h.Store.ResponseCacheSemanticKey(),
		"compression":        "gzip",
	}
	if h.ResponseCache != nil {
		stats := h.ResponseCache.Stats()
		for _, key := range []string{
			"disk_dir",
			"memory_ttl_seconds",
			"memory_max_bytes",
			"disk_ttl_seconds",
			"disk_max_bytes",
			"max_body_bytes",
			"semantic_key",
			"compression",
		} {
			if value, ok := stats[key]; ok {
				if key == "disk_dir" {
					response["dir"] = value
				} else {
					response[key] = value
				}
			}
		}
	}
	return map[string]any{"response": response}
}
