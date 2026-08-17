package updates

import "github.com/go-chi/chi/v5"

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/updates", h.getUpdates)
	r.Post("/updates/check", h.checkUpdates)
	r.Post("/updates/download", h.downloadUpdate)
	r.Post("/updates/apply", h.applyUpdate)
	r.Post("/updates/rollback", h.rollbackUpdate)
	r.Put("/updates/settings", h.updateSettings)
}
