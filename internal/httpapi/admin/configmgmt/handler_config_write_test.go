package configmgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/config"
)

func TestUpdateKeyAllowsChangingKeyValue(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"api_keys":[{"key":"old-key","name":"old","remark":"legacy"}]
	}`)
	body := map[string]any{
		"key":    "new-key",
		"name":   "primary",
		"remark": "rotated",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/admin/keys/old-key", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.updateKey(rec, requestWithKeyParam(req, "old-key"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	snap := h.Store.Snapshot()
	if len(snap.APIKeys) != 1 {
		t.Fatalf("expected 1 api key, got %#v", snap.APIKeys)
	}
	if snap.APIKeys[0].Key != "new-key" || snap.APIKeys[0].Name != "primary" || snap.APIKeys[0].Remark != "rotated" {
		t.Fatalf("unexpected updated key: %#v", snap.APIKeys[0])
	}
	if len(snap.Keys) != 1 || snap.Keys[0] != "new-key" {
		t.Fatalf("expected only new key in legacy key list, got %#v", snap.Keys)
	}
}

func TestConfigExportRedactsSecretsAndDisablesCaching(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"keys":["api-secret"],
		"accounts":[{"email":"user@example.com","password":"account-secret","token":"runtime-token"}],
		"proxies":[{"type":"http","host":"proxy.example","port":8080,"username":"proxy-user","password":"proxy-secret"}],
		"proxy_subscriptions":[{"id":"sub-1","url":"https://example.invalid/sub?token=subscription-secret"}],
		"admin":{"key":"admin-secret","password_hash":"hash-secret","jwt_secret":"jwt-secret"}
	}`)
	req := httptest.NewRequest(http.MethodGet, "/admin/config/export", nil)
	rec := httptest.NewRecorder()
	h.configExport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
		t.Fatalf("cache-control = %q", got)
	}
	for _, secret := range []string{"api-secret", "account-secret", "runtime-token", "proxy-secret", "subscription-secret", "admin-secret", "jwt-secret"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("export contains secret %q: %s", secret, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "example.invalid/sub") {
		t.Fatalf("redacted export discarded the subscription endpoint: %s", rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if redacted, ok := response["redacted"].(bool); !ok || !redacted {
		t.Fatalf("expected redacted marker, got %#v", response["redacted"])
	}
}

func TestConfigExportRedactsNestedAndVariantSecretFields(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"AdditionalFields":{"Proxy_URI":"vless://user:secret@example.invalid","nested":[{"ADMIN_KEY":"admin-secret","refresh_token":"refresh-secret","auth_token":"auth-token-secret","sessionToken":"session-token-secret","credential":"credential-secret","passphrase":"passphrase-secret","signature":"signature-secret","apiKey":"camel-api-secret","accessToken":"camel-access-secret","client-secret":"hyphen-secret","subscriptionUrl":"https://user:pass@example.invalid/sub?api-key=query-secret&auth_token=query-auth-secret&session-token=query-session-secret&mode=full"}]}
	}`)
	rec := httptest.NewRecorder()
	h.configExport(rec, httptest.NewRequest(http.MethodGet, "/admin/config/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"vless://user:secret", "admin-secret", "refresh-secret", "auth-token-secret", "session-token-secret", "credential-secret", "passphrase-secret", "signature-secret", "camel-api-secret", "camel-access-secret", "hyphen-secret", "query-secret", "query-auth-secret", "query-session-secret", "user:pass"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("export leaked nested/variant secret %q: %s", secret, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "vless://example.invalid") {
		t.Fatalf("redacted export discarded the safe proxy endpoint: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "example.invalid/sub?mode=full") {
		t.Fatalf("redacted export discarded the safe subscription endpoint: %s", rec.Body.String())
	}
}

func TestRedactedExportOmitsCoreNodesAndClearsDanglingRoutes(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"proxies":[
			{"id":"regular","type":"socks5","host":"127.0.0.1","port":1080},
			{"id":"core","type":"vless","uri":"vless://11111111-1111-1111-1111-111111111111@example.invalid:443?encryption=none&security=tls"}
		],
		"proxy_policy":{"fallback_proxy_id":"core"},
		"accounts":[{"email":"user@example.com","password":"account-secret","proxy_id":"core","proxy_auto_route":true}]
	}`)
	safe, err := sanitizedExportSnapshot(h.Store.Snapshot())
	if err != nil {
		t.Fatalf("sanitize export: %v", err)
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		t.Fatalf("marshal sanitized export: %v", err)
	}
	if strings.Contains(string(encoded), "11111111-1111") || strings.Contains(string(encoded), "account-secret") {
		t.Fatalf("sanitized export retained core credentials: %s", encoded)
	}
	var restored config.Config
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("decode sanitized export: %v", err)
	}
	if len(restored.Proxies) != 1 || restored.Proxies[0].ID != "regular" {
		t.Fatalf("unexpected export proxy set: %#v", restored.Proxies)
	}
	if restored.ProxyPolicy.FallbackProxyID != "" || restored.Accounts[0].ProxyID != "" || restored.Accounts[0].ProxyAutoRoute {
		t.Fatalf("redacted export left dangling routing state: %#v %#v", restored.ProxyPolicy, restored.Accounts[0])
	}
	if err := config.ValidateConfig(restored); err != nil {
		t.Fatalf("redacted export must remain importable: %v; config=%s", err, encoded)
	}
}

