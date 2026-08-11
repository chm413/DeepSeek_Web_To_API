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
	address, err := manager.Ensure(context.Background(), Spec{
		ID:   "lifecycle-test",
		Type: "vless",
		URI:  "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com",
	}, Settings{BinaryPath: binary, RuntimeDir: t.TempDir(), StartupTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("start Xray: %v", err)
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("dial local SOCKS listener: %v", err)
	}
	_ = conn.Close()
	if manager.Count() != 1 {
		t.Fatalf("expected one running instance, got %d", manager.Count())
	}
	manager.Stop("lifecycle-test")
	if manager.Count() != 0 {
		t.Fatalf("expected stopped manager, got %d instances", manager.Count())
	}
}
