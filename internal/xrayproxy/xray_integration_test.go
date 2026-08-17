package xrayproxy

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficialXrayAcceptsGeneratedConfigs(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("XRAY_TEST_BINARY is not set")
	}
	tests := []Spec{
		{
			ID:   "vless-test",
			Type: "vless",
			URI:  "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com&type=ws&host=example.com&path=%2Fws",
		},
		{
			ID:   "vmess-test",
			Type: "vmess",
			URI:  "vmess://eyJ2IjoiMiIsInBzIjoiVGVzdCIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiMjIyMjIyMjItMjIyMi0yMjIyLTIyMjItMjIyMjIyMjIyMjIyIiwiYWlkIjoiMCIsInNjeSI6ImF1dG8iLCJuZXQiOiJ3cyIsImhvc3QiOiJleGFtcGxlLmNvbSIsInBhdGgiOiIvd3MiLCJ0bHMiOiJ0bHMifQ",
		},
		{
			ID:   "hysteria2-test",
			Type: "hysteria2",
			URI:  "hysteria2://secret@example.com:443?sni=example.com",
		},
		{
			ID:   "shadowsocks-test",
			Type: "shadowsocks",
			URI:  "ss://YWVzLTI1Ni1nY206c2hhZG93c29ja3MtcGFzc3dvcmQ@example.com:8388#SS",
		},
	}
	for _, spec := range tests {
		t.Run(spec.Type, func(t *testing.T) {
			encoded, err := BuildConfig(spec, 23456)
			if err != nil {
				t.Fatalf("build config: %v", err)
			}
			configPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cmd := exec.Command(binary, "run", "-test", "-c", configPath)
			cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(binary))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Xray rejected config: %v\n%s", err, output)
			}
		})
	}
}

func TestManagerStartsAndStopsOfficialXray(t *testing.T) {
	binary := os.Getenv("XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("XRAY_TEST_BINARY is not set")
	}
	manager := NewManager()
	defer manager.StopAll()
	addresses, err := manager.EnsureMany(context.Background(), []Spec{
		{ID: "lifecycle-vless", Type: "vless", URI: "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com"},
		{ID: "lifecycle-vmess", Type: "vmess", URI: "vmess://eyJ2IjoiMiIsInBzIjoiVGVzdCIsImFkZCI6ImV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiMjIyMjIyMjItMjIyMi0yMjIyLTIyMjItMjIyMjIyMjIyMjIyIiwiYWlkIjoiMCIsInNjeSI6ImF1dG8iLCJuZXQiOiJ3cyIsImhvc3QiOiJleGFtcGxlLmNvbSIsInBhdGgiOiIvd3MiLCJ0bHMiOiJ0bHMifQ"},
	}, Settings{BinaryPath: binary, RuntimeDir: t.TempDir(), StartupTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("start Xray: %v", err)
	}
	for proxyID, address := range addresses {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatalf("dial local SOCKS listener for %s: %v", proxyID, err)
		}
		_ = conn.Close()
	}
	if manager.Count() != 1 {
		t.Fatalf("expected one shared running instance, got %d", manager.Count())
	}
	if manager.RouteCount() != 2 {
		t.Fatalf("expected two routes, got %d", manager.RouteCount())
	}
	manager.Stop("lifecycle-vless")
	if manager.Count() != 1 || manager.RouteCount() != 1 {
		t.Fatalf("expected one process with one remaining route, got process=%d routes=%d", manager.Count(), manager.RouteCount())
	}
	manager.Stop("lifecycle-vmess")
	if manager.Count() != 0 {
		t.Fatalf("expected stopped manager, got %d instances", manager.Count())
	}
}
