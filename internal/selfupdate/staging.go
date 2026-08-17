package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/version"
)

const (
	markerCurrent         = "current.version"
	markerPrevious        = "previous.version"
	markerPending         = "pending.version"
	markerPendingPrevious = "pending.previous.version"
	// markerPendingRollbackPrevious preserves the old rollback pointer while a
	// candidate is being confirmed. It is deliberately not a normal version
	// marker because the literal "none" represents an absent previous marker.
	markerPendingRollbackPrevious = "pending.rollback.previous.version"
	markerAttempt                 = "pending.attempted"
	markerFailed                  = "failed.version"
	metadataFile                  = ".verified.json"
	defaultArchiveEntries         = 10000
	// legacyTarRegularType is the NUL wire value that old tar writers use for
	// regular files. archive/tar's TypeRegA name is deprecated, but staged
	// release archives must remain compatible with that representation.
	legacyTarRegularType byte = 0
)

type verifiedMetadata struct {
	Tag       string    `json:"tag"`
	AssetName string    `json:"asset_name"`
	SHA256    string    `json:"sha256"`
	StagedAt  time.Time `json:"staged_at"`
}

func (m *Manager) Download(ctx context.Context, requestedTag string) (*Release, error) {
	if m == nil {
		return nil, errors.New("self-update manager is unavailable")
	}
	if !m.container {
		return nil, ErrContainerRequired
	}
	release, err := m.resolveRelease(ctx, requestedTag)
	if err != nil {
		m.setLastError(err)
		return nil, err
	}
	if !m.beginDownload() {
		return nil, ErrOperationInProgress
	}
	defer m.finishDownload()
	if !release.Downloadable {
		err := fmt.Errorf("release %s has no verified linux/%s update asset", release.Tag, m.goarch)
		m.setLastError(err)
		return nil, err
	}
	if err := m.ensureRoot(); err != nil {
		m.setLastError(err)
		return nil, err
	}
	if staged, err := m.stagedRelease(release.Tag); err == nil && staged {
		config.Logger.Info("[self_update] verified release already staged", "tag", release.Tag)
		return cloneRelease(release), nil
	}

	checksum, err := m.fetchChecksum(ctx, release.ChecksumURL, release.AssetName)
	if err != nil {
		m.setLastError(err)
		return nil, err
	}
	archivePath, actual, err := m.downloadArchive(ctx, release.AssetURL)
	if err != nil {
		m.setLastError(err)
		return nil, err
	}
	defer removeFileForCleanup(archivePath, "downloaded release archive")
	if actual != checksum {
		err := fmt.Errorf("release checksum mismatch for %s", release.AssetName)
		m.setLastError(err)
		return nil, err
	}
	if err := m.extractVerifiedArchive(archivePath, release, actual); err != nil {
		m.setLastError(err)
		return nil, err
	}
	m.mu.Lock()
	m.lastError = ""
	m.mu.Unlock()
	config.Logger.Info("[self_update] verified release staged", "tag", release.Tag, "asset", release.AssetName, "sha256", actual)
	return cloneRelease(release), nil
}

