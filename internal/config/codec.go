package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func promptLimitConfigured(p PromptLimitConfig) bool {
	return p.Enabled != nil || p.MaxCharsDefault > 0 || p.MaxCharsExpert > 0 ||
		p.AutoCompressEnabled != nil || p.CompressKeepRecent > 0 ||
		p.CompressKeepSystem != nil || p.ProFlashCompressionEnabled != nil ||
		p.ProFlashCompressionTargetChars > 0 || p.SessionChunkingEnabled != nil ||
		p.SessionChunkingTargetRatio > 0 || p.SessionChunkingMaxChunks > 0 ||
		p.SessionChunkingCommitTimeoutSeconds > 0 || p.SummaryCompactionEnabled != nil ||
		p.SummaryCompactionThreshold > 0 || p.IncrementalMaxTurns != nil ||
		p.IncrementalRotationKeepRecent > 0
}

func proxyCoreConfigured(c ProxyCoreConfig) bool {
	return strings.TrimSpace(c.XrayBinaryPath) != "" || strings.TrimSpace(c.RuntimeDir) != "" ||
		c.StartupTimeoutSeconds > 0 || c.AutoDownloadDisabled || strings.TrimSpace(c.DownloadDir) != "" ||
		strings.TrimSpace(c.DownloadVersion) != "" || strings.TrimSpace(c.InstalledVersion) != ""
}

func proxyPolicyConfigured(p ProxyPolicyConfig) bool {
	return p.HealthCheckEnabled != nil || p.AutomaticRoutingEnabled != nil || p.HealthCheckIntervalMinutes > 0 ||
		p.AutoDisableAfterFailures > 0 || p.AutoEnableOnRecovery != nil ||
		strings.TrimSpace(p.FallbackProxyID) != "" || p.SubscriptionUpdateIntervalMinutes > 0 ||
		p.TestConcurrency > 0
}

func appUpdateConfigured(c AppUpdateConfig) bool {
	return c.Enabled != nil || c.AutoDownload != nil || c.AutoApply != nil || c.CheckIntervalMinutes > 0
}

func (c Config) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	for k, v := range c.AdditionalFields {
		m[k] = v
	}
	if c.ConfigSchemaVersion > 0 {
		m["config_schema_version"] = c.ConfigSchemaVersion
	}
	if len(c.Keys) > 0 {
		m["keys"] = c.Keys
	}
	if len(c.APIKeys) > 0 {
		m["api_keys"] = c.APIKeys
	}
	if len(c.Accounts) > 0 {
		m["accounts"] = c.Accounts
	}
	if len(c.Proxies) > 0 {
		m["proxies"] = c.Proxies
	}
	if len(c.ProxySubscriptions) > 0 {
		m["proxy_subscriptions"] = c.ProxySubscriptions
	}
	if proxyCoreConfigured(c.ProxyCore) {
		m["proxy_core"] = c.ProxyCore
	}
	if proxyPolicyConfigured(c.ProxyPolicy) {
		m["proxy_policy"] = c.ProxyPolicy
	}
	if len(c.ModelAliases) > 0 {
		m["model_aliases"] = c.ModelAliases
	}
	if strings.TrimSpace(c.Admin.Key) != "" || strings.TrimSpace(c.Admin.PasswordHash) != "" || strings.TrimSpace(c.Admin.JWTSecret) != "" || c.Admin.JWTExpireHours > 0 || c.Admin.JWTValidAfterUnix > 0 {
		m["admin"] = c.Admin
	}
	if strings.TrimSpace(c.Server.Port) != "" || strings.TrimSpace(c.Server.BindAddr) != "" || strings.TrimSpace(c.Server.LogLevel) != "" || strings.TrimSpace(c.Server.StaticAdminDir) != "" || c.Server.AutoBuildWebUI != nil || c.Server.HTTPTotalTimeoutSeconds > 0 {
		m["server"] = c.Server
	}
	if strings.TrimSpace(c.Storage.DataDir) != "" ||
		strings.TrimSpace(c.Storage.AccountsSQLitePath) != "" ||
		strings.TrimSpace(c.Storage.ChatHistoryPath) != "" ||
		strings.TrimSpace(c.Storage.ChatHistorySQLitePath) != "" ||
		strings.TrimSpace(c.Storage.RawStreamSampleRoot) != "" {
		m["storage"] = c.Storage
	}
	if responseCacheConfigured(c.Cache.Response) {
		m["cache"] = c.Cache
	}
	if safetyConfigured(c.Safety) {
		m["safety"] = c.Safety
	}
	if c.Runtime.AccountMaxInflight > 0 || c.Runtime.AccountMaxQueue > 0 || c.Runtime.GlobalMaxInflight > 0 || c.Runtime.TokenRefreshIntervalHours > 0 {
		m["runtime"] = c.Runtime
	}
	if c.Compat.WideInputStrictOutput != nil || c.Compat.StripReferenceMarkers != nil {
		m["compat"] = c.Compat
	}
	if c.Responses.StoreTTLSeconds > 0 {
		m["responses"] = c.Responses
	}
	if strings.TrimSpace(c.Embeddings.Provider) != "" {
		m["embeddings"] = c.Embeddings
	}
	m["auto_delete"] = c.AutoDelete
	if c.HistorySplit.Enabled != nil || c.HistorySplit.TriggerAfterTurns != nil {
		m["history_split"] = c.HistorySplit
	}
	if c.CurrentInputFile.Enabled != nil || c.CurrentInputFile.MinChars != 0 {
		m["current_input_file"] = c.CurrentInputFile
	}
	if c.ThinkingInjection.Enabled != nil || strings.TrimSpace(c.ThinkingInjection.Prompt) != "" {
		m["thinking_injection"] = c.ThinkingInjection
	}
	if promptLimitConfigured(c.PromptLimit) {
		m["prompt_limit"] = c.PromptLimit
	}
	if appUpdateConfigured(c.AppUpdate) {
		m["app_update"] = c.AppUpdate
	}
	return json.Marshal(m)
}

