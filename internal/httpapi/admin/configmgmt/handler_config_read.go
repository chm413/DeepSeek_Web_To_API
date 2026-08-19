package configmgmt

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
	"DeepSeek_Web_To_API/internal/proxysubscription"
	"DeepSeek_Web_To_API/internal/proxyuri"
)

func (h *Handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	snap := h.Store.Snapshot()
	safe := map[string]any{
		"keys":                  []string{},
		"api_keys":              publicAPIKeys(snap.APIKeys),
		"key_count":             len(snap.APIKeys),
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
			"installed_version":       snap.ProxyCore.InstalledVersion,
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
			"has_username":              strings.TrimSpace(proxy.Username) != "",
			"username_preview":          maskSecretPreview(proxy.Username),
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
			"last_test_error":           safeConfigError(proxy.LastTestError),
			"last_exit_ip":              proxy.LastExitIP,
			"last_country":              proxy.LastCountry,
			"last_colo":                 proxy.LastColo,
			"route_available":           proxyservice.ProxyAvailableForRouting(proxy, snap.ProxyPolicy),
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
			"last_error":              safeConfigError(subscription.LastError),
			"node_count":              subscription.NodeCount,
		})
	}
	safe["proxy_subscriptions"] = subscriptions
	writeJSON(w, http.StatusOK, safe)
}

func safeConfigError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return proxysubscription.SanitizeError(fmt.Errorf("%s", value))
}

func publicAPIKeys(keys []config.APIKey) []map[string]any {
	items := make([]map[string]any, 0, len(keys))
	for _, item := range keys {
		key := strings.TrimSpace(item.Key)
		if key == "" {
			continue
		}
		items = append(items, map[string]any{
			"id":          apiKeyID(key),
			"key_preview": maskSecretPreview(key),
			"has_key":     true,
			"name":        item.Name,
			"remark":      item.Remark,
		})
	}
	return items
}

// apiKeyID is safe to expose in URLs and logs; the secret itself is never
// returned by the configuration endpoint.
func apiKeyID(key string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return "key_" + hex.EncodeToString(digest[:8])
}

func (h *Handler) exportConfig(w http.ResponseWriter, _ *http.Request) {
	h.configExport(w, nil)
}

func (h *Handler) configExport(w http.ResponseWriter, _ *http.Request) {
	// Export is intended for configuration recovery, not secret retrieval.
	// Keep credentials out of the response and browser/proxy caches by default.
	snap := h.Store.Snapshot()
	safe, err := sanitizedExportSnapshot(snap)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"redacted": true,
		"config":   safe,
		"json":     string(encoded),
		"base64":   base64.StdEncoding.EncodeToString(encoded),
		"notice":   "敏感凭据已从导出内容中移除；导入后需重新配置管理员、账号、代理和订阅凭据。",
	})
}

// sanitizedExportSnapshot marshals through the public JSON representation so
// newly added config fields are included automatically, then removes all
// credential-bearing fields before the result reaches an HTTP response.
func sanitizedExportSnapshot(snap config.Config) (map[string]any, error) {
	encoded, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	redactExportRoutingReferences(value)
	redactExportMap(value)
	return value, nil
}

