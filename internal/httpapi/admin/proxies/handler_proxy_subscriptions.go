package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
	"DeepSeek_Web_To_API/internal/proxysubscription"
)

func subscriptionResponse(subscription config.ProxySubscription) map[string]any {
	return map[string]any{
		"id":                      subscription.ID,
		"name":                    subscription.Name,
		"has_url":                 strings.TrimSpace(subscription.URL) != "",
		"disabled":                subscription.Disabled,
		"auto_update":             !subscription.AutoUpdateDisabled,
		"auto_test":               !subscription.AutoTestDisabled,
		"update_interval_minutes": subscription.UpdateIntervalMinutes,
		"last_updated_at_unix":    subscription.LastUpdatedAtUnix,
		"last_attempt_at_unix":    subscription.LastAttemptAtUnix,
		"last_error":              safeSubscriptionError(subscription.LastError),
		"node_count":              subscription.NodeCount,
	}
}

func safeSubscriptionError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return proxysubscription.SanitizeError(fmt.Errorf("%s", value))
}

// refreshSubscription is replaceable in package tests. RefreshSubscription can
// return a committed result together with an Xray synchronization error; the
// admin handlers must preserve that result so moved accounts are re-logged in.
var refreshSubscription = proxyservice.RefreshSubscription

func recordSubscriptionRefresh(response map[string]any, result proxyservice.SubscriptionRefreshResult, err error) bool {
	if err != nil {
		response["refresh_error"] = err.Error()
	}
	if err == nil || isCommittedRefreshResult(result, err) {
		response["refresh"] = result
		return true
	}
	return false
}

func (h *Handler) attachSubscriptionRelogins(ctx context.Context, response map[string]any, autoRelogins map[string]map[string]any, changes []proxyservice.AutoRouteChange) {
	manualRelogins := h.reloginManualRouteChanges(ctx, changes)
	if len(autoRelogins) > 0 || len(manualRelogins) > 0 {
		response["relogin"] = mergeReloginResults(autoRelogins, manualRelogins)
	}
}

