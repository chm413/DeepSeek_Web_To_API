package proxies

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
)

func (h *Handler) testProxiesBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyIDs    []string `json:"proxy_ids"`
		Concurrency int      `json:"concurrency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	results, err := proxyservice.TestProxies(r.Context(), h.Store, req.ProxyIDs, req.Concurrency, proxyConnectivityTester)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	passed := 0
	autoDisabled := 0
	for _, result := range results {
		if result.Success {
			passed++
		}
		if result.AutoDisabled {
			autoDisabled++
		}
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"total":         len(results),
		"passed":        passed,
		"failed":        len(results) - passed,
		"auto_disabled": autoDisabled,
		"results":       results,
	})
}

func (h *Handler) proxyBatchAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyIDs []string `json:"proxy_ids"`
		Action   string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != "enable" && action != "disable" && action != "delete" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "action must be enable, disable, or delete"})
		return
	}
	wanted := make(map[string]struct{}, len(req.ProxyIDs))
	for _, id := range req.ProxyIDs {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "proxy_ids is required"})
		return
	}
	affected := 0
	err := h.Store.Update(func(cfg *config.Config) error {
		now := time.Now().Unix()
		if action == "delete" {
			kept := make([]config.Proxy, 0, len(cfg.Proxies))
			for _, proxy := range cfg.Proxies {
				proxy = config.NormalizeProxy(proxy)
				if _, exists := wanted[proxy.ID]; !exists {
					kept = append(kept, proxy)
					continue
				}
				affected++
				if cfg.ProxyPolicy.FallbackProxyID == proxy.ID {
					cfg.ProxyPolicy.FallbackProxyID = ""
				}
				for i := range cfg.Accounts {
					if strings.TrimSpace(cfg.Accounts[i].ProxyID) == proxy.ID {
						cfg.Accounts[i].ProxyID = ""
					}
				}
			}
			cfg.Proxies = kept
		} else {
			for i := range cfg.Proxies {
				proxy := config.NormalizeProxy(cfg.Proxies[i])
				if _, exists := wanted[proxy.ID]; !exists {
					continue
				}
				affected++
				if action == "enable" {
					proxy.Disabled = false
					proxy.DisabledReason = ""
					proxy.DisabledAtUnix = 0
					proxy.ConsecutiveFailures = 0
				} else {
					proxy.Disabled = true
					proxy.DisabledReason = config.ProxyDisabledManual
					proxy.DisabledAtUnix = now
				}
				cfg.Proxies[i] = proxy
			}
		}
		return config.ValidateConfig(*cfg)
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "action": action, "affected": affected})
}
