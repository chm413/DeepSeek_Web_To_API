package configmgmt

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authn "DeepSeek_Web_To_API/internal/auth"
)

func TestConfigImportCredentialChangeInvalidatesExistingJWT(t *testing.T) {
	h := newAdminTestHandler(t, `{"admin":{"key":"old-admin-key","jwt_secret":"test-jwt-secret"}}`)
	oldToken, err := authn.CreateJWTWithStore(1, h.Store)
	if err != nil {
		t.Fatalf("create old token: %v", err)
	}
	if _, err := authn.VerifyJWTWithStore(oldToken, h.Store); err != nil {
		t.Fatalf("old token should be valid before import: %v", err)
	}
	before := h.Store.AdminJWTValidAfterUnix()

	req := httptest.NewRequest(http.MethodPost, "/admin/config/import?mode=merge", bytes.NewBufferString(
		`{"admin":{"password_hash":"`+authn.HashAdminPassword("new-admin-password1")+`"}}`,
	))
	rec := httptest.NewRecorder()
	h.configImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	cutoff := h.Store.AdminJWTValidAfterUnix()
	if cutoff < before || cutoff < time.Now().Unix()-1 {
		t.Fatalf("JWT cutoff was not advanced: before=%d after=%d", before, cutoff)
	}
	if _, err := authn.VerifyJWTWithStore(oldToken, h.Store); err == nil {
		t.Fatal("expected old JWT to be invalid after imported password change")
	}
}

func TestConfigImportMergeCanRotateJWTSecretAndInvalidatesJWT(t *testing.T) {
	h := newAdminTestHandler(t, `{"admin":{"key":"admin-key","jwt_secret":"old-jwt-secret"}}`)
	oldToken, err := authn.CreateJWTWithStore(1, h.Store)
	if err != nil {
		t.Fatalf("create old token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/config/import?mode=merge", bytes.NewBufferString(
		`{"admin":{"jwt_secret":"new-jwt-secret"}}`,
	))
	rec := httptest.NewRecorder()
	h.configImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := h.Store.AdminJWTSecret(); got != "new-jwt-secret" {
		t.Fatalf("JWT secret=%q, want new-jwt-secret", got)
	}
	if _, err := authn.VerifyJWTWithStore(oldToken, h.Store); err == nil {
		t.Fatal("expected old JWT to be invalid after JWT secret rotation")
	}
}

func TestConfigImportReplacePreservesAdminCredentialsFromRedactedExport(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"admin":{"key":"existing-admin-key","password_hash":"existing-password-hash","jwt_secret":"existing-jwt-secret","jwt_expire_hours":12},
		"model_aliases":{"alias":"deepseek-v4-pro"}
	}`)

	exportRec := httptest.NewRecorder()
	h.configExport(exportRec, httptest.NewRequest(http.MethodGet, "/admin/config/export", nil))
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}
	var exported map[string]any
	if err := json.Unmarshal(exportRec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	safeConfig, ok := exported["config"].(map[string]any)
	if !ok {
		t.Fatalf("missing export config: %#v", exported)
	}
	if encoded, _ := json.Marshal(safeConfig); bytes.Contains(encoded, []byte("existing-admin-key")) || bytes.Contains(encoded, []byte("existing-jwt-secret")) {
		t.Fatalf("expected a redacted export, got %s", encoded)
	}

	body, err := json.Marshal(map[string]any{"config": safeConfig})
	if err != nil {
		t.Fatalf("encode import: %v", err)
	}
	importRec := httptest.NewRecorder()
	h.configImport(importRec, httptest.NewRequest(http.MethodPost, "/admin/config/import?mode=replace", bytes.NewReader(body)))
	if importRec.Code != http.StatusOK {
		t.Fatalf("replace import status=%d body=%s", importRec.Code, importRec.Body.String())
	}
	if got := h.Store.AdminKey(); got != "existing-admin-key" {
		t.Fatalf("admin key was cleared by redacted replace import: %q", got)
	}
	if got := h.Store.AdminPasswordHash(); got != "existing-password-hash" {
		t.Fatalf("admin password hash was cleared by redacted replace import: %q", got)
	}
	if got := h.Store.AdminJWTSecret(); got != "existing-jwt-secret" {
		t.Fatalf("admin JWT secret was cleared by redacted replace import: %q", got)
	}
}
