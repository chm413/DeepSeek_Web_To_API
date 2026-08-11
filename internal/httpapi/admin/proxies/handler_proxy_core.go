package proxies

import (
	"encoding/json"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func proxyCoreSettings(core config.ProxyCoreConfig) xrayproxy.Settings {
	return xrayproxy.SettingsFromConfig(core)
}

func (h *Handler) getProxyCore(w http.ResponseWriter, r *http.Request) {
	core := h.Store.Snapshot().ProxyCore
	status := xrayproxy.Probe(r.Context(), proxyCoreSettings(core))
	writeJSON(w, http.StatusOK, map[string]any{
		"config": map[string]any{
			"xray_binary_path":        core.XrayBinaryPath,
			"runtime_dir":             core.RuntimeDir,
			"startup_timeout_seconds": core.StartupTimeoutSeconds,
			"auto_download":           !core.AutoDownloadDisabled,
			"download_dir":            core.DownloadDir,
			"download_version":        core.DownloadVersion,
		},
		"status": status,
	})
}

func (h *Handler) updateProxyCore(w http.ResponseWriter, r *http.Request) {
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
	if err := syncProxyRoutes(r.Context(), h.Store.Snapshot()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	status := xrayproxy.Probe(r.Context(), proxyCoreSettings(core))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status})
}

func (h *Handler) downloadProxyCore(w http.ResponseWriter, r *http.Request) {
	core := h.Store.Snapshot().ProxyCore
	xrayproxy.Default().StopAll()
	binaryPath, err := xrayproxy.DownloadCore(r.Context(), proxyCoreSettings(core), true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	if err := syncProxyRoutes(r.Context(), h.Store.Snapshot()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	status := xrayproxy.Probe(r.Context(), proxyCoreSettings(core))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binary_path": binaryPath, "status": status})
}
