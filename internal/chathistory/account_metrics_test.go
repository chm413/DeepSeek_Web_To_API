package chathistory

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAccountTokenUsageByAccountSeparatesAccounts(t *testing.T) {
	tests := []struct {
		name string
		open func(string) *Store
	}{
		{
			name: "json",
			open: func(dir string) *Store {
				return New(filepath.Join(dir, "chat_history.json"))
			},
		},
		{
			name: "sqlite",
			open: func(dir string) *Store {
				return NewSQLite(filepath.Join(dir, "chat_history.sqlite"), filepath.Join(dir, "missing.json"))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t.TempDir())
			defer func() { _ = store.Close() }()
			if err := store.Err(); err != nil {
				t.Fatalf("open store: %v", err)
			}

			first, err := store.Start(StartParams{
				AccountID: "USER@example.com",
				Model:     "deepseek-v4-flash",
				UserInput: "first",
			})
			if err != nil {
				t.Fatalf("start first entry: %v", err)
			}
			if _, err := store.Update(first.ID, UpdateParams{
				Status: "success",
				Usage: map[string]any{
					"input_tokens":            100,
					"output_tokens":           50,
					"input_cache_hit_tokens":  25,
					"input_cache_miss_tokens": 75,
				},
				Completed: true,
			}); err != nil {
				t.Fatalf("update first entry: %v", err)
			}

			second, err := store.Start(StartParams{
				AccountID: "other@example.com",
				Model:     "deepseek-v4-pro",
				UserInput: "second",
			})
			if err != nil {
				t.Fatalf("start second entry: %v", err)
			}
			if _, err := store.Update(second.ID, UpdateParams{
				Status:    "success",
				Usage:     map[string]any{"input_tokens": 20, "output_tokens": 10},
				Completed: true,
			}); err != nil {
				t.Fatalf("update second entry: %v", err)
			}

			stats, err := store.AccountTokenUsageByAccount(24 * time.Hour)
			if err != nil {
				t.Fatalf("account token usage: %v", err)
			}
			user := stats["user@example.com"]
			if user.Requests != 1 || user.TotalTokens != 150 || user.CacheHitInputTokens != 25 || user.CacheMissInputTokens != 75 {
				t.Fatalf("unexpected first account usage: %#v", user)
			}
			if user.ByModel["deepseek-v4-flash"].TotalTokens != 150 {
				t.Fatalf("unexpected first account model usage: %#v", user.ByModel)
			}
			other := stats["other@example.com"]
			if other.Requests != 1 || other.TotalTokens != 30 || other.ByModel["deepseek-v4-pro"].TotalTokens != 30 {
				t.Fatalf("unexpected second account usage: %#v", other)
			}
		})
	}
}
