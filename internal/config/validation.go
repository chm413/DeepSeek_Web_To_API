package config

import (
	"fmt"
	"regexp"
	"strings"

	"DeepSeek_Web_To_API/internal/proxyuri"
)

func ValidateConfig(c Config) error {
	if err := ValidateProxyConfig(c.Proxies); err != nil {
		return err
	}
	if err := ValidateProxyCoreConfig(c.ProxyCore); err != nil {
		return err
	}
	if err := ValidateAdminConfig(c.Admin); err != nil {
		return err
	}
	if err := ValidateServerConfig(c.Server); err != nil {
		return err
	}
	if err := ValidateCacheConfig(c.Cache); err != nil {
		return err
	}
	if err := ValidateSafetyConfig(c.Safety); err != nil {
		return err
	}
	if err := ValidateRuntimeConfig(c.Runtime); err != nil {
		return err
	}
	if err := ValidateResponsesConfig(c.Responses); err != nil {
		return err
	}
	if err := ValidateEmbeddingsConfig(c.Embeddings); err != nil {
		return err
	}
	if err := ValidateAutoDeleteConfig(c.AutoDelete); err != nil {
		return err
	}
	if err := ValidateCurrentInputFileConfig(c.CurrentInputFile); err != nil {
		return err
	}
	if err := ValidateAccountProxyReferences(c.Accounts, c.Proxies); err != nil {
		return err
	}
	return nil
}

func ValidateProxyConfig(proxies []Proxy) error {
	seen := make(map[string]struct{}, len(proxies))
	for _, proxy := range proxies {
		proxy = NormalizeProxy(proxy)
		if err := ValidateTrimmedString("proxies.id", proxy.ID, true); err != nil {
			return err
		}
		switch proxy.Type {
		case "socks5", "socks5h":
			if err := ValidateTrimmedString("proxies.host", proxy.Host, true); err != nil {
				return err
			}
			if err := ValidateIntRange("proxies.port", proxy.Port, 1, 65535, true); err != nil {
				return err
			}
		case "vless", "vmess", "hysteria2":
			if _, err := proxyuri.Parse(proxy.Type, proxy.URI); err != nil {
				return fmt.Errorf("invalid %s proxy URI: %w", proxy.Type, err)
			}
		default:
			return fmt.Errorf("proxies.type must be one of socks5, socks5h, vless, vmess, hysteria2")
		}
		if _, ok := seen[proxy.ID]; ok {
			return fmt.Errorf("duplicate proxy id: %s", proxy.ID)
		}
		seen[proxy.ID] = struct{}{}
	}
	return nil
}

func ValidateProxyCoreConfig(core ProxyCoreConfig) error {
	if core.StartupTimeoutSeconds != 0 {
		return ValidateIntRange("proxy_core.startup_timeout_seconds", core.StartupTimeoutSeconds, 1, 60, true)
	}
	return nil
}

func ValidateAccountProxyReferences(accounts []Account, proxies []Proxy) error {
	if len(accounts) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(proxies))
	for _, proxy := range proxies {
		ids[NormalizeProxy(proxy).ID] = struct{}{}
	}
	for _, acc := range accounts {
		proxyID := strings.TrimSpace(acc.ProxyID)
		if proxyID == "" {
			continue
		}
		if _, ok := ids[proxyID]; !ok {
			return fmt.Errorf("account proxy_id references unknown proxy: %s", proxyID)
		}
	}
	return nil
}

func ValidateAdminConfig(admin AdminConfig) error {
	return ValidateIntRange("admin.jwt_expire_hours", admin.JWTExpireHours, 1, 720, false)
}

func ValidateServerConfig(server ServerConfig) error {
	if err := ValidateIntRange("server.http_total_timeout_seconds", server.HTTPTotalTimeoutSeconds, 1, 86400, false); err != nil {
		return err
	}
	switch strings.ToUpper(strings.TrimSpace(server.LogLevel)) {
	case "", "DEBUG", "INFO", "WARN", "ERROR":
		return nil
	default:
		return fmt.Errorf("server.log_level must be one of DEBUG, INFO, WARN, ERROR")
	}
}

func ValidateCacheConfig(cache CacheConfig) error {
	response := cache.Response
	if err := ValidateIntRange("cache.response.memory_ttl_seconds", response.MemoryTTLSeconds, 1, 86400, false); err != nil {
		return err
	}
	if err := ValidateIntRange("cache.response.disk_ttl_seconds", response.DiskTTLSeconds, 1, 604800, false); err != nil {
		return err
	}
	if err := ValidateInt64Range("cache.response.max_body_bytes", response.MaxBodyBytes, 1, 1<<30, false); err != nil {
		return err
	}
	if err := ValidateInt64Range("cache.response.memory_max_bytes", response.MemoryMaxBytes, 1, 1<<40, false); err != nil {
		return err
	}
	return ValidateInt64Range("cache.response.disk_max_bytes", response.DiskMaxBytes, 1, 1<<42, false)
}

