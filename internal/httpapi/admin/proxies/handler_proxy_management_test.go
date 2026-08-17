package proxies

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/config"
)

func TestProxyPolicyUpdatePersistsEffectiveSettings(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"accounts":[],
		"proxies":[{"id":"proxy-1","name":"Fallback","type":"socks5","host":"127.0.0.1","port":1080}]
	}`, &testingDSMock{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPut, "/proxies/policy", []byte(`{
		"health_check_enabled":true,
		"health_check_interval_minutes":5,
		"auto_disable_after_failures":2,
		"auto_enable_on_recovery":true,
		"fallback_proxy_id":"proxy-1",
		"subscription_update_interval_minutes":30,
		"test_concurrency":8
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("policy update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode policy response: %v", err)
	}
	policy, _ := payload["policy"].(map[string]any)
	if policy["fallback_proxy_id"] != "proxy-1" || policy["test_concurrency"] != float64(8) {
		t.Fatalf("unexpected policy response: %#v", policy)
	}
}

func TestProxySubscriptionURLIsStoredButNeverReturned(t *testing.T) {
	router := newHTTPAdminHarness(t, `{"accounts":[]}`, &testingDSMock{})
	secretURL := "https://user:secret@example.com/subscription?token=private"
	body, _ := json.Marshal(map[string]any{"name": "Airport", "url": secretURL, "refresh": false})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies/subscriptions", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("subscription add status=%d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(secretURL)) || bytes.Contains(rec.Body.Bytes(), []byte(`"url"`)) {
		t.Fatalf("subscription response exposed URL: %s", rec.Body.String())
	}

	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, adminReq(http.MethodGet, "/proxies/subscriptions", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("subscription list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	if bytes.Contains(listRec.Body.Bytes(), []byte(secretURL)) || bytes.Contains(listRec.Body.Bytes(), []byte(`"url"`)) {
		t.Fatalf("subscription list exposed URL: %s", listRec.Body.String())
	}
}

func TestDeleteSubscriptionRetainsAssignedNodeDisabledAndClearsFallback(t *testing.T) {
	raw := `{
		"proxy_subscriptions":[{"id":"sub-1","name":"Airport","url":"https://example.com/sub"}],
		"proxies":[{"id":"proxy-1","name":"Node","type":"socks5","host":"127.0.0.1","port":1080,"subscription_id":"sub-1"}],
		"proxy_policy":{"fallback_proxy_id":"proxy-1"},
		"accounts":[{"email":"user@example.com","password":"pwd","proxy_id":"proxy-1"}]
	}`
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", raw)
	store := config.LoadStore()
	h := newHTTPAdminHarnessWithStore(store)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq(http.MethodDelete, "/proxies/subscriptions/sub-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("subscription delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	snapshot := store.Snapshot()
	if len(snapshot.ProxySubscriptions) != 0 || snapshot.ProxyPolicy.FallbackProxyID != "" {
		t.Fatalf("expected subscription and fallback cleared, got %#v", snapshot)
	}
	if len(snapshot.Proxies) != 1 || !snapshot.Proxies[0].Disabled || snapshot.Proxies[0].DisabledReason != config.ProxyDisabledSubscriptionRemoved {
		t.Fatalf("expected assigned node retained disabled, got %#v", snapshot.Proxies)
	}
}

func TestProxyBatchActionDisablesSelectedNodes(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"accounts":[],
		"proxies":[
			{"id":"proxy-1","type":"socks5","host":"127.0.0.1","port":1080},
			{"id":"proxy-2","type":"socks5","host":"127.0.0.1","port":1081}
		]
	}`, &testingDSMock{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies/actions", []byte(`{"proxy_ids":["proxy-2"],"action":"disable"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch disable status=%d body=%s", rec.Code, rec.Body.String())
	}

	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, adminReq(http.MethodGet, "/proxies", nil))
	var payload struct {
		Items []struct {
			ID             string `json:"id"`
			Disabled       bool   `json:"disabled"`
			DisabledReason string `json:"disabled_reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode proxy list: %v", err)
	}
	if len(payload.Items) != 2 || payload.Items[0].Disabled || !payload.Items[1].Disabled || payload.Items[1].DisabledReason != config.ProxyDisabledManual {
		t.Fatalf("unexpected batch state: %#v", payload.Items)
	}
}

func TestProxyBatchDeleteRejectsSelectedFallbackAtomically(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"accounts":[{"email":"user@example.com","password":"pwd","proxy_id":"proxy-1"}],
		"proxies":[
			{"id":"proxy-1","type":"socks5","host":"127.0.0.1","port":1080},
			{"id":"proxy-2","type":"socks5","host":"127.0.0.1","port":1081},
			{"id":"proxy-3","type":"socks5","host":"127.0.0.1","port":1082}
		],
		"proxy_policy":{"fallback_proxy_id":"proxy-2"}
	}`, &testingDSMock{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies/actions", []byte(`{
		"proxy_ids":["proxy-1","proxy-2","proxy-3"],
		"action":"delete"
	}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		References []struct {
			ProxyID       string `json:"proxy_id"`
			AccountCount  int    `json:"account_count"`
			FallbackRoute bool   `json:"fallback_route"`
		} `json:"references"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if len(payload.References) != 1 || payload.References[0].ProxyID != "proxy-2" || payload.References[0].AccountCount != 0 || !payload.References[0].FallbackRoute {
		t.Fatalf("unexpected fallback reference: %#v", payload.References)
	}

	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, adminReq(http.MethodGet, "/proxies", nil))
	var listing struct {
		Items []struct {
			ID         string `json:"id"`
			IsFallback bool   `json:"is_fallback"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode proxy listing: %v", err)
	}
	if len(listing.Items) != 3 || !listing.Items[1].IsFallback {
		t.Fatalf("blocked batch deletion changed proxy state: %#v", listing.Items)
	}
}

