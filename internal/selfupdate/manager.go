// Package selfupdate verifies and stages checksum-verified release artifacts for
// the Docker self-update entrypoint. It deliberately never overwrites the
// immutable image binary or application configuration.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/version"
)

const (
	DefaultRepository           = "chm413/DeepSeek_Web_To_API"
	defaultGitHubAPI            = "https://api.github.com"
	defaultCheckIntervalMinutes = 360
	defaultArchiveLimit         = 256 << 20
	defaultManifestLimit        = 1 << 20
	defaultRestartExitCode      = 75
)

var (
	ErrContainerRequired    = errors.New("self-update installation is only available in a managed container")
	ErrNoUpdateAvailable    = errors.New("no newer release is available")
	ErrUpdateNotDownloaded  = errors.New("the requested release has not been verified and staged")
	ErrOperationInProgress  = errors.New("an update operation is already in progress")
	ErrInvalidRelease       = errors.New("release is not a supported stable version")
	ErrRestartNotConfigured = errors.New("self-update restart is not configured")
)

// ConfigStore is intentionally small so the updater can be exercised without
// constructing the rest of the gateway runtime.
type ConfigStore interface {
	Snapshot() config.Config
	Update(func(*config.Config) error) error
}

type Options struct {
	Root            string
	Repository      string
	GitHubAPI       string
	HTTPClient      *http.Client
	GOOS            string
	GOARCH          string
	CurrentVersion  func() string
	Now             func() time.Time
	Container       *bool
	RestartExitCode int
}

type Settings struct {
	Enabled              bool
	AutoDownload         bool
	AutoApply            bool
	CheckIntervalMinutes int
}

// Release contains only non-secret release metadata safe for admin responses.
type Release struct {
	Tag          string    `json:"tag"`
	URL          string    `json:"url,omitempty"`
	PublishedAt  time.Time `json:"published_at,omitempty"`
	AssetName    string    `json:"asset_name,omitempty"`
	AssetURL     string    `json:"-"`
	ChecksumURL  string    `json:"-"`
	ArchiveTag   string    `json:"-"`
	Downloadable bool      `json:"downloadable"`
}

