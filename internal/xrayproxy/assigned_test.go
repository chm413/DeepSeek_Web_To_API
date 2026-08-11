package xrayproxy

import (
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func TestAssignedSpecsUsesOneRoutePerReferencedNode(t *testing.T) {
	cfg := config.Config{
		Accounts: []config.Account{
			{Email: "first@example.com", ProxyID: "node-a"},
			{Email: "second@example.com", ProxyID: "node-a"},
			{Email: "third@example.com", ProxyID: "node-b"},
			{Email: "disabled@example.com", ProxyID: "node-c", Disabled: true},
		},
		Proxies: []config.Proxy{
			{ID: "node-a", Type: "vless", URI: "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none"},
			{ID: "node-b", Type: "vmess", URI: "vmess://eyJ2IjoiMiIsInBzIjoiYiIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiMTExMTExMTEtMTExMS0xMTExLTExMTEtMTExMTExMTExMTExIiwiYWlkIjoiMCIsIm5ldCI6InRjcCIsInR5cGUiOiJub25lIiwiaG9zdCI6IiIsInBhdGgiOiIiLCJ0bHMiOiJ0bHMifQ=="},
			{ID: "node-c", Type: "hysteria2", URI: "hysteria2://password@example.com:443"},
			{ID: "unused", Type: "vless", URI: "vless://22222222-2222-2222-2222-222222222222@example.net:443?encryption=none"},
		},
	}

	specs := AssignedSpecs(cfg)
	if len(specs) != 2 {
		t.Fatalf("expected two unique assigned routes, got %#v", specs)
	}
	ids := map[string]bool{}
	for _, spec := range specs {
		ids[spec.ID] = true
	}
	if !ids["node-a"] || !ids["node-b"] || ids["unused"] {
		t.Fatalf("unexpected assigned routes: %#v", specs)
	}
}

func TestAssignedSpecsUsesEnabledFallbackForDisabledRoute(t *testing.T) {
	cfg := config.Config{
		Accounts: []config.Account{{Email: "user@example.com", ProxyID: "primary"}},
		Proxies: []config.Proxy{
			{ID: "primary", Type: "vless", URI: "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none", Disabled: true},
			{ID: "fallback", Type: "hysteria2", URI: "hysteria2://password@example.net:443"},
		},
		ProxyPolicy: config.ProxyPolicyConfig{FallbackProxyID: "fallback"},
	}

	specs := AssignedSpecs(cfg)
	if len(specs) != 1 || specs[0].ID != "fallback" {
		t.Fatalf("expected fallback route, got %#v", specs)
	}
}