func (c *Config) UnmarshalJSON(b []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	c.AdditionalFields = map[string]any{}
	for k, v := range raw {
		switch k {
		case "config_schema_version":
			if err := json.Unmarshal(v, &c.ConfigSchemaVersion); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "keys":
			if err := json.Unmarshal(v, &c.Keys); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "api_keys":
			if err := json.Unmarshal(v, &c.APIKeys); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "accounts":
			if err := json.Unmarshal(v, &c.Accounts); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "proxies":
			if err := json.Unmarshal(v, &c.Proxies); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "proxy_subscriptions":
			if err := json.Unmarshal(v, &c.ProxySubscriptions); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "proxy_core":
			if err := json.Unmarshal(v, &c.ProxyCore); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "proxy_policy":
			if err := json.Unmarshal(v, &c.ProxyPolicy); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "claude_mapping":
		case "claude_model_mapping":
			// Removed legacy mapping fields are ignored instead of persisted.
		case "model_aliases":
			if err := json.Unmarshal(v, &c.ModelAliases); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "admin":
			if err := json.Unmarshal(v, &c.Admin); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "server":
			if err := json.Unmarshal(v, &c.Server); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "storage":
			if err := json.Unmarshal(v, &c.Storage); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "cache":
			if err := json.Unmarshal(v, &c.Cache); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "safety":
			if err := json.Unmarshal(v, &c.Safety); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "runtime":
			if err := json.Unmarshal(v, &c.Runtime); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "compat":
			if err := json.Unmarshal(v, &c.Compat); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "toolcall":
			// Legacy field ignored. Toolcall policy is fixed and no longer configurable.
		case "responses":
			if err := json.Unmarshal(v, &c.Responses); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "embeddings":
			if err := json.Unmarshal(v, &c.Embeddings); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "auto_delete":
			if err := json.Unmarshal(v, &c.AutoDelete); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "history_split":
			if err := json.Unmarshal(v, &c.HistorySplit); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "current_input_file":
			if err := json.Unmarshal(v, &c.CurrentInputFile); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "thinking_injection":
			if err := json.Unmarshal(v, &c.ThinkingInjection); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "prompt_limit":
			if err := json.Unmarshal(v, &c.PromptLimit); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "app_update":
			if err := json.Unmarshal(v, &c.AppUpdate); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		default:
			var anyVal any
			if err := json.Unmarshal(v, &anyVal); err == nil {
				c.AdditionalFields[k] = anyVal
			}
		}
	}
	c.NormalizeCredentials()
	return nil
}

