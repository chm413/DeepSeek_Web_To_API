package claude

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	"DeepSeek_Web_To_API/internal/safetyllm"
	"DeepSeek_Web_To_API/internal/upstreamsession"
	"DeepSeek_Web_To_API/internal/util"
)

// writeJSON is a package-internal alias to avoid mass-renaming all call-sites.
var writeJSON = util.WriteJSON

type Handler struct {
	Store       ConfigReader
	Auth        AuthResolver
	DS          DeepSeekCaller
	OpenAI      OpenAIChatRunner
	SafetyLLM   safetyllm.Checker
	Incremental *upstreamsession.Store

	ChatHistory *chathistory.Store
}

func (h *Handler) compatStripReferenceMarkers() bool {
	if h == nil || h.Store == nil {
		return true
	}
	return h.Store.CompatStripReferenceMarkers()
}

var (
	claudeStreamPingInterval    = time.Duration(dsprotocol.KeepAliveTimeout) * time.Second
	claudeStreamIdleTimeout     = time.Duration(dsprotocol.StreamIdleTimeout) * time.Second
	claudeStreamMaxKeepaliveCnt = dsprotocol.MaxKeepaliveCount
)

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/anthropic/v1/models", h.ListModels)
	r.Post("/anthropic/v1/messages", h.Messages)
	r.Post("/anthropic/v1/messages/count_tokens", h.CountTokens)
	r.Post("/v1/messages", h.Messages)
	r.Post("/v1/v1/messages", h.Messages)
	r.Post("/messages", h.Messages)
	r.Post("/v1/messages/count_tokens", h.CountTokens)
	r.Post("/v1/v1/messages/count_tokens", h.CountTokens)
	r.Post("/messages/count_tokens", h.CountTokens)
	// /api/messages is an alias used by some client SDKs (e.g. OpenCode when a
	// custom Anthropic baseURL is configured to a non-anthropic.com host) so
	// they reach the messages handler without requiring users to append /v1.
	r.Post("/api/messages", h.Messages)
	r.Post("/api/messages/count_tokens", h.CountTokens)
}

func (h *Handler) ListModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, config.ClaudeModelsResponse())
}