func TestProxyBatchDeleteMigratesManualAndAutomaticRoutes(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxy_policy":{"auto_route_enabled":true,"fallback_proxy_id":"fallback"},
		"proxies":[
			{"id":"retired","type":"socks5","host":"127.0.0.1","port":1080},
			{"id":"fallback","type":"socks5","host":"127.0.0.1","port":1081},
			{"id":"healthy","type":"socks5","host":"127.0.0.1","port":1082,"last_test_at_unix":10,"last_test_success":true,"last_latency_ms":30}
		],
		"accounts":[
			{"email":"manual@example.com","password":"pwd","token":"manual-token","proxy_id":"retired"},
			{"email":"automatic@example.com","password":"pwd","token":"automatic-token","proxy_id":"retired","proxy_auto_route":true}
		]
	}`)
	h.DS = nil // Persisted routes must be correct before relogin can run.
	r := chi.NewRouter()
	r.Post("/admin/proxies/actions", h.proxyBatchAction)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, adminReq(http.MethodPost, "/admin/proxies/actions", []byte(`{"proxy_ids":["retired"],"action":"delete"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected route migration, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot := h.Store.Snapshot()
	if len(snapshot.Proxies) != 2 {
		t.Fatalf("unexpected proxies after deletion: %#v", snapshot.Proxies)
	}
	accounts := map[string]config.Account{}
	for _, account := range snapshot.Accounts {
		accounts[account.Identifier()] = account
	}
	manual := accounts["manual@example.com"]
	if manual.ProxyID != "fallback" || manual.Token != "" || manual.ProxyAutoRoute {
		t.Fatalf("manual route was not moved to fallback: %#v", manual)
	}
	automatic := accounts["automatic@example.com"]
	if automatic.ProxyID != "healthy" || automatic.Token != "" || !automatic.ProxyAutoRoute {
		t.Fatalf("automatic route was not reassigned to a tested node: %#v", automatic)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("node_deleted")) || bytes.Contains(rec.Body.Bytes(), []byte(`"to_proxy_id":""`)) {
		t.Fatalf("unexpected route migration result: %s", rec.Body.String())
	}
}

func TestProxyBatchDeleteRejectsAutomaticRouteWithoutHealthyReplacement(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"proxy_policy":{"auto_route_enabled":true},
		"proxies":[{"id":"retired","type":"socks5","host":"127.0.0.1","port":1080}],
		"accounts":[{"email":"automatic@example.com","password":"pwd","proxy_id":"retired","proxy_auto_route":true}]
	}`, &testingDSMock{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies/actions", []byte(`{"proxy_ids":["retired"],"action":"delete"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected safe rejection, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("no tested replacement node")) {
		t.Fatalf("unexpected deletion error: %s", rec.Body.String())
	}
}

func TestProxyBatchDeleteRemovesOnlyUnreferencedNodes(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"accounts":[],
		"proxies":[
			{"id":"proxy-1","type":"socks5","host":"127.0.0.1","port":1080},
			{"id":"proxy-2","type":"socks5","host":"127.0.0.1","port":1081}
		]
	}`, &testingDSMock{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/proxies/actions", []byte(`{
		"proxy_ids":["proxy-2"],
		"action":"delete"
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode deletion response: %v", err)
	}
	if payload["affected"] != float64(1) {
		t.Fatalf("unexpected deletion summary: %#v", payload)
	}

	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, adminReq(http.MethodGet, "/proxies", nil))
	var listing struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode proxy listing: %v", err)
	}
	if len(listing.Items) != 1 || listing.Items[0].ID != "proxy-1" {
		t.Fatalf("unexpected remaining proxies: %#v", listing.Items)
	}
}
