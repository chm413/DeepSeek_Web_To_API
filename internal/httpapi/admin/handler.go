package admin

import (
	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/chathistory"
	adminaccounts "DeepSeek_Web_To_API/internal/httpapi/admin/accounts"
	adminauth "DeepSeek_Web_To_API/internal/httpapi/admin/auth"
	adminconfig "DeepSeek_Web_To_API/internal/httpapi/admin/configmgmt"
	admindevcapture "DeepSeek_Web_To_API/internal/httpapi/admin/devcapture"
	adminhistory "DeepSeek_Web_To_API/internal/httpapi/admin/history"
	adminmetrics "DeepSeek_Web_To_API/internal/httpapi/admin/metrics"
	adminproxies "DeepSeek_Web_To_API/internal/httpapi/admin/proxies"
	adminrawsamples "DeepSeek_Web_To_API/internal/httpapi/admin/rawsamples"
	adminsettings "DeepSeek_Web_To_API/internal/httpapi/admin/settings"
	adminshared "DeepSeek_Web_To_API/internal/httpapi/admin/shared"
	adminupdates "DeepSeek_Web_To_API/internal/httpapi/admin/updates"
	adminversion "DeepSeek_Web_To_API/internal/httpapi/admin/version"
	"DeepSeek_Web_To_API/internal/safetystore"
	"DeepSeek_Web_To_API/internal/selfupdate"
)

type Handler struct {
	Store         adminshared.ConfigStore
	Pool          adminshared.PoolController
	DS            adminshared.DeepSeekCaller
	OpenAI        adminshared.OpenAIChatCaller
	ChatHistory   *chathistory.Store
	ResponseCache adminshared.ResponseCacheRuntimeProvider
	SafetyWords   *safetystore.WordsStore
	SafetyIPs     *safetystore.IPsStore
	Updater       *selfupdate.Manager
}

func RegisterRoutes(r chi.Router, h *Handler) {
	deps := adminsharedDeps(h)
	authHandler := &adminauth.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	accountsHandler := &adminaccounts.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	configHandler := &adminconfig.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	settingsHandler := &adminsettings.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory, ResponseCache: deps.ResponseCache, SafetyWords: h.SafetyWords, SafetyIPs: h.SafetyIPs}
	proxiesHandler := &adminproxies.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	rawSamplesHandler := &adminrawsamples.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	historyHandler := &adminhistory.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	devCaptureHandler := &admindevcapture.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	versionHandler := &adminversion.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	updatesHandler := &adminupdates.Handler{Updater: h.Updater}
	metricsHandler := &adminmetrics.Handler{Store: deps.Store, Pool: deps.Pool, ChatHistory: deps.ChatHistory, ResponseCache: deps.ResponseCache}

	adminauth.RegisterPublicRoutes(r, authHandler)
	r.Group(func(pr chi.Router) {
		pr.Use(authHandler.RequireAdmin)
		adminconfig.RegisterRoutes(pr, configHandler)
		adminsettings.RegisterRoutes(pr, settingsHandler)
		adminproxies.RegisterRoutes(pr, proxiesHandler)
		adminaccounts.RegisterRoutes(pr, accountsHandler)
		adminrawsamples.RegisterRoutes(pr, rawSamplesHandler)
		admindevcapture.RegisterRoutes(pr, devCaptureHandler)
		adminhistory.RegisterRoutes(pr, historyHandler)
		adminversion.RegisterRoutes(pr, versionHandler)
		adminupdates.RegisterRoutes(pr, updatesHandler)
		adminmetrics.RegisterRoutes(pr, metricsHandler)
	})
}

func adminsharedDeps(h *Handler) adminsharedDepsValue {
	if h == nil {
		return adminsharedDepsValue{}
	}
	return adminsharedDepsValue{Store: h.Store, Pool: h.Pool, DS: h.DS, OpenAI: h.OpenAI, ChatHistory: h.ChatHistory, ResponseCache: h.ResponseCache}
}

type adminsharedDepsValue struct {
	Store         adminshared.ConfigStore
	Pool          adminshared.PoolController
	DS            adminshared.DeepSeekCaller
	OpenAI        adminshared.OpenAIChatCaller
	ChatHistory   *chathistory.Store
	ResponseCache adminshared.ResponseCacheRuntimeProvider
}
