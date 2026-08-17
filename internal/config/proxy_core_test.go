package config

import (
	"encoding/base64"
	"testing"
)

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

func TestValidateShadowsocksProxy(t *testing.T) {
	credential := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:config-password"))
	if err := ValidateProxyConfig([]Proxy{{
		Type: "ss",
		URI:  "ss://" + credential + "@ss.example.com:8388#Config",
	}}); err != nil {
		t.Fatalf("validate Shadowsocks proxy: %v", err)
	}
	if err := ValidateProxyConfig([]Proxy{{
		Type: "shadowsocks",
		URI:  "ss://" + credential + "@ss.example.com:8388?plugin=v2ray-plugin",
	}}); err == nil {
		t.Fatal("expected unsupported Shadowsocks plugin error")
	}
}
