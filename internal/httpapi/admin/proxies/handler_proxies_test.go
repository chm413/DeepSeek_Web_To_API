package proxies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func newAdminProxyTestHandler(t *testing.T, raw string) *Handler {
	t.Helper()
	// Keep fixtures that model a successful health probe fresh as the routing
	// policy now rejects stale probe results.
	raw = strings.ReplaceAll(raw, `"last_test_at_unix":10`, `"last_test_at_unix":`+fmt.Sprint(time.Now().Unix()))
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", raw)
	store := config.LoadStore()
	return &Handler{
		Store: store,
		Pool:  account.NewPool(store),
		DS:    &testingDSMock{},
	}
}

func TestAddProxyPersistsNormalizedProxy(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{"accounts":[]}`)

	r := chi.NewRouter()
	r.Post("/admin/proxies", h.addProxy)

	req := httptest.NewRequest(http.MethodPost, "/admin/proxies", bytes.NewBufferString(`{
		"name":"  HK Exit  ",
		"type":" SOCKS5H ",
		"host":" 127.0.0.1 ",
		"port":1081,
		"username":" user ",
		"password":" pass "
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	proxies := h.Store.Snapshot().Proxies
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy, got %d", len(proxies))
	}
	if proxies[0].Name != "HK Exit" {
		t.Fatalf("unexpected proxy name: %#v", proxies[0])
	}
	if proxies[0].Type != "socks5h" {
		t.Fatalf("unexpected proxy type: %#v", proxies[0])
	}
	if proxies[0].Username != "user" || proxies[0].Password != "pass" {
		t.Fatalf("expected trimmed credentials, got %#v", proxies[0])
	}
	if proxies[0].ID == "" {
		t.Fatalf("expected generated proxy id, got %#v", proxies[0])
	}
}

func TestAddCoreProxyStoresURIWithoutExposingIt(t *testing.T) {
	router := newHTTPAdminHarness(t, `{"accounts":[]}`, &testingDSMock{})
	uri := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com"

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies", []byte(`{
		"name":"VLESS node",
		"type":"vless",
		"uri":"`+uri+`"
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("add core proxy status=%d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(uri)) || bytes.Contains(rec.Body.Bytes(), []byte(`"uri"`)) {
		t.Fatalf("response exposed proxy URI: %s", rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	proxy, _ := payload["proxy"].(map[string]any)
	if hasURI, _ := proxy["has_uri"].(bool); !hasURI {
		t.Fatalf("expected has_uri=true, got %#v", proxy)
	}
	if managed, _ := proxy["core_managed"].(bool); !managed {
		t.Fatalf("expected core_managed=true, got %#v", proxy)
	}
	if proxy["host"] != "example.com" || proxy["port"] != float64(443) {
		t.Fatalf("expected derived endpoint, got %#v", proxy)
	}

	readRec := httptest.NewRecorder()
	router.ServeHTTP(readRec, adminReq(http.MethodGet, "/config", nil))
	if readRec.Code != http.StatusOK {
		t.Fatalf("config read status=%d body=%s", readRec.Code, readRec.Body.String())
	}
	if bytes.Contains(readRec.Body.Bytes(), []byte(uri)) || bytes.Contains(readRec.Body.Bytes(), []byte(`"uri"`)) {
		t.Fatalf("safe config response exposed proxy URI: %s", readRec.Body.String())
	}
}

func TestProxyResponseMasksUsernameAndBlankEditPreservesIt(t *testing.T) {
	response := proxyResponse(config.Proxy{ID: "proxy-1", Username: "proxy-user", LastTestError: "request https://user:secret@example.invalid/path?token=secret failed"})
	if _, exposed := response["username"]; exposed {
		t.Fatalf("proxy response exposed raw username: %#v", response)
	}
	if response["has_username"] != true || response["username_preview"] != "pr****er" {
		t.Fatalf("unexpected username metadata: %#v", response)
	}
	if strings.Contains(response["last_test_error"].(string), "secret") {
		t.Fatalf("proxy test error exposed secret: %#v", response["last_test_error"])
	}

	h := newAdminProxyTestHandler(t, `{"proxies":[{"id":"proxy-1","type":"socks5","host":"127.0.0.1","port":1080,"username":"proxy-user","password":"proxy-password"}]}`)
	r := chi.NewRouter()
	r.Put("/admin/proxies/{proxyID}", h.updateProxy)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/admin/proxies/proxy-1", bytes.NewBufferString(`{"type":"socks5","host":"127.0.0.1","port":1081}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("blank credential edit failed: %d %s", rec.Code, rec.Body.String())
	}
	updated := h.Store.Snapshot().Proxies[0]
	if updated.Username != "proxy-user" || updated.Password != "proxy-password" || updated.Port != 1081 {
		t.Fatalf("blank edit did not preserve credentials: %#v", updated)
	}
}

func TestSubscriptionResponseSanitizesLegacyLastError(t *testing.T) {
	response := subscriptionResponse(config.ProxySubscription{
		ID:        "sub-1",
		LastError: "legacy fetch failed for https://user:secret@example.invalid/list?token=private-token",
	})
	lastError, _ := response["last_error"].(string)
	for _, secret := range []string{"user:secret", "private-token", "example.invalid"} {
		if strings.Contains(lastError, secret) {
			t.Fatalf("subscription response leaked legacy error data %q: %q", secret, lastError)
		}
	}
}

func TestUpdateCoreProxyBlankURIPreservesStoredSecret(t *testing.T) {
	uri := "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com"
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","name":"Node 1","type":"vless","uri":"`+uri+`"}]
	}`)
	r := chi.NewRouter()
	r.Put("/admin/proxies/{proxyID}", h.updateProxy)

	req := httptest.NewRequest(http.MethodPut, "/admin/proxies/proxy-1", bytes.NewBufferString(`{
		"name":"Renamed node",
		"type":"vless",
		"uri":""
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update core proxy status=%d body=%s", rec.Code, rec.Body.String())
	}
	proxies := h.Store.Snapshot().Proxies
	if len(proxies) != 1 || proxies[0].URI != uri {
		t.Fatalf("expected stored URI to be preserved, got %#v", proxies)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(uri)) || bytes.Contains(rec.Body.Bytes(), []byte(`"uri"`)) {
		t.Fatalf("update response exposed proxy URI: %s", rec.Body.String())
	}
}