func (m *Manager) Apply(requestedTag string) (Status, error) {
	if m == nil {
		return Status{}, errors.New("self-update manager is unavailable")
	}
	if !m.container {
		return Status{}, ErrContainerRequired
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	tag, err := canonicalStableTag(requestedTag)
	if err != nil {
		return Status{}, err
	}
	if version.Compare(tag, m.currentVersion()) <= 0 {
		return Status{}, fmt.Errorf("release %s is not newer than the running version", tag)
	}
	staged, err := m.stagedRelease(tag)
	if err != nil {
		return Status{}, err
	}
	if !staged {
		return Status{}, ErrUpdateNotDownloaded
	}
	if pending, pendingErr := m.readMarker(markerPending); pendingErr == nil && pending != "" {
		return Status{}, ErrOperationInProgress
	} else if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return Status{}, pendingErr
	}
	// Capture the version that is actually running. current.version may point
	// at an older persistent release after a newer immutable image was deployed.
	// The captured value is committed to previous.version only after the
	// candidate has passed ConfirmStartup.
	previous := ""
	if active, activeErr := canonicalStableTag(version.Tag(m.currentVersion())); activeErr == nil {
		previous = active
	}
	if err := m.writePendingRollbackPrevious(); err != nil {
		return Status{}, fmt.Errorf("preserve prior rollback release: %w", err)
	}
	if previous != "" {
		if err := m.writeMarker(markerPendingPrevious, previous); err != nil {
			removeFileForCleanup(filepath.Join(m.root, markerPendingRollbackPrevious), "pending rollback marker")
			return Status{}, fmt.Errorf("preserve pending previous release: %w", err)
		}
	} else if err := os.Remove(filepath.Join(m.root, markerPendingPrevious)); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeFileForCleanup(filepath.Join(m.root, markerPendingRollbackPrevious), "pending rollback marker")
		return Status{}, fmt.Errorf("clear pending previous release: %w", err)
	}
	if err := os.Remove(filepath.Join(m.root, markerAttempt)); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeFileForCleanup(filepath.Join(m.root, markerPendingPrevious), "pending previous marker")
		removeFileForCleanup(filepath.Join(m.root, markerPendingRollbackPrevious), "pending rollback marker")
		return Status{}, fmt.Errorf("clear previous pending attempt: %w", err)
	}
	if err := m.writeMarker(markerPending, tag); err != nil {
		removeFileForCleanup(filepath.Join(m.root, markerPendingPrevious), "pending previous marker")
		removeFileForCleanup(filepath.Join(m.root, markerPendingRollbackPrevious), "pending rollback marker")
		return Status{}, fmt.Errorf("mark pending release: %w", err)
	}
	// A successful pending transaction is the only point at which a manual
	// retry may clear a failed-version quarantine. If this fails, remove the
	// pending transaction and leave the quarantine intact for automatic checks.
	if err := os.Remove(filepath.Join(m.root, markerFailed)); err != nil && !errors.Is(err, os.ErrNotExist) {
		removeFileForCleanup(filepath.Join(m.root, markerPending), "pending marker")
		removeFileForCleanup(filepath.Join(m.root, markerPendingPrevious), "pending previous marker")
		removeFileForCleanup(filepath.Join(m.root, markerPendingRollbackPrevious), "pending rollback marker")
		return Status{}, fmt.Errorf("clear failed release marker: %w", err)
	}
	config.Logger.Info("[self_update] release pending restart", "tag", tag, "previous", previous)
	return m.Status(), nil
}

