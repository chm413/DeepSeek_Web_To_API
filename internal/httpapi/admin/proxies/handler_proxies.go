package proxies

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/proxyservice"
	"DeepSeek_Web_To_API/internal/proxyuri"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

var proxyConnectivityTester = func(ctx context.Context, proxy config.Proxy, core config.ProxyCoreConfig) map[string]any {
	return dsclient.TestProxyConnectivityWithCore(ctx, proxy, core)
}

func validateProxyMutation(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	if err := config.ValidateProxyConfig(cfg.Proxies); err != nil {
		return err
	}
	if err := config.ValidateProxyPolicyConfig(cfg.ProxyPolicy, cfg.Proxies); err != nil {
		return err
	}
	return config.ValidateAccountProxyReferences(cfg.Accounts, cfg.Proxies)
}

func proxyResponse(proxy config.Proxy) map[string]any {
	proxy = config.NormalizeProxy(proxy)
	return map[string]any{
		"id":                   proxy.ID,
		"name":                 proxy.Name,
		"type":                 proxy.Type,
		"host":                 proxy.Host,
		"port":                 proxy.Port,
		"username":             proxy.Username,
		"has_password":         strings.TrimSpace(proxy.Password) != "",
		"has_uri":              strings.TrimSpace(proxy.URI) != "",
		"core_managed":         proxyuri.IsCoreType(proxy.Type),
		"subscription_id":      proxy.SubscriptionID,
		"disabled":             proxy.Disabled,
		"disabled_reason":      proxy.DisabledReason,
		"disabled_at_unix":     proxy.DisabledAtUnix,
		"consecutive_failures": proxy.ConsecutiveFailures,
		"last_test_at_unix":    proxy.LastTestAtUnix,
		"last_test_success":    proxy.LastTestSuccess,
		"last_latency_ms":      proxy.LastLatencyMS,
		"last_http_status":     proxy.LastHTTPStatus,
		"last_test_error":      proxy.LastTestError,
		"last_exit_ip":         proxy.LastExitIP,
		"last_country":         proxy.LastCountry,
		"last_colo":            proxy.LastColo,
	}
}

func (h *Handler) listProxies(w http.ResponseWriter, _ *http.Request) {
	snapshot := h.Store.Snapshot()
	proxies := snapshot.Proxies
	assigned, automatic := proxyservice.ProxyAssignmentCounts(snapshot)
	items := make([]map[string]any, 0, len(proxies))
	for _, proxy := range proxies {
		item := proxyResponse(proxy)
		normalized := config.NormalizeProxy(proxy)
		item["route_available"] = !normalized.Disabled && normalized.LastTestAtUnix > 0 && normalized.LastTestSuccess
		item["assigned_account_count"] = assigned[normalized.ID]
		item["auto_routed_account_count"] = automatic[normalized.ID]
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"total":      len(items),
		"route_pool": proxyservice.AvailableRoutePool(snapshot),
	})
}

func (h *Handler) addProxy(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	proxy := toProxy(req)
	err := h.Store.Update(func(c *config.Config) error {
		c.Proxies = append(c.Proxies, proxy)
		return validateProxyMutation(c)
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "proxy": proxyResponse(proxy)})
}

