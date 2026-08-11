package xrayproxy

import (
	"context"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyuri"
)

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
