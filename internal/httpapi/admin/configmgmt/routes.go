package configmgmt

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/config", h.getConfig)
	r.Post("/config", h.updateConfig)
	r.Post("/config/import", h.configImport)
	r.Get("/config/export", h.configExport)
	r.Get("/export", h.exportConfig)
	r.Post("/keys", h.addKey)
	r.Put("/keys/{key}", h.updateKey)
	r.Delete("/keys/{key}", h.deleteKey)
	// Opaque key IDs keep secrets out of browser history, proxies, and logs.
	r.Put("/keys/id/{id}", h.updateKeyByID)
	r.Delete("/keys/id/{id}", h.deleteKeyByID)
	r.Post("/import", h.batchImport)
}

func (h *Handler) updateKeyByID(w http.ResponseWriter, r *http.Request) {
	h.updateKey(w, r)
}

func (h *Handler) deleteKeyByID(w http.ResponseWriter, r *http.Request) {
	h.deleteKey(w, r)
}

func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request)    { h.getConfig(w, r) }
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) { h.updateConfig(w, r) }
func (h *Handler) ConfigImport(w http.ResponseWriter, r *http.Request) { h.configImport(w, r) }
func (h *Handler) BatchImport(w http.ResponseWriter, r *http.Request)  { h.batchImport(w, r) }
func (h *Handler) AddKey(w http.ResponseWriter, r *http.Request)       { h.addKey(w, r) }
func (h *Handler) UpdateKey(w http.ResponseWriter, r *http.Request)    { h.updateKey(w, r) }
func (h *Handler) DeleteKey(w http.ResponseWriter, r *http.Request)    { h.deleteKey(w, r) }