func (c Config) Clone() Config {
	clone := Config{
		ConfigSchemaVersion: c.ConfigSchemaVersion,
		Keys:                slices.Clone(c.Keys),
		APIKeys:             slices.Clone(c.APIKeys),
		Accounts:            slices.Clone(c.Accounts),
		Proxies:             slices.Clone(c.Proxies),
		ProxySubscriptions:  slices.Clone(c.ProxySubscriptions),
		ProxyCore:           c.ProxyCore,
		ProxyPolicy: ProxyPolicyConfig{
			HealthCheckEnabled:                cloneBoolPtr(c.ProxyPolicy.HealthCheckEnabled),
			AutomaticRoutingEnabled:           cloneBoolPtr(c.ProxyPolicy.AutomaticRoutingEnabled),
			HealthCheckIntervalMinutes:        c.ProxyPolicy.HealthCheckIntervalMinutes,
			AutoDisableAfterFailures:          c.ProxyPolicy.AutoDisableAfterFailures,
			AutoEnableOnRecovery:              cloneBoolPtr(c.ProxyPolicy.AutoEnableOnRecovery),
			FallbackProxyID:                   c.ProxyPolicy.FallbackProxyID,
			SubscriptionUpdateIntervalMinutes: c.ProxyPolicy.SubscriptionUpdateIntervalMinutes,
			TestConcurrency:                   c.ProxyPolicy.TestConcurrency,
		},
		ModelAliases: cloneStringMap(c.ModelAliases),
		Admin:        c.Admin,
		Server: ServerConfig{
			Port:                    c.Server.Port,
			BindAddr:                c.Server.BindAddr,
			LogLevel:                c.Server.LogLevel,
			StaticAdminDir:          c.Server.StaticAdminDir,
			AutoBuildWebUI:          cloneBoolPtr(c.Server.AutoBuildWebUI),
			HTTPTotalTimeoutSeconds: c.Server.HTTPTotalTimeoutSeconds,
		},
		Storage: c.Storage,
		Cache: CacheConfig{
			Response: ResponseCacheConfig{
				Dir:              c.Cache.Response.Dir,
				MemoryTTLSeconds: c.Cache.Response.MemoryTTLSeconds,
				DiskTTLSeconds:   c.Cache.Response.DiskTTLSeconds,
				MaxBodyBytes:     c.Cache.Response.MaxBodyBytes,
				MemoryMaxBytes:   c.Cache.Response.MemoryMaxBytes,
				DiskMaxBytes:     c.Cache.Response.DiskMaxBytes,
				SemanticKey:      cloneBoolPtr(c.Cache.Response.SemanticKey),
			},
		},
		Safety: SafetyConfig{
			Enabled:                cloneBoolPtr(c.Safety.Enabled),
			BlockMessage:           c.Safety.BlockMessage,
			BlockedIPs:             slices.Clone(c.Safety.BlockedIPs),
			AllowedIPs:             slices.Clone(c.Safety.AllowedIPs),
			BlockedConversationIDs: slices.Clone(c.Safety.BlockedConversationIDs),
			BannedContent:          slices.Clone(c.Safety.BannedContent),
			BannedRegex:            slices.Clone(c.Safety.BannedRegex),
			Jailbreak: JailbreakConfig{
				Enabled:  cloneBoolPtr(c.Safety.Jailbreak.Enabled),
				Patterns: slices.Clone(c.Safety.Jailbreak.Patterns),
			},
			AutoBan: SafetyAutoBanConfig{
				Enabled:       cloneBoolPtr(c.Safety.AutoBan.Enabled),
				Threshold:     c.Safety.AutoBan.Threshold,
				WindowSeconds: c.Safety.AutoBan.WindowSeconds,
			},
			DisabledBuiltinRules: slices.Clone(c.Safety.DisabledBuiltinRules),
			LLMCheck: SafetyLLMCheckConfig{
				Enabled:         cloneBoolPtr(c.Safety.LLMCheck.Enabled),
				Model:           c.Safety.LLMCheck.Model,
				TimeoutMs:       c.Safety.LLMCheck.TimeoutMs,
				FailOpen:        cloneBoolPtr(c.Safety.LLMCheck.FailOpen),
				CacheTTLSeconds: c.Safety.LLMCheck.CacheTTLSeconds,
				CacheMaxEntries: c.Safety.LLMCheck.CacheMaxEntries,
				MinInputChars:   c.Safety.LLMCheck.MinInputChars,
				MaxInputChars:   c.Safety.LLMCheck.MaxInputChars,
				MaxConcurrent:   c.Safety.LLMCheck.MaxConcurrent,
			},
		},
		Runtime: c.Runtime,
		Compat: CompatConfig{
			WideInputStrictOutput: cloneBoolPtr(c.Compat.WideInputStrictOutput),
			StripReferenceMarkers: cloneBoolPtr(c.Compat.StripReferenceMarkers),
		},
		Responses:  c.Responses,
		Embeddings: c.Embeddings,
		AutoDelete: c.AutoDelete,
		HistorySplit: HistorySplitConfig{
			Enabled:           cloneBoolPtr(c.HistorySplit.Enabled),
			TriggerAfterTurns: cloneIntPtr(c.HistorySplit.TriggerAfterTurns),
		},
		CurrentInputFile: CurrentInputFileConfig{
			Enabled:  cloneBoolPtr(c.CurrentInputFile.Enabled),
			MinChars: c.CurrentInputFile.MinChars,
		},
		ThinkingInjection: ThinkingInjectionConfig{
			Enabled: cloneBoolPtr(c.ThinkingInjection.Enabled),
			Prompt:  c.ThinkingInjection.Prompt,
		},
		PromptLimit: PromptLimitConfig{
			Enabled:                             cloneBoolPtr(c.PromptLimit.Enabled),
			MaxCharsDefault:                     c.PromptLimit.MaxCharsDefault,
			MaxCharsExpert:                      c.PromptLimit.MaxCharsExpert,
			AutoCompressEnabled:                 cloneBoolPtr(c.PromptLimit.AutoCompressEnabled),
			CompressKeepRecent:                  c.PromptLimit.CompressKeepRecent,
			CompressKeepSystem:                  cloneBoolPtr(c.PromptLimit.CompressKeepSystem),
			ProFlashCompressionEnabled:          cloneBoolPtr(c.PromptLimit.ProFlashCompressionEnabled),
			ProFlashCompressionTargetChars:      c.PromptLimit.ProFlashCompressionTargetChars,
			SessionChunkingEnabled:              cloneBoolPtr(c.PromptLimit.SessionChunkingEnabled),
			SessionChunkingTargetRatio:          c.PromptLimit.SessionChunkingTargetRatio,
			SessionChunkingMaxChunks:            c.PromptLimit.SessionChunkingMaxChunks,
			SessionChunkingCommitTimeoutSeconds: c.PromptLimit.SessionChunkingCommitTimeoutSeconds,
			SummaryCompactionEnabled:            cloneBoolPtr(c.PromptLimit.SummaryCompactionEnabled),
			SummaryCompactionThreshold:          c.PromptLimit.SummaryCompactionThreshold,
			IncrementalMaxTurns:                 cloneIntPtr(c.PromptLimit.IncrementalMaxTurns),
			IncrementalRotationKeepRecent:       c.PromptLimit.IncrementalRotationKeepRecent,
		},
		AppUpdate: AppUpdateConfig{
			Enabled:              cloneBoolPtr(c.AppUpdate.Enabled),
			AutoDownload:         cloneBoolPtr(c.AppUpdate.AutoDownload),
			AutoApply:            cloneBoolPtr(c.AppUpdate.AutoApply),
			CheckIntervalMinutes: c.AppUpdate.CheckIntervalMinutes,
		},
		AdditionalFields: map[string]any{},
	}
	for k, v := range c.AdditionalFields {
		clone.AdditionalFields[k] = v
	}
	return clone
}
