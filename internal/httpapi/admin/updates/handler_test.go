package updates

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/selfupdate"
)

func TestGetUpdatesReturnsWebUIEnvelope(t *testing.T) {
	container := false
	handler := &Handler{Updater: selfupdate.New(nil, selfupdate.Options{
		Root:           t.TempDir(),
		Container:      &container,
		CurrentVersion: func() string { return "1.1.9" },
	})}
	recorder := httptest.NewRecorder()
	handler.getUpdates(recorder, httptest.NewRequest(http.MethodGet, "/admin/updates", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body struct {
		Success  bool `json:"success"`
		Settings struct {
			Enabled bool `json:"enabled"`
		} `json:"settings"`
		Status struct {
			Supported    bool   `json:"supported"`
			CurrentTag   string `json:"current_tag"`
			Downloadable bool   `json:"downloadable"`
		} `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success || !body.Settings.Enabled || body.Status.Supported || body.Status.Downloadable || body.Status.CurrentTag != "v1.1.9" {
		t.Fatalf("unexpected update response: %#v", body)
	}
}

func TestWriteUpdateResponseExposesDownloadableAsset(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeUpdateResponse(recorder, http.StatusOK, selfupdate.Status{
		Available: &selfupdate.Release{Tag: "v1.2.0", AssetName: "deepseek-web-to-api_v1.2.0_linux_amd64.tar.gz", Downloadable: true},
		FailedTag: "v1.1.9",
	}, selfupdate.Settings{})
	var body struct {
		Status struct {
			Downloadable bool   `json:"downloadable"`
			FailedTag    string `json:"failed_tag"`
		} `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Status.Downloadable {
		t.Fatal("expected downloadable asset to be exposed to the Web UI")
	}
	if body.Status.FailedTag != "v1.1.9" {
		t.Fatalf("failed tag = %q, want v1.1.9", body.Status.FailedTag)
	}
}

func TestUpdateSettingsAcceptsDirectBody(t *testing.T) {
	container := true
	store := &configStore{}
	handler := &Handler{Updater: selfupdate.New(store, selfupdate.Options{Root: t.TempDir(), Container: &container})}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/updates/settings", bytes.NewBufferString(`{"enabled":false,"auto_download":true,"auto_apply":true,"check_interval_minutes":15}`))
	handler.updateSettings(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	settings := handler.Updater.Settings()
	if settings.Enabled || !settings.AutoDownload || !settings.AutoApply || settings.CheckIntervalMinutes != 15 {
		t.Fatalf("unexpected persisted settings: %#v", settings)
	}
}

type configStore struct {
	mu  sync.Mutex
	cfg config.Config
}

func (s *configStore) Snapshot() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

func (s *configStore) Update(mutator func(*config.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return mutator(&s.cfg)
}
