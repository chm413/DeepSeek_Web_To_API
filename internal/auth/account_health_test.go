package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/config"
)

func TestResolverSkipsAccountReportedAsTemporarilyMuted(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"muted@example.com","password":"pwd"},
			{"email":"healthy@example.com","token":"token-2"}
		]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		if acc.Email == "muted@example.com" {
			return "", &AccountHealthError{State: account.HealthTemporarilyMuted, Until: time.Now().Add(time.Hour), Message: "muted"}
		}
		return acc.Token, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer managed-key")
	a, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("Determine failed: %v", err)
	}
	defer resolver.Release(a)
	if a.AccountID != "healthy@example.com" {
		t.Fatalf("expected healthy account, got %q", a.AccountID)
	}
	health, ok := pool.AccountHealth("muted@example.com")
	if !ok || health.State != account.HealthTemporarilyMuted {
		t.Fatalf("expected muted account health, got %#v, %v", health, ok)
	}
}

func TestResolverPermanentlyBannedAccountIsPersistentlyDisabled(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"banned@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	resolver.MarkAccountHealth("banned@example.com", &AccountHealthError{
		State:   account.HealthPermanentlyBanned,
		Code:    40012,
		Message: "banned",
	})
	acc, ok := store.FindAccount("banned@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != config.AccountDisabledUpstreamBanned {
		t.Fatalf("expected persistent automatic disable, got %#v, %v", acc, ok)
	}
	if _, ok := pool.Acquire("banned@example.com", nil); ok {
		t.Fatal("automatically disabled account must not be acquired")
	}
}

func TestResolverInvalidCredentialsArePersistentlyDisabled(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"invalid@example.com","password":"wrong"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		return "", &AccountHealthError{State: account.HealthInvalidCredentials, Message: "password rejected"}
	})

	resolver.MarkAccountHealth("invalid@example.com", &AccountHealthError{
		State:   account.HealthInvalidCredentials,
		Message: "password rejected",
	})
	acc, ok := store.FindAccount("invalid@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != config.AccountDisabledInvalidCredentials {
		t.Fatalf("expected invalid credentials to persistently disable account, got %#v, %v", acc, ok)
	}
	if _, ok := pool.Acquire("invalid@example.com", nil); ok {
		t.Fatal("account with invalid credentials must not be acquired")
	}
}