func (h *Handler) updateProxy(w http.ResponseWriter, r *http.Request) {
	proxyID := chi.URLParam(r, "proxyID")
	if decoded, err := url.PathUnescape(proxyID); err == nil {
		proxyID = decoded
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	proxy := toProxy(req)
	proxy.ID = strings.TrimSpace(proxyID)

	err := h.Store.Update(func(c *config.Config) error {
		for i, existing := range c.Proxies {
			existing = config.NormalizeProxy(existing)
			if existing.ID != proxy.ID {
				continue
			}
			if proxy.Password == "" {
				proxy.Password = existing.Password
			}
			if proxy.URI == "" && proxyuri.IsCoreType(proxy.Type) && proxy.Type == existing.Type {
				proxy.URI = existing.URI
			}
			proxy.SubscriptionID = existing.SubscriptionID
			proxy.Disabled = existing.Disabled
			proxy.DisabledReason = existing.DisabledReason
			proxy.DisabledAtUnix = existing.DisabledAtUnix
			proxy.ConsecutiveFailures = existing.ConsecutiveFailures
			proxy.LastTestAtUnix = existing.LastTestAtUnix
			proxy.LastTestSuccess = existing.LastTestSuccess
			proxy.LastLatencyMS = existing.LastLatencyMS
			proxy.LastHTTPStatus = existing.LastHTTPStatus
			proxy.LastTestError = existing.LastTestError
			proxy.LastExitIP = existing.LastExitIP
			proxy.LastCountry = existing.LastCountry
			proxy.LastColo = existing.LastColo
			proxy = config.NormalizeProxy(proxy)
			c.Proxies[i] = proxy
			return validateProxyMutation(c)
		}
		return newRequestError("代理不存在")
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "proxy": proxyResponse(proxy)})
}

func (h *Handler) deleteProxy(w http.ResponseWriter, r *http.Request) {
	proxyID := chi.URLParam(r, "proxyID")
	if decoded, err := url.PathUnescape(proxyID); err == nil {
		proxyID = decoded
	}
	err := h.Store.Update(func(c *config.Config) error {
		idx := -1
		for i, existing := range c.Proxies {
			existing = config.NormalizeProxy(existing)
			if existing.ID == strings.TrimSpace(proxyID) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return newRequestError("代理不存在")
		}
		c.Proxies = append(c.Proxies[:idx], c.Proxies[idx+1:]...)
		if strings.TrimSpace(c.ProxyPolicy.FallbackProxyID) == strings.TrimSpace(proxyID) {
			c.ProxyPolicy.FallbackProxyID = ""
		}
		for i := range c.Accounts {
			if strings.TrimSpace(c.Accounts[i].ProxyID) == strings.TrimSpace(proxyID) {
				c.Accounts[i].ProxyID = ""
			}
		}
		return validateProxyMutation(c)
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *Handler) testProxy(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	proxyID := fieldString(req, "proxy_id")

	var proxy config.Proxy
	if proxyID != "" {
		var ok bool
		proxy, ok = findProxyByID(h.Store.Snapshot(), proxyID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "代理不存在"})
			return
		}
	} else {
		proxy = toProxy(req)
	}

	results, err := proxyservice.TestProxies(r.Context(), h.Store, []string{proxy.ID}, 1, proxyConnectivityTester)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	if len(results) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "代理不存在"})
		return
	}
	if _, err := h.reconcileAndSyncProxyRoutes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, results[0])
}

