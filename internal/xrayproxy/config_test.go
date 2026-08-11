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

func TestBuildConfigManyCreatesIndependentRoutes(t *testing.T) {
	encoded, err := BuildConfigMany([]Route{
		{Spec: Spec{ID: "vless-1", Type: "vless", URI: "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com"}, SocksPort: 23001},
		{Spec: Spec{ID: "vmess-1", Type: "vmess", URI: "vmess://eyJ2IjoiMiIsInBzIjoiVGVzdCIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiMjIyMjIyMjItMjIyMi0yMjIyLTIyMjItMjIyMjIyMjIyMjIyIiwiYWlkIjoiMCIsInNjeSI6ImF1dG8iLCJuZXQiOiJ3cyIsImhvc3QiOiJleGFtcGxlLmNvbSIsInBhdGgiOiIvd3MiLCJ0bHMiOiJ0bHMifQ"}, SocksPort: 23002},
	})
	if err != nil {
		t.Fatalf("build shared config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(encoded, &config); err != nil {
		t.Fatalf("decode shared config: %v", err)
	}
	if got := len(config["inbounds"].([]any)); got != 2 {
		t.Fatalf("expected two inbounds, got %d", got)
	}
	if got := len(config["outbounds"].([]any)); got != 4 {
		t.Fatalf("expected two proxy outbounds plus direct/block, got %d", got)
	}
	routing := config["routing"].(map[string]any)
	if got := len(routing["rules"].([]any)); got != 2 {
		t.Fatalf("expected two routing rules, got %d", got)
	}
}
