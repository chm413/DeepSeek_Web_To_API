package settings

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	authn "DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/responsecache"
	"DeepSeek_Web_To_API/internal/safetystore"
)

// mirrorWarn logs a non-fatal failure when mirroring a config write into one
// of the dedicated safety SQLite stores. The config-side write succeeds
// regardless so the request itself is never blocked by sqlite I/O.
func mirrorWarn(scope string, err error) {
	config.Logger.Warn("[admin_settings_safety_mirror] write failed", "scope", scope, "error", err)
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	promptLimitCfg, err := parsePromptLimitUpdate(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	adminCfg, runtimeCfg, compatCfg, responsesCfg, embeddingsCfg, cacheCfg, autoDeleteCfg, currentInputCfg, thinkingInjCfg, safetyCfg, aliasMap, err := parseSettingsUpdateRequest(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if runtimeCfg != nil {
		if err := validateMergedRuntimeSettings(h.Store.Snapshot().Runtime, runtimeCfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
	}
	currentInputEnabledSet := hasNestedSettingsKey(req, "current_input_file", "enabled")
	currentInputMinCharsSet := hasNestedSettingsKey(req, "current_input_file", "min_chars")
	thinkingInjectionEnabledSet := hasNestedSettingsKey(req, "thinking_injection", "enabled")
	thinkingInjectionPromptSet := hasNestedSettingsKey(req, "thinking_injection", "prompt")
	accountHealthIntervalSet := hasNestedSettingsKey(req, "runtime", "account_health_check_interval_minutes")
	cacheDirSet := hasNestedSettingsPath(req, "cache", "response", "dir")
	cacheMemoryTTLSet := hasNestedSettingsPath(req, "cache", "response", "memory_ttl_seconds")
	cacheDiskTTLSet := hasNestedSettingsPath(req, "cache", "response", "disk_ttl_seconds")
	cacheMaxBodySet := hasNestedSettingsPath(req, "cache", "response", "max_body_bytes")
	cacheMemoryMaxSet := hasNestedSettingsPath(req, "cache", "response", "memory_max_bytes")
	cacheDiskMaxSet := hasNestedSettingsPath(req, "cache", "response", "disk_max_bytes")
	cacheSemanticSet := hasNestedSettingsPath(req, "cache", "response", "semantic_key")

	if err := h.Store.Update(func(c *config.Config) error {
		if adminCfg != nil {
			if adminCfg.JWTExpireHours > 0 {
				c.Admin.JWTExpireHours = adminCfg.JWTExpireHours
			}
		}
		if runtimeCfg != nil {
			if runtimeCfg.AccountMaxInflight > 0 {
				c.Runtime.AccountMaxInflight = runtimeCfg.AccountMaxInflight
			}
			if runtimeCfg.AccountMaxQueue > 0 {
				c.Runtime.AccountMaxQueue = runtimeCfg.AccountMaxQueue
			}
			if runtimeCfg.GlobalMaxInflight > 0 {
				c.Runtime.GlobalMaxInflight = runtimeCfg.GlobalMaxInflight
			}
			if runtimeCfg.TokenRefreshIntervalHours > 0 {
				c.Runtime.TokenRefreshIntervalHours = runtimeCfg.TokenRefreshIntervalHours
			}
			if accountHealthIntervalSet {
				c.Runtime.AccountHealthCheckIntervalMinutes = runtimeCfg.AccountHealthCheckIntervalMinutes
			}
		}
		if compatCfg != nil {
			if compatCfg.WideInputStrictOutput != nil {
				c.Compat.WideInputStrictOutput = compatCfg.WideInputStrictOutput
			}
			if compatCfg.StripReferenceMarkers != nil {
				c.Compat.StripReferenceMarkers = compatCfg.StripReferenceMarkers
			}
		}
		if responsesCfg != nil && responsesCfg.StoreTTLSeconds > 0 {
			c.Responses.StoreTTLSeconds = responsesCfg.StoreTTLSeconds
		}
		if embeddingsCfg != nil && strings.TrimSpace(embeddingsCfg.Provider) != "" {
			c.Embeddings.Provider = strings.TrimSpace(embeddingsCfg.Provider)
		}
		if cacheCfg != nil {
			if cacheDirSet {
				c.Cache.Response.Dir = strings.TrimSpace(cacheCfg.Response.Dir)
			}
			if cacheMemoryTTLSet {
				c.Cache.Response.MemoryTTLSeconds = cacheCfg.Response.MemoryTTLSeconds
			}
			if cacheDiskTTLSet {
				c.Cache.Response.DiskTTLSeconds = cacheCfg.Response.DiskTTLSeconds
			}
			if cacheMaxBodySet {
				c.Cache.Response.MaxBodyBytes = cacheCfg.Response.MaxBodyBytes
			}
			if cacheMemoryMaxSet {
				c.Cache.Response.MemoryMaxBytes = cacheCfg.Response.MemoryMaxBytes
			}
			if cacheDiskMaxSet {
				c.Cache.Response.DiskMaxBytes = cacheCfg.Response.DiskMaxBytes
			}
			if cacheSemanticSet {
				c.Cache.Response.SemanticKey = cacheCfg.Response.SemanticKey
			}
		}
		if autoDeleteCfg != nil {
			c.AutoDelete.Mode = autoDeleteCfg.Mode
			c.AutoDelete.Sessions = autoDeleteCfg.Sessions
		}
		if currentInputCfg != nil {
			if currentInputEnabledSet {
				c.CurrentInputFile.Enabled = currentInputCfg.Enabled
			}
			if currentInputMinCharsSet {
				c.CurrentInputFile.MinChars = currentInputCfg.MinChars
			}
		}
		if thinkingInjCfg != nil {
			if thinkingInjectionEnabledSet {
				c.ThinkingInjection.Enabled = thinkingInjCfg.Enabled
			}
			if thinkingInjectionPromptSet {
				c.ThinkingInjection.Prompt = thinkingInjCfg.Prompt
			}
		}
		if promptLimitCfg != nil {
			if promptLimitCfg.Enabled != nil {
				c.PromptLimit.Enabled = promptLimitCfg.Enabled
			}
			if promptLimitCfg.MaxCharsDefault > 0 {
				c.PromptLimit.MaxCharsDefault = promptLimitCfg.MaxCharsDefault
			}
			if promptLimitCfg.MaxCharsExpert > 0 {
				c.PromptLimit.MaxCharsExpert = promptLimitCfg.MaxCharsExpert
			}
			if promptLimitCfg.AutoCompressEnabled != nil {
				c.PromptLimit.AutoCompressEnabled = promptLimitCfg.AutoCompressEnabled
			}
			if promptLimitCfg.CompressKeepRecent > 0 {
				c.PromptLimit.CompressKeepRecent = promptLimitCfg.CompressKeepRecent
			}
			if promptLimitCfg.CompressKeepSystem != nil {
				c.PromptLimit.CompressKeepSystem = promptLimitCfg.CompressKeepSystem
			}
			if promptLimitCfg.ProFlashCompressionEnabled != nil {
				c.PromptLimit.ProFlashCompressionEnabled = promptLimitCfg.ProFlashCompressionEnabled
			}
			if promptLimitCfg.ProFlashCompressionTargetChars > 0 {
				c.PromptLimit.ProFlashCompressionTargetChars = promptLimitCfg.ProFlashCompressionTargetChars
			}
			if promptLimitCfg.SessionChunkingEnabled != nil {
				c.PromptLimit.SessionChunkingEnabled = promptLimitCfg.SessionChunkingEnabled
			}
			if promptLimitCfg.SessionChunkingTargetRatio > 0 {
				c.PromptLimit.SessionChunkingTargetRatio = promptLimitCfg.SessionChunkingTargetRatio
			}
			if promptLimitCfg.SessionChunkingMaxChunks > 0 {
				c.PromptLimit.SessionChunkingMaxChunks = promptLimitCfg.SessionChunkingMaxChunks
			}
			if promptLimitCfg.SessionChunkingCommitTimeoutSeconds > 0 {
				c.PromptLimit.SessionChunkingCommitTimeoutSeconds = promptLimitCfg.SessionChunkingCommitTimeoutSeconds
			}
			if promptLimitCfg.IncrementalMaxTurns != nil {
				c.PromptLimit.IncrementalMaxTurns = promptLimitCfg.IncrementalMaxTurns
			}
			if promptLimitCfg.IncrementalRotationKeepRecent > 0 {
				c.PromptLimit.IncrementalRotationKeepRecent = promptLimitCfg.IncrementalRotationKeepRecent
			}
		}
		if safetyCfg != nil {
			// Preserve disabled_builtin_rules even if the request omits the
			// safety object's other slices entirely — operator can flip a
			// builtin rule on/off in isolation without losing custom lists.
			c.Safety = *safetyCfg
			// Mirror the list fields into the dedicated SQLite stores so
			// runtime state stays consistent across both sources. Failures
			// here are logged and do not roll back the config write — the
			// stores eventually catch up via the legacy fallback path in
			// requestguard.
			if h.SafetyWords != nil {
				if err := h.SafetyWords.ReplaceKind(safetystore.KindContent, safetyCfg.BannedContent); err != nil {
					mirrorWarn("safety_words.banned_content", err)
				}
				if err := h.SafetyWords.ReplaceKind(safetystore.KindRegex, safetyCfg.BannedRegex); err != nil {
					mirrorWarn("safety_words.banned_regex", err)
				}
				if err := h.SafetyWords.ReplaceKind(safetystore.KindJailbreak, safetyCfg.Jailbreak.Patterns); err != nil {
					mirrorWarn("safety_words.jailbreak", err)
				}
			}
			if h.SafetyIPs != nil {
				if err := h.SafetyIPs.ReplaceBlockedIPs(safetyCfg.BlockedIPs); err != nil {
					mirrorWarn("safety_ips.blocked_ips", err)
				}
				if err := h.SafetyIPs.ReplaceAllowedIPs(safetyCfg.AllowedIPs); err != nil {
					mirrorWarn("safety_ips.allowed_ips", err)
				}
				if err := h.SafetyIPs.ReplaceBlockedConversationIDs(safetyCfg.BlockedConversationIDs); err != nil {
					mirrorWarn("safety_ips.blocked_conversation_ids", err)
				}
			}
		}
		if aliasMap != nil {
			c.ModelAliases = aliasMap
		}
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}

	h.applyRuntimeSettings()
	if cacheCfg != nil {
		h.applyResponseCacheSettings()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "settings updated and hot reloaded",
		"env_backed": h.Store.IsEnvBacked(),
	})
}

func (h *Handler) updateSettingsPassword(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	newPassword := strings.TrimSpace(fieldString(req, "new_password"))
	if newPassword == "" {
		newPassword = strings.TrimSpace(fieldString(req, "password"))
	}
	if len(newPassword) < 10 || !containsAlphaNum(newPassword) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "new password must be at least 10 characters and include letters and digits"})
		return
	}

	now := time.Now().Unix()
	hash := authn.HashAdminPassword(newPassword)
	if err := h.Store.Update(func(c *config.Config) error {
		c.Admin.PasswordHash = hash
		c.Admin.JWTValidAfterUnix = now
		return nil
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":              true,
		"message":              "password updated",
		"force_relogin":        true,
		"jwt_valid_after_unix": now,
	})
}