func TestConfigReadMasksAPIKeysAndOpaqueIDRoutesWork(t *testing.T) {
	h := newAdminTestHandler(t, `{"api_keys":[{"key":"api-secret","name":"primary","remark":"prod"}],"proxy_subscriptions":[{"id":"sub-1","last_error":"fetch failed for https://user:subscription-secret@example.invalid/sub?token=private-token"}]}`)
	readRec := httptest.NewRecorder()
	h.getConfig(readRec, httptest.NewRequest(http.MethodGet, "/admin/config", nil))
	if readRec.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", readRec.Code, readRec.Body.String())
	}
	if strings.Contains(readRec.Body.String(), "api-secret") {
		t.Fatalf("config response exposed API key: %s", readRec.Body.String())
	}
	for _, secret := range []string{"user:subscription-secret", "private-token", "example.invalid"} {
		if strings.Contains(readRec.Body.String(), secret) {
			t.Fatalf("config response exposed subscription error data %q: %s", secret, readRec.Body.String())
		}
	}
	var payload struct {
		Keys    []string         `json:"keys"`
		APIKeys []map[string]any `json:"api_keys"`
	}
	if err := json.Unmarshal(readRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if len(payload.Keys) != 0 || len(payload.APIKeys) != 1 {
		t.Fatalf("unexpected public key payload: %#v", payload)
	}
	id, _ := payload.APIKeys[0]["id"].(string)
	if id == "" || !strings.HasPrefix(id, "key_") || payload.APIKeys[0]["key_preview"] == "api-secret" {
		t.Fatalf("key was not represented by opaque metadata: %#v", payload.APIKeys[0])
	}

	r := chi.NewRouter()
	r.Put("/admin/keys/id/{id}", h.updateKeyByID)
	r.Delete("/admin/keys/id/{id}", h.deleteKeyByID)
	updateRec := httptest.NewRecorder()
	r.ServeHTTP(updateRec, httptest.NewRequest(http.MethodPut, "/admin/keys/id/"+id, strings.NewReader(`{"name":"renamed"}`)))
	if updateRec.Code != http.StatusOK {
		t.Fatalf("opaque update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	if h.Store.Snapshot().APIKeys[0].Name != "renamed" {
		t.Fatalf("opaque update did not persist: %#v", h.Store.Snapshot().APIKeys)
	}
	deleteRec := httptest.NewRecorder()
	r.ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete, "/admin/keys/id/"+id, nil))
	if deleteRec.Code != http.StatusOK || len(h.Store.Snapshot().APIKeys) != 0 {
		t.Fatalf("opaque delete failed: status=%d body=%s keys=%#v", deleteRec.Code, deleteRec.Body.String(), h.Store.Snapshot().APIKeys)
	}
}

func TestBatchImportPlainAccountText(t *testing.T) {
	h := newAdminTestHandler(t, `{"keys":["k1"],"accounts":[]}`)
	body := map[string]any{
		"accounts_text": "user@example.com:p1\n13800000000:p2\n# skipped",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.batchImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["imported_accounts"].(float64)); got != 2 {
		t.Fatalf("expected 2 imported accounts, got %d body=%v", got, resp)
	}
	if got := len(h.Store.Accounts()); got != 2 {
		t.Fatalf("expected 2 accounts in store, got %d", got)
	}
	if _, ok := h.Store.FindAccount("user@example.com"); !ok {
		t.Fatal("expected email account to be imported")
	}
	if _, ok := h.Store.FindAccount("13800000000"); !ok {
		t.Fatal("expected mobile account to be imported")
	}
}

func TestBatchImportLegacyJSONTreatsEmailInMobileAsEmail(t *testing.T) {
	h := newAdminTestHandler(t, `{"keys":["k1"],"accounts":[]}`)
	body := map[string]any{
		"accounts": []any{
			map[string]any{
				"mobile":   "legacy@example.com",
				"password": "p1",
				"token":    "runtime-token",
			},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.batchImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["imported_accounts"].(float64)); got != 1 {
		t.Fatalf("expected 1 imported account, got %d body=%v", got, resp)
	}
	acc, ok := h.Store.FindAccount("legacy@example.com")
	if !ok {
		t.Fatal("expected legacy JSON email account to be findable by email")
	}
	if acc.Email != "legacy@example.com" || acc.Mobile != "" {
		t.Fatalf("expected legacy mobile email normalized to email, got %#v", acc)
	}
	if acc.Token != "" {
		t.Fatalf("expected imported runtime token to be ignored, got %q", acc.Token)
	}
}

func TestBatchImportPlainAccountTextRejectsInvalidLine(t *testing.T) {
	h := newAdminTestHandler(t, `{"keys":["k1"],"accounts":[]}`)
	body := map[string]any{"accounts_text": "missing-separator"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.batchImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func requestWithKeyParam(req *http.Request, key string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", key)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