func (m *Manager) Rollback() (Status, error) {
	if m == nil {
		return Status{}, errors.New("self-update manager is unavailable")
	}
	if !m.container {
		return Status{}, ErrContainerRequired
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	previous, err := m.readMarker(markerPrevious)
	if err != nil || previous == "" {
		if err == nil {
			err = ErrUpdateNotDownloaded
		}
		return Status{}, fmt.Errorf("no previous release is available: %w", err)
	}
	staged, stagedErr := m.stagedRelease(previous)
	useImmutableFallback := !staged && (errors.Is(stagedErr, os.ErrNotExist) || stagedErr != nil)
	if stagedErr != nil && !errors.Is(stagedErr, os.ErrNotExist) {
		config.Logger.Warn("[self_update] previous staged release is unavailable; rolling back to immutable image", "tag", previous, "error", stagedErr)
	}
	current, err := m.readMarker(markerCurrent)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	if useImmutableFallback {
		if err := os.Remove(filepath.Join(m.root, markerCurrent)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Status{}, err
		}
	} else if err := m.writeMarker(markerCurrent, previous); err != nil {
		return Status{}, err
	}
	if current != "" && current != previous {
		if err := m.writeMarker(markerPrevious, current); err != nil {
			return Status{}, err
		}
	}
	removeFileForCleanup(filepath.Join(m.root, markerPending), "pending marker")
	removeFileForCleanup(filepath.Join(m.root, markerPendingPrevious), "pending previous marker")
	removeFileForCleanup(filepath.Join(m.root, markerPendingRollbackPrevious), "pending rollback marker")
	removeFileForCleanup(filepath.Join(m.root, markerAttempt), "pending attempt marker")
	removeFileForCleanup(filepath.Join(m.root, markerFailed), "failed release marker")
	config.Logger.Warn("[self_update] rollback scheduled", "target", previous, "previous", current, "immutable_fallback", useImmutableFallback)
	return m.Status(), nil
}

// ConfirmStartup promotes a pending candidate only after the new process has
// bound its listening socket. The entrypoint keeps the old current marker
// intact until this confirmation, so a candidate that dies during startup is
// rolled back by the entrypoint before it starts the previous release.
func (m *Manager) ConfirmStartup() error {
	if m == nil || !m.container {
		return nil
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	active := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_SELF_UPDATE_ACTIVE_VERSION"))
	if active == "" {
		return nil
	}
	tag, err := canonicalStableTag(active)
	if err != nil {
		return fmt.Errorf("invalid active self-update version: %w", err)
	}
	pending, err := m.readMarker(markerPending)
	if err != nil || pending != tag {
		return nil
	}
	if version.Tag(m.currentVersion()) != tag {
		return fmt.Errorf("candidate binary version %s does not match pending release %s", version.Tag(m.currentVersion()), tag)
	}
	staged, err := m.stagedRelease(tag)
	if err != nil || !staged {
		if err == nil {
			err = ErrUpdateNotDownloaded
		}
		return err
	}
	previous, previousErr := m.readMarker(markerPendingPrevious)
	if errors.Is(previousErr, os.ErrNotExist) {
		// Compatibility with a pending marker written by the earlier updater:
		// its current marker still identifies the release that was running.
		previous, previousErr = m.readMarker(markerCurrent)
	}
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return previousErr
	}
	if err := m.writeMarker(markerCurrent, tag); err != nil {
		return err
	}
	if previous != "" && previous != tag {
		if err := m.writeMarker(markerPrevious, previous); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(m.root, markerFailed)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Removing pending.version commits the promotion. Until this succeeds the
	// entrypoint can restore both current.version and previous.version from the
	// pending transaction if this process dies partway through confirmation.
	if err := os.Remove(filepath.Join(m.root, markerPending)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// These are post-commit housekeeping files. A stale sidecar is inert unless
	// pending.version exists, so do not turn a healthy started candidate into a
	// failed process solely because cleanup encountered an I/O error.
	if err := os.Remove(filepath.Join(m.root, markerPendingPrevious)); err != nil && !errors.Is(err, os.ErrNotExist) {
		config.Logger.Warn("[self_update] clear pending previous marker after promotion failed", "error", err)
	}
	if err := os.Remove(filepath.Join(m.root, markerPendingRollbackPrevious)); err != nil && !errors.Is(err, os.ErrNotExist) {
		config.Logger.Warn("[self_update] clear pending rollback marker after promotion failed", "error", err)
	}
	if err := os.Remove(filepath.Join(m.root, markerAttempt)); err != nil && !errors.Is(err, os.ErrNotExist) {
		config.Logger.Warn("[self_update] clear pending attempt marker after promotion failed", "error", err)
	}
	config.Logger.Info("[self_update] candidate promoted after startup", "tag", tag, "previous", previous)
	return nil
}

func (m *Manager) ScheduleRestart(delay time.Duration) error {
	if m == nil || !m.container || m.restartCh == nil {
		return ErrRestartNotConfigured
	}
	if delay < 0 {
		delay = 0
	}
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		select {
		case m.restartCh <- struct{}{}:
			config.Logger.Info("[self_update] graceful restart requested", "exit_code", m.restartExitCode)
		default:
		}
	}()
	return nil
}

func (m *Manager) resolveRelease(ctx context.Context, requestedTag string) (*Release, error) {
	requestedTag = strings.TrimSpace(requestedTag)
	m.mu.RLock()
	available := cloneRelease(m.available)
	m.mu.RUnlock()
	if available == nil || (requestedTag != "" && !sameStableTag(available.Tag, requestedTag)) {
		checked, err := m.Check(ctx)
		if err != nil {
			return nil, err
		}
		available = checked
	}
	if available == nil {
		return nil, ErrNoUpdateAvailable
	}
	if version.Compare(available.Tag, m.currentVersion()) <= 0 {
		return nil, ErrNoUpdateAvailable
	}
	if requestedTag != "" && !sameStableTag(available.Tag, requestedTag) {
		return nil, fmt.Errorf("requested release %s is not the latest available release %s", requestedTag, available.Tag)
	}
	return available, nil
}

func (m *Manager) beginDownload() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checking || m.downloading {
		return false
	}
	m.downloading = true
	return true
}

func (m *Manager) finishDownload() {
	m.mu.Lock()
	m.downloading = false
	m.mu.Unlock()
}

func (m *Manager) ensureRoot() error {
	if err := os.MkdirAll(m.root, 0o750); err != nil {
		return fmt.Errorf("create self-update directory: %w", err)
	}
	return nil
}

func (m *Manager) fetchChecksum(ctx context.Context, rawURL, expectedName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create checksum request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "deepseek-web-to-api-self-update")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download checksum manifest: %w", err)
	}
	defer closeResource(resp.Body, "checksum response body")
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum manifest returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, defaultManifestLimit+1))
	if err != nil {
		return "", fmt.Errorf("read checksum manifest: %w", err)
	}
	if len(body) > defaultManifestLimit {
		return "", errors.New("checksum manifest exceeds size limit")
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(strings.TrimSpace(fields[len(fields)-1]), "*")
		if name != expectedName {
			continue
		}
		sum := strings.ToLower(strings.TrimSpace(fields[0]))
		if len(sum) != sha256.Size*2 {
			return "", fmt.Errorf("invalid SHA-256 for %s", expectedName)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", fmt.Errorf("invalid SHA-256 for %s", expectedName)
		}
		return sum, nil
	}
	return "", fmt.Errorf("checksum manifest does not contain %s", expectedName)
}

