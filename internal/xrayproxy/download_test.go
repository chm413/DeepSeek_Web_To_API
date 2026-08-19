package xrayproxy

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestXrayAssetName(t *testing.T) {
	tests := map[string]string{
		"windows/amd64": "Xray-windows-64.zip",
		"windows/arm64": "Xray-windows-arm64-v8a.zip",
		"linux/amd64":   "Xray-linux-64.zip",
		"linux/arm64":   "Xray-linux-arm64-v8a.zip",
	}
	for platform, want := range tests {
		parts := splitPlatform(platform)
		got, err := xrayAssetName(parts[0], parts[1])
		if err != nil {
			t.Fatalf("asset for %s: %v", platform, err)
		}
		if got != want {
			t.Fatalf("asset for %s = %s, want %s", platform, got, want)
		}
	}
}

func TestExplicitXrayVersionNeedsRefresh(t *testing.T) {
	for _, test := range []struct {
		requested string
		installed string
		want      bool
	}{
		{requested: "v24.9.9", installed: "v24.9.9", want: false},
		{requested: "24.9.9", installed: "v24.9.8", want: true},
		{requested: "v24.9.8", installed: "v24.9.9", want: true},
		{requested: "v24.9.9", installed: "", want: true},
		{requested: "latest", installed: "v24.9.8", want: false},
		{requested: "", installed: "v24.9.8", want: false},
	} {
		if got := explicitXrayVersionNeedsRefresh(test.requested, test.installed); got != test.want {
			t.Errorf("requested=%q installed=%q got=%v want=%v", test.requested, test.installed, got, test.want)
		}
	}
}

func TestNormalizeXrayDigest(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, raw := range []string{digest, "sha256:" + digest, "SHA256:" + digest} {
		got, err := normalizeXrayDigest(raw)
		if err != nil || got != digest {
			t.Fatalf("normalize %q = %q, %v", raw, got, err)
		}
	}
	if _, err := normalizeXrayDigest("not-a-digest"); err == nil {
		t.Fatal("expected malformed digest to fail")
	}
}

func TestExtractXrayArchiveOnlyInstallsRequiredFiles(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "xray.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, body := range map[string]string{
		xrayExecutableName(): "binary",
		"geoip.dat":          "geoip",
		"geosite.dat":        "geosite",
		"ignored.txt":        "ignored",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := extractXrayArchive(archivePath, target); err != nil {
		t.Fatalf("extract archive: %v", err)
	}
	for _, name := range []string{xrayExecutableName(), "geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected ignored file: %v", err)
	}
}

func TestExtractXrayArchiveMissingRequiredFilePreservesExistingInstall(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "incomplete.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, body := range map[string]string{
		xrayExecutableName(): "new-binary",
		"geoip.dat":          "new-geoip",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	for name, body := range map[string]string{
		xrayExecutableName(): "old-binary",
		"geoip.dat":          "old-geoip",
		"geosite.dat":        "old-geosite",
	} {
		if err := os.WriteFile(filepath.Join(target, name), []byte(body), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := extractXrayArchive(archivePath, target); err == nil {
		t.Fatal("expected missing required file error")
	}
	for name, want := range map[string]string{
		xrayExecutableName(): "old-binary",
		"geoip.dat":          "old-geoip",
		"geosite.dat":        "old-geosite",
	} {
		got, readErr := os.ReadFile(filepath.Join(target, name))
		if readErr != nil || string(got) != want {
			t.Fatalf("existing %s changed after failed extraction: got=%q err=%v", name, got, readErr)
		}
	}
}

func TestDownloadOfficialXrayRelease(t *testing.T) {
	if strings.TrimSpace(os.Getenv("XRAY_DOWNLOAD_INTEGRATION")) != "1" {
		t.Skip("set XRAY_DOWNLOAD_INTEGRATION=1 to test the official release download")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	binaryPath, err := DownloadCore(ctx, Settings{DownloadDir: t.TempDir()}, true)
	if err != nil {
		t.Fatalf("download official Xray release: %v", err)
	}
	cmd := exec.CommandContext(ctx, binaryPath, "version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run downloaded Xray: %v output=%s", err, output)
	}
	if !strings.Contains(strings.ToLower(string(output)), "xray") {
		t.Fatalf("unexpected downloaded Xray version output: %s", output)
	}
}

func splitPlatform(value string) [2]string {
	for i := range value {
		if value[i] == '/' {
			return [2]string{value[:i], value[i+1:]}
		}
	}
	return [2]string{value, ""}
}