func (h *Handler) updateAccountProxy(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	proxyID := fieldString(req, "proxy_id")
	autoRoute, _ := req["auto_route"].(bool)
	if autoRoute && !h.Store.Snapshot().ProxyPolicy.AutoRouteEnabled() {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "automatic proxy routing is disabled in proxy policy"})
		return
	}

	proxyChanged := false
	originalProxyID := ""
	err := h.Store.Update(func(c *config.Config) error {
		if proxyID != "" {
			if _, ok := findProxyByID(*c, proxyID); !ok {
				return newRequestError("代理不存在")
			}
		}
		for i, acc := range c.Accounts {
			if !accountMatchesIdentifier(acc, identifier) {
				continue
			}
			originalProxyID = strings.TrimSpace(acc.ProxyID)
			if autoRoute && proxyID == "" {
				proxyID = originalProxyID
			}
			if autoRoute && strings.TrimSpace(acc.Password) == "" {
				return newRequestError("account password is required for automatic proxy routing")
			}
			proxyChanged = strings.TrimSpace(acc.ProxyID) != proxyID
			if proxyChanged && strings.TrimSpace(acc.Password) == "" {
				return newRequestError("account password is required when changing the egress proxy")
			}
			c.Accounts[i].ProxyID = proxyID
			c.Accounts[i].ProxyAutoRoute = autoRoute
			if proxyChanged {
				c.Accounts[i].Token = ""
			}
			return validateProxyMutation(c)
		}
		return newRequestError("账号不存在")
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	autoRelogins, err := h.reconcileAndSyncProxyRoutes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	effective, _ := h.Store.FindAccount(identifier)
	routeChanged := originalProxyID != strings.TrimSpace(effective.ProxyID)
	response := map[string]any{"success": true, "proxy_id": effective.ProxyID, "auto_route": autoRoute, "route_changed": routeChanged}
	if routeChanged && !autoRoute {
		response["relogin"] = h.reloginAccountAfterRouteChange(r.Context(), identifier)
	} else if autoRoute {
		if relogin, ok := autoRelogins[effective.Identifier()]; ok {
			response["relogin"] = relogin
		} else {
			response["relogin"] = map[string]any{"success": true, "changed": false}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) reloginAccountAfterRouteChange(parent context.Context, identifier string) map[string]any {
	accountConfig, ok := h.Store.FindAccount(identifier)
	if !ok {
		return map[string]any{"success": false, "reason": "account not found after proxy update"}
	}
	if h.DS == nil {
		return map[string]any{"success": false, "reason": "DeepSeek login client is unavailable"}
	}
	loginCtx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	token, err := h.DS.Login(loginCtx, accountConfig)
	if err != nil {
		config.Logger.Warn("[proxy_router] manual route relogin failed", "account", identifier, "proxy_id", accountConfig.ProxyID, "error", err)
		return map[string]any{"success": false, "reason": err.Error()}
	}
	if err := h.Store.UpdateAccountToken(identifier, token); err != nil {
		return map[string]any{"success": false, "reason": err.Error()}
	}
	h.Pool.Reset()
	config.Logger.Info("[proxy_router] manual route relogin succeeded", "account", identifier, "proxy_id", accountConfig.ProxyID)
	return map[string]any{"success": true}
}

func (h *Handler) reconcileAndSyncProxyRoutes(ctx context.Context) (map[string]map[string]any, error) {
	changes, err := proxyservice.ReconcileAutoRoutes(h.Store)
	if err != nil {
		return nil, err
	}
	if err := syncProxyRoutes(ctx, h.Store.Snapshot()); err != nil {
		return nil, err
	}
	results := h.reloginAutoRouteChanges(ctx, changes)
	if len(changes) > 0 {
		h.Pool.Reset()
		config.Logger.Info("[proxy_router] admin route reconciliation completed", "accounts", len(changes), "available_nodes", len(proxyservice.AvailableRoutePool(h.Store.Snapshot())))
	}
	return results, nil
}

func (h *Handler) reloginAutoRouteChanges(ctx context.Context, changes []proxyservice.AutoRouteChange) map[string]map[string]any {
	return h.reloginRouteChanges(ctx, changes, true)
}

func (h *Handler) reloginManualRouteChanges(ctx context.Context, changes []proxyservice.AutoRouteChange) map[string]map[string]any {
	return h.reloginRouteChanges(ctx, changes, false)
}

func (h *Handler) reloginRouteChanges(ctx context.Context, changes []proxyservice.AutoRouteChange, requireProxy bool) map[string]map[string]any {
	results := make(map[string]map[string]any, len(changes))
	if len(changes) == 0 {
		return results
	}
	type reloginResult struct {
		accountID string
		value     map[string]any
	}
	jobs := make(chan proxyservice.AutoRouteChange)
	completed := make(chan reloginResult, len(changes))
	workerCount := 4
	if len(changes) < workerCount {
		workerCount = len(changes)
	}
	var workers sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for change := range jobs {
				if requireProxy && strings.TrimSpace(change.ToProxyID) == "" {
					completed <- reloginResult{accountID: change.AccountID, value: map[string]any{"success": false, "reason": "no tested proxy node is currently available"}}
					continue
				}
				completed <- reloginResult{accountID: change.AccountID, value: h.reloginAccountAfterRouteChange(ctx, change.AccountID)}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, change := range changes {
			select {
			case jobs <- change:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(completed)
	}()
	for result := range completed {
		results[result.accountID] = result.value
	}
	return results
}

func syncProxyRoutes(ctx context.Context, cfg config.Config) error {
	syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return xrayproxy.SyncAssigned(syncCtx, cfg)
}
