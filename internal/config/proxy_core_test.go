package config

import "testing"

func TestValidateCoreProxyAndSettings(t *testing.T) {
	err := ValidateProxyConfig([]Proxy{{
		Type: "vless",
		URI:  "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com",
	}})
	if err != nil {
		t.Fatalf("validate VLESS proxy: %v", err)
	}
	if err := ValidateProxyCoreConfig(ProxyCoreConfig{StartupTimeoutSeconds: 60}); err != nil {
		t.Fatalf("validate maximum startup timeout: %v", err)
	}
	if err := ValidateProxyCoreConfig(ProxyCoreConfig{StartupTimeoutSeconds: 61}); err == nil {
		t.Fatal("expected startup timeout validation error")
	}
}
