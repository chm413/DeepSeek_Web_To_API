package proxies

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
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
		"last_error":              subscription.LastError,
		"node_count":              subscription.NodeCount,
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
	if shouldRefresh && !subscription.Disabled {
		result, err := proxyservice.RefreshSubscription(r.Context(), h.Store, subscription.ID)
		if err != nil {
			response["refresh_error"] = err.Error()
		} else {
			response["refresh"] = result
		}
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
	if req.Refresh != nil && *req.Refresh && !updated.Disabled {
		result, err := proxyservice.RefreshSubscription(r.Context(), h.Store, subscriptionID)
		if err != nil {
			response["refresh_error"] = err.Error()
		} else {
			response["refresh"] = result
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) deleteProxySubscription(w http.ResponseWriter, r *http.Request) {
	subscriptionID := subscriptionIDParam(r)
	now := time.Now().Unix()
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
		assigned := map[string]bool{}
		for _, account := range cfg.Accounts {
			assigned[strings.TrimSpace(account.ProxyID)] = true
		}
		kept := make([]config.Proxy, 0, len(cfg.Proxies))
		for _, proxy := range cfg.Proxies {
			proxy = config.NormalizeProxy(proxy)
			if proxy.SubscriptionID != subscriptionID {
				kept = append(kept, proxy)
				continue
			}
			if cfg.ProxyPolicy.FallbackProxyID == proxy.ID {
				cfg.ProxyPolicy.FallbackProxyID = ""
			}
			if assigned[proxy.ID] {
				proxy.Disabled = true
				proxy.DisabledReason = config.ProxyDisabledSubscriptionRemoved
				proxy.DisabledAtUnix = now
				kept = append(kept, proxy)
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := syncProxyRoutes(r.Context(), h.Store.Snapshot()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *Handler) refreshProxySubscription(w http.ResponseWriter, r *http.Request) {
	result, err := proxyservice.RefreshSubscription(r.Context(), h.Store, subscriptionIDParam(r))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

func (h *Handler) refreshAllProxySubscriptions(w http.ResponseWriter, r *http.Request) {
	snapshot := h.Store.Snapshot()
	results := make([]map[string]any, 0, len(snapshot.ProxySubscriptions))
	for _, subscription := range snapshot.ProxySubscriptions {
		if subscription.Disabled {
			continue
		}
		result, err := proxyservice.RefreshSubscription(r.Context(), h.Store, subscription.ID)
		item := map[string]any{"subscription_id": subscription.ID, "name": subscription.Name}
		if err != nil {
			item["success"] = false
			item["error"] = err.Error()
		} else {
			item["success"] = true
			item["result"] = result
		}
		results = append(results, item)
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "results": results})
}

func subscriptionIDParam(r *http.Request) string {
	id := chi.URLParam(r, "subscriptionID")
	if decoded, err := url.PathUnescape(id); err == nil {
		id = decoded
	}
	return strings.TrimSpace(id)
}