func (h *Handler) listProxySubscriptions(w http.ResponseWriter, _ *http.Request) {
	subscriptions := h.Store.Snapshot().ProxySubscriptions
	items := make([]map[string]any, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		items = append(items, subscriptionResponse(subscription))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

type subscriptionMutationRequest struct {
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	Disabled              bool   `json:"disabled"`
	AutoUpdate            *bool  `json:"auto_update"`
	AutoTest              *bool  `json:"auto_test"`
	UpdateIntervalMinutes int    `json:"update_interval_minutes"`
	Refresh               *bool  `json:"refresh"`
}

func (h *Handler) addProxySubscription(w http.ResponseWriter, r *http.Request) {
	var req subscriptionMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	subscription := config.ProxySubscription{
		ID:                    uuid.NewString(),
		Name:                  strings.TrimSpace(req.Name),
		URL:                   strings.TrimSpace(req.URL),
		Disabled:              req.Disabled,
		UpdateIntervalMinutes: req.UpdateIntervalMinutes,
	}
	if req.AutoUpdate != nil {
		subscription.AutoUpdateDisabled = !*req.AutoUpdate
	}
	if req.AutoTest != nil {
		subscription.AutoTestDisabled = !*req.AutoTest
	}
	if subscription.Name == "" {
		subscription.Name = "Subscription"
	}
	if err := config.ValidateProxySubscriptions([]config.ProxySubscription{subscription}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := h.Store.Update(func(cfg *config.Config) error {
		cfg.ProxySubscriptions = append(cfg.ProxySubscriptions, subscription)
		return config.ValidateConfig(*cfg)
	}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	shouldRefresh := req.Refresh == nil || *req.Refresh
	response := map[string]any{"success": true, "subscription": subscriptionResponse(subscription)}
	var refreshResult proxyservice.SubscriptionRefreshResult
	refreshFailed := false
	if shouldRefresh && !subscription.Disabled {
		result, err := refreshSubscription(r.Context(), h.Store, subscription.ID)
		// RefreshSubscription can commit route changes before a follow-up
		// Xray sync fails. Keep the result so those accounts are re-logged in
		// even when the response reports a partial refresh failure.
		refreshResult = result
		recordSubscriptionRefresh(response, result, err)
		refreshFailed = err != nil
	}
	autoRelogins, routeErr := h.reconcileAndSyncProxyRoutes(r.Context())
	if routeErr != nil {
		response["route_error"] = routeErr.Error()
	}
	h.attachSubscriptionRelogins(r.Context(), response, autoRelogins, refreshResult.RouteChanges)
	if refreshFailed || routeErr != nil {
		response["success"] = false
		response["partial"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) updateProxySubscription(w http.ResponseWriter, r *http.Request) {
	subscriptionID := subscriptionIDParam(r)
	var req subscriptionMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	var updated config.ProxySubscription
	err := h.Store.Update(func(cfg *config.Config) error {
		for i := range cfg.ProxySubscriptions {
			if cfg.ProxySubscriptions[i].ID != subscriptionID {
				continue
			}
			updated = cfg.ProxySubscriptions[i]
			if name := strings.TrimSpace(req.Name); name != "" {
				updated.Name = name
			}
			if rawURL := strings.TrimSpace(req.URL); rawURL != "" {
				updated.URL = rawURL
			}
			updated.Disabled = req.Disabled
			updated.UpdateIntervalMinutes = req.UpdateIntervalMinutes
			if req.AutoUpdate != nil {
				updated.AutoUpdateDisabled = !*req.AutoUpdate
			}
			if req.AutoTest != nil {
				updated.AutoTestDisabled = !*req.AutoTest
			}
			if err := config.ValidateProxySubscriptions([]config.ProxySubscription{updated}); err != nil {
				return err
			}
			cfg.ProxySubscriptions[i] = updated
			return config.ValidateConfig(*cfg)
		}
		return newRequestError("订阅不存在")
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	response := map[string]any{"success": true, "subscription": subscriptionResponse(updated)}
	var refreshResult proxyservice.SubscriptionRefreshResult
	refreshFailed := false
	if req.Refresh != nil && *req.Refresh && !updated.Disabled {
		result, err := refreshSubscription(r.Context(), h.Store, subscriptionID)
		// Preserve a committed result when only the post-commit Xray sync
		// failed; route changes still require token refresh below.
		refreshResult = result
		recordSubscriptionRefresh(response, result, err)
		refreshFailed = err != nil
	}
	autoRelogins, routeErr := h.reconcileAndSyncProxyRoutes(r.Context())
	if routeErr != nil {
		response["route_error"] = routeErr.Error()
	}
	h.attachSubscriptionRelogins(r.Context(), response, autoRelogins, refreshResult.RouteChanges)
	if refreshFailed || routeErr != nil {
		response["success"] = false
		response["partial"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) deleteProxySubscription(w http.ResponseWriter, r *http.Request) {
	subscriptionID := subscriptionIDParam(r)
	var routeChanges []proxyservice.AutoRouteChange
	err := h.Store.Update(func(cfg *config.Config) error {
		index := -1
		for i := range cfg.ProxySubscriptions {
			if cfg.ProxySubscriptions[i].ID == subscriptionID {
				index = i
				break
			}
		}
		if index < 0 {
			return newRequestError("订阅不存在")
		}
		removedIDs := make(map[string]struct{})
		for _, rawProxy := range cfg.Proxies {
			proxy := config.NormalizeProxy(rawProxy)
			if proxy.SubscriptionID == subscriptionID {
				removedIDs[proxy.ID] = struct{}{}
			}
		}
		changes, routeErr := proxyservice.ReassignSubscriptionRemovedRoutes(cfg, removedIDs)
		if routeErr != nil {
			return routeErr
		}
		routeChanges = changes
		kept := make([]config.Proxy, 0, len(cfg.Proxies))
		for _, proxy := range cfg.Proxies {
			proxy = config.NormalizeProxy(proxy)
			if proxy.SubscriptionID != subscriptionID {
				kept = append(kept, proxy)
				continue
			}
		}
		cfg.Proxies = kept
		cfg.ProxySubscriptions = append(cfg.ProxySubscriptions[:index], cfg.ProxySubscriptions[index+1:]...)
		return config.ValidateConfig(*cfg)
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": detail})
			return
		}
		var migrationErr *proxyservice.SubscriptionRouteMigrationError
		if errors.As(err, &migrationErr) {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": migrationErr.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	autoRelogins, routeErr := h.reconcileAndSyncProxyRoutes(r.Context())
	h.Pool.Reset()
	response := map[string]any{"success": routeErr == nil}
	if len(routeChanges) > 0 {
		response["route_changes"] = routeChanges
	}
	manualRelogins := h.reloginManualRouteChanges(r.Context(), routeChanges)
	if len(autoRelogins) > 0 || len(manualRelogins) > 0 {
		response["relogin"] = mergeReloginResults(autoRelogins, manualRelogins)
	}
	if routeErr != nil {
		response["route_error"] = routeErr.Error()
		response["partial"] = true
		writeJSON(w, http.StatusBadGateway, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) refreshProxySubscription(w http.ResponseWriter, r *http.Request) {
	result, refreshErr := refreshSubscription(r.Context(), h.Store, subscriptionIDParam(r))
	if refreshErr != nil && !isCommittedRefreshResult(result, refreshErr) {
		var migrationErr *proxyservice.SubscriptionRouteMigrationError
		if errors.As(refreshErr, &migrationErr) {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": migrationErr.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": refreshErr.Error()})
		return
	}
	autoRelogins, routeErr := h.reconcileAndSyncProxyRoutes(r.Context())
	h.Pool.Reset()
	response := map[string]any{"success": refreshErr == nil && routeErr == nil, "result": result}
	if refreshErr != nil {
		response["refresh_error"] = refreshErr.Error()
		response["partial"] = true
	}
	if routeErr != nil {
		response["route_error"] = routeErr.Error()
	}
	manualRelogins := h.reloginManualRouteChanges(r.Context(), result.RouteChanges)
	if len(autoRelogins) > 0 || len(manualRelogins) > 0 {
		response["relogin"] = mergeReloginResults(autoRelogins, manualRelogins)
	}
	status := http.StatusOK
	if refreshErr != nil || routeErr != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, response)
}

func (h *Handler) refreshAllProxySubscriptions(w http.ResponseWriter, r *http.Request) {
	snapshot := h.Store.Snapshot()
	results := make([]map[string]any, 0, len(snapshot.ProxySubscriptions))
	manualChanges := make([]proxyservice.AutoRouteChange, 0)
	for _, subscription := range snapshot.ProxySubscriptions {
		if subscription.Disabled {
			continue
		}
		result, err := refreshSubscription(r.Context(), h.Store, subscription.ID)
		item := map[string]any{"subscription_id": subscription.ID, "name": subscription.Name}
		if len(result.RouteChanges) > 0 {
			manualChanges = append(manualChanges, result.RouteChanges...)
		}
		if err != nil {
			item["success"] = false
			item["error"] = err.Error()
			if isCommittedRefreshResult(result, err) {
				// The configuration is already committed. Return the result so
				// the caller can see the route migration despite the sync error.
				item["result"] = result
				item["partial"] = true
			}
		} else {
			item["success"] = true
			item["result"] = result
		}
		results = append(results, item)
	}
	autoRelogins, routeErr := h.reconcileAndSyncProxyRoutes(r.Context())
	h.Pool.Reset()
	manualRelogins := h.reloginManualRouteChanges(r.Context(), manualChanges)
	allRefreshesSucceeded := routeErr == nil
	for _, item := range results {
		if success, ok := item["success"].(bool); ok && !success {
			allRefreshesSucceeded = false
			break
		}
	}
	response := map[string]any{"success": allRefreshesSucceeded, "results": results}
	if len(autoRelogins) > 0 || len(manualRelogins) > 0 {
		response["relogin"] = mergeReloginResults(autoRelogins, manualRelogins)
	}
	if routeErr != nil {
		response["route_error"] = routeErr.Error()
		writeJSON(w, http.StatusBadGateway, response)
		return
	}
	if !allRefreshesSucceeded {
		response["partial"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func isCommittedRefreshResult(result proxyservice.SubscriptionRefreshResult, err error) bool {
	if len(result.RouteChanges) > 0 {
		return true
	}
	var commitErr *proxyservice.SubscriptionRefreshCommitError
	return err != nil && errors.As(err, &commitErr)
}

func mergeReloginResults(primary, secondary map[string]map[string]any) map[string]map[string]any {
	merged := make(map[string]map[string]any, len(primary)+len(secondary))
	for key, value := range primary {
		merged[key] = value
	}
	for key, value := range secondary {
		merged[key] = value
	}
	return merged
}

func subscriptionIDParam(r *http.Request) string {
	id := chi.URLParam(r, "subscriptionID")
	if decoded, err := url.PathUnescape(id); err == nil {
		id = decoded
	}
	return strings.TrimSpace(id)
}
