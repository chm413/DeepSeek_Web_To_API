package version

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestGetVersionIncludesAPIContractVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Handler{}).getVersion(rec, httptest.NewRequest("GET", "/admin/version", nil))

	var body struct {
		Success            bool `json:"success"`
		APIContractVersion int  `json:"api_contract_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success {
		t.Fatal("expected successful version response")
	}
	if body.APIContractVersion != APIContractVersion {
		t.Fatalf("api contract version = %d, want %d", body.APIContractVersion, APIContractVersion)
	}
}
