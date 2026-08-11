package proxies

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func proxyCoreSettings(core config.ProxyCoreConfig) xrayproxy.Settings {
	return xrayproxy.Settings{
		BinaryPath:     core.XrayBinaryPath,
		RuntimeDir:     core.RuntimeDir,
		StartupTimeout: time.Duration(core.StartupTimeoutSeconds) * time.Second,
	}
}

func (h *Handler) getProxyCore(w http.ResponseWriter, r *http.Request) {
	core := h.Store.Snapshot().ProxyCore
	status := xrayproxy.Probe(r.Context(), proxyCoreSettings(core))
	writeJSON(w, http.StatusOK, map[string]any{
		"config": map[string]any{
			"xray_binary_path":        core.XrayBinaryPath,
			"runtime_dir":             core.RuntimeDir,
			"startup_timeout_seconds": core.StartupTimeoutSeconds,
		},
		"status": status,
	})
}

func (h *Handler) updateProxyCore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		XrayBinaryPath        string `json:"xray_binary_path"`
		RuntimeDir            string `json:"runtime_dir"`
		StartupTimeoutSeconds int    `json:"startup_timeout_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	core := config.ProxyCoreConfig{
		XrayBinaryPath:        strings.TrimSpace(req.XrayBinaryPath),
		RuntimeDir:            strings.TrimSpace(req.RuntimeDir),
		StartupTimeoutSeconds: req.StartupTimeoutSeconds,
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
	h.Pool.Reset()
	status := xrayproxy.Probe(r.Context(), proxyCoreSettings(core))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status})
}
