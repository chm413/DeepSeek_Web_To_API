package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestAPIRoutesRemainRegistered(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":["k1"],"accounts":[{"email":"u@example.com","password":"p"}]}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "0")

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp() error: %v", err)
	}
	routes, ok := app.Router.(chi.Routes)
	if !ok {
		t.Fatalf("app router does not expose chi routes: %T", app.Router)
	}

	got := map[string]bool{}
	if err := chi.Walk(routes, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got[fmt.Sprintf("%s %s", method, route)] = true
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for _, want := range []string{
		"GET /v1/models",
		"GET /v1/models/{model_id}",
		"POST /v1/chat/completions",
		"POST /v1/responses",
		"GET /v1/responses/{response_id}",
		"POST /v1/files",
		"POST /v1/embeddings",
		"GET /v1/v1/models",
		"GET /v1/v1/models/{model_id}",
		"POST /v1/v1/chat/completions",
		"POST /v1/v1/responses",
		"GET /v1/v1/responses/{response_id}",
		"POST /v1/v1/files",
		"POST /v1/v1/embeddings",
		"GET /models",
		"GET /models/{model_id}",
		"POST /chat/completions",
		"POST /responses",
		"GET /responses/{response_id}",
		"POST /files",
		"POST /embeddings",
		"GET /anthropic/v1/models",
		"POST /anthropic/v1/messages",
		"POST /anthropic/v1/messages/count_tokens",
		"POST /v1/messages",
		"POST /v1/v1/messages",
		"POST /messages",
		"POST /v1/messages/count_tokens",
		"POST /v1/v1/messages/count_tokens",
		"POST /messages/count_tokens",
		"POST /v1beta/models/{model}:generateContent",
		"POST /v1beta/models/{model}:streamGenerateContent",
		"POST /v1/models/{model}:generateContent",
		"POST /v1/models/{model}:streamGenerateContent",
		"POST /admin/login",
		"GET /admin/verify",
		"GET /admin/config",
		"POST /admin/config",
		"GET /admin/settings",
		"PUT /admin/settings",
		"POST /admin/settings/password",
		"POST /admin/config/import",
		"GET /admin/config/export",
		"POST /admin/keys",
		"PUT /admin/keys/{key}",
		"DELETE /admin/keys/{key}",
		"GET /admin/proxies",
		"GET /admin/proxies/core",
		"PUT /admin/proxies/core",
		"POST /admin/proxies/core/download",
		"GET /admin/proxies/policy",
		"PUT /admin/proxies/policy",
		"GET /admin/proxies/subscriptions",
		"POST /admin/proxies/subscriptions",
		"PUT /admin/proxies/subscriptions/{subscriptionID}",
		"DELETE /admin/proxies/subscriptions/{subscriptionID}",
		"POST /admin/proxies/subscriptions/{subscriptionID}/refresh",
		"POST /admin/proxies/subscriptions/refresh-all",
		"POST /admin/proxies",
		"PUT /admin/proxies/{proxyID}",
		"DELETE /admin/proxies/{proxyID}",
		"POST /admin/proxies/test",
		"POST /admin/proxies/test-batch",
		"POST /admin/proxies/actions",
		"GET /admin/accounts",
		"POST /admin/accounts",
		"POST /admin/accounts/batch",
		"POST /admin/accounts/batch/actions",
		"PUT /admin/accounts/{identifier}",
		"DELETE /admin/accounts/{identifier}",
		"PUT /admin/accounts/{identifier}/proxy",
		"GET /admin/queue/status",
		"POST /admin/accounts/test",
		"POST /admin/accounts/test-all",
		"POST /admin/accounts/sessions/delete-all",
		"POST /admin/import",
		"POST /admin/test",
		"POST /admin/dev/raw-samples/capture",
		"GET /admin/dev/raw-samples/query",
		"POST /admin/dev/raw-samples/save",
		"GET /admin/export",
		"GET /admin/dev/captures",
		"DELETE /admin/dev/captures",
		"GET /admin/chat-history",
		"GET /admin/chat-history/{id}",
		"DELETE /admin/chat-history",
		"DELETE /admin/chat-history/{id}",
		"PUT /admin/chat-history/settings",
		"GET /admin/version",
		"GET /admin/metrics/overview",
	} {
		if !got[want] {
			t.Fatalf("expected route %s to be registered", want)
		}
	}
}

func TestAdminBrowserNavigationUsesSPAFallbackBeforeAuth(t *testing.T) {
	staticDir := t.TempDir()
	indexPath := filepath.Join(staticDir, "index.html")
	if err := os.WriteFile(indexPath, []byte(`<!doctype html><title>admin-spa</title>`), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":["k1"],"accounts":[{"email":"u@example.com","password":"p"}],"server":{"auto_build_webui":false}}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "0")
	t.Setenv("DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR", staticDir)

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp() error: %v", err)
	}

	for _, path := range []string{"/admin/accounts", "/admin/proxies", "/admin/settings"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		rec := httptest.NewRecorder()
		app.Router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected %s navigation to return SPA 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "<!doctype html><title>admin-spa</title>" {
			t.Fatalf("expected %s navigation to return index.html, got %q", path, got)
		}
	}
}

func TestAdminAPIStillRequiresAuthForJSONRequests(t *testing.T) {
	staticDir := t.TempDir()
	indexPath := filepath.Join(staticDir, "index.html")
	if err := os.WriteFile(indexPath, []byte(`<!doctype html><title>admin-spa</title>`), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":["k1"],"accounts":[{"email":"u@example.com","password":"p"}],"server":{"auto_build_webui":false}}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "0")
	t.Setenv("DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR", staticDir)

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp() error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected JSON API request to require auth, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "authentication required") {
		t.Fatalf("expected auth error body, got %q", got)
	}
}
