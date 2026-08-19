package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

func TestManagerStagesAppliesAndConfirmsVerifiedRelease(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchive(t, tag, "amd64", nil)
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()

	container := true
	currentVersion := "1.1.9"
	manager := New(nil, Options{
		Root:           t.TempDir(),
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return currentVersion },
		Container:      &container,
	})

	latest, err := manager.Check(context.Background())
	if err != nil {
		t.Fatalf("check latest release: %v", err)
	}
	if latest == nil || latest.Tag != tag || !latest.Downloadable {
		t.Fatalf("unexpected latest release: %#v", latest)
	}
	if !manager.Status().UpdateAvailable {
		t.Fatal("expected update availability after a newer release check")
	}
	if _, err := manager.Download(context.Background(), tag); err != nil {
		t.Fatalf("download release: %v", err)
	}
	if staged, err := manager.stagedRelease(tag); err != nil || !staged {
		t.Fatalf("release was not staged: staged=%v err=%v", staged, err)
	}
	if got := manager.Status().DownloadedTag; got != tag {
		t.Fatalf("downloaded tag = %q, want %q", got, tag)
	}
	if err := manager.writeMarker(markerPrevious, "v1.0.0"); err != nil {
		t.Fatalf("seed previous release: %v", err)
	}
	status, err := manager.Apply(tag)
	if err != nil {
		t.Fatalf("apply release: %v", err)
	}
	if status.PendingTag != tag || status.PreviousTag != "v1.0.0" {
		t.Fatalf("pending tag = %q, want %q", status.PendingTag, tag)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_SELF_UPDATE_ACTIVE_VERSION", tag)
	currentVersion = "1.2.0"
	if err := manager.ConfirmStartup(); err != nil {
		t.Fatalf("confirm startup: %v", err)
	}
	status = manager.Status()
	if status.InstalledTag != tag || status.PreviousTag != "v1.1.9" || status.PendingTag != "" {
		t.Fatalf("unexpected post-confirm status: %#v", status)
	}
	if err := manager.ScheduleRestart(0); err != nil {
		t.Fatalf("schedule restart: %v", err)
	}
	select {
	case <-manager.RestartRequests():
	case <-time.After(time.Second):
		t.Fatal("expected scheduled restart request")
	}
}

func TestManagerRejectsChecksumMismatchWithoutStaging(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchive(t, tag, "amd64", nil)
	server := releaseServer(t, tag, "amd64", archive, strings.Repeat("0", sha256.Size*2))
	defer server.Close()
	container := true
	root := t.TempDir()
	manager := New(nil, Options{
		Root:           root,
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return "1.1.9" },
		Container:      &container,
	})
	if _, err := manager.Download(context.Background(), tag); err == nil {
		t.Fatal("expected checksum mismatch to fail")
	}
	if _, err := os.Stat(filepath.Join(root, "versions", tag)); !os.IsNotExist(err) {
		t.Fatalf("unverified version directory must not exist, stat err=%v", err)
	}
}

func TestManagerRejectsTamperedStagedRuntime(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchive(t, tag, "amd64", nil)
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()
	container := true
	manager := New(nil, Options{
		Root:           t.TempDir(),
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return "1.1.9" },
		Container:      &container,
	})
	if _, err := manager.Download(context.Background(), tag); err != nil {
		t.Fatalf("stage release: %v", err)
	}
	root := filepath.Join(manager.root, "versions", tag)
	if err := os.WriteFile(filepath.Join(root, "deepseek-web-to-api"), []byte("tampered"), 0o750); err != nil {
		t.Fatalf("tamper staged binary: %v", err)
	}
	if staged, err := manager.stagedRelease(tag); err == nil || staged || !strings.Contains(err.Error(), "binary checksum mismatch") {
		t.Fatalf("tampered binary was accepted: staged=%v err=%v", staged, err)
	}
}

func TestManagerRejectsTraversalArchive(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchive(t, tag, "amd64", map[string][]byte{
		"../outside": []byte("must not be written"),
	})
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()
	container := true
	root := t.TempDir()
	manager := New(nil, Options{
		Root:           root,
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return "1.1.9" },
		Container:      &container,
	})
	if _, err := manager.Download(context.Background(), tag); err == nil {
		t.Fatal("expected traversal archive to fail")
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
		t.Fatalf("archive traversal created an outside file: %v", err)
	}
}

func TestManagerStagesArchiveWithBareTopLevelDirectory(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchiveWithBareTopLevelDirectory(t, tag, "amd64")
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()

	container := true
	manager := New(nil, Options{
		Root:           t.TempDir(),
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return "1.1.9" },
		Container:      &container,
	})
	if _, err := manager.Download(context.Background(), tag); err != nil {
		t.Fatalf("stage archive with bare top-level directory: %v", err)
	}
	if staged, err := manager.stagedRelease(tag); err != nil || !staged {
		t.Fatalf("release was not staged: staged=%v err=%v", staged, err)
	}
}