type Status struct {
	CurrentVersion       string    `json:"current_version"`
	CurrentTag           string    `json:"current_tag"`
	Repository           string    `json:"repository"`
	CheckEnabled         bool      `json:"check_enabled"`
	AutoDownload         bool      `json:"auto_download"`
	AutoApply            bool      `json:"auto_apply"`
	CheckIntervalMinutes int       `json:"check_interval_minutes"`
	ContainerManaged     bool      `json:"container_managed"`
	CanInstall           bool      `json:"can_install"`
	InstalledTag         string    `json:"installed_tag,omitempty"`
	PreviousTag          string    `json:"previous_tag,omitempty"`
	PendingTag           string    `json:"pending_tag,omitempty"`
	FailedTag            string    `json:"failed_tag,omitempty"`
	DownloadedTag        string    `json:"downloaded_tag,omitempty"`
	Available            *Release  `json:"available,omitempty"`
	UpdateAvailable      bool      `json:"update_available"`
	Checking             bool      `json:"checking"`
	Downloading          bool      `json:"downloading"`
	LastCheckedAt        time.Time `json:"last_checked_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
}

type Manager struct {
	store           ConfigStore
	root            string
	repository      string
	githubAPI       string
	client          *http.Client
	goos            string
	goarch          string
	currentVersion  func() string
	now             func() time.Time
	container       bool
	restartExitCode int

	mu            sync.RWMutex
	stateMu       sync.Mutex
	available     *Release
	lastCheckedAt time.Time
	lastError     string
	checking      bool
	downloading   bool
	restartCh     chan struct{}
}

func New(store ConfigStore, options Options) *Manager {
	root := strings.TrimSpace(options.Root)
	if root == "" {
		root = strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_SELF_UPDATE_ROOT"))
	}
	if root == "" {
		root = filepath.Join(config.BaseDir(), "data", "self-update")
	}
	repository := strings.TrimSpace(options.Repository)
	if repository == "" {
		repository = DefaultRepository
	}
	githubAPI := strings.TrimRight(strings.TrimSpace(options.GitHubAPI), "/")
	if githubAPI == "" {
		githubAPI = defaultGitHubAPI
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	currentVersion := options.CurrentVersion
	if currentVersion == nil {
		currentVersion = func() string {
			value, _ := version.Current()
			return value
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	goos := strings.TrimSpace(options.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := strings.TrimSpace(options.GOARCH)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	container := strings.EqualFold(strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_SELF_UPDATE_CONTAINER")), "true")
	if options.Container != nil {
		container = *options.Container
	}
	exitCode := options.RestartExitCode
	if exitCode <= 0 {
		exitCode = restartExitCodeFromEnv()
	}
	if exitCode <= 0 {
		exitCode = defaultRestartExitCode
	}

	return &Manager{
		store:           store,
		root:            filepath.Clean(root),
		repository:      repository,
		githubAPI:       githubAPI,
		client:          client,
		goos:            goos,
		goarch:          goarch,
		currentVersion:  currentVersion,
		now:             now,
		container:       container,
		restartExitCode: exitCode,
		restartCh:       make(chan struct{}, 1),
	}
}

func (m *Manager) RestartRequests() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.restartCh
}

func (m *Manager) RestartExitCode() int {
	if m == nil || m.restartExitCode <= 0 {
		return defaultRestartExitCode
	}
	return m.restartExitCode
}

func (m *Manager) ContainerManaged() bool {
	return m != nil && m.container
}

func (m *Manager) Settings() Settings {
	if m == nil || m.store == nil {
		return defaultSettings()
	}
	return resolveSettings(m.store.Snapshot().AppUpdate)
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	settings := m.Settings()
	current := strings.TrimSpace(m.currentVersion())
	m.mu.RLock()
	available := cloneRelease(m.available)
	status := Status{
		CurrentVersion:       current,
		CurrentTag:           version.Tag(current),
		Repository:           m.repository,
		CheckEnabled:         settings.Enabled,
		AutoDownload:         settings.AutoDownload,
		AutoApply:            settings.AutoApply,
		CheckIntervalMinutes: settings.CheckIntervalMinutes,
		ContainerManaged:     m.container,
		CanInstall:           m.container,
		Available:            available,
		Checking:             m.checking,
		Downloading:          m.downloading,
		LastCheckedAt:        m.lastCheckedAt,
		LastError:            m.lastError,
	}
	m.mu.RUnlock()
	status.InstalledTag, _ = m.readMarker(markerCurrent)
	status.PreviousTag, _ = m.readMarker(markerPrevious)
	status.PendingTag, _ = m.readMarker(markerPending)
	status.FailedTag, _ = m.readMarker(markerFailed)
	if available != nil {
		if staged, _ := m.stagedRelease(available.Tag); staged {
			status.DownloadedTag = available.Tag
		}
	}
	status.UpdateAvailable = available != nil && version.Compare(available.Tag, current) > 0
	return status
}

func (m *Manager) Start(ctx context.Context) {
	if m == nil || ctx == nil {
		return
	}
	go m.run(ctx)
}

func (m *Manager) run(ctx context.Context) {
	for {
		settings := m.Settings()
		if settings.Enabled {
			m.checkAndMaybeStage(ctx, settings)
		}
		interval := time.Duration(settings.CheckIntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = time.Duration(defaultCheckIntervalMinutes) * time.Minute
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (m *Manager) checkAndMaybeStage(ctx context.Context, settings Settings) {
	release, err := m.Check(ctx)
	if err != nil || release == nil || version.Compare(release.Tag, m.currentVersion()) <= 0 {
		return
	}
	if !settings.AutoDownload {
		return
	}
	if _, err := m.Download(ctx, release.Tag); err != nil {
		config.Logger.Warn("[self_update] automatic download failed", "tag", release.Tag, "error", err)
		return
	}
	if settings.AutoApply {
		failedTag, _ := m.readMarker(markerFailed)
		if sameStableTag(failedTag, release.Tag) {
			config.Logger.Warn("[self_update] automatic apply skipped after candidate startup failure", "tag", release.Tag)
			return
		}
		if _, err := m.Apply(release.Tag); err != nil {
			config.Logger.Warn("[self_update] automatic apply failed", "tag", release.Tag, "error", err)
			return
		}
		m.ScheduleRestart(2 * time.Second)
	}
}

func (m *Manager) Check(ctx context.Context) (*Release, error) {
	if m == nil {
		return nil, errors.New("self-update manager is unavailable")
	}
	if !m.beginCheck() {
		return nil, ErrOperationInProgress
	}
	defer m.finishCheck()

	release, err := m.fetchLatest(ctx)
	if err != nil {
		m.setLastError(err)
		config.Logger.Warn("[self_update] release check failed", "repository", m.repository, "error", err)
		return nil, err
	}
	m.mu.Lock()
	m.available = cloneRelease(release)
	m.lastCheckedAt = m.now().UTC()
	m.lastError = ""
	m.mu.Unlock()
	if release != nil && version.Compare(release.Tag, m.currentVersion()) > 0 {
		config.Logger.Info("[self_update] update available", "current", version.Tag(m.currentVersion()), "available", release.Tag, "downloadable", release.Downloadable)
	} else {
		config.Logger.Info("[self_update] release check completed", "current", version.Tag(m.currentVersion()), "update_available", false)
	}
	return cloneRelease(release), nil
}

func (m *Manager) UpdateSettings(patch SettingsPatch) (Status, error) {
	if m == nil || m.store == nil {
		return Status{}, errors.New("self-update configuration store is unavailable")
	}
	if patch.CheckIntervalMinutes != nil {
		if *patch.CheckIntervalMinutes < 5 || *patch.CheckIntervalMinutes > 10080 {
			return Status{}, fmt.Errorf("app_update.check_interval_minutes must be between 5 and 10080")
		}
	}
	if patch.AutoApply != nil && *patch.AutoApply && !m.container {
		return Status{}, ErrContainerRequired
	}
	current := m.Settings()
	willAutoDownload := current.AutoDownload
	if patch.AutoDownload != nil {
		willAutoDownload = *patch.AutoDownload
	}
	willAutoApply := current.AutoApply
	if patch.AutoApply != nil {
		willAutoApply = *patch.AutoApply
	}
	if willAutoApply && !willAutoDownload {
		return Status{}, errors.New("app_update.auto_apply requires app_update.auto_download")
	}
	if err := m.store.Update(func(c *config.Config) error {
		if patch.Enabled != nil {
			c.AppUpdate.Enabled = cloneBool(patch.Enabled)
		}
		if patch.AutoDownload != nil {
			c.AppUpdate.AutoDownload = cloneBool(patch.AutoDownload)
		}
		if patch.AutoApply != nil {
			c.AppUpdate.AutoApply = cloneBool(patch.AutoApply)
		}
		if patch.CheckIntervalMinutes != nil {
			c.AppUpdate.CheckIntervalMinutes = *patch.CheckIntervalMinutes
		}
		return nil
	}); err != nil {
		return Status{}, err
	}
	return m.Status(), nil
}

type SettingsPatch struct {
	Enabled              *bool
	AutoDownload         *bool
	AutoApply            *bool
	CheckIntervalMinutes *int
}

func defaultSettings() Settings {
	return Settings{Enabled: true, AutoDownload: false, AutoApply: false, CheckIntervalMinutes: defaultCheckIntervalMinutes}
}

func resolveSettings(c config.AppUpdateConfig) Settings {
	settings := defaultSettings()
	if c.Enabled != nil {
		settings.Enabled = *c.Enabled
	}
	if c.AutoDownload != nil {
		settings.AutoDownload = *c.AutoDownload
	}
	if c.AutoApply != nil {
		settings.AutoApply = *c.AutoApply
	}
	if c.CheckIntervalMinutes > 0 {
		settings.CheckIntervalMinutes = c.CheckIntervalMinutes
	}
	return settings
}

func (m *Manager) fetchLatest(ctx context.Context) (*Release, error) {
	if !validRepository(m.repository) {
		return nil, fmt.Errorf("invalid self-update repository")
	}
	endpoint := m.githubAPI + "/repos/" + m.repository + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "deepseek-web-to-api-self-update")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("latest release returned HTTP %d", resp.StatusCode)
	}
	var payload githubRelease
	decoder := json.NewDecoder(io.LimitReader(resp.Body, defaultManifestLimit))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode latest release: %w", err)
	}
	if payload.Draft || payload.Prerelease {
		return nil, ErrInvalidRelease
	}
	tag, err := canonicalStableTag(payload.TagName)
	if err != nil {
		return nil, err
	}
	release := &Release{Tag: tag, URL: safeReleaseURL(payload.HTMLURL), PublishedAt: payload.PublishedAt.UTC(), ArchiveTag: payload.TagName}
	expectedAsset := fmt.Sprintf("deepseek-web-to-api_%s_linux_%s.tar.gz", payload.TagName, m.goarch)
	for _, asset := range payload.Assets {
		switch asset.Name {
		case expectedAsset:
			release.AssetName = asset.Name
			release.AssetURL = asset.BrowserDownloadURL
		case "sha256sums.txt":
			release.ChecksumURL = asset.BrowserDownloadURL
		}
	}
	release.Downloadable = release.AssetName != "" && release.AssetURL != "" && release.ChecksumURL != "" && m.goos == "linux" && (m.goarch == "amd64" || m.goarch == "arm64")
	return release, nil
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (m *Manager) beginCheck() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checking || m.downloading {
		return false
	}
	m.checking = true
	return true
}

func (m *Manager) finishCheck() {
	m.mu.Lock()
	m.checking = false
	m.mu.Unlock()
}

func (m *Manager) setLastError(err error) {
	m.mu.Lock()
	m.lastCheckedAt = m.now().UTC()
	m.lastError = strings.TrimSpace(err.Error())
	m.mu.Unlock()
}

func cloneRelease(value *Release) *Release {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
				continue
			}
			return false
		}
	}
	return true
}

func safeReleaseURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return parsed.String()
}

func restartExitCodeFromEnv() int {
	value := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_SELF_UPDATE_RESTART_EXIT_CODE"))
	if value == "75" {
		return 75
	}
	return 0
}
