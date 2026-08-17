package proxies

import (
	"encoding/json"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func (h *Handler) getProxyCore(w http.ResponseWriter, r *http.Request) {
	core := h.Store.Snapshot().ProxyCore
	status := xrayproxy.ProbeWithStore(r.Context(), h.Store)
	writeJSON(w, http.StatusOK, map[string]any{
		"config": map[string]any{
			"xray_binary_path":        core.XrayBinaryPath,
			"runtime_dir":             core.RuntimeDir,
			"startup_timeout_seconds": core.StartupTimeoutSeconds,
			"auto_download":           !core.AutoDownloadDisabled,
			"download_dir":            core.DownloadDir,
			"download_version":        core.DownloadVersion,
			"installed_version":       core.InstalledVersion,
		},
		"status": status,
	})
}

func (h *Handler) updateProxyCore(w http.ResponseWriter, r *http.Request) {
	previous := h.Store.Snapshot().ProxyCore
	var req struct {
		XrayBinaryPath        string `json:"xray_binary_path"`
		RuntimeDir            string `json:"runtime_dir"`
		StartupTimeoutSeconds int    `json:"startup_timeout_seconds"`
		AutoDownload          *bool  `json:"auto_download"`
		DownloadDir           string `json:"download_dir"`
		DownloadVersion       string `json:"download_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	core := config.ProxyCoreConfig{
		XrayBinaryPath:        strings.TrimSpace(req.XrayBinaryPath),
		RuntimeDir:            strings.TrimSpace(req.RuntimeDir),
		StartupTimeoutSeconds: req.StartupTimeoutSeconds,
		DownloadDir:           strings.TrimSpace(req.DownloadDir),
		DownloadVersion:       strings.TrimSpace(req.DownloadVersion),
	}
	if req.AutoDownload != nil {
		core.AutoDownloadDisabled = !*req.AutoDownload
	}
	if sameCoreInstallationTarget(previous, core) {
		core.InstalledVersion = previous.InstalledVersion
	}
	if err := config.ValidateProxyCoreConfig(core); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := h.Store.Update(func(c *config.Config) error {
		c.ProxyCore = core
		return nil
	}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	xrayproxy.Default().StopAll()
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	status := xrayproxy.ProbeWithStore(r.Context(), h.Store)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status})
}

func (h *Handler) downloadProxyCore(w http.ResponseWriter, r *http.Request) {
	settings := xrayproxy.SettingsFromStore(h.Store)
	xrayproxy.Default().StopAll()
	binaryPath, err := xrayproxy.DownloadCore(r.Context(), settings, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	xrayproxy.PersistInstalledCore(settings, binaryPath)
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	status := xrayproxy.ProbeWithStore(r.Context(), h.Store)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binary_path": binaryPath, "status": status})
}

func sameCoreInstallationTarget(left, right config.ProxyCoreConfig) bool {
	return strings.TrimSpace(left.XrayBinaryPath) == strings.TrimSpace(right.XrayBinaryPath) &&
		strings.TrimSpace(left.DownloadDir) == strings.TrimSpace(right.DownloadDir) &&
		strings.TrimSpace(left.DownloadVersion) == strings.TrimSpace(right.DownloadVersion)
}
