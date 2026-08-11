package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/httpapi/admin"
	"DeepSeek_Web_To_API/internal/httpapi/claude"
	"DeepSeek_Web_To_API/internal/httpapi/gemini"
	"DeepSeek_Web_To_API/internal/httpapi/openai/chat"
	"DeepSeek_Web_To_API/internal/httpapi/openai/embeddings"
	"DeepSeek_Web_To_API/internal/httpapi/openai/files"
	"DeepSeek_Web_To_API/internal/httpapi/openai/responses"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/requestguard"
	"DeepSeek_Web_To_API/internal/requestmeta"
	"DeepSeek_Web_To_API/internal/responsecache"
	"DeepSeek_Web_To_API/internal/safetyllm"
	"DeepSeek_Web_To_API/internal/safetystore"
	"DeepSeek_Web_To_API/internal/upstreamsession"
	"DeepSeek_Web_To_API/internal/webui"
)

type App struct {
	Store                    *config.Store
	Pool                     *account.Pool
	Resolver                 *auth.Resolver
	DS                       *dsclient.Client
	Router                   http.Handler
	stopAccountHealthMonitor context.CancelFunc
}

func NewApp() (*App, error) {
	store, err := config.LoadStoreWithError()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	pool := account.NewPool(store)
	var dsClient *dsclient.Client
	resolver := auth.NewResolver(store, pool, func(ctx context.Context, acc config.Account) (string, error) {
		return dsClient.Login(ctx, acc)
	})
	dsClient = dsclient.NewClient(store, resolver)
	if err := dsClient.PreloadPow(context.Background()); err != nil {
		config.Logger.Warn("[PoW] init failed", "error", err)
	} else {
		config.Logger.Info("[PoW] pure Go solver ready")
	}
	chatHistoryStore := chathistory.NewSQLiteWithTokenStats(store.ChatHistorySQLitePath(), store.ChatHistoryPath(), store.TokenUsageSQLitePath())
	if err := chatHistoryStore.Err(); err != nil {
		config.Logger.Warn("[chat_history] unavailable", "path", chatHistoryStore.Path(), "error", err)
	}

	// Dedicated SQLite stores for the safety policy lists. These run in
	// parallel to the legacy config.SafetyConfig list fields; on first start
	// any pre-existing config-side lists are migrated into the store and
	// future admin saves dual-write to keep both sources in sync.
	safetyWordsStore, err := safetystore.NewWordsStore(store.SafetyWordsSQLitePath())
	if err != nil {
		config.Logger.Warn("[safety_words] unavailable", "path", store.SafetyWordsSQLitePath(), "error", err)
	}
	safetyIPsStore, err := safetystore.NewIPsStore(store.SafetyIPsSQLitePath())
	if err != nil {
		config.Logger.Warn("[safety_ips] unavailable", "path", store.SafetyIPsSQLitePath(), "error", err)
	}
	if safetyWordsStore != nil || safetyIPsStore != nil {
		legacy := store.SafetyConfig()
		if safetyWordsStore != nil {
			if err := safetyWordsStore.MigrateLegacyOnce(legacy.BannedContent, legacy.BannedRegex, legacy.Jailbreak.Patterns); err != nil {
				config.Logger.Warn("[safety_words] legacy migration failed", "error", err)
			}
		}
		if safetyIPsStore != nil {
			if err := safetyIPsStore.MigrateLegacyOnce(legacy.BlockedIPs, nil, legacy.BlockedConversationIDs); err != nil {
				config.Logger.Warn("[safety_ips] legacy migration failed", "error", err)
			}
		}
	}

	modelsHandler := &shared.ModelsHandler{Store: store}
	// v1.0.14: build the LLM-based safety checker. Doer wraps dsclient so
	// the checker can reuse the caller's RequestAuth + managed account
	// pool to run a fast deepseek-v4-flash-nothinking audit before the
	// real upstream call. Disabled checker is a no-op (still injected so
	// handlers can call CheckWithAuth unconditionally).
	// v1.0.15: bind the checker to a live config source so the operator can
	// flip safety.llm_check.enabled (and other runtime knobs) via the WebUI
	// PUT /admin/settings — the very next request picks up the new state
	// without a process restart. Cache size + concurrency depth still come
	// from the bootstrap snapshot (those back finite-size objects).
	safetyLLMSource := safetyLLMConfigSource{store: store}
	safetyLLMChecker := safetyllm.NewLLMCheckerWithSource(safetyLLMSource, safetyllm.NewDeepSeekDoer(dsClient))
	incrementalSessions := upstreamsession.NewStore(0, 0)
	chatHandler := &chat.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore, SafetyLLM: safetyLLMChecker, Incremental: incrementalSessions}
	responsesHandler := &responses.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore, SafetyLLM: safetyLLMChecker, Incremental: incrementalSessions}
	filesHandler := &files.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore}
	embeddingsHandler := &embeddings.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore}
	claudeHandler := &claude.Handler{Store: store, Auth: resolver, DS: dsClient, OpenAI: chatHandler, ChatHistory: chatHistoryStore, SafetyLLM: safetyLLMChecker, Incremental: incrementalSessions}
	geminiHandler := &gemini.Handler{Store: store, Auth: resolver, DS: dsClient, OpenAI: chatHandler}
	protocolResponseCache := responsecache.New(responsecache.Options{
		Dir:            store.ResponseCacheDir(),
		MemoryTTL:      store.ResponseCacheMemoryTTL(),
		DiskTTL:        store.ResponseCacheDiskTTL(),
		MaxBody:        store.ResponseCacheMaxBodyBytes(),
		MemoryMaxBytes: store.ResponseCacheMemoryMaxBytes(),
		DiskMaxBytes:   store.ResponseCacheDiskMaxBytes(),
		SemanticKey:    store.ResponseCacheSemanticKey(),
		OnHit:          responsesHandler.OnProtocolResponseCacheHit,
	})
	adminHandler := &admin.Handler{Store: store, Pool: pool, DS: dsClient, OpenAI: chatHandler, ChatHistory: chatHistoryStore, ResponseCache: protocolResponseCache, SafetyWords: safetyWordsStore, SafetyIPs: safetyIPsStore}
	webuiHandler := webui.NewHandler(store.StaticAdminDir())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// chi's middleware.RealIP unconditionally rewrites RemoteAddr from
	// X-Forwarded-For / X-Real-IP / True-Client-IP regardless of who the
	// peer is. That defeats the trusted-proxy model in
	// requestmeta.ClientIP, which only honors those headers when the
	// peer is in the trusted CIDR set. So we deliberately do NOT use
	// middleware.RealIP here; ClientIP performs the equivalent
	// resolution with proxy-trust enforcement.
	r.Use(filteredLogger())
	r.Use(middleware.Recoverer)
	r.Use(cors)
	r.Use(securityHeaders)
	r.Use(requestguard.Middleware(requestguard.Options{
		Store:       store,
		ChatHistory: chatHistoryStore,
		SafetyWords: safetyWordsStore,
		SafetyIPs:   safetyIPsStore,
	}))
	r.Use(requestbody.ValidateJSONUTF8)
	r.Use(timeout(0))
	r.Use(protocolResponseCache.Middleware(resolver))

	healthzHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	readyzHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
	r.Get("/healthz", healthzHandler)
	r.Head("/healthz", healthzHandler)
	r.Get("/readyz", readyzHandler)
	r.Head("/readyz", readyzHandler)
	r.Get("/v1/models", modelsHandler.ListModels)
	r.Get("/v1/models/{model_id}", modelsHandler.GetModel)
	r.Post("/v1/chat/completions", chatHandler.ChatCompletions)
	r.Post("/v1/responses", responsesHandler.Responses)
	r.Get("/v1/responses/{response_id}", responsesHandler.GetResponseByID)
	// Local compact state is an opaque, per-caller handle held by this process
	// for the normal Responses TTL; it is not provider-owned encrypted state.
	r.Post("/v1/responses/compact", responsesHandler.Compact)
	r.Post("/v1/v1/responses/compact", responsesHandler.Compact)
	r.Post("/responses/compact", responsesHandler.Compact)
	r.Post("/v1/files", filesHandler.UploadFile)
	r.Post("/v1/embeddings", embeddingsHandler.Embeddings)
	// Some SDK wrappers append their own /v1 prefix even when users configure
	// a base URL that already ends in /v1. Keep these aliases in-process so the
	// session is still accepted instead of failing as a 404.
	r.Get("/v1/v1/models", modelsHandler.ListModels)
	r.Get("/v1/v1/models/{model_id}", modelsHandler.GetModel)
	r.Post("/v1/v1/chat/completions", chatHandler.ChatCompletions)
	r.Post("/v1/v1/responses", responsesHandler.Responses)
	r.Get("/v1/v1/responses/{response_id}", responsesHandler.GetResponseByID)
	r.Post("/v1/v1/files", filesHandler.UploadFile)
	r.Post("/v1/v1/embeddings", embeddingsHandler.Embeddings)
	// Root OpenAI aliases support clients configured with the bare DeepSeek_Web_To_API service URL.
	r.Get("/models", modelsHandler.ListModels)
	r.Get("/models/{model_id}", modelsHandler.GetModel)
	r.Post("/chat/completions", chatHandler.ChatCompletions)
	r.Post("/responses", responsesHandler.Responses)
	r.Get("/responses/{response_id}", responsesHandler.GetResponseByID)
	r.Post("/files", filesHandler.UploadFile)
	r.Post("/embeddings", embeddingsHandler.Embeddings)
	claude.RegisterRoutes(r, claudeHandler)
	gemini.RegisterRoutes(r, geminiHandler)
	r.Route("/admin", func(ar chi.Router) {
		ar.Use(adminBrowserNavigationFallback(webuiHandler))
		admin.RegisterRoutes(ar, adminHandler)
	})
	webui.RegisterRoutes(r, webuiHandler)
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/admin/") && webuiHandler.HandleAdminFallback(w, req) {
			return
		}
		http.NotFound(w, req)
	})

	app := &App{Store: store, Pool: pool, Resolver: resolver, DS: dsClient, Router: r}
	if intervalMinutes := store.RuntimeAccountHealthCheckIntervalMinutes(); intervalMinutes > 0 {
		monitorCtx, cancel := context.WithCancel(context.Background())
		app.stopAccountHealthMonitor = cancel
		startAccountHealthMonitor(monitorCtx, store, pool, resolver, dsClient, time.Duration(intervalMinutes)*time.Minute)
	}
	return app, nil
}

