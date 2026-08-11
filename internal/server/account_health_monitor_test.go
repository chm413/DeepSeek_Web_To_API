package server

import (
	"context"
	"testing"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

type accountHealthCheckerFunc func(context.Context, config.Account) (string, error)

func (f accountHealthCheckerFunc) CheckAccountHealth(ctx context.Context, acc config.Account) (string, error) {
	return f(ctx, acc)
}

func TestRunAccountHealthChecksOnceAutomaticallyDisablesBan(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"banned@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	checker := accountHealthCheckerFunc(func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, &auth.AccountHealthError{State: account.HealthPermanentlyBanned, Code: 40012, Message: "banned"}
	})

	runAccountHealthChecksOnce(context.Background(), store, pool, resolver, checker)
	acc, ok := store.FindAccount("banned@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != config.AccountDisabledUpstreamBanned {
		t.Fatalf("expected monitor to persist ban disable, got %#v, %v", acc, ok)
	}
}

func TestRunAccountHealthChecksOnceAutomaticallyDisablesInvalidCredentials(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"invalid@example.com","password":"wrong"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	checker := accountHealthCheckerFunc(func(_ context.Context, _ config.Account) (string, error) {
		return "", &auth.AccountHealthError{State: account.HealthInvalidCredentials, Message: "password rejected"}
	})

	runAccountHealthChecksOnce(context.Background(), store, pool, resolver, checker)
	acc, ok := store.FindAccount("invalid@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != config.AccountDisabledInvalidCredentials {
		t.Fatalf("expected monitor to persist invalid credential disable, got %#v, %v", acc, ok)
	}
}

func TestAccountHealthFingerprintIsStableAndDoesNotExposeIdentifier(t *testing.T) {
	identifier := "private-account@example.test"
	first := accountHealthFingerprint(identifier)
	second := accountHealthFingerprint(identifier)
	if first == "" || first != second {
		t.Fatalf("account fingerprint must be stable and non-empty: %q, %q", first, second)
	}
	if first == identifier {
		t.Fatalf("account fingerprint must not expose the account identifier")
	}
}
