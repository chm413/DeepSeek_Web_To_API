package server

import (
	"context"
	"testing"
	"time"

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
	if err := store.UpdateAccountToken("banned@example.com", "token-1"); err != nil {
		t.Fatalf("seed account token: %v", err)
	}
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
	if err := store.UpdateAccountToken("invalid@example.com", "token-1"); err != nil {
		t.Fatalf("seed account token: %v", err)
	}
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

func TestHealthMonitorDoesNotClearActiveCompletionMute(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"muted@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("muted@example.com", "token-1"); err != nil {
		t.Fatalf("seed muted account token: %v", err)
	}
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	until := time.Now().Add(time.Hour)
	pool.MarkTemporaryMute("muted@example.com", until, "completion returned user is muted")
	checker := accountHealthCheckerFunc(func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	runAccountHealthChecksOnce(context.Background(), store, pool, resolver, checker)
	health, ok := pool.AccountHealth("muted@example.com")
	if !ok || health.State != account.HealthTemporarilyMuted || !health.Until.Equal(until) {
		t.Fatalf("expected active completion mute to survive monitor check, got %#v, %v", health, ok)
	}
}

func TestHealthMonitorSkipsCooldownBusyAndTokenlessAccounts(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[
			{"email":"ready@example.com","token":"ready-token"},
			{"email":"muted@example.com","token":"muted-token"},
			{"email":"busy@example.com","token":"busy-token"},
			{"email":"tokenless@example.com","password":"password"}
		]
	}`)
	store := config.LoadStore()
	for _, identifier := range []string{"ready@example.com", "muted@example.com", "busy@example.com"} {
		if err := store.UpdateAccountToken(identifier, identifier+"-token"); err != nil {
			t.Fatalf("seed %s token: %v", identifier, err)
		}
	}
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	pool.MarkTemporaryMute("muted@example.com", time.Now().Add(time.Hour), "upstream mute")
	if _, ok := pool.Acquire("busy@example.com", nil); !ok {
		t.Fatal("expected busy account to acquire")
	}
	defer pool.Release("busy@example.com")

	checked := make(map[string]int)
	checker := accountHealthCheckerFunc(func(_ context.Context, acc config.Account) (string, error) {
		checked[acc.Identifier()]++
		return acc.Token, nil
	})
	runAccountHealthChecksOnce(context.Background(), store, pool, resolver, checker)
	if len(checked) != 1 || checked["ready@example.com"] != 1 {
		t.Fatalf("scheduled checker hit protected accounts: %#v", checked)
	}
}

func TestRunNextAccountHealthCheckRotatesOneEligibleAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[
			{"email":"one@example.com","token":"token-1"},
			{"email":"two@example.com","token":"token-2"},
			{"email":"three@example.com","token":"token-3"}
		]
	}`)
	store := config.LoadStore()
	for _, identifier := range []string{"one@example.com", "two@example.com", "three@example.com"} {
		if err := store.UpdateAccountToken(identifier, identifier+"-token"); err != nil {
			t.Fatalf("seed %s token: %v", identifier, err)
		}
	}
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	checked := make([]string, 0, 3)
	checker := accountHealthCheckerFunc(func(_ context.Context, acc config.Account) (string, error) {
		checked = append(checked, acc.Identifier())
		return acc.Token, nil
	})
	cursor := ""
	for range 3 {
		runNextAccountHealthCheck(context.Background(), store, pool, resolver, checker, &cursor)
	}
	if len(checked) != 3 {
		t.Fatalf("expected exactly one check per interval, got %#v", checked)
	}
	seen := make(map[string]bool, len(checked))
	for _, identifier := range checked {
		seen[identifier] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected round-robin coverage without repeated account, got %#v", checked)
	}
}

func TestPreserveMonitorTransientHealthRejectsExpiredCooldown(t *testing.T) {
	now := time.Now()
	if preserveMonitorTransientHealth(account.Health{State: account.HealthTemporarilyMuted, Until: now.Add(-time.Second)}, now) {
		t.Fatal("expired mute must not be preserved")
	}
	if preserveMonitorTransientHealth(account.Health{State: account.HealthPermanentlyBanned}, now) {
		t.Fatal("permanent states are managed by persistent disable, not transient cooldown preservation")
	}
}

func TestHealthyCheckDoesNotClearActivePersistedCooldown(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"muted@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("muted@example.com", "token-1"); err != nil {
		t.Fatalf("seed account token: %v", err)
	}
	if err := store.SetAccountCooldown("muted@example.com", config.AccountCooldownTemporarilyMuted, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed persisted cooldown: %v", err)
	}
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	checker := accountHealthCheckerFunc(func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	acc, ok := store.FindAccount("muted@example.com")
	if !ok {
		t.Fatal("expected seeded account")
	}

	// Even a direct successful probe must not shorten an active upstream mute.
	// This avoids turning background observability into repeated pressure on an
	// account whose authoritative mute_until has not elapsed yet.
	checkAccountHealth(context.Background(), store, pool, resolver, checker, acc)
	stored, ok := store.FindAccount("muted@example.com")
	if !ok || stored.CooldownState != config.AccountCooldownTemporarilyMuted || stored.CooldownUntilUnix == 0 {
		t.Fatalf("expected active cooldown preserved after healthy check, got %#v, %v", stored, ok)
	}
}
