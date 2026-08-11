package proxies

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/proxies", h.listProxies)
	r.Get("/proxies/core", h.getProxyCore)
	r.Put("/proxies/core", h.updateProxyCore)
	r.Post("/proxies/core/download", h.downloadProxyCore)
	r.Get("/proxies/policy", h.getProxyPolicy)
	r.Put("/proxies/policy", h.updateProxyPolicy)
	r.Get("/proxies/subscriptions", h.listProxySubscriptions)
	r.Post("/proxies/subscriptions", h.addProxySubscription)
	r.Put("/proxies/subscriptions/{subscriptionID}", h.updateProxySubscription)
	r.Delete("/proxies/subscriptions/{subscriptionID}", h.deleteProxySubscription)
	r.Post("/proxies/subscriptions/{subscriptionID}/refresh", h.refreshProxySubscription)
	r.Post("/proxies/subscriptions/refresh-all", h.refreshAllProxySubscriptions)
	r.Post("/proxies", h.addProxy)
	r.Put("/proxies/{proxyID}", h.updateProxy)
	r.Delete("/proxies/{proxyID}", h.deleteProxy)
	r.Post("/proxies/test", h.testProxy)
	r.Post("/proxies/test-batch", h.testProxiesBatch)
	r.Post("/proxies/actions", h.proxyBatchAction)
	r.Put("/accounts/{identifier}/proxy", h.updateAccountProxy)
}

func (h *Handler) AddProxy(w http.ResponseWriter, r *http.Request)    { h.addProxy(w, r) }
func (h *Handler) UpdateProxy(w http.ResponseWriter, r *http.Request) { h.updateProxy(w, r) }
func (h *Handler) DeleteProxy(w http.ResponseWriter, r *http.Request) { h.deleteProxy(w, r) }
func (h *Handler) TestProxy(w http.ResponseWriter, r *http.Request)   { h.testProxy(w, r) }
func (h *Handler) UpdateAccountProxy(w http.ResponseWriter, r *http.Request) {
	h.updateAccountProxy(w, r)
}
