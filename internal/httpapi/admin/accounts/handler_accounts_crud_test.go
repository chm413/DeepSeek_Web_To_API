package accounts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/config"
)

func TestListAccountsPageSizeCapIs5000(t *testing.T) {
	accounts := make([]string, 0, 150)
	for i := range 150 {
		accounts = append(accounts, fmt.Sprintf(`{"email":"u%d@example.com","password":"pwd"}`, i))
	}
	raw := fmt.Sprintf(`{"accounts":[%s]}`, strings.Join(accounts, ","))
	router := newHTTPAdminHarness(t, raw, &testingDSMock{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodGet, "/accounts?page=1&page_size=200", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, _ := payload["items"].([]any)
	if len(items) != 150 {
		t.Fatalf("expected all 150 accounts with page_size=200, got %d", len(items))
	}
	if ps, _ := payload["page_size"].(float64); ps != 200 {
		t.Fatalf("expected page_size=200 in response, got %v", payload["page_size"])
	}
}

func TestListAccountsPageSizeAbove5000ClampedTo5000(t *testing.T) {
	router := newHTTPAdminHarness(t, `{"accounts":[{"email":"u@example.com","password":"pwd"}]}`, &testingDSMock{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodGet, "/accounts?page=1&page_size=9999", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ps, _ := payload["page_size"].(float64); ps != 5000 {
		t.Fatalf("expected page_size clamped to 5000, got %v", payload["page_size"])
	}
}

func TestListAccountsClampsDeletedLastPageToLastAvailablePage(t *testing.T) {
	router := newHTTPAdminHarness(t, `{
		"accounts":[
			{"email":"one@example.com","password":"pwd"},
			{"email":"two@example.com","password":"pwd"},
			{"email":"three@example.com","password":"pwd"}
		]
	}`, &testingDSMock{})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodGet, "/accounts?page=9&page_size=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if page, _ := payload["page"].(float64); page != 2 {
		t.Fatalf("expected final valid page 2, got %#v", payload["page"])
	}
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one item on final page, got %#v", items)
	}
}

func TestUpdateAccountMetadataPreservesCredentials(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[{"email":"u@example.com","name":"old name","remark":"old remark","password":"secret"}]
	}`)

	r := chi.NewRouter()
	r.Put("/admin/accounts/{identifier}", h.updateAccount)

	body := []byte(`{"name":"new name","remark":"new remark"}`)
	req := httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	snap := h.Store.Snapshot()
	if len(snap.Accounts) != 1 {
		t.Fatalf("unexpected accounts after update: %#v", snap.Accounts)
	}
	acc := snap.Accounts[0]
	if acc.Email != "u@example.com" {
		t.Fatalf("identifier changed unexpectedly: %#v", acc)
	}
	if acc.Name != "new name" || acc.Remark != "new remark" {
		t.Fatalf("metadata update did not persist: %#v", acc)
	}
	if acc.Password != "secret" {
		t.Fatalf("password should be preserved, got %#v", acc)
	}
}

func TestUpdateAccountEnabledStatePersistsAndResetsPool(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[{"email":"u@example.com","password":"secret"}]
	}`)
	r := chi.NewRouter()
	r.Put("/admin/accounts/{identifier}", h.updateAccount)

	req := httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com", strings.NewReader(`{"enabled":false}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status: %d body=%s", rec.Code, rec.Body.String())
	}
	acc, ok := h.Store.FindAccount("u@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != "manual" {
		t.Fatalf("expected manually disabled account, got %#v, %v", acc, ok)
	}
	if pool, ok := h.Pool.(*account.Pool); ok {
		if _, acquired := pool.Acquire("u@example.com", nil); acquired {
			t.Fatal("disabled account remained acquirable")
		}
	}

	req = httptest.NewRequest(http.MethodPut, "/admin/accounts/u@example.com", strings.NewReader(`{"enabled":true}`))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status: %d body=%s", rec.Code, rec.Body.String())
	}
	acc, ok = h.Store.FindAccount("u@example.com")
	if !ok || acc.Disabled || acc.DisabledReason != "" || acc.DisabledAtUnix != 0 {
		t.Fatalf("expected enabled account, got %#v, %v", acc, ok)
	}
}

func TestListAccountsMasksTokenPreview(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[{"email":"u@example.com","password":"pwd"}]
	}`)
	if err := h.Store.UpdateAccountToken("u@example.com", "abcdefgh"); err != nil {
		t.Fatalf("seed runtime token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if got, _ := first["token_preview"].(string); got != "ab****gh" {
		t.Fatalf("expected masked token preview, got %q", got)
	}
}

func TestListAccountsReturnsRuntimeSessionCount(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[{"email":"u@example.com","password":"pwd"}]
	}`)
	if err := h.Store.UpdateAccountSessionCount("u@example.com", 9); err != nil {
		t.Fatalf("seed runtime session count: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if got, _ := first["session_count"].(float64); got != 9 {
		t.Fatalf("expected session_count 9, got %#v", first["session_count"])
	}
}

func TestListAccountsReturnsDetailedRuntimeState(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"runtime":{"account_max_inflight":2},
		"accounts":[{"email":"busy@example.com","password":"pwd"}]
	}`)
	pool := h.Pool.(*account.Pool)
	if _, ok := pool.Acquire("busy@example.com", nil); !ok {
		t.Fatal("expected account acquisition")
	}
	defer pool.Release("busy@example.com")

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	items, _ := payload["items"].([]any)
	item, _ := items[0].(map[string]any)
	if item["account_state"] != "busy" || item["runtime_state"] != "busy" {
		t.Fatalf("unexpected account state: %#v", item)
	}
	if item["in_use"] != float64(1) || item["available_slots"] != float64(1) {
		t.Fatalf("unexpected runtime counters: %#v", item)
	}
}

func TestListAccountsReturnsLatestTestFailureDetail(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"accounts":[{"email":"u@example.com","password":"pwd"}]
	}`)
	resultStore, ok := h.Store.(interface {
		UpdateAccountTestResult(string, config.AccountTestResult) error
	})
	if !ok {
		t.Fatal("handler store does not support runtime test result details")
	}
	if err := resultStore.UpdateAccountTestResult("u@example.com", config.AccountTestResult{
		Status:        "failed",
		Phase:         "token_refresh",
		FailureReason: "login rejected by upstream",
		ErrorCode:     40012,
	}); err != nil {
		t.Fatalf("seed runtime test result: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	items, _ := payload["items"].([]any)
	first, _ := items[0].(map[string]any)
	detail, _ := first["test_result"].(map[string]any)
	if got, _ := detail["phase"].(string); got != "token_refresh" {
		t.Fatalf("test result phase = %q", got)
	}
	if got, _ := detail["failure_reason"].(string); got != "login rejected by upstream" {
		t.Fatalf("test failure reason = %q", got)
	}
	if got, _ := detail["error_code"].(float64); got != 40012 {
		t.Fatalf("test error code = %#v", detail["error_code"])
	}
}