func TestManagerRejectsRegularFileAtReleaseRoot(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchiveWithBareRootFile(t, tag, "amd64")
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()

	container := true
	manager := New(nil, Options{
		Root:           t.TempDir(),
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return "1.1.9" },
		Container:      &container,
	})
	if _, err := manager.Download(context.Background(), tag); err == nil || !strings.Contains(err.Error(), "unexpected top-level path") {
		t.Fatalf("expected regular top-level root file to fail, got %v", err)
	}
}

func TestManagerUpdateSettingsPersistsAndRejectsAutoApplyOutsideContainer(t *testing.T) {
	store := &memoryStore{}
	container := false
	manager := New(store, Options{Root: t.TempDir(), Container: &container})
	enabled := false
	interval := 15
	if _, err := manager.UpdateSettings(SettingsPatch{Enabled: &enabled, CheckIntervalMinutes: &interval}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	settings := manager.Settings()
	if settings.Enabled || settings.CheckIntervalMinutes != interval {
		t.Fatalf("settings were not persisted: %#v", settings)
	}
	autoApply := true
	if _, err := manager.UpdateSettings(SettingsPatch{AutoApply: &autoApply}); err == nil {
		t.Fatal("expected auto-apply outside a managed container to fail")
	}
}

func TestManagerUpdateSettingsRequiresAutoDownloadForAutoApply(t *testing.T) {
	store := &memoryStore{}
	container := true
	manager := New(store, Options{Root: t.TempDir(), Container: &container})
	autoApply := true
	if _, err := manager.UpdateSettings(SettingsPatch{AutoApply: &autoApply}); err == nil {
		t.Fatal("expected auto-apply without automatic download to fail")
	}
}

func TestAutomaticApplySkipsFailedCandidateUntilManualRetry(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchive(t, tag, "amd64", nil)
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()

	container := true
	autoDownload := true
	autoApply := true
	store := &memoryStore{cfg: config.Config{AppUpdate: config.AppUpdateConfig{
		AutoDownload: &autoDownload,
		AutoApply:    &autoApply,
	}}}
	manager := New(store, Options{
		Root:           t.TempDir(),
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return "1.1.9" },
		Container:      &container,
	})
	if err := manager.writeMarker(markerFailed, tag); err != nil {
		t.Fatalf("record failed candidate: %v", err)
	}
	manager.checkAndMaybeStage(context.Background(), manager.Settings())
	status := manager.Status()
	if status.PendingTag != "" || status.FailedTag != tag {
		t.Fatalf("automatic retry must remain quarantined: %#v", status)
	}
	if _, err := manager.Apply(tag); err != nil {
		t.Fatalf("manual retry should clear the quarantine: %v", err)
	}
	status = manager.Status()
	if status.PendingTag != tag || status.FailedTag != "" {
		t.Fatalf("manual retry did not reset candidate state: %#v", status)
	}
}

func TestCandidatePromotionSnapshotsRollbackMarkerUntilCommit(t *testing.T) {
	const tag = "v1.2.0"
	archive := releaseArchive(t, tag, "amd64", nil)
	checksum := sha256.Sum256(archive)
	server := releaseServer(t, tag, "amd64", archive, hex.EncodeToString(checksum[:]))
	defer server.Close()

	container := true
	currentVersion := "1.1.9"
	root := t.TempDir()
	manager := New(nil, Options{
		Root:           root,
		GitHubAPI:      server.URL,
		HTTPClient:     server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		CurrentVersion: func() string { return currentVersion },
		Container:      &container,
	})
	if _, err := manager.Download(context.Background(), tag); err != nil {
		t.Fatalf("download release: %v", err)
	}
	if err := manager.writeMarker(markerPrevious, "v1.0.0"); err != nil {
		t.Fatalf("seed rollback marker: %v", err)
	}
	if _, err := manager.Apply(tag); err != nil {
		t.Fatalf("apply release: %v", err)
	}

	if previous, err := manager.readMarker(markerPrevious); err != nil || previous != "v1.0.0" {
		t.Fatalf("rollback marker changed before candidate confirmation: previous=%q err=%v", previous, err)
	}
	backup, err := os.ReadFile(filepath.Join(root, markerPendingRollbackPrevious))
	if err != nil || strings.TrimSpace(string(backup)) != "v1.0.0" {
		t.Fatalf("pending rollback snapshot = %q err=%v, want v1.0.0", backup, err)
	}

	t.Setenv("DEEPSEEK_WEB_TO_API_SELF_UPDATE_ACTIVE_VERSION", tag)
	currentVersion = "1.2.0"
	if err := manager.ConfirmStartup(); err != nil {
		t.Fatalf("confirm candidate startup: %v", err)
	}
	if previous, err := manager.readMarker(markerPrevious); err != nil || previous != "v1.1.9" {
		t.Fatalf("rollback marker after promotion = %q err=%v, want v1.1.9", previous, err)
	}
	if _, err := os.Stat(filepath.Join(root, markerPendingRollbackPrevious)); !os.IsNotExist(err) {
		t.Fatalf("pending rollback snapshot was not cleared after promotion: %v", err)
	}
}

