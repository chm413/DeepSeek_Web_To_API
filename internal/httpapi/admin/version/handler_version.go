package version

import (
	"net/http"
	"time"

	"DeepSeek_Web_To_API/internal/version"
)

// APIContractVersion is incremented when the WebUI requires new admin routes
// or response fields that older server binaries do not provide.
const APIContractVersion = 2

func (h *Handler) getVersion(w http.ResponseWriter, _ *http.Request) {
	current, source := version.Current()
	resp := map[string]any{
		"success":              true,
		"api_contract_version": APIContractVersion,
		"current_version":      current,
		"current_tag":          version.Tag(current),
		"source":               source,
		"update_policy":        "self_managed",
		"checked_at":           time.Now().UTC().Format(time.RFC3339),
	}

	writeJSON(w, http.StatusOK, resp)
}
