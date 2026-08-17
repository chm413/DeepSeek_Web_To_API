package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
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

func TestProxyCoreReportsAndPreservesInstalledVersion(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-xray.exe")
	raw, err := json.Marshal(map[string]any{
		"accounts": []any{},
		"proxy_core": map[string]any{
			"xray_binary_path":       missing,
			"auto_download_disabled": true,
			"installed_version":      "v25.1.0",
		},
	})
	if err != nil {
		t.Fatalf("marshal core config: %v", err)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", string(raw))
	store := config.LoadStore()
	router := newHTTPAdminHarnessWithStore(store)

	get := httptest.NewRecorder()
	router.ServeHTTP(get, adminReq(http.MethodGet, "/proxies/core", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get core status=%d body=%s", get.Code, get.Body.String())
	}
	var response struct {
		Config struct {
			InstalledVersion string `json:"installed_version"`
		} `json:"config"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode core response: %v", err)
	}
	if response.Config.InstalledVersion != "v25.1.0" {
		t.Fatalf("reported installed version = %q", response.Config.InstalledVersion)
	}

	body, err := json.Marshal(map[string]any{
		"xray_binary_path": missing,
		"auto_download":    false,
	})
	if err != nil {
		t.Fatalf("marshal core update: %v", err)
	}
	put := httptest.NewRecorder()
	router.ServeHTTP(put, adminReq(http.MethodPut, "/proxies/core", body))
	if put.Code != http.StatusOK {
		t.Fatalf("update core status=%d body=%s", put.Code, put.Body.String())
	}
	if got := store.Snapshot().ProxyCore.InstalledVersion; got != "v25.1.0" {
		t.Fatalf("installed version was cleared by unchanged update: %q", got)
	}
}