type memoryStore struct {
	mu  sync.Mutex
	cfg config.Config
}

func (s *memoryStore) Snapshot() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

func (s *memoryStore) Update(mutator func(*config.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return mutator(&s.cfg)
}

func releaseServer(t *testing.T, tag, arch string, archive []byte, checksum string) *httptest.Server {
	t.Helper()
	archiveName := "deepseek-web-to-api_" + tag + "_linux_" + arch + ".tar.gz"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/chm413/DeepSeek_Web_To_API/releases/latest":
			payload := map[string]any{
				"tag_name":     tag,
				"html_url":     server.URL + "/release/" + tag,
				"published_at": "2026-08-17T12:00:00Z",
				"assets": []map[string]string{
					{"name": archiveName, "browser_download_url": server.URL + "/assets/release.tar.gz"},
					{"name": "sha256sums.txt", "browser_download_url": server.URL + "/assets/sha256sums.txt"},
				},
			}
			_ = json.NewEncoder(w).Encode(payload)
		case "/assets/release.tar.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archive)
		case "/assets/sha256sums.txt":
			_, _ = fmt.Fprintf(w, "%s  %s\n", checksum, archiveName)
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func releaseArchive(t *testing.T, tag, arch string, extra map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := "deepseek-web-to-api_" + tag + "_linux_" + arch + "/"
	// GNU tar writes this directory header for the release layout produced by
	// scripts/build-release-archives.sh.
	if err := tarWriter.WriteHeader(&tar.Header{Name: prefix, Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("write release root directory: %v", err)
	}
	write := func(name string, data []byte, mode int64) {
		t.Helper()
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write archive header: %v", err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatalf("write archive data: %v", err)
		}
	}
	write(prefix+"README.MD", []byte("# DeepSeek Web To API\n"), 0o644)
	write(prefix+"README.en.md", []byte("# DeepSeek Web To API\n"), 0o644)
	write(prefix+".env.example", []byte("PORT=5000\n"), 0o640)
	write(prefix+"LICENSE", []byte("license\n"), 0o644)
	write(prefix+"deepseek-web-to-api", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	write(prefix+"static/admin/index.html", []byte("<!doctype html>"), 0o644)
	write(prefix+"config.example.json", []byte("{}"), 0o640)
	for name, data := range extra {
		write(prefix+name, data, 0o644)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar archive: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip archive: %v", err)
	}
	return buffer.Bytes()
}

func releaseArchiveWithBareTopLevelDirectory(t *testing.T, tag, arch string) []byte {
	t.Helper()
	return releaseArchiveWithRootHeader(t, tag, arch, tar.TypeDir, nil)
}

func releaseArchiveWithBareRootFile(t *testing.T, tag, arch string) []byte {
	t.Helper()
	return releaseArchiveWithRootHeader(t, tag, arch, tar.TypeReg, []byte("not a directory"))
}

func releaseArchiveWithRootHeader(t *testing.T, tag, arch string, rootType byte, rootData []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := "deepseek-web-to-api_" + tag + "_linux_" + arch + "/"
	root := strings.TrimSuffix(prefix, "/")
	if err := tarWriter.WriteHeader(&tar.Header{Name: root, Mode: 0o755, Size: int64(len(rootData)), Typeflag: rootType}); err != nil {
		t.Fatalf("write root archive header: %v", err)
	}
	if len(rootData) > 0 {
		if _, err := tarWriter.Write(rootData); err != nil {
			t.Fatalf("write root archive data: %v", err)
		}
	}
	if rootType == tar.TypeDir {
		write := func(name string, data []byte, mode int64) {
			t.Helper()
			if err := tarWriter.WriteHeader(&tar.Header{Name: prefix + name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatalf("write archive header: %v", err)
			}
			if _, err := tarWriter.Write(data); err != nil {
				t.Fatalf("write archive data: %v", err)
			}
		}
		write("deepseek-web-to-api", []byte("#!/bin/sh\nexit 0\n"), 0o755)
		write("static/admin/index.html", []byte("<!doctype html>"), 0o644)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar archive: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip archive: %v", err)
	}
	return buffer.Bytes()
}
