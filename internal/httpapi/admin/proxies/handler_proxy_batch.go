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
	var routeChanges []proxyservice.AutoRouteChange
	err := h.Store.Update(func(cfg *config.Config) error {
		now := time.Now().Unix()
		if action == "delete" {
			found := make(map[string]struct{}, len(wanted))
			for _, proxy := range cfg.Proxies {
				proxy = config.NormalizeProxy(proxy)
				if _, selected := wanted[proxy.ID]; selected {
					found[proxy.ID] = struct{}{}
				}
			}
			if len(found) != len(wanted) {
				return newRequestError("one or more selected proxies do not exist")
			}
			var routeErr error
			routeChanges, routeErr = applyProxyDeletionRoutes(cfg, wanted)
			if routeErr != nil {
				return routeErr
			}
			affected = len(wanted)
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
		if writeProxyDeletionConflict(w, err) {
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	relogin := h.reloginRouteChanges(r.Context(), routeChanges, false)
	h.Pool.Reset()
	response := map[string]any{"success": true, "action": action, "affected": affected}
	if action == "delete" {
		response["route_changes"] = routeChanges
		if len(relogin) > 0 {
			response["relogin"] = relogin
		}
	}
	writeJSON(w, http.StatusOK, response)
}