func (a *App) Close() {
	if a == nil || a.stopAccountHealthMonitor == nil {
		return
	}
	a.stopAccountHealthMonitor()
}

func adminBrowserNavigationFallback(webuiHandler *webui.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isAdminBrowserNavigation(r) && webuiHandler.HandleAdminFallback(w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isAdminBrowserNavigation(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	path := strings.TrimSpace(r.URL.Path)
	if !strings.HasPrefix(path, "/admin/") {
		return false
	}
	rel := strings.TrimPrefix(path, "/admin/")
	if rel == "" || strings.Contains(rel, ".") {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if !strings.Contains(accept, "text/html") {
		return false
	}
	mode := strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode"))
	return mode == "" || strings.EqualFold(mode, "navigate")
}

func timeout(d time.Duration) func(http.Handler) http.Handler {
	if d <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return middleware.Timeout(d)
}

func filteredLogger() func(http.Handler) http.Handler {
	color := !isWindowsRuntime()
	base := &middleware.DefaultLogFormatter{
		Logger:  log.New(os.Stdout, "", log.LstdFlags),
		NoColor: !color,
	}
	return middleware.RequestLogger(&filteredLogFormatter{base: base})
}

func isWindowsRuntime() bool {
	return runtime.GOOS == "windows"
}

type filteredLogFormatter struct {
	base *middleware.DefaultLogFormatter
}

func (f *filteredLogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	if r != nil && r.Method == http.MethodGet {
		path := strings.TrimSpace(r.URL.Path)
		if path == "/admin/chat-history" || strings.HasPrefix(path, "/admin/chat-history/") {
			return noopLogEntry{}
		}
	}
	return f.base.NewLogEntry(r)
}

type noopLogEntry struct{}

func (noopLogEntry) Write(_ int, _ int, _ http.Header, _ time.Duration, _ interface{}) {}

func (noopLogEntry) Panic(_ interface{}, _ []byte) {}

var defaultCORSAllowHeaders = []string{
	"Content-Type",
	"Authorization",
	"X-API-Key",
	"X-DeepSeek-Web-To-API-Target-Account",
	"X-DeepSeek-Web-To-API-Source",
	"X-Ds2-Target-Account",
	"X-Ds2-Source",
	"X-DeepSeek-Web-To-API-Conversation-ID",
	"X-Ds2-Conversation-ID",
	"X-Conversation-ID",
	"Conversation-ID",
	"X-Codex-Conversation-ID",
	"X-Codex-Session-ID",
	"X-OpenCode-Conversation-ID",
	"X-OpenCode-Session-ID",
	"OpenAI-Conversation-ID",
	"Anthropic-Conversation-ID",
	"X-Goog-Api-Key",
	"Anthropic-Version",
	"Anthropic-Beta",
}

var blockedCORSRequestHeaders = map[string]struct{}{
	"x-deepseek-web-to-api-internal-token":       {},
	"x-deepseek-web-to-api-session-affinity-key": {},
	"x-ds2-internal-token":                       {},
	"x-ds2-session-affinity-key":                 {},
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		// Content-Security-Policy is applied to admin/webui responses
		// only — the public API plane returns JSON / SSE consumed by
		// SDKs that have no DOM, and shipping CSP there is noise.
		// `default-src 'self'` blocks every external resource by
		// default; `connect-src 'self' https://api.github.com` lets
		// the dashboard's "check for new release" probe still reach
		// the GitHub API; `script-src 'self'` blocks inline scripts
		// (the React build emits hashed asset bundles, no inline);
		// `style-src 'self' 'unsafe-inline'` is required by Tailwind
		// keyframe + utility-class injection at runtime.
		path := ""
		if r.URL != nil {
			path = r.URL.Path
		}
		if isWebAdminPath(path) {
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; "+
					"script-src 'self'; "+
					"style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data:; "+
					"font-src 'self' data:; "+
					"connect-src 'self' https://api.github.com; "+
					"frame-ancestors 'none'; "+
					"base-uri 'self'; "+
					"form-action 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// isWebAdminPath returns true for the WebUI HTML / asset surface where
// CSP is meaningful. The admin JSON API (/admin/*) and the public LLM
// proxy plane do not render HTML and are excluded.
func isWebAdminPath(path string) bool {
	if path == "" || path == "/" {
		return true
	}
	switch {
	case strings.HasPrefix(path, "/static/"),
		strings.HasPrefix(path, "/admin/"),
		strings.HasPrefix(path, "/webui/"),
		strings.HasPrefix(path, "/assets/"):
		return true
	}
	return false
}

func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	// /admin/* is the privileged plane — Bearer JWT authentication and
	// stateful state mutation. Only echo the Origin when it is the same
	// host the request was delivered to (same-origin). For any other
	// origin we omit Access-Control-Allow-Origin entirely so the
	// browser blocks the request before any handler runs. This closes
	// the wildcard-reflect gap that combined with the localStorage JWT
	// pattern would otherwise enable cross-origin token theft chains
	// for an XSS-first attacker.
	if isAdminPlane(path) {
		if origin != "" && originMatchesHost(origin, r) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			addVaryHeaderToken(w.Header(), "Origin")
		}
		// else: leave Access-Control-Allow-Origin unset; preflight fails.
	} else if origin == "" {
		// Public API plane (/v1/*, /healthz, /readyz, etc.). Bearer-auth
		// requests from non-browser SDKs do not set Origin — emit the
		// open wildcard so existing OpenAI/Claude/Gemini SDK clients
		// continue to work unchanged.
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		addVaryHeaderToken(w.Header(), "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", buildCORSAllowHeaders(r))
	w.Header().Set("Access-Control-Max-Age", "600")
	addVaryHeaderToken(w.Header(), "Access-Control-Request-Headers")
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Access-Control-Request-Private-Network")), "true") {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		addVaryHeaderToken(w.Header(), "Access-Control-Request-Private-Network")
	}
}

// isAdminPlane returns true for paths that mutate privileged state and
// must not accept cross-origin requests. The webui static assets at
// /admin/index.html etc. are intentionally NOT included — those are
// fetched same-origin by the browser before any cross-origin script
// has a chance to run.
func isAdminPlane(path string) bool {
	return strings.HasPrefix(path, "/admin/")
}

// originMatchesHost compares the scheme://host of the Origin header
// against the request's own Host header. The Origin string is parsed
// strictly — scheme and host must match; port presence/absence on
// either side requires explicit equality.
func originMatchesHost(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func buildCORSAllowHeaders(r *http.Request) string {
	names := make([]string, 0, len(defaultCORSAllowHeaders)+4)
	seen := make(map[string]struct{}, len(defaultCORSAllowHeaders)+4)
	for _, name := range defaultCORSAllowHeaders {
		appendCORSHeaderName(&names, seen, name)
	}
	if r == nil {
		return strings.Join(names, ", ")
	}
	for _, name := range splitCORSRequestHeaders(r.Header.Get("Access-Control-Request-Headers")) {
		appendCORSHeaderName(&names, seen, name)
	}
	for _, name := range requestmeta.ConversationIDHeaders {
		appendCORSHeaderName(&names, seen, name)
	}
	return strings.Join(names, ", ")
}

func splitCORSRequestHeaders(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if !isValidCORSHeaderToken(name) {
			continue
		}
		if _, blocked := blockedCORSRequestHeaders[strings.ToLower(name)]; blocked {
			continue
		}
		out = append(out, name)
	}
	return out
}

func appendCORSHeaderName(dst *[]string, seen map[string]struct{}, name string) {
	name = strings.TrimSpace(name)
	if !isValidCORSHeaderToken(name) {
		return
	}
	key := strings.ToLower(name)
	if _, blocked := blockedCORSRequestHeaders[key]; blocked {
		return
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*dst = append(*dst, name)
}

func isValidCORSHeaderToken(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func addVaryHeaderToken(h http.Header, token string) {
	if h == nil {
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	current := h.Values("Vary")
	seen := map[string]struct{}{}
	merged := make([]string, 0, len(current)+1)
	for _, value := range current {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, name)
		}
	}
	key := strings.ToLower(token)
	if _, ok := seen[key]; !ok {
		merged = append(merged, token)
	}
	h.Set("Vary", strings.Join(merged, ", "))
}

func WriteUnhandledError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "api_error", "message": "Internal Server Error", "detail": err.Error()}})
}

