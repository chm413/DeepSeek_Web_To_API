package xrayproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

type corePersistenceStore struct {
	cfg     config.Config
	updates int
}

func (s *corePersistenceStore) Snapshot() config.Config {
	return s.cfg.Clone()
}

func (s *corePersistenceStore) Update(mutator func(*config.Config) error) error {
	next := s.cfg.Clone()
	if err := mutator(&next); err != nil {
		return err
	}
	s.cfg = next
	s.updates++
	return nil
}

func TestSettingsFromStorePersistsManagedInstallation(t *testing.T) {
	downloadDir := t.TempDir()
	binaryPath := writeManagedCore(t, downloadDir, "v25.1.0")
	store := &corePersistenceStore{cfg: config.Config{ProxyCore: config.ProxyCoreConfig{
		DownloadDir:     downloadDir,
		DownloadVersion: "v24.9.9",
	}}}

	resolved, err := ResolveOrDownload(context.Background(), SettingsFromStore(store))
	if err != nil {
		t.Fatalf("resolve managed core: %v", err)
	}
	if !sameLocalPath(resolved, binaryPath) {
		t.Fatalf("resolved core = %q, want %q", resolved, binaryPath)
	}
	core := store.Snapshot().ProxyCore
	if !sameLocalPath(core.DownloadDir, downloadDir) {
		t.Fatalf("persisted download dir = %q, want %q", core.DownloadDir, downloadDir)
	}
	if core.InstalledVersion != "v25.1.0" {
		t.Fatalf("persisted installed version = %q", core.InstalledVersion)
	}
	if core.DownloadVersion != "v24.9.9" {
		t.Fatalf("requested version was overwritten: %q", core.DownloadVersion)
	}
	if store.updates != 1 {
		t.Fatalf("persistence writes = %d, want 1", store.updates)
	}

	if _, err := ResolveOrDownload(context.Background(), SettingsFromStore(store)); err != nil {
		t.Fatalf("resolve persisted managed core: %v", err)
	}
	if store.updates != 1 {
		t.Fatalf("unchanged managed core wrote configuration again: %d updates", store.updates)
	}
}

func TestManagedInstallationDoesNotOverwriteNewDownloadTarget(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	binaryPath := writeManagedCore(t, firstDir, "v25.1.0")
	store := &corePersistenceStore{cfg: config.Config{ProxyCore: config.ProxyCoreConfig{DownloadDir: firstDir}}}
	settings := SettingsFromStore(store)
	store.cfg.ProxyCore.DownloadDir = secondDir

	PersistInstalledCore(settings, binaryPath)
	core := store.Snapshot().ProxyCore
	if !sameLocalPath(core.DownloadDir, secondDir) {
		t.Fatalf("new download target was overwritten: %q", core.DownloadDir)
	}
	if core.InstalledVersion != "" {
		t.Fatalf("installed version from stale target was persisted: %q", core.InstalledVersion)
	}
	if store.updates != 0 {
		t.Fatalf("stale target caused %d unexpected writes", store.updates)
	}
}

func TestExplicitXrayBinaryIsNotPersistedAsManagedDownload(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "custom-"+xrayExecutableName())
	if err := os.WriteFile(binaryPath, []byte("custom core"), 0o700); err != nil {
		t.Fatalf("write explicit core: %v", err)
	}
	store := &corePersistenceStore{cfg: config.Config{ProxyCore: config.ProxyCoreConfig{XrayBinaryPath: binaryPath}}}

	resolved, err := ResolveOrDownload(context.Background(), SettingsFromStore(store))
	if err != nil {
		t.Fatalf("resolve explicit core: %v", err)
	}
	if !sameLocalPath(resolved, binaryPath) {
		t.Fatalf("resolved explicit core = %q, want %q", resolved, binaryPath)
	}
	if core := store.Snapshot().ProxyCore; core.InstalledVersion != "" || core.DownloadDir != "" {
		t.Fatalf("explicit core unexpectedly changed managed state: %#v", core)
	}
	if store.updates != 0 {
		t.Fatalf("explicit core caused %d unexpected writes", store.updates)
	}
}

func writeManagedCore(t *testing.T, downloadDir, version string) string {
	t.Helper()
	binaryPath := filepath.Join(downloadDir, xrayExecutableName())
	if err := os.WriteFile(binaryPath, []byte("managed core"), 0o700); err != nil {
		t.Fatalf("write managed core: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, ".version"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatalf("write managed core version: %v", err)
	}
	return binaryPath
}