func (m *Manager) downloadArchive(ctx context.Context, rawURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create archive request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "deepseek-web-to-api-self-update")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("download release archive: %w", err)
	}
	defer closeResource(resp.Body, "release archive response body")
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release archive returned HTTP %d", resp.StatusCode)
	}
	file, err := os.CreateTemp(m.root, ".download-*.tar.gz")
	if err != nil {
		return "", "", fmt.Errorf("create download file: %w", err)
	}
	filePath := file.Name()
	keepFile := false
	fileOpen := true
	defer func() {
		if !keepFile {
			if fileOpen {
				closeResource(file, "downloaded release archive")
			}
			removeFileForCleanup(filePath, "downloaded release archive")
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, defaultArchiveLimit+1))
	closeErr := file.Close()
	fileOpen = false
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", fmt.Errorf("write release archive: %w", err)
	}
	if written > defaultArchiveLimit {
		return "", "", errors.New("release archive exceeds size limit")
	}
	keepFile = true
	return filePath, hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *Manager) extractVerifiedArchive(archivePath string, release *Release, checksum string) error {
	if release == nil {
		return ErrNoUpdateAvailable
	}
	stage, err := os.MkdirTemp(m.root, ".stage-"+strings.TrimPrefix(release.Tag, "v")+"-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	keepStage := false
	defer func() {
		if !keepStage {
			removeAllForCleanup(stage, "temporary staging directory")
		}
	}()
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer closeResource(file, "release archive")
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open compressed archive: %w", err)
	}
	defer closeResource(gzipReader, "compressed release archive")

	archiveTag := strings.TrimSpace(release.ArchiveTag)
	if archiveTag == "" {
		archiveTag = release.Tag
	}
	prefix := fmt.Sprintf("deepseek-web-to-api_%s_linux_%s/", archiveTag, m.goarch)
	if strings.TrimSpace(archiveTag) == "" {
		return ErrInvalidRelease
	}
	reader := tar.NewReader(gzipReader)
	var total int64
	entries := 0
	hasBinary := false
	hasStatic := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		entries++
		if entries > defaultArchiveEntries {
			return errors.New("release archive contains too many entries")
		}
		if header.Size < 0 {
			return errors.New("release archive contains an invalid file size")
		}
		total += header.Size
		if total > defaultArchiveLimit {
			return errors.New("release archive expands beyond size limit")
		}
		name, err := cleanArchiveName(header.Name)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(name, prefix) {
			return errors.New("release archive contains an unexpected top-level path")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, legacyTarRegularType:
		default:
			return errors.New("release archive contains an unsupported link or special file")
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		if rel == "deepseek-web-to-api" {
			if err := extractFile(reader, filepath.Join(stage, "deepseek-web-to-api"), header.Size, 0o750); err != nil {
				return err
			}
			hasBinary = true
			continue
		}
		if strings.HasPrefix(rel, "static/admin/") {
			staticRel := strings.TrimPrefix(rel, "static/admin/")
			if staticRel == "" {
				continue
			}
			destination, err := safeStagePath(stage, filepath.FromSlash(filepath.Join("static/admin", staticRel)))
			if err != nil {
				return err
			}
			if err := extractFile(reader, destination, header.Size, 0o640); err != nil {
				return err
			}
			hasStatic = true
			continue
		}
		// The release archive also includes README and example configuration.
		// They remain intentionally outside the staged runtime tree.
		discarded, err := io.Copy(io.Discard, io.LimitReader(reader, header.Size))
		if err != nil {
			return fmt.Errorf("discard non-runtime archive file: %w", err)
		}
		if discarded != header.Size {
			return errors.New("release archive ended before the declared file size")
		}
	}
	if !hasBinary || !hasStatic {
		return errors.New("release archive is missing binary or web UI assets")
	}
	metadata := verifiedMetadata{Tag: release.Tag, AssetName: release.AssetName, SHA256: checksum, StagedAt: m.now().UTC()}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, metadataFile), encoded, 0o640); err != nil {
		return fmt.Errorf("write verification metadata: %w", err)
	}
	versionsDir := filepath.Join(m.root, "versions")
	if err := os.MkdirAll(versionsDir, 0o750); err != nil {
		return fmt.Errorf("create versions directory: %w", err)
	}
	target := filepath.Join(versionsDir, release.Tag)
	if _, err := os.Stat(target); err == nil {
		staged, stagedErr := m.stagedRelease(release.Tag)
		if stagedErr == nil && staged {
			return nil
		}
		return fmt.Errorf("staged version directory already exists: %s", release.Tag)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect version directory: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("activate staged release directory: %w", err)
	}
	keepStage = true
	return nil
}

