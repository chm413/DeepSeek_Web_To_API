package xrayproxy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyuri"
)

// CoreConfigStore is the small store surface needed to persist a managed Xray
// installation after automatic download. It intentionally excludes accounts
// and proxy credentials.
type CoreConfigStore interface {
	Snapshot() config.Config
	Update(func(*config.Config) error) error
}

func SettingsFromConfig(core config.ProxyCoreConfig) Settings {
	return Settings{
		BinaryPath:      core.XrayBinaryPath,
		RuntimeDir:      core.RuntimeDir,
		StartupTimeout:  time.Duration(core.StartupTimeoutSeconds) * time.Second,
		AutoDownload:    !core.AutoDownloadDisabled,
		DownloadDir:     core.DownloadDir,
		DownloadVersion: core.DownloadVersion,
	}
}

// SettingsFromStore adds a persistence callback to the normal runtime
// settings. Explicit DownloadVersion remains untouched; InstalledVersion is
// status metadata for the downloaded local binary.
func SettingsFromStore(store CoreConfigStore) Settings {
	if store == nil {
		return Settings{}
	}
	return settingsFromStore(store, store.Snapshot().ProxyCore)
}

func settingsFromStore(store CoreConfigStore, core config.ProxyCoreConfig) Settings {
	settings := SettingsFromConfig(core)
	settings.PersistInstallation = func(installation Installation) error {
		return persistManagedInstallation(store, installation)
	}
	return settings
}

func persistManagedInstallation(store CoreConfigStore, installation Installation) error {
	if store == nil || strings.TrimSpace(installation.DownloadDir) == "" {
		return nil
	}
	installation.DownloadDir = filepath.Clean(installation.DownloadDir)
	current := store.Snapshot().ProxyCore
	if current.AutoDownloadDisabled || !sameLocalPath(effectiveDownloadDir(SettingsFromConfig(current)), installation.DownloadDir) {
		return nil
	}
	if sameLocalPath(strings.TrimSpace(current.DownloadDir), installation.DownloadDir) &&
		strings.TrimSpace(current.InstalledVersion) == strings.TrimSpace(installation.Version) {
		return nil
	}
	return store.Update(func(cfg *config.Config) error {
		core := cfg.ProxyCore
		if core.AutoDownloadDisabled || !sameLocalPath(effectiveDownloadDir(SettingsFromConfig(core)), installation.DownloadDir) {
			return nil
		}
		core.DownloadDir = installation.DownloadDir
		core.InstalledVersion = strings.TrimSpace(installation.Version)
		if err := config.ValidateProxyCoreConfig(core); err != nil {
			return err
		}
		cfg.ProxyCore = core
		return nil
	})
}

func AssignedSpecs(cfg config.Config) []Spec {
	proxies := make(map[string]config.Proxy, len(cfg.Proxies))
	for _, proxy := range cfg.Proxies {
		proxy = config.NormalizeProxy(proxy)
		proxies[proxy.ID] = proxy
	}
	selected := make(map[string]Spec)
	for _, account := range cfg.Accounts {
		if account.Disabled {
			continue
		}
		proxyID := strings.TrimSpace(account.ProxyID)
		if proxyID == "" {
			continue
		}
		proxy, exists := proxies[proxyID]
		if !exists || proxy.Disabled {
			fallbackID := strings.TrimSpace(cfg.ProxyPolicy.FallbackProxyID)
			proxy, exists = proxies[fallbackID]
		}
		if !exists || proxy.Disabled || !proxyuri.IsCoreType(proxy.Type) {
			continue
		}
		selected[proxy.ID] = Spec{ID: proxy.ID, Type: proxy.Type, URI: proxy.URI}
	}
	out := make([]Spec, 0, len(selected))
	for _, spec := range selected {
		out = append(out, spec)
	}
	return out
}

func SyncAssigned(ctx context.Context, cfg config.Config) error {
	_, err := Default().Sync(ctx, AssignedSpecs(cfg), SettingsFromConfig(cfg.ProxyCore))
	return err
}

// SyncAssignedWithStore keeps the shared Xray route process in sync and writes
// successful automatic downloads back to the persistent proxy-core settings.
func SyncAssignedWithStore(ctx context.Context, store CoreConfigStore) error {
	if store == nil {
		return errors.New("xray proxy configuration store is nil")
	}
	cfg := store.Snapshot()
	_, err := Default().Sync(ctx, AssignedSpecs(cfg), settingsFromStore(store, cfg.ProxyCore))
	return err
}

// ProbeWithStore is the persistence-aware counterpart of Probe for status
// checks that may trigger automatic core download.
func ProbeWithStore(ctx context.Context, store CoreConfigStore) Status {
	if store == nil {
		return Probe(ctx, Settings{})
	}
	return Probe(ctx, SettingsFromStore(store))
}
