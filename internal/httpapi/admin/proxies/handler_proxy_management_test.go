package proxies

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
