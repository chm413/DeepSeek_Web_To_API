package requestguard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

// v1.0.14: substring-based banned_content / banned_regex / jailbreak.patterns
// matching was removed in favor of LLM binary review (internal/safetyllm).
// The middleware no longer blocks on content payloads — only IP /
// conversation-id blocklists. The previous TestMiddlewareBlocksConfiguredContent
// + TestAutoBanTripsAfterRepeatedContentViolations cases targeted the
// removed mechanism and were dropped; auto-ban semantics now live in the
// safetyllm package under llm_safety_blocked verdicts.

func TestMiddlewareAddsRequestMetadata(t *testing.T) {
	store := testStore(t, `{
		"keys":["k"],
		"admin":{"key":"admin","jwt_secret":"secret","jwt_expire_hours":24}
	}`)
	defer func() { _ = store.Close() }()
	var seenIP, seenConversation string
	handler := Middleware(Options{Store: store})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta, ok := FromContext(r.Context())
		if !ok {
			t.Fatal("missing request metadata")
		}
		seenIP = meta.ClientIP
		seenConversation = meta.ConversationID
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"ok"}]}`))
	req.RemoteAddr = "198.51.100.8:1234"
	req.Header.Set("X-OpenCode-Session-ID", "opencode-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%q", rec.Code, rec.Body.String())
	}
	if seenIP != "198.51.100.8" || seenConversation != "opencode-1" {
		t.Fatalf("metadata ip=%q conversation=%q", seenIP, seenConversation)
	}
}

func TestRequestBodyShouldNotEagerlyCopyAdminPayloads(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"admin_key":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	if requestBodyShouldBeRead(req) {
		t.Fatal("admin login body must be left for its route-specific size limit")
	}
}

func testStore(t *testing.T, cfg string) *config.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", path)
	t.Setenv("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH", filepath.Join(dir, "accounts.sqlite"))
	store, err := config.LoadStoreWithError()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return store
}
