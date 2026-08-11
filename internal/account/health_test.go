package account

import (
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

func TestPoolHealthSkipsTemporaryAndPermanentAccounts(t *testing.T) {
	p := newPoolForTest(t, "1")

	p.MarkTemporaryMute("acc1@example.com", time.Now().Add(50*time.Millisecond), "muted")
	if _, ok := p.Acquire("acc1@example.com", nil); ok {
		t.Fatal("temporarily muted account should not be acquired")
	}
	acc, ok := p.Acquire("acc2@example.com", nil)
	if !ok {
		t.Fatal("healthy account should remain available")
	}
	p.Release(acc.Identifier())

	time.Sleep(70 * time.Millisecond)
	acc, ok = p.Acquire("acc1@example.com", nil)
	if !ok {
		t.Fatal("temporary mute should expire and restore the account")
	}
	p.Release(acc.Identifier())

	p.MarkPermanentlyBanned("acc2@example.com", "banned")
	if _, ok := p.Acquire("acc2@example.com", nil); ok {
		t.Fatal("permanently banned account should never be acquired")
	}
	health, ok := p.AccountHealth("acc2@example.com")
	if !ok || health.State != HealthPermanentlyBanned {
		t.Fatalf("unexpected permanent health state: %#v, %v", health, ok)
	}

	p.MarkInvalidCredentials("acc1@example.com", "password rejected")
	if _, ok := p.Acquire("acc1@example.com", nil); ok {
		t.Fatal("account with invalid credentials should never be acquired")
	}
	health, ok = p.AccountHealth("acc1@example.com")
	if !ok || health.State != HealthInvalidCredentials {
		t.Fatalf("unexpected invalid credential health state: %#v, %v", health, ok)
	}
}

func TestPoolSkipsPersistentlyDisabledAccounts(t *testing.T) {
	p := newPoolForTest(t, "1")
	if err := p.store.SetAccountDisabled("acc1@example.com", true, config.AccountDisabledManual); err != nil {
		t.Fatalf("disable account: %v", err)
	}
	p.Reset()
	if _, ok := p.Acquire("acc1@example.com", nil); ok {
		t.Fatal("targeted acquire must reject a disabled account")
	}
	acc, ok := p.Acquire("", nil)
	if !ok || acc.Identifier() != "acc2@example.com" {
		t.Fatalf("rotation should skip disabled account, got %#v, %v", acc, ok)
	}
	status := p.Status()
	if status["disabled"] != 1 || status["enabled"] != 1 {
		t.Fatalf("unexpected disabled pool status: %#v", status)
	}
}

func TestPoolRateLimitAndRuntimeStatus(t *testing.T) {
	p := newPoolForTest(t, "2")
	acc, ok := p.Acquire("acc1@example.com", nil)
	if !ok {
		t.Fatal("expected account acquisition")
	}
	status := p.Status()
	runtimeAccounts, _ := status["account_runtime"].(map[string]map[string]any)
	runtime := runtimeAccounts[acc.Identifier()]
	if runtime["state"] != "busy" || runtime["in_use"] != 1 || runtime["available_slots"] != 1 {
		t.Fatalf("unexpected busy runtime: %#v", runtime)
	}
	p.Release(acc.Identifier())

	p.MarkRateLimited(acc.Identifier(), time.Now().Add(50*time.Millisecond), "HTTP 429")
	if _, ok := p.Acquire(acc.Identifier(), nil); ok {
		t.Fatal("rate-limited account should not be acquired")
	}
	status = p.Status()
	runtimeAccounts, _ = status["account_runtime"].(map[string]map[string]any)
	if runtimeAccounts[acc.Identifier()]["state"] != string(HealthRateLimited) {
		t.Fatalf("unexpected rate-limited runtime: %#v", runtimeAccounts[acc.Identifier()])
	}
	time.Sleep(70 * time.Millisecond)
	if _, ok := p.Acquire(acc.Identifier(), nil); !ok {
		t.Fatal("rate-limit cooldown should expire")
	}
}