func extractFile(reader io.Reader, destination string, size int64, mode os.FileMode) error {
	if size < 0 || size > defaultArchiveLimit {
		return errors.New("invalid release archive file size")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("create extracted directory: %w", err)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create extracted file: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, size))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("extract archive file: %w", copyErr)
	}
	if written != size {
		return errors.New("release archive ended before the declared file size")
	}
	if closeErr != nil {
		return fmt.Errorf("close extracted file: %w", closeErr)
	}
	return nil
}

func cleanArchiveName(name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "./")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", errors.New("release archive contains an invalid path")
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("release archive contains a traversal path")
	}
	return clean, nil
}

func safeStagePath(stage, rel string) (string, error) {
	destination := filepath.Join(stage, rel)
	resolved, err := filepath.Rel(stage, destination)
	if err != nil || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) || filepath.IsAbs(resolved) {
		return "", errors.New("release archive path escapes staging directory")
	}
	return destination, nil
}

func (m *Manager) stagedRelease(tag string) (bool, error) {
	tag, err := canonicalStableTag(tag)
	if err != nil {
		return false, err
	}
	root := filepath.Join(m.root, "versions", tag)
	binaryPath := filepath.Join(root, "deepseek-web-to-api")
	staticDir := filepath.Join(root, "static", "admin")
	metadataPath := filepath.Join(root, metadataFile)
	if info, err := os.Stat(binaryPath); err != nil || info.IsDir() || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("staged binary is not a regular file")
		}
		return false, err
	}
	if info, err := os.Stat(staticDir); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("staged web UI assets are missing")
		}
		return false, err
	}
	encoded, err := os.ReadFile(metadataPath)
	if err != nil {
		return false, err
	}
	var metadata verifiedMetadata
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		return false, fmt.Errorf("decode staged verification metadata: %w", err)
	}
	if metadata.Tag != tag || len(metadata.SHA256) != sha256.Size*2 {
		return false, errors.New("staged verification metadata is invalid")
	}
	if _, err := hex.DecodeString(metadata.SHA256); err != nil {
		return false, errors.New("staged verification metadata has an invalid checksum")
	}
	return true, nil
}