// redactExportRoutingReferences removes core proxy nodes whose credentials
// cannot be represented safely, then clears account/fallback references to
// those nodes. The resulting redacted document remains importable and makes
// the missing egress credentials explicit instead of failing validation with
// dangling proxy IDs.
func redactExportRoutingReferences(value map[string]any) {
	rawProxies, ok := value["proxies"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(rawProxies))
	available := make(map[string]struct{}, len(rawProxies))
	for _, raw := range rawProxies {
		proxy, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		proxyType, _ := proxy["type"].(string)
		if proxyuri.IsCoreType(strings.ToLower(strings.TrimSpace(proxyType))) {
			continue
		}
		if id, _ := proxy["id"].(string); strings.TrimSpace(id) != "" {
			available[strings.TrimSpace(id)] = struct{}{}
		}
		kept = append(kept, proxy)
	}
	value["proxies"] = kept

	if accounts, ok := value["accounts"].([]any); ok {
		for _, raw := range accounts {
			account, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if proxyID, _ := account["proxy_id"].(string); strings.TrimSpace(proxyID) != "" {
				if _, exists := available[strings.TrimSpace(proxyID)]; !exists {
					delete(account, "proxy_id")
				}
			}
			// Passwords are removed from a redacted export. Automatic routing
			// requires a password, so disable it until the operator restores
			// the credential rather than producing an invalid import.
			if auto, _ := account["proxy_auto_route"].(bool); auto {
				delete(account, "proxy_auto_route")
			}
		}
	}
	if policy, ok := value["proxy_policy"].(map[string]any); ok {
		if fallback, _ := policy["fallback_proxy_id"].(string); strings.TrimSpace(fallback) != "" {
			if _, exists := available[strings.TrimSpace(fallback)]; !exists {
				delete(policy, "fallback_proxy_id")
			}
		}
	}
}

func redactExportMap(value map[string]any) {
	if value == nil {
		return
	}
	for key, item := range value {
		normalizedKey := canonicalExportFieldKey(key)
		switch normalizedKey {
		case "url", "uri", "proxyuri", "subscriptionurl":
			if raw, ok := item.(string); ok {
				value[key] = redactExportURL(raw)
				continue
			}
			delete(value, key)
			continue
		case "keys", "apikeys", "apikey", "adminkey", "accesstoken", "refreshtoken", "authtoken", "sessiontoken", "idtoken", "bearertoken", "clientsecret", "privatekey", "secretkey", "signingkey", "signaturekey", "credential", "credentials", "passphrase", "signature":
			// API key values are not needed to restore non-secret settings.
			delete(value, key)
			continue
		case "key", "password", "passwordhash", "token", "jwtsecret", "secret", "authorization", "username", "accesskey", "clientkey", "servicekey":
			delete(value, key)
			continue
		}
		redactExportValue(item)
	}
}

func canonicalExportFieldKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	var builder strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

var exportSensitiveQueryKeys = map[string]struct{}{
	"token": {}, "accesstoken": {}, "auth": {}, "authorization": {},
	"password": {}, "passwd": {}, "pass": {}, "secret": {}, "key": {},
	"apikey": {}, "clientsecret": {}, "privatekey": {}, "secretkey": {},
	"authtoken": {}, "sessiontoken": {}, "idtoken": {}, "bearertoken": {},
	"credential": {}, "credentials": {}, "passphrase": {}, "signature": {},
	"signingkey": {}, "signaturekey": {}, "accesskey": {}, "clientkey": {}, "servicekey": {},
}

// redactExportURL preserves a useful endpoint while removing URL credentials,
// fragments, and known secret-bearing query parameters. Non-HTTP proxy URIs
// keep only their scheme and network endpoint because their opaque payload is
// commonly a credential (for example VMess or Shadowsocks).
func redactExportURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u == nil || strings.TrimSpace(u.Scheme) == "" {
		return "<redacted-url>"
	}
	u.Scheme = strings.ToLower(strings.TrimSpace(u.Scheme))
	u.User = nil
	u.Fragment = ""
	query := u.Query()
	for key := range query {
		if _, sensitive := exportSensitiveQueryKeys[canonicalExportFieldKey(key)]; sensitive {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	if u.Scheme != "http" && u.Scheme != "https" {
		if u.Host == "" {
			return u.Scheme + "://<redacted>"
		}
		u.Path = ""
		u.RawPath = ""
		u.Opaque = ""
	}
	return u.String()
}

func redactExportValue(value any) {
	switch child := value.(type) {
	case map[string]any:
		redactExportMap(child)
	case []any:
		for _, entry := range child {
			redactExportValue(entry)
		}
	}
}
