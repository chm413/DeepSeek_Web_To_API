package xrayproxy

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	xrayLatestReleaseAPI = "https://api.github.com/repos/XTLS/Xray-core/releases/latest"
	xrayReleaseAPIBase   = "https://api.github.com/repos/XTLS/Xray-core/releases/tags/"
	maxXrayArchiveBytes  = 160 << 20
	maxXrayExpandedBytes = 256 << 20
)

var xrayDownloadMu sync.Mutex

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type releaseMetadata struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

func ResolveOrDownload(ctx context.Context, settings Settings) (string, error) {
	if resolved, err := ResolveBinary(settings.BinaryPath); err == nil {
		return resolved, nil
	} else if !settings.AutoDownload {
		return "", err
	}
	downloadDir := effectiveDownloadDir(settings)
	if resolved, ok := executableFile(filepath.Join(downloadDir, xrayExecutableName())); ok {
		if !explicitXrayVersionNeedsRefresh(settings.DownloadVersion, installedVersion(downloadDir)) {
			PersistInstalledCore(settings, resolved)
			return resolved, nil
		}
	}
	resolved, err := DownloadCore(ctx, settings, false)
	if err != nil {
		return "", err
	}
	PersistInstalledCore(settings, resolved)
	return resolved, nil
}

func DownloadCore(ctx context.Context, settings Settings, force bool) (string, error) {
	xrayDownloadMu.Lock()
	defer xrayDownloadMu.Unlock()
	downloadDir := effectiveDownloadDir(settings)
	targetBinary := filepath.Join(downloadDir, xrayExecutableName())
	if !force {
		if resolved, ok := executableFile(targetBinary); ok {
			if !explicitXrayVersionNeedsRefresh(settings.DownloadVersion, installedVersion(downloadDir)) {
				return resolved, nil
			}
		}
	}
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		return "", fmt.Errorf("create xray download directory: %w", err)
	}
	metadata, err := fetchReleaseMetadata(ctx, strings.TrimSpace(settings.DownloadVersion))
	if err != nil {
		return "", err
	}
	assetName, err := xrayAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	assetURL := ""
	assetDigest := ""
	digestURL := ""
	for _, asset := range metadata.Assets {
		if asset.Name == assetName {
			assetURL = asset.BrowserDownloadURL
			assetDigest = asset.Digest
			continue
		}
		if asset.Name == assetName+".dgst" || asset.Name == assetName+".sha256" || asset.Name == assetName+".sha256sum" {
			digestURL = asset.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("xray release %s does not contain %s", metadata.TagName, assetName)
	}
	expectedDigest, digestErr := normalizeXrayDigest(assetDigest)
	if digestErr != nil && digestURL == "" {
		return "", fmt.Errorf("xray release %s has no usable SHA-256 digest for %s: %w", metadata.TagName, assetName, digestErr)
	}
	if digestErr != nil {
		expectedDigest, err = fetchXrayDigest(ctx, digestURL)
		if err != nil {
			return "", fmt.Errorf("fetch xray checksum sidecar: %w", err)
		}
	}
	archivePath, err := downloadArchive(ctx, downloadDir, assetURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archivePath) }()
	actualDigest, err := hashFile(archivePath)
	if err != nil {
		return "", fmt.Errorf("hash downloaded xray archive: %w", err)
	}
	if actualDigest != expectedDigest {
		return "", fmt.Errorf("xray archive checksum mismatch: expected %s, got %s", expectedDigest, actualDigest)
	}
	if err := extractXrayArchive(archivePath, downloadDir); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(downloadDir, ".version"), []byte(metadata.TagName+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write xray version marker: %w", err)
	}
	resolved, ok := executableFile(targetBinary)
	if !ok {
		return "", errors.New("downloaded xray executable is missing")
	}
	return resolved, nil
}

func explicitXrayVersionNeedsRefresh(requested, installed string) bool {
	requested = strings.TrimSpace(requested)
	installed = strings.TrimSpace(installed)
	if requested == "" || strings.EqualFold(requested, "latest") {
		return false
	}
	if installed == "" {
		return true
	}
	if !strings.HasPrefix(strings.ToLower(requested), "v") {
		requested = "v" + requested
	}
	// DownloadVersion is an exact operator pin, not a minimum version. This
	// includes downgrades: leaving an older vulnerable/newer incompatible core
	// in place would silently ignore the configured release.
	installed = strings.TrimPrefix(strings.ToLower(installed), "v")
	requested = strings.TrimPrefix(strings.ToLower(requested), "v")
	return installed != requested
}

func normalizeXrayDigest(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if strings.HasPrefix(raw, "sha256:") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "sha256:"))
	}
	if len(raw) != sha256.Size*2 {
		return "", errors.New("metadata digest is missing or not SHA-256")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return "", errors.New("metadata digest is not hexadecimal")
	}
	return raw, nil
}