// safetyLLMConfigSource adapts *config.Store to safetyllm.ConfigSource
// so the checker reads the latest snapshot per-request. PUT
// /admin/settings → Store.Update mutates the snapshot in-place under
// the store's RWMutex, so subsequent SafetyLLMCheckConfig() calls see
// the new value without polling or callbacks.
type safetyLLMConfigSource struct {
	store *config.Store
}

func (s safetyLLMConfigSource) SafetyLLMCheckConfig() safetyllm.Config {
	if s.store == nil {
		return safetyllm.DefaultConfig()
	}
	return buildSafetyLLMConfig(s.store.Snapshot().Safety.LLMCheck)
}

// buildSafetyLLMConfig translates the operator-facing
// config.SafetyLLMCheckConfig snapshot into the runtime safetyllm.Config.
// Default values come from safetyllm.DefaultConfig() so any field the
// operator left unset stays at the safe default.
func buildSafetyLLMConfig(src config.SafetyLLMCheckConfig) safetyllm.Config {
	cfg := safetyllm.DefaultConfig()
	if src.Enabled != nil {
		cfg.Enabled = *src.Enabled
	}
	if model := strings.TrimSpace(src.Model); model != "" {
		cfg.Model = model
	}
	if src.TimeoutMs > 0 {
		cfg.TimeoutMs = src.TimeoutMs
	}
	if src.FailOpen != nil {
		cfg.FailOpen = *src.FailOpen
	}
	if src.CacheTTLSeconds > 0 {
		cfg.CacheTTLSeconds = src.CacheTTLSeconds
	}
	if src.CacheMaxEntries > 0 {
		cfg.CacheMaxEntries = src.CacheMaxEntries
	}
	if src.MinInputChars > 0 {
		cfg.MinInputChars = src.MinInputChars
	}
	if src.MaxInputChars > 0 {
		cfg.MaxInputChars = src.MaxInputChars
	}
	if src.MaxConcurrent > 0 {
		cfg.MaxConcurrent = src.MaxConcurrent
	}
	return cfg
}
