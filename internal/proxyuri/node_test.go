package proxyuri

import (
	"encoding/base64"
	"encoding/json"
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
