package proxyuri

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVLESSReality(t *testing.T) {
	node, err := Parse("vless", "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=reality&type=tcp&sni=example.com&fp=chrome&pbk=public-key&sid=abcd#Node")
	if err != nil {
		t.Fatalf("parse VLESS: %v", err)
	}
	if node.Address != "example.com" || node.Port != 443 || node.DisplayName != "Node" {
		t.Fatalf("unexpected node: %#v", node)
	}
	stream := node.Outbound["streamSettings"].(map[string]any)
	if stream["security"] != "reality" || stream["network"] != "tcp" {
		t.Fatalf("unexpected stream: %#v", stream)
	}
}

func TestParseVMessWebSocketTLS(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"v": "2", "ps": "VMess node", "add": "vm.example.com", "port": "443",
		"id": "22222222-2222-2222-2222-222222222222", "aid": "0", "scy": "auto",
		"net": "ws", "host": "cdn.example.com", "path": "/ws", "tls": "tls", "sni": "cdn.example.com",
	})
	raw := "vmess://" + base64.RawStdEncoding.EncodeToString(payload)
	node, err := Parse("vmess", raw)
	if err != nil {
		t.Fatalf("parse VMess: %v", err)
	}
	if node.Address != "vm.example.com" || node.Port != 443 {
		t.Fatalf("unexpected endpoint: %#v", node)
	}
	stream := node.Outbound["streamSettings"].(map[string]any)
	if stream["network"] != "ws" || stream["security"] != "tls" {
		t.Fatalf("unexpected stream: %#v", stream)
	}
}

func TestParseHysteria2(t *testing.T) {
	node, err := Parse("hy2", "hy2://secret@example.com:8443?sni=edge.example.com#HY2")
	if err != nil {
		t.Fatalf("parse Hysteria2: %v", err)
	}
	if node.Type != "hysteria2" || node.Address != "example.com" || node.Port != 8443 {
		t.Fatalf("unexpected node: %#v", node)
	}
	if node.Outbound["protocol"] != "hysteria" {
		t.Fatalf("unexpected outbound: %#v", node.Outbound)
	}
	stream := node.Outbound["streamSettings"].(map[string]any)
	hysteria := stream["hysteriaSettings"].(map[string]any)
	if hysteria["auth"] != "secret" {
		t.Fatalf("unexpected hysteria auth settings: %#v", hysteria)
	}
}

func TestParseRejectsInsecureTLS(t *testing.T) {
	_, err := Parse("vless", "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&insecure=1")
	if err == nil {
		t.Fatal("expected unsupported insecure TLS error")
	}
}

func TestParseHysteria2RejectsUnsupportedObfs(t *testing.T) {
	_, err := Parse("hysteria2", "hysteria2://secret@example.com:443?obfs=salamander&obfs-password=value")
	if err == nil {
		t.Fatal("expected unsupported obfs error")
	}
}

func TestParseShadowsocksSIP002URLSafeUserinfo(t *testing.T) {
	credential := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:opaque:password@value"))
	node, err := Parse("ss", "ss://"+credential+"@edge.example.com:8388#SS%20Node")
	if err != nil {
		t.Fatalf("parse Shadowsocks URI: %v", err)
	}
	if node.Type != "shadowsocks" || node.Address != "edge.example.com" || node.Port != 8388 || node.DisplayName != "SS Node" {
		t.Fatalf("unexpected node: %#v", node)
	}
	if node.Outbound["protocol"] != "shadowsocks" {
		t.Fatalf("unexpected outbound: %#v", node.Outbound)
	}
	settings := node.Outbound["settings"].(map[string]any)
	if settings["method"] != "aes-256-gcm" || settings["password"] != "opaque:password@value" {
		t.Fatalf("unexpected Shadowsocks settings: %#v", settings)
	}
}

func TestParseShadowsocksLegacyWholeBase64(t *testing.T) {
	legacy := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:legacy-password@legacy.example.com:443"))
	node, err := Parse("shadowsocks", "ss://"+legacy+"#Legacy")
	if err != nil {
		t.Fatalf("parse legacy Shadowsocks URI: %v", err)
	}
	settings := node.Outbound["settings"].(map[string]any)
	if node.Address != "legacy.example.com" || node.Port != 443 || settings["method"] != "chacha20-ietf-poly1305" || settings["password"] != "legacy-password" {
		t.Fatalf("unexpected legacy Shadowsocks node: %#v", node)
	}
}

func TestParseShadowsocksSupportsDocumentedAEADAndSS2022Methods(t *testing.T) {
	methods := []string{
		"aes-128-gcm",
		"aes-256-gcm",
		"chacha20-poly1305",
		"chacha20-ietf-poly1305",
		"xchacha20-poly1305",
		"xchacha20-ietf-poly1305",
		"2022-blake3-aes-128-gcm",
		"2022-blake3-aes-256-gcm",
		"2022-blake3-chacha20-poly1305",
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			credential := base64.RawURLEncoding.EncodeToString([]byte(method + ":test-password"))
			node, err := Parse("shadowsocks", "ss://"+credential+"@example.com:8388")
			if err != nil {
				t.Fatalf("parse %s: %v", method, err)
			}
			settings := node.Outbound["settings"].(map[string]any)
			if settings["method"] != method {
				t.Fatalf("expected method %q, got %#v", method, settings)
			}
		})
	}
}

func TestParseShadowsocksRejectsMalformedPluginsAndUnsupportedParameters(t *testing.T) {
	credential := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:password"))
	tests := []string{
		"ss://not-base64@example.com:8388",
		"ss://" + credential + "@example.com:8388?plugin=v2ray-plugin",
		"ss://" + credential + "@example.com:8388?udp=true",
		"ss://" + base64.RawStdEncoding.EncodeToString([]byte("rc4-md5:password@example.com:8388")),
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse("shadowsocks", raw)
			if err == nil {
				t.Fatalf("expected URI to be rejected: %q", raw)
			}
		})
	}

	_, err := Parse("shadowsocks", "ss://"+credential+"@example.com:8388?plugin=v2ray-plugin")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "plugin") {
		t.Fatalf("expected plugin-specific error, got %v", err)
	}
}