func containsAlphaNum(v string) bool {
	hasAlpha := false
	hasDigit := false
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			fallthrough
		case r >= 'A' && r <= 'Z':
			hasAlpha = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
		if hasAlpha && hasDigit {
			return true
		}
	}
	return false
}

func hasNestedSettingsKey(req map[string]any, section, key string) bool {
	raw, ok := req[section].(map[string]any)
	if !ok {
		return false
	}
	_, exists := raw[key]
	return exists
}

func hasNestedSettingsPath(req map[string]any, first, second, key string) bool {
	raw, ok := req[first].(map[string]any)
	if !ok {
		return false
	}
	nested, ok := raw[second].(map[string]any)
	if !ok {
		return false
	}
	_, exists := nested[key]
	return exists
}

func (h *Handler) applyResponseCacheSettings() {
	if h == nil || h.Store == nil || h.ResponseCache == nil {
		return
	}
	h.ResponseCache.ApplyOptions(responsecache.Options{
		Dir:            h.Store.ResponseCacheDir(),
		MemoryTTL:      h.Store.ResponseCacheMemoryTTL(),
		DiskTTL:        h.Store.ResponseCacheDiskTTL(),
		MaxBody:        h.Store.ResponseCacheMaxBodyBytes(),
		MemoryMaxBytes: h.Store.ResponseCacheMemoryMaxBytes(),
		DiskMaxBytes:   h.Store.ResponseCacheDiskMaxBytes(),
		SemanticKey:    h.Store.ResponseCacheSemanticKey(),
	})
}