func ValidateSafetyConfig(safety SafetyConfig) error {
	if err := validateStringList("safety.blocked_ips", safety.BlockedIPs, 256); err != nil {
		return err
	}
	if err := validateStringList("safety.allowed_ips", safety.AllowedIPs, 256); err != nil {
		return err
	}
	if err := validateStringList("safety.blocked_conversation_ids", safety.BlockedConversationIDs, 256); err != nil {
		return err
	}
	if err := validateStringList("safety.banned_content", safety.BannedContent, 4096); err != nil {
		return err
	}
	if err := validateStringList("safety.jailbreak.patterns", safety.Jailbreak.Patterns, 4096); err != nil {
		return err
	}
	for _, pattern := range safety.BannedRegex {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if len(pattern) > 4096 {
			return fmt.Errorf("safety.banned_regex item is too long")
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("safety.banned_regex invalid pattern: %w", err)
		}
	}
	if safety.AutoBan.Threshold < 0 || safety.AutoBan.Threshold > 1_000_000 {
		return fmt.Errorf("safety.auto_ban.threshold out of range: %d", safety.AutoBan.Threshold)
	}
	if safety.AutoBan.WindowSeconds < 0 || safety.AutoBan.WindowSeconds > 30*24*60*60 {
		return fmt.Errorf("safety.auto_ban.window_seconds out of range: %d", safety.AutoBan.WindowSeconds)
	}
	return nil
}

func validateStringList(name string, values []string, maxLen int) error {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > maxLen {
			return fmt.Errorf("%s item is too long", name)
		}
	}
	return nil
}

func ValidateRuntimeConfig(runtime RuntimeConfig) error {
	if err := ValidateIntRange("runtime.account_max_inflight", runtime.AccountMaxInflight, 1, 256, false); err != nil {
		return err
	}
	if err := ValidateIntRange("runtime.account_max_queue", runtime.AccountMaxQueue, 1, 200000, false); err != nil {
		return err
	}
	if err := ValidateIntRange("runtime.global_max_inflight", runtime.GlobalMaxInflight, 1, 200000, false); err != nil {
		return err
	}
	if err := ValidateIntRange("runtime.token_refresh_interval_hours", runtime.TokenRefreshIntervalHours, 1, 720, false); err != nil {
		return err
	}
	if err := ValidateIntRange("runtime.account_health_check_interval_minutes", runtime.AccountHealthCheckIntervalMinutes, 1, 1440, false); err != nil {
		return err
	}
	if runtime.AccountMaxInflight > 0 && runtime.GlobalMaxInflight > 0 && runtime.GlobalMaxInflight < runtime.AccountMaxInflight {
		return fmt.Errorf("runtime.global_max_inflight must be >= runtime.account_max_inflight")
	}
	return nil
}

func ValidateResponsesConfig(responses ResponsesConfig) error {
	return ValidateIntRange("responses.store_ttl_seconds", responses.StoreTTLSeconds, 30, 86400, false)
}

func ValidateEmbeddingsConfig(embeddings EmbeddingsConfig) error {
	return ValidateTrimmedString("embeddings.provider", embeddings.Provider, false)
}

func ValidateAutoDeleteConfig(autoDelete AutoDeleteConfig) error {
	return ValidateAutoDeleteMode(autoDelete.Mode)
}

func ValidateCurrentInputFileConfig(currentInputFile CurrentInputFileConfig) error {
	if currentInputFile.MinChars != 0 {
		return ValidateIntRange("current_input_file.min_chars", currentInputFile.MinChars, 1, 100000000, true)
	}
	return nil
}

func ValidateIntRange(name string, value, min, max int, required bool) error {
	if value == 0 && !required {
		return nil
	}
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return nil
}

func ValidateInt64Range(name string, value, min, max int64, required bool) error {
	if value == 0 && !required {
		return nil
	}
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return nil
}

func ValidateTrimmedString(name, value string, required bool) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if !required && value == "" {
			return nil
		}
		return fmt.Errorf("%s cannot be empty", name)
	}
	return nil
}

func ValidateAutoDeleteMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "none", "single", "all":
		return nil
	default:
		return fmt.Errorf("auto_delete.mode must be one of none, single, all")
	}
}
