package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestUpdateProxyCoreRejectsInvalidStartupTimeout(t *testing.T) {
	router := newHTTPAdminHarness(t, `{"accounts":[]}`, &testingDSMock{})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPut, "/proxies/core", []byte(`{
		"startup_timeout_seconds":61
	}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid timeout status, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateProxyCoreReturnsConfiguredPathFailure(t *testing.T) {
	router := newHTTPAdminHarness(t, `{"accounts":[]}`, &testingDSMock{})
	missing := filepath.Join(t.TempDir(), "missing-xray.exe")
	body, err := json.Marshal(map[string]any{
		"xray_binary_path":        missing,
		"runtime_dir":             t.TempDir(),
		"startup_timeout_seconds": 10,
		"auto_download":           false,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, adminReq(http.MethodPut, "/proxies/core", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("update core status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Status struct {
			Available bool   `json:"available"`
			Error     string `json:"error"`
		} `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status.Available || payload.Status.Error == "" {
		t.Fatalf("expected unavailable status with reason, got %#v", payload.Status)
	}
}
