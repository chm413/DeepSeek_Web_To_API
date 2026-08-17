package xrayproxy

import (
	"archive/zip"
	"context"
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
)

var xrayDownloadMu sync.Mutex

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
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
		PersistInstalledCore(settings, resolved)
		return resolved, nil
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
			return resolved, nil
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
	for _, asset := range metadata.Assets {
		if asset.Name == assetName {
			assetURL = asset.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("xray release %s does not contain %s", metadata.TagName, assetName)
	}
	archivePath, err := downloadArchive(ctx, downloadDir, assetURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archivePath) }()
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
	wanted := map[string]bool{
		strings.ToLower(xrayExecutableName()): false,
		"geoip.dat":                           false,
		"geosite.dat":                         false,
	}
	for _, file := range reader.File {
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(file.Name, "\\", "/")))
		if _, exists := wanted[name]; !exists || file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > maxXrayArchiveBytes {
			return fmt.Errorf("xray archive entry %s exceeds size limit", name)
		}
		input, err := file.Open()
		if err != nil {
			return fmt.Errorf("open xray archive entry %s: %w", name, err)
		}
		target := filepath.Join(targetDir, filepath.Base(file.Name))
		tmp, err := os.CreateTemp(targetDir, ".extract-*")
		if err != nil {
			_ = input.Close()
			return fmt.Errorf("create xray extracted file: %w", err)
		}
		_, copyErr := io.Copy(tmp, io.LimitReader(input, maxXrayArchiveBytes+1))
		closeErr := input.Close()
		tmpCloseErr := tmp.Close()
		if copyErr != nil || closeErr != nil || tmpCloseErr != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("extract xray archive entry %s", name)
		}
		mode := os.FileMode(0o600)
		if name == strings.ToLower(xrayExecutableName()) {
			mode = 0o700
		}
		if err := os.Chmod(tmp.Name(), mode); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("set xray file permissions: %w", err)
		}
		if err := replaceFile(tmp.Name(), target); err != nil {
			return err
		}
		wanted[name] = true
	}
	for name, found := range wanted {
		if !found {
			return fmt.Errorf("xray archive is missing %s", name)
		}
	}
	return nil
}

func replaceFile(source, target string) error {
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(source)
		return fmt.Errorf("replace xray file %s: %w", filepath.Base(target), err)
	}
	if err := os.Rename(source, target); err != nil {
		_ = os.Remove(source)
		return fmt.Errorf("install xray file %s: %w", filepath.Base(target), err)
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
