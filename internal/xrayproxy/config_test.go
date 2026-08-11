package xrayproxy

import (
	"encoding/json"
	"testing"
)

func TestBuildConfigCreatesLocalSOCKSAndVLESSOutbound(t *testing.T) {
	encoded, err := BuildConfig(Spec{
		ID:   "proxy-1",
		Type: "vless",
		URI:  "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com",
	}, 23456)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(encoded, &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	inbounds := config["inbounds"].([]any)
	inbound := inbounds[0].(map[string]any)
	if inbound["port"] != float64(23456) || inbound["protocol"] != "socks" {
		t.Fatalf("unexpected inbound: %#v", inbound)
	}
	outbounds := config["outbounds"].([]any)
	outbound := outbounds[0].(map[string]any)
	if outbound["protocol"] != "vless" {
		t.Fatalf("unexpected outbound: %#v", outbound)
	}
}