func (m *Manager) readMarker(name string) (string, error) {
	if m == nil {
		return "", errors.New("self-update manager is unavailable")
	}
	if name != markerCurrent && name != markerPrevious && name != markerPending && name != markerPendingPrevious && name != markerFailed {
		return "", errors.New("invalid self-update marker")
	}
	encoded, err := os.ReadFile(filepath.Join(m.root, name))
	if err != nil {
		return "", err
	}
	return canonicalStableTag(strings.TrimSpace(string(encoded)))
}

func (m *Manager) writeMarker(name, tag string) error {
	if name != markerCurrent && name != markerPrevious && name != markerPending && name != markerPendingPrevious && name != markerFailed {
		return errors.New("invalid self-update marker")
	}
	tag, err := canonicalStableTag(tag)
	if err != nil {
		return err
	}
	if err := m.ensureRoot(); err != nil {
		return err
	}
	temp, err := os.CreateTemp(m.root, "."+name+"-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer removeFileForCleanup(tempPath, "temporary marker file")
	if _, err := temp.WriteString(tag + "\n"); err != nil {
		closeResource(temp, "temporary marker file")
		return err
	}
	if err := temp.Chmod(0o640); err != nil {
		closeResource(temp, "temporary marker file")
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, filepath.Join(m.root, name))
}

// writePendingRollbackPrevious snapshots the existing previous.version before
// a candidate starts. The entrypoint uses it only when the candidate dies
// before pending.version has been committed away. "none" records the absence
// of a previous marker so recovery can restore that state exactly.
func (m *Manager) writePendingRollbackPrevious() error {
	value := "none"
	previous, err := m.readMarker(markerPrevious)
	if err == nil && previous != "" {
		value = previous
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := m.ensureRoot(); err != nil {
		return err
	}
	temp, err := os.CreateTemp(m.root, "."+markerPendingRollbackPrevious+"-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer removeFileForCleanup(tempPath, "temporary rollback marker file")
	if _, err := temp.WriteString(value + "\n"); err != nil {
		closeResource(temp, "temporary rollback marker file")
		return err
	}
	if err := temp.Chmod(0o640); err != nil {
		closeResource(temp, "temporary rollback marker file")
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, filepath.Join(m.root, markerPendingRollbackPrevious))
}

func canonicalStableTag(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", ErrInvalidRelease
	}
	for _, part := range parts {
		if part == "" || len(part) > 9 {
			return "", ErrInvalidRelease
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", ErrInvalidRelease
			}
		}
	}
	return "v" + value, nil
}

func sameStableTag(a, b string) bool {
	left, leftErr := canonicalStableTag(a)
	right, rightErr := canonicalStableTag(b)
	return leftErr == nil && rightErr == nil && left == right
}

func closeResource(closer io.Closer, resource string) {
	if err := closer.Close(); err != nil {
		config.Logger.Warn("[self_update] close resource failed", "resource", resource, "error", err)
	}
}

func removeFileForCleanup(filePath, resource string) {
	if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		config.Logger.Warn("[self_update] remove cleanup file failed", "resource", resource, "path", filePath, "error", err)
	}
}

func removeAllForCleanup(filePath, resource string) {
	if err := os.RemoveAll(filePath); err != nil {
		config.Logger.Warn("[self_update] remove cleanup directory failed", "resource", resource, "path", filePath, "error", err)
	}
}
