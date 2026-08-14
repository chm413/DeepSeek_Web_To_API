package configmgmt

import (
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
	"DeepSeek_Web_To_API/internal/proxyuri"
)

func (h *Handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	snap := h.Store.Snapshot()
	safe := map[string]any{
		"keys":                  snap.Keys,
		"api_keys":              snap.APIKeys,
		"accounts":              []map[string]any{},
		"proxies":               []map[string]any{},
		"env_backed":            h.Store.IsEnvBacked(),
		"env_source_present":    h.Store.HasEnvConfigSource(),
		"env_writeback_enabled": h.Store.IsEnvWritebackEnabled(),
		"config_path":           h.Store.ConfigPath(),
		"model_aliases":         snap.ModelAliases,
		"proxy_core": map[string]any{
			"xray_binary_path":        snap.ProxyCore.XrayBinaryPath,
			"runtime_dir":             snap.ProxyCore.RuntimeDir,
			"startup_timeout_seconds": snap.ProxyCore.StartupTimeoutSeconds,
			"auto_download":           !snap.ProxyCore.AutoDownloadDisabled,
			"download_dir":            snap.ProxyCore.DownloadDir,
			"download_version":        snap.ProxyCore.DownloadVersion,
		},
		"proxy_policy": map[string]any{
			"health_check_enabled":                 snap.ProxyPolicy.HealthChecksEnabled(),
			"auto_route_enabled":                   snap.ProxyPolicy.AutoRouteEnabled(),
			"health_check_interval_minutes":        snap.ProxyPolicy.HealthIntervalMinutes(),
			"auto_disable_after_failures":          snap.ProxyPolicy.DisableAfterFailures(),
			"auto_enable_on_recovery":              snap.ProxyPolicy.EnableOnRecovery(),
			"fallback_proxy_id":                    snap.ProxyPolicy.FallbackProxyID,
			"subscription_update_interval_minutes": snap.ProxyPolicy.SubscriptionIntervalMinutes(),
			"test_concurrency":                     snap.ProxyPolicy.Concurrency(),
		},
		"proxy_subscriptions": []map[string]any{},
	}
	accounts := make([]map[string]any, 0, len(snap.Accounts))
	for _, acc := range snap.Accounts {
		token := strings.TrimSpace(acc.Token)
		accounts = append(accounts, map[string]any{
			"identifier":       acc.Identifier(),
			"name":             acc.Name,
			"remark":           acc.Remark,
			"email":            acc.Email,
			"mobile":           acc.Mobile,
			"proxy_id":         acc.ProxyID,
			"proxy_auto_route": acc.ProxyAutoRoute,
			"has_password":     strings.TrimSpace(acc.Password) != "",
			"has_token":        token != "",
			"token_preview":    maskSecretPreview(token),
		})
	}
	safe["accounts"] = accounts
	proxies := make([]map[string]any, 0, len(snap.Proxies))
	assignedProxyAccounts, automaticProxyAccounts := proxyservice.ProxyAssignmentCounts(snap)
	for _, proxy := range snap.Proxies {
		proxy = config.NormalizeProxy(proxy)
		proxies = append(proxies, map[string]any{
			"id":                        proxy.ID,
			"name":                      proxy.Name,
			"type":                      proxy.Type,
			"host":                      proxy.Host,
			"port":                      proxy.Port,
			"username":                  proxy.Username,
			"has_password":              strings.TrimSpace(proxy.Password) != "",
			"has_uri":                   strings.TrimSpace(proxy.URI) != "",
			"core_managed":              proxyuri.IsCoreType(proxy.Type),
			"subscription_id":           proxy.SubscriptionID,
			"disabled":                  proxy.Disabled,
			"disabled_reason":           proxy.DisabledReason,
			"disabled_at_unix":          proxy.DisabledAtUnix,
			"consecutive_failures":      proxy.ConsecutiveFailures,
			"last_test_at_unix":         proxy.LastTestAtUnix,
			"last_test_success":         proxy.LastTestSuccess,
			"last_latency_ms":           proxy.LastLatencyMS,
			"last_http_status":          proxy.LastHTTPStatus,
			"last_test_error":           proxy.LastTestError,
			"last_exit_ip":              proxy.LastExitIP,
			"last_country":              proxy.LastCountry,
			"last_colo":                 proxy.LastColo,
			"route_available":           !proxy.Disabled && proxy.LastTestAtUnix > 0 && proxy.LastTestSuccess,
			"assigned_account_count":    assignedProxyAccounts[proxy.ID],
			"auto_routed_account_count": automaticProxyAccounts[proxy.ID],
		})
	}
	safe["proxies"] = proxies
	subscriptions := make([]map[string]any, 0, len(snap.ProxySubscriptions))
	for _, subscription := range snap.ProxySubscriptions {
		subscriptions = append(subscriptions, map[string]any{
			"id":                      subscription.ID,
			"name":                    subscription.Name,
			"has_url":                 strings.TrimSpace(subscription.URL) != "",
			"disabled":                subscription.Disabled,
			"auto_update":             !subscription.AutoUpdateDisabled,
			"auto_test":               !subscription.AutoTestDisabled,
			"update_interval_minutes": subscription.UpdateIntervalMinutes,
			"last_updated_at_unix":    subscription.LastUpdatedAtUnix,
			"last_attempt_at_unix":    subscription.LastAttemptAtUnix,
			"last_error":              subscription.LastError,
			"node_count":              subscription.NodeCount,
		})
	}
	safe["proxy_subscriptions"] = subscriptions
	writeJSON(w, http.StatusOK, safe)
}

func (h *Handler) exportConfig(w http.ResponseWriter, _ *http.Request) {
	h.configExport(w, nil)
}

func (h *Handler) configExport(w http.ResponseWriter, _ *http.Request) {
	snap := h.Store.Snapshot()
	jsonStr, b64, err := h.Store.ExportJSONAndBase64()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"config":  snap,
		"json":    jsonStr,
		"base64":  b64,
	})
}