func TestAddProxyDoesNotFailOnUnrelatedInvalidRuntimeConfig(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"keys":["k1"],
		"runtime":{
			"account_max_inflight":8,
			"global_max_inflight":4
		}
	}`, &testingDSMock{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies", []byte(`{
		"name":"HK Exit",
		"type":"socks5h",
		"host":"127.0.0.1",
		"port":1080
	}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected add proxy success despite unrelated runtime issue, got %d body=%s", rec.Code, rec.Body.String())
	}

	readRec := httptest.NewRecorder()
	router.ServeHTTP(readRec, adminReq(http.MethodGet, "/config", nil))
	if readRec.Code != http.StatusOK {
		t.Fatalf("config read status=%d body=%s", readRec.Code, readRec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(readRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	proxies, _ := payload["proxies"].([]any)
	if len(proxies) != 1 {
		t.Fatalf("expected proxy to be persisted, got %#v", payload["proxies"])
	}
}

func TestDeleteProxyRejectsAssignedAccountWithoutFallbackWithoutMutatingRoutes(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","name":"Node 1","type":"socks5","host":"127.0.0.1","port":1080}],
		"accounts":[{"email":"u@example.com","password":"pwd","proxy_id":"proxy-1"}]
	}`)

	r := chi.NewRouter()
	r.Delete("/admin/proxies/{proxyID}", h.deleteProxy)

	req := httptest.NewRequest(http.MethodDelete, "/admin/proxies/proxy-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got %d body=%s", rec.Code, rec.Body.String())
	}
	snap := h.Store.Snapshot()
	if len(snap.Proxies) != 1 {
		t.Fatalf("proxy deletion should be atomic, got %#v", snap.Proxies)
	}
	if len(snap.Accounts) != 1 {
		t.Fatalf("expected account kept, got %#v", snap.Accounts)
	}
	if snap.Accounts[0].ProxyID != "proxy-1" {
		t.Fatalf("assigned route was silently cleared: %#v", snap.Accounts[0])
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("configured fallback")) {
		t.Fatalf("expected fallback routing error, got %s", rec.Body.String())
	}
}

func TestDeleteProxyMigratesAssignedAccountToFallback(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[
			{"id":"proxy-1","name":"Retired","type":"socks5","host":"127.0.0.1","port":1080},
			{"id":"proxy-2","name":"Fallback","type":"socks5","host":"127.0.0.1","port":1081}
		],
		"proxy_policy":{"fallback_proxy_id":"proxy-2"},
		"accounts":[{"email":"u@example.com","password":"pwd","token":"stale","proxy_id":"proxy-1"}]
	}`)
	h.DS = nil // Verify the persisted routing change independently of relogin.

	r := chi.NewRouter()
	r.Delete("/admin/proxies/{proxyID}", h.deleteProxy)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/admin/proxies/proxy-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected fallback migration, got %d body=%s", rec.Code, rec.Body.String())
	}
	snap := h.Store.Snapshot()
	if len(snap.Proxies) != 1 || snap.Proxies[0].ID != "proxy-2" {
		t.Fatalf("unexpected remaining proxies: %#v", snap.Proxies)
	}
	if len(snap.Accounts) != 1 || snap.Accounts[0].ProxyID != "proxy-2" || snap.Accounts[0].Token != "" {
		t.Fatalf("manual account was not moved to fallback with token invalidation: %#v", snap.Accounts)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("node_deleted_fallback")) {
		t.Fatalf("missing route-change summary: %s", rec.Body.String())
	}
}

