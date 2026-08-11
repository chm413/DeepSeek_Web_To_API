package accounts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestBatchAccountsCreatesWithoutEchoingCredentials(t *testing.T) {
	router := newHTTPAdminHarness(t, `{}`, &testingDSMock{})
	body := `{"accounts":[{"email":"one@example.com","password":"top-secret","token":"token-secret","name":"One"}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/accounts/batch", []byte(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "top-secret") || strings.Contains(rec.Body.String(), "token-secret") {
		t.Fatalf("response leaked credentials: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["created"] != float64(1) || payload["invalid"] != float64(0) {
		t.Fatalf("unexpected summary: %#v", payload)
	}
}

func TestBatchAccountsDryRunDoesNotPersist(t *testing.T) {
	router := newHTTPAdminHarness(t, `{}`, &testingDSMock{})
	body := `{"dry_run":true,"accounts":[{"email":"dry@example.com","password":"secret"}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/accounts/batch", []byte(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload["created"] != float64(1) || payload["total_accounts"] != float64(0) {
		t.Fatalf("dry-run persisted data: %#v", payload)
	}
}

func TestBatchAccountsUpdatePreservesOmittedCredentials(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[{"email":"one@example.com","password":"old-secret","token":"old-token","name":"Old","remark":"keep"}]}`)
	if err := h.Store.UpdateAccountToken("one@example.com", "old-token"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	router := chi.NewRouter()
	router.Post("/admin/accounts/batch", h.batchAccounts)
	body := `{"on_duplicate":"update","accounts":[{"email":"ONE@example.com"}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/admin/accounts/batch", []byte(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload["updated"] != float64(1) {
		t.Fatalf("expected update: %#v", payload)
	}
	acc, ok := h.Store.FindAccount("one@example.com")
	if !ok || acc.Password != "old-secret" || acc.Token != "old-token" || acc.Name != "Old" || acc.Remark != "keep" {
		t.Fatalf("omitted fields were not preserved: %#v, %v", acc, ok)
	}
}

func TestBatchAccountsReportsPerItemValidation(t *testing.T) {
	router := newHTTPAdminHarness(t, `{}`, &testingDSMock{})
	body := `{"accounts":[{"email":"missing-credentials@example.com"},{"password":"secret"}]}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPost, "/accounts/batch", []byte(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload["invalid"] != float64(2) || payload["success"] != false {
		t.Fatalf("unexpected validation summary: %#v", payload)
	}
}

func TestBatchAccountsRequiresJSONContentType(t *testing.T) {
	router := newHTTPAdminHarness(t, `{}`, &testingDSMock{})
	req := httptest.NewRequest(http.MethodPost, "/accounts/batch", strings.NewReader(`{"accounts":[]}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d body=%s", rec.Code, rec.Body.String())
	}
}