func fetchXrayDigest(ctx context.Context, digestURL string) (string, error) {
	if strings.TrimSpace(digestURL) == "" {
		return "", errors.New("checksum sidecar URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, digestURL, nil)
	if err != nil {
		return "", fmt.Errorf("create checksum sidecar request: %w", err)
	}
	req.Header.Set("User-Agent", "DeepSeek-Web-To-API")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download checksum sidecar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum sidecar returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read checksum sidecar: %w", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		for _, prefix := range []string{"SHA2-256=", "SHA256=", "SHA-256="} {
			if strings.HasPrefix(upper, prefix) {
				return normalizeXrayDigest(strings.TrimSpace(line[len(prefix):]))
			}
		}
	}
	return "", errors.New("checksum sidecar does not contain SHA-256")
}

func hashFile(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func effectiveDownloadDir(settings Settings) string {
	if value := strings.TrimSpace(os.ExpandEnv(settings.DownloadDir)); value != "" {
		return absoluteDownloadDir(value)
	}
	if value := strings.TrimSpace(os.ExpandEnv(settings.BinaryPath)); value != "" {
		return absoluteDownloadDir(filepath.Dir(value))
	}
	return absoluteDownloadDir(filepath.Join("data", "xray"))
}

func absoluteDownloadDir(value string) string {
	value = filepath.Clean(value)
	if absolute, err := filepath.Abs(value); err == nil {
		return absolute
	}
	return value
}

// PersistInstalledCore records an automatically managed Xray installation
// through the optional settings callback. Persistence failures must not turn a
// working local core into an unavailable proxy route, so they are logged.
func PersistInstalledCore(settings Settings, binaryPath string) {
	callback := settings.PersistInstallation
	if callback == nil {
		return
	}
	installation, ok := managedInstallation(settings, binaryPath)
	if !ok {
		return
	}
	if err := callback(installation); err != nil {
		slog.Warn("[xray_proxy] persist managed core installation failed", "error", err)
	}
}

func managedInstallation(settings Settings, binaryPath string) (Installation, bool) {
	binaryPath, ok := executableFile(binaryPath)
	if !ok {
		return Installation{}, false
	}
	downloadDir := effectiveDownloadDir(settings)
	expected := filepath.Join(downloadDir, xrayExecutableName())
	if !sameLocalPath(binaryPath, expected) {
		return Installation{}, false
	}
	return Installation{
		BinaryPath:  binaryPath,
		DownloadDir: downloadDir,
		Version:     installedVersion(downloadDir),
	}, true
}

func installedVersion(downloadDir string) string {
	file, err := os.Open(filepath.Join(downloadDir, ".version"))
	if err != nil {
		return ""
	}
	content, readErr := io.ReadAll(io.LimitReader(file, 257))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(content) > 256 {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func sameLocalPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func fetchReleaseMetadata(ctx context.Context, version string) (releaseMetadata, error) {
	endpoint := xrayLatestReleaseAPI
	if version != "" && !strings.EqualFold(version, "latest") {
		version = strings.TrimSpace(version)
		if !strings.HasPrefix(strings.ToLower(version), "v") {
			version = "v" + version
		}
		endpoint = xrayReleaseAPIBase + version
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("create xray release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "DeepSeek-Web-To-API")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return releaseMetadata{}, fmt.Errorf("fetch xray release metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return releaseMetadata{}, fmt.Errorf("fetch xray release metadata: HTTP %d", resp.StatusCode)
	}
	var metadata releaseMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&metadata); err != nil {
		return releaseMetadata{}, fmt.Errorf("decode xray release metadata: %w", err)
	}
	if strings.TrimSpace(metadata.TagName) == "" {
		return releaseMetadata{}, errors.New("xray release metadata has no tag")
	}
	return metadata, nil
}

func downloadArchive(ctx context.Context, dir, assetURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return "", fmt.Errorf("create xray download request: %w", err)
	}
	req.Header.Set("User-Agent", "DeepSeek-Web-To-API")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download xray archive: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download xray archive: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, ".xray-*.zip")
	if err != nil {
		return "", fmt.Errorf("create xray archive file: %w", err)
	}
	path := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxXrayArchiveBytes+1))
	if err != nil {
		return "", fmt.Errorf("save xray archive: %w", err)
	}
	if written > maxXrayArchiveBytes {
		return "", errors.New("xray archive exceeds size limit")
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close xray archive: %w", err)
	}
	ok = true
	return path, nil
}