func TestUpdateProxyResponseDoesNotExposeStoredPassword(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","name":"Node 1","type":"socks5h","host":"127.0.0.1","port":1080,"username":"u","password":"secret"}]
	}`)

	r := chi.NewRouter()
	r.Put("/admin/proxies/{proxyID}", h.updateProxy)

	req := httptest.NewRequest(http.MethodPut, "/admin/proxies/proxy-1", bytes.NewBufferString(`{
		"name":"Node 1",
		"type":"socks5h",
		"host":"127.0.0.2",
		"port":1081,
		"username":"u2"
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	proxy, _ := payload["proxy"].(map[string]any)
	if _, exists := proxy["password"]; exists {
		t.Fatalf("response should not expose password, got %#v", proxy)
	}
	if hasPassword, _ := proxy["has_password"].(bool); !hasPassword {
		t.Fatalf("expected has_password=true, got %#v", proxy)
	}
}

func TestUpdateAccountProxyAssignsProxyID(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","name":"Node 1","type":"socks5h","host":"127.0.0.1","port":1080}],
		"accounts":[{"email":"u@example.com","password":"pwd"}]
	}`)

	r := chi.NewRouter()
	r.Put("/admin/accounts/{identifier}/proxy", h.updateAccountProxy)

	req := httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com/proxy", bytes.NewBufferString(`{"proxy_id":"proxy-1"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	acc, ok := h.Store.FindAccount("u@example.com")
	if !ok {
		t.Fatal("expected account")
	}
	if acc.ProxyID != "proxy-1" {
		t.Fatalf("expected proxy assigned, got %#v", acc)
	}
	if acc.Token != "token" {
		t.Fatalf("expected relogin token after egress change, got %#v", acc)
	}
}

func TestUpdateAccountProxyEnablesAutomaticRouteAndRelogins(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxy_policy":{"auto_route_enabled":true},
		"proxies":[{"id":"proxy-1","name":"Node 1","type":"socks5h","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":true,"last_latency_ms":25}],
		"accounts":[{"email":"u@example.com","password":"pwd"}]
	}`)
	r := chi.NewRouter()
	r.Put("/admin/accounts/{identifier}/proxy", h.updateAccountProxy)
	req := httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com/proxy", bytes.NewBufferString(`{"proxy_id":"","auto_route":true}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	acc, ok := h.Store.FindAccount("u@example.com")
	if !ok || !acc.ProxyAutoRoute || acc.ProxyID != "proxy-1" || acc.Token != "token" {
		t.Fatalf("automatic route was not assigned and relogged: %#v", acc)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["proxy_id"] != "proxy-1" || payload["auto_route"] != true {
		t.Fatalf("unexpected automatic route response: %#v", payload)
	}
}

