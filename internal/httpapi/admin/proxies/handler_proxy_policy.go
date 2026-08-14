package proxies

import (
	"encoding/json"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
)

func proxyPolicyResponse(policy config.ProxyPolicyConfig) map[string]any {
	return map[string]any{
		"health_check_enabled":                 policy.HealthChecksEnabled(),
		"auto_route_enabled":                   policy.AutoRouteEnabled(),
		"health_check_interval_minutes":        policy.HealthIntervalMinutes(),
		"auto_disable_after_failures":          policy.DisableAfterFailures(),
		"auto_enable_on_recovery":              policy.EnableOnRecovery(),
		"fallback_proxy_id":                    policy.FallbackProxyID,
		"subscription_update_interval_minutes": policy.SubscriptionIntervalMinutes(),
		"test_concurrency":                     policy.Concurrency(),
	}
}

func (h *Handler) getProxyPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, proxyPolicyResponse(h.Store.Snapshot().ProxyPolicy))
}

func (h *Handler) updateProxyPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HealthCheckEnabled                *bool  `json:"health_check_enabled"`
		AutomaticRoutingEnabled           *bool  `json:"auto_route_enabled"`
		HealthCheckIntervalMinutes        int    `json:"health_check_interval_minutes"`
		AutoDisableAfterFailures          int    `json:"auto_disable_after_failures"`
		AutoEnableOnRecovery              *bool  `json:"auto_enable_on_recovery"`
		FallbackProxyID                   string `json:"fallback_proxy_id"`
		SubscriptionUpdateIntervalMinutes int    `json:"subscription_update_interval_minutes"`
		TestConcurrency                   int    `json:"test_concurrency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	policy := config.ProxyPolicyConfig{
		HealthCheckEnabled:                req.HealthCheckEnabled,
		AutomaticRoutingEnabled:           req.AutomaticRoutingEnabled,
		HealthCheckIntervalMinutes:        req.HealthCheckIntervalMinutes,
		AutoDisableAfterFailures:          req.AutoDisableAfterFailures,
		AutoEnableOnRecovery:              req.AutoEnableOnRecovery,
		FallbackProxyID:                   strings.TrimSpace(req.FallbackProxyID),
		SubscriptionUpdateIntervalMinutes: req.SubscriptionUpdateIntervalMinutes,
		TestConcurrency:                   req.TestConcurrency,
	}
	snapshot := h.Store.Snapshot()
	if err := config.ValidateProxyPolicyConfig(policy, snapshot.Proxies); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := h.Store.Update(func(cfg *config.Config) error {
		cfg.ProxyPolicy = policy
		return nil
	}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "policy": proxyPolicyResponse(policy)})
}
