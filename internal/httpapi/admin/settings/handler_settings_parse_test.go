package settings

import (
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func TestPromptLimitResponseReportsOperatorCeilings(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsDefaultConfigured = false
	cfg.MaxCharsExpertConfigured = true
	response := promptLimitResponse(cfg)

	if response["max_chars_default_configured"] != false {
		t.Fatalf("default configured flag = %#v", response["max_chars_default_configured"])
	}
	if response["max_chars_expert_configured"] != true {
		t.Fatalf("expert configured flag = %#v", response["max_chars_expert_configured"])
	}
}

func TestParseSettingsUpdateRequestSafetyConfig(t *testing.T) {
	enabled := true
	req := map[string]any{
		"safety": map[string]any{
			"enabled":                      enabled,
			"block_message":                "blocked",
			"blocked_ips":                  []any{"203.0.113.10", "198.51.100.0/24"},
			"blocked_conversation_ids":     "conv-1\nconv-2",
			"banned_content":               []any{"secret phrase"},
			"banned_regex":                 []any{"(?i)do not allow"},
			"jailbreak":                    map[string]any{"enabled": true, "patterns": "ignore guardrails"},
			"unused_forward_compatibility": "ignored",
		},
	}

	_, _, _, _, _, _, _, _, _, safety, _, err := parseSettingsUpdateRequest(req)
	if err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	if safety == nil || safety.Enabled == nil || !*safety.Enabled {
		t.Fatalf("expected enabled safety config, got %#v", safety)
	}
	if safety.BlockMessage != "blocked" {
		t.Fatalf("block message=%q", safety.BlockMessage)
	}
	if len(safety.BlockedIPs) != 2 || safety.BlockedIPs[1] != "198.51.100.0/24" {
		t.Fatalf("blocked ips=%v", safety.BlockedIPs)
	}
	if len(safety.BlockedConversationIDs) != 2 || safety.BlockedConversationIDs[1] != "conv-2" {
		t.Fatalf("blocked conversation ids=%v", safety.BlockedConversationIDs)
	}
	if len(safety.BannedRegex) != 1 || safety.BannedRegex[0] != "(?i)do not allow" {
		t.Fatalf("banned regex=%v", safety.BannedRegex)
	}
	if safety.Jailbreak.Enabled == nil || !*safety.Jailbreak.Enabled || len(safety.Jailbreak.Patterns) != 1 {
		t.Fatalf("jailbreak=%#v", safety.Jailbreak)
	}
}

func TestParseSettingsUpdateRequestRejectsInvalidSafetyRegex(t *testing.T) {
	req := map[string]any{
		"safety": map[string]any{
			"enabled":      true,
			"banned_regex": []any{"["},
		},
	}

	_, _, _, _, _, _, _, _, _, _, _, err := parseSettingsUpdateRequest(req)
	if err == nil {
		t.Fatal("expected invalid safety regex error")
	}
}

func TestParseSettingsUpdateRequestResponseCacheConfig(t *testing.T) {
	semanticKey := false
	req := map[string]any{
		"cache": map[string]any{
			"response": map[string]any{
				"dir":                "data/cache2",
				"memory_ttl_seconds": float64(300),
				"memory_max_bytes":   float64(1024),
				"disk_ttl_seconds":   float64(14400),
				"disk_max_bytes":     "4096",
				"max_body_bytes":     float64(2048),
				"semantic_key":       semanticKey,
			},
		},
	}

	_, _, _, _, _, cacheCfg, _, _, _, _, _, err := parseSettingsUpdateRequest(req)
	if err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	if cacheCfg == nil {
		t.Fatal("expected cache config")
	}
	if cacheCfg.Response.Dir != "data/cache2" {
		t.Fatalf("dir=%q", cacheCfg.Response.Dir)
	}
	if cacheCfg.Response.MemoryTTLSeconds != 300 || cacheCfg.Response.DiskTTLSeconds != 14400 {
		t.Fatalf("unexpected ttl config: %#v", cacheCfg.Response)
	}
	if cacheCfg.Response.MemoryMaxBytes != 1024 || cacheCfg.Response.DiskMaxBytes != 4096 || cacheCfg.Response.MaxBodyBytes != 2048 {
		t.Fatalf("unexpected size config: %#v", cacheCfg.Response)
	}
	if cacheCfg.Response.SemanticKey == nil || *cacheCfg.Response.SemanticKey {
		t.Fatalf("semantic_key=%#v", cacheCfg.Response.SemanticKey)
	}
}

func TestParseSettingsUpdateRequestRejectsInvalidResponseCacheLimit(t *testing.T) {
	req := map[string]any{
		"cache": map[string]any{
			"response": map[string]any{
				"memory_max_bytes": float64(0),
			},
		},
	}

	_, _, _, _, _, _, _, _, _, _, _, err := parseSettingsUpdateRequest(req)
	if err == nil {
		t.Fatal("expected invalid cache limit error")
	}
}

func TestParsePromptLimitUpdateSupportsProFlashCompression(t *testing.T) {
	req := map[string]any{
		"prompt_limit": map[string]any{
			"enabled":                            true,
			"max_chars_expert":                   float64(163840),
			"pro_flash_compression_enabled":      true,
			"pro_flash_compression_target_chars": float64(150000),
		},
	}

	cfg, err := parsePromptLimitUpdate(req)
	if err != nil {
		t.Fatalf("parse prompt limit: %v", err)
	}
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatalf("expected enabled prompt limit, got %#v", cfg)
	}
	if cfg.MaxCharsExpert != 163840 {
		t.Fatalf("max_chars_expert=%d", cfg.MaxCharsExpert)
	}
	if cfg.ProFlashCompressionEnabled == nil || !*cfg.ProFlashCompressionEnabled {
		t.Fatalf("pro flash switch=%#v", cfg.ProFlashCompressionEnabled)
	}
	if cfg.ProFlashCompressionTargetChars != 150000 {
		t.Fatalf("pro flash target=%d", cfg.ProFlashCompressionTargetChars)
	}
}

func TestParsePromptLimitUpdateRejectsInvalidProFlashTarget(t *testing.T) {
	_, err := parsePromptLimitUpdate(map[string]any{
		"prompt_limit": map[string]any{
			"pro_flash_compression_target_chars": float64(0),
		},
	})
	if err == nil {
		t.Fatal("expected invalid Pro Flash target error")
	}
}