func TestUpdateAccountProxyKeepsHealthyRouteWhenEnablingAutomaticMode(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxy_policy":{"auto_route_enabled":true},
		"proxies":[
			{"id":"proxy-1","type":"socks5h","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":true,"last_latency_ms":25},
			{"id":"proxy-2","type":"socks5h","host":"127.0.0.1","port":1081,"last_test_at_unix":10,"last_test_success":true,"last_latency_ms":10}
		],
		"accounts":[{"email":"u@example.com","password":"pwd","proxy_id":"proxy-1","token":"keep-token"}]
	}`)
	if err := h.Store.UpdateAccountToken("u@example.com", "keep-token"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	r := chi.NewRouter()
	r.Put("/admin/accounts/{identifier}/proxy", h.updateAccountProxy)
	req := httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com/proxy", bytes.NewBufferString(`{"auto_route":true}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	acc, ok := h.Store.FindAccount("u@example.com")
	if !ok || !acc.ProxyAutoRoute || acc.ProxyID != "proxy-1" || acc.Token != "keep-token" {
		t.Fatalf("healthy route should remain sticky: %#v", acc)
	}
}

func TestUpdateAccountProxyRejectsAutomaticRouteWithoutPassword(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxy_policy":{"auto_route_enabled":true},
		"proxies":[{"id":"proxy-1","type":"socks5h","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":true}],
		"accounts":[{"email":"u@example.com","token":"token-only"}]
	}`)
	r := chi.NewRouter()
	r.Put("/admin/accounts/{identifier}/proxy", h.updateAccountProxy)
	req := httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com/proxy", bytes.NewBufferString(`{"auto_route":true}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTestProxyUsesStoredProxy(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","name":"Node 1","type":"socks5h","host":"127.0.0.1","port":1080}]
	}`)

	original := proxyConnectivityTester
	defer func() { proxyConnectivityTester = original }()

	var got config.Proxy
	proxyConnectivityTester = func(_ context.Context, proxy config.Proxy, _ config.ProxyCoreConfig) map[string]any {
		got = proxy
		return map[string]any{
			"success":       true,
			"proxy_id":      proxy.ID,
			"proxy_type":    proxy.Type,
			"response_time": 12,
		}
	}

	r := chi.NewRouter()
	r.Post("/admin/proxies/test", h.testProxy)

	req := httptest.NewRequest(http.MethodPost, "/admin/proxies/test", bytes.NewBufferString(`{"proxy_id":"proxy-1"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got.ID != "proxy-1" || got.Type != "socks5h" {
		t.Fatalf("expected stored proxy passed to tester, got %#v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := payload["success"].(bool); !ok {
		t.Fatalf("expected success payload, got %#v", payload)
	}
}

func TestReconcileAndSyncReloginsCommittedAutomaticRouteOnSyncFailure(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxy_policy":{"auto_route_enabled":true},
		"proxies":[{"id":"healthy","type":"socks5","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":true}],
		"accounts":[{"email":"auto@example.com","password":"pwd","proxy_auto_route":true,"token":"old-token"}]
	}`)
	originalSync := syncProxyRoutes
	t.Cleanup(func() { syncProxyRoutes = originalSync })
	syncProxyRoutes = func(context.Context, xrayproxy.CoreConfigStore) error {
		return errors.New("simulated xray sync failure")
	}

	results, err := h.reconcileAndSyncProxyRoutes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "simulated xray sync failure") {
		t.Fatalf("expected sync failure, results=%#v err=%v", results, err)
	}
	account, ok := h.Store.FindAccount("auto@example.com")
	if !ok {
		t.Fatal("automatic account disappeared")
	}
	if account.ProxyID != "healthy" || account.Token != "token" {
		t.Fatalf("committed route was not compensated with a relogin: %#v", account)
	}
	if len(results) != 1 || !results[account.Identifier()]["success"].(bool) {
		t.Fatalf("expected relogin result for committed route, got %#v", results)
	}
}
