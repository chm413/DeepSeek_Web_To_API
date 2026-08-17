package proxies

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestBatchAccountActionsSetProxyReloginsSelectedAccounts(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","type":"socks5h","host":"127.0.0.1","port":1080}],
		"accounts":[
			{"email":"one@example.com","password":"one-password","token":"old-token"},
			{"email":"two@example.com","password":"two-password","token":"old-token"}
		]
	}`)
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch/actions", h.batchAccountActions)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch/actions", bytes.NewBufferString(`{
		"identifiers":["one@example.com","two@example.com"],
		"action":"set_proxy",
		"proxy_id":"proxy-1"
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	for _, identifier := range []string{"one@example.com", "two@example.com"} {
		account, ok := h.Store.FindAccount(identifier)
		if !ok || account.ProxyID != "proxy-1" || account.ProxyAutoRoute || account.Token != "token" {
			t.Fatalf("expected %s to use proxy-1 with refreshed token, got %#v", identifier, account)
		}
	}
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "old-token") {
		t.Fatalf("batch action response leaked credentials: %s", rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["affected"] != float64(2) || response["route_changed"] != float64(2) {
		t.Fatalf("unexpected action summary: %#v", response)
	}
	relogin, _ := response["relogin"].(map[string]any)
	if relogin["attempted"] != float64(2) || relogin["failed"] != float64(0) {
		t.Fatalf("unexpected relogin summary: %#v", relogin)
	}
}

func TestBatchAccountActionsRejectsRouteChangeWithoutPasswordAtomically(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxies":[{"id":"proxy-1","type":"socks5h","host":"127.0.0.1","port":1080}],
		"accounts":[
			{"email":"ready@example.com","password":"ready-password"},
			{"email":"token-only@example.com"}
		]
	}`)
	if err := h.Store.UpdateAccountToken("token-only@example.com", "existing-token"); err != nil {
		t.Fatalf("seed runtime token: %v", err)
	}
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch/actions", h.batchAccountActions)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch/actions", bytes.NewBufferString(`{
		"identifiers":["ready@example.com","token-only@example.com"],
		"action":"set_proxy",
		"proxy_id":"proxy-1"
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d body=%s", rec.Code, rec.Body.String())
	}
	ready, _ := h.Store.FindAccount("ready@example.com")
	tokenOnly, _ := h.Store.FindAccount("token-only@example.com")
	if ready.ProxyID != "" || tokenOnly.ProxyID != "" || tokenOnly.Token != "existing-token" {
		t.Fatalf("failed validation must not partially change accounts: ready=%#v token-only=%#v", ready, tokenOnly)
	}
}

func TestBatchAccountActionsEnableAndDisable(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"accounts":[
			{"email":"one@example.com","password":"password","disabled":true},
			{"email":"two@example.com","password":"password","disabled":true}
		]
	}`)
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch/actions", h.batchAccountActions)

	for _, action := range []string{"enable", "disable"} {
		req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch/actions", bytes.NewBufferString(`{
			"identifiers":["one@example.com","two@example.com"],
			"action":"`+action+`"
		}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", action, rec.Code, rec.Body.String())
		}
		wantDisabled := action == "disable"
		for _, identifier := range []string{"one@example.com", "two@example.com"} {
			account, _ := h.Store.FindAccount(identifier)
			if account.Disabled != wantDisabled {
				t.Fatalf("%s: %s disabled=%v, want %v", action, identifier, account.Disabled, wantDisabled)
			}
		}
	}
}

func TestBatchAccountActionsDeletePersistsSelectedAccountsAndResetsPool(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"accounts":[
			{"email":"one@example.com","password":"one-password","token":"one-token"},
			{"email":"two@example.com","password":"two-password","token":"two-token"},
			{"email":"keep@example.com","password":"keep-password","token":"keep-token"}
		]
	}`)
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch/actions", h.batchAccountActions)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch/actions", bytes.NewBufferString(`{
		"identifiers":["one@example.com","two@example.com","one@example.com"],
		"action":"delete"
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if _, found := h.Store.FindAccount("one@example.com"); found {
		t.Fatal("first selected account still exists")
	}
	if _, found := h.Store.FindAccount("two@example.com"); found {
		t.Fatal("second selected account still exists")
	}
	if _, found := h.Store.FindAccount("keep@example.com"); !found {
		t.Fatal("unselected account was removed")
	}
	if total, _ := h.Pool.Status()["total"].(int); total != 1 {
		t.Fatalf("pool was not reset after deletion: %#v", h.Pool.Status())
	}
	if strings.Contains(rec.Body.String(), "one-password") || strings.Contains(rec.Body.String(), "one-token") {
		t.Fatalf("batch delete response leaked credentials: %s", rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["action"] != "delete" || response["affected"] != float64(2) || response["total_accounts"] != float64(1) {
		t.Fatalf("unexpected delete summary: %#v", response)
	}
}

func TestBatchAccountActionsDeleteRejectsMissingAccountAtomically(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"accounts":[
			{"email":"one@example.com","password":"one-password"},
			{"email":"two@example.com","password":"two-password"}
		]
	}`)
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch/actions", h.batchAccountActions)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch/actions", bytes.NewBufferString(`{
		"identifiers":["one@example.com","missing@example.com"],
		"action":"delete"
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := len(h.Store.Snapshot().Accounts); got != 2 {
		t.Fatalf("failed batch delete changed persisted accounts: %#v", h.Store.Snapshot().Accounts)
	}
}

func TestBatchAccountActionsEnableAutomaticRouteAndRelogin(t *testing.T) {
	h := newAdminProxyTestHandler(t, `{
		"proxy_policy":{"auto_route_enabled":true},
		"proxies":[{"id":"proxy-1","type":"socks5h","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":true,"last_latency_ms":25}],
		"accounts":[{"email":"auto@example.com","password":"password"}]
	}`)
	r := chi.NewRouter()
	r.Post("/admin/accounts/batch/actions", h.batchAccountActions)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/batch/actions", bytes.NewBufferString(`{
		"identifiers":["auto@example.com"],
		"action":"set_proxy",
		"proxy_id":"",
		"auto_route":true
	}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	account, ok := h.Store.FindAccount("auto@example.com")
	if !ok || !account.ProxyAutoRoute || account.ProxyID != "proxy-1" || account.Token != "token" {
		t.Fatalf("automatic route was not assigned and relogged: %#v", account)
	}
}