func extractXrayArchive(archivePath, targetDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open xray archive: %w", err)
	}
	defer func() { _ = reader.Close() }()
	stage, err := os.MkdirTemp(targetDir, ".xray-extract-*")
	if err != nil {
		return fmt.Errorf("create xray extraction directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	wanted := map[string]bool{
		strings.ToLower(xrayExecutableName()): false,
		"geoip.dat":                           false,
		"geosite.dat":                         false,
	}
	var expandedBytes uint64
	for _, file := range reader.File {
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(file.Name, "\\", "/")))
		if _, exists := wanted[name]; !exists || file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > maxXrayArchiveBytes {
			return fmt.Errorf("xray archive entry %s exceeds size limit", name)
		}
		if expandedBytes > uint64(maxXrayExpandedBytes)-file.UncompressedSize64 {
			return errors.New("xray archive expanded contents exceed size limit")
		}
		expandedBytes += file.UncompressedSize64
		input, err := file.Open()
		if err != nil {
			return fmt.Errorf("open xray archive entry %s: %w", name, err)
		}
		target := filepath.Join(stage, filepath.Base(file.Name))
		tmp, err := os.CreateTemp(stage, ".extract-*")
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("create xray extracted file: %w", err)
		}
		written, copyErr := io.Copy(tmp, io.LimitReader(input, maxXrayArchiveBytes+1))
		closeErr := input.Close()
		tmpCloseErr := tmp.Close()
		if copyErr != nil || closeErr != nil || tmpCloseErr != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("extract xray archive entry %s", name)
		}
		// Do not trust the ZIP header's uncompressed size: a crafted archive
		// can declare a small value and expand much further while being read.
		// The actual copy must obey the same per-entry cap as the declared size.
		if written > maxXrayArchiveBytes {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("xray archive entry %s exceeds size limit", name)
		}
		mode := os.FileMode(0o600)
		if name == strings.ToLower(xrayExecutableName()) {
			mode = 0o700
		}
		if err := os.Chmod(tmp.Name(), mode); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("set xray file permissions: %w", err)
		}
		if err := os.Rename(tmp.Name(), target); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("stage xray file %s: %w", name, err)
		}
		wanted[name] = true
	}
	for name, found := range wanted {
		if !found {
			return fmt.Errorf("xray archive is missing %s", name)
		}
	}
	// Only touch the active installation after every required entry has been
	// fully read, permissioned, and validated in the private staging directory.
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	return commitStagedFiles(stage, targetDir, names)
}

type stagedFileReplacement struct {
	target string
	backup string
	hadOld bool
}

func commitStagedFiles(stage, targetDir string, names []string) error {
	replacements := make([]stagedFileReplacement, 0, len(names))
	rollback := func() {
		for i := len(replacements) - 1; i >= 0; i-- {
			item := replacements[i]
			_ = os.Remove(item.target)
			if item.hadOld {
				_ = os.Rename(item.backup, item.target)
			}
		}
		for _, item := range replacements {
			if item.backup != "" {
				_ = os.Remove(item.backup)
			}
		}
	}
	for _, name := range names {
		source := filepath.Join(stage, name)
		target := filepath.Join(targetDir, name)
		item := stagedFileReplacement{target: target}
		if _, err := os.Lstat(target); err == nil {
			backupFile, err := os.CreateTemp(targetDir, ".xray-backup-*")
			if err != nil {
				rollback()
				return fmt.Errorf("prepare xray file %s replacement: %w", name, err)
			}
			item.backup = backupFile.Name()
			if err := backupFile.Close(); err != nil {
				_ = os.Remove(item.backup)
				rollback()
				return fmt.Errorf("prepare xray file %s replacement: %w", name, err)
			}
			if err := os.Remove(item.backup); err != nil {
				rollback()
				return fmt.Errorf("prepare xray file %s replacement: %w", name, err)
			}
			if err := os.Rename(target, item.backup); err != nil {
				rollback()
				return fmt.Errorf("preserve xray file %s: %w", name, err)
			}
			item.hadOld = true
		} else if !errors.Is(err, os.ErrNotExist) {
			rollback()
			return fmt.Errorf("inspect xray file %s: %w", name, err)
		}
		if err := os.Rename(source, target); err != nil {
			if item.hadOld {
				_ = os.Rename(item.backup, target)
			}
			rollback()
			return fmt.Errorf("install xray file %s: %w", name, err)
		}
		replacements = append(replacements, item)
	}
	for _, item := range replacements {
		if item.backup != "" {
			_ = os.Remove(item.backup)
		}
	}
	return nil
}

func xrayAssetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "Xray-windows-64.zip", nil
	case "windows/arm64":
		return "Xray-windows-arm64-v8a.zip", nil
	case "linux/amd64":
		return "Xray-linux-64.zip", nil
	case "linux/arm64":
		return "Xray-linux-arm64-v8a.zip", nil
	case "darwin/amd64":
		return "Xray-macos-64.zip", nil
	case "darwin/arm64":
		return "Xray-macos-arm64-v8a.zip", nil
	default:
		return "", fmt.Errorf("automatic Xray download is unsupported on %s/%s", goos, goarch)
	}
}
