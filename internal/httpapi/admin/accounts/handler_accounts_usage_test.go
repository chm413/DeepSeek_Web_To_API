package accounts

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"DeepSeek_Web_To_API/internal/chathistory"
)

func TestListAccountsIncludesEstimatedTokenCost(t *testing.T) {
	h := newAdminTestHandler(t, `{"accounts":[{"email":"usage@example.com","password":"pwd"}]}`)
	history := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	defer func() { _ = history.Close() }()
	h.ChatHistory = history

	entry, err := history.Start(chathistory.StartParams{
		AccountID: "usage@example.com",
		Model:     "deepseek-v4-flash",
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history entry: %v", err)
	}
	if _, err := history.Update(entry.ID, chathistory.UpdateParams{
		Status: "success",
		Usage: map[string]any{
			"input_tokens":            100,
			"output_tokens":           50,
			"input_cache_hit_tokens":  20,
			"input_cache_miss_tokens": 80,
		},
		Completed: true,
	}); err != nil {
		t.Fatalf("update history entry: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/accounts?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.listAccounts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			TokenUsage struct {
				WindowSeconds    int64   `json:"window_seconds"`
				Requests         int64   `json:"requests"`
				TotalTokens      int64   `json:"total_tokens"`
				EstimatedCostUSD float64 `json:"estimated_cost_usd"`
				Currency         string  `json:"currency"`
			} `json:"token_usage_24h"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("unexpected items: %#v", payload.Items)
	}
	usage := payload.Items[0].TokenUsage
	if usage.WindowSeconds != 86400 || usage.Requests != 1 || usage.TotalTokens != 150 || usage.Currency != "USD" {
		t.Fatalf("unexpected account usage: %#v", usage)
	}
	want := (20*0.0028 + 80*0.14 + 50*0.28) / 1_000_000
	if math.Abs(usage.EstimatedCostUSD-want) > 1e-12 {
		t.Fatalf("unexpected estimated cost: got %.12f want %.12f", usage.EstimatedCostUSD, want)
	}
}
