package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

const (
	accountHealthCheckTimeout = 30 * time.Second
	// Current-user polling is intentionally serialized. A monitor request is
	// lower priority than actual traffic and must not create a burst across a
	// shared upstream exit when the account fleet is large.
	accountHealthWorkers = 1
)

type accountHealthChecker interface {
	CheckAccountHealth(context.Context, config.Account) (string, error)
}

func startAccountHealthMonitor(ctx context.Context, store *config.Store, pool *account.Pool, resolver *auth.Resolver, client accountHealthChecker, interval time.Duration) {
	if interval <= 0 || store == nil || pool == nil || resolver == nil || client == nil {
		return
	}
	config.Logger.Info("[account_health_monitor] started", "interval", interval, "workers", accountHealthWorkers, "mode", "round_robin_one_account_per_interval")
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		cursor := ""
		for {
			select {
			case <-ctx.Done():
				config.Logger.Info("[account_health_monitor] stopped")
				return
			case <-ticker.C:
				runNextAccountHealthCheck(ctx, store, pool, resolver, client, &cursor)
			}
		}
	}()
}

func runAccountHealthChecksOnce(ctx context.Context, store *config.Store, pool *account.Pool, resolver *auth.Resolver, client accountHealthChecker) {
	candidates := collectAccountHealthCandidates(store, pool)
	enabled := candidates.accounts
	started := time.Now()
	logAccountHealthCheckStarted(candidates, len(enabled), "full_sweep")
	jobs := make(chan config.Account)
	var wg sync.WaitGroup
	workers := accountHealthWorkers
	if len(enabled) < workers {
		workers = len(enabled)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for acc := range jobs {
				checkAccountHealth(ctx, store, pool, resolver, client, acc)
			}
		}()
	}
	for _, acc := range enabled {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case jobs <- acc:
		}
	}
	close(jobs)
	wg.Wait()
	config.Logger.Info("[account_health_monitor] check completed",
		"accounts", len(enabled),
		"skipped_cooldown", candidates.skippedCooldown,
		"skipped_busy", candidates.skippedBusy,
		"skipped_tokenless", candidates.skippedTokenless,
		"elapsed_ms", time.Since(started).Milliseconds())
}

// runNextAccountHealthCheck is the production scheduler. Sampling one account
// per interval avoids turning a short configured interval into a full-account
// burst against the same upstream exit. runAccountHealthChecksOnce remains a
// full sweep helper for explicit test coverage.
func runNextAccountHealthCheck(ctx context.Context, store *config.Store, pool *account.Pool, resolver *auth.Resolver, client accountHealthChecker, cursor *string) {
	candidates := collectAccountHealthCandidates(store, pool)
	selected, ok := nextAccountHealthCandidate(candidates.accounts, cursor)
	scheduled := 0
	if ok {
		scheduled = 1
	}
	logAccountHealthCheckStarted(candidates, scheduled, "round_robin")
	if !ok {
		config.Logger.Info("[account_health_monitor] check completed",
			"accounts", 0,
			"skipped_cooldown", candidates.skippedCooldown,
			"skipped_busy", candidates.skippedBusy,
			"skipped_tokenless", candidates.skippedTokenless,
			"elapsed_ms", 0)
		return
	}
	started := time.Now()
	checkAccountHealth(ctx, store, pool, resolver, client, selected)
	config.Logger.Info("[account_health_monitor] check completed",
		"accounts", 1,
		"skipped_cooldown", candidates.skippedCooldown,
		"skipped_busy", candidates.skippedBusy,
		"skipped_tokenless", candidates.skippedTokenless,
		"elapsed_ms", time.Since(started).Milliseconds())
}

type accountHealthCandidates struct {
	accounts         []config.Account
	enabledAccounts  int
	disabledAccounts int
	skippedCooldown  int
	skippedBusy      int
	skippedTokenless int
	observedAt       time.Time
}

func collectAccountHealthCandidates(store *config.Store, pool *account.Pool) accountHealthCandidates {
	accounts := store.Accounts()
	candidates := accountHealthCandidates{
		accounts:   make([]config.Account, 0, len(accounts)),
		observedAt: time.Now(),
	}
	for _, acc := range accounts {
		if acc.Disabled {
			candidates.disabledAccounts++
			continue
		}
		candidates.enabledAccounts++
		if strings.TrimSpace(acc.Token) == "" {
			// Scheduled health checks must not turn into repeated password
			// logins. A tokenless account is authenticated only when a real
			// request is routed to it or the operator explicitly tests it.
			candidates.skippedTokenless++
			continue
		}
		if health, blocked := pool.AccountHealth(acc.Identifier()); blocked {
			if health.State != account.HealthHealthy && (!health.Until.IsZero() || health.State == account.HealthPermanentlyBanned || health.State == account.HealthInvalidCredentials) {
				candidates.skippedCooldown++
				continue
			}
		}
		if pool.AccountInUse(acc.Identifier()) {
			candidates.skippedBusy++
			continue
		}
		candidates.accounts = append(candidates.accounts, acc)
	}
	return candidates
}

func logAccountHealthCheckStarted(candidates accountHealthCandidates, scheduled int, mode string) {
	config.Logger.Info("[account_health_monitor] check started",
		"mode", mode,
		"enabled_accounts", candidates.enabledAccounts,
		"eligible_accounts", len(candidates.accounts),
		"scheduled_accounts", scheduled,
		"disabled_accounts", candidates.disabledAccounts,
		"skipped_cooldown", candidates.skippedCooldown,
		"skipped_busy", candidates.skippedBusy,
		"skipped_tokenless", candidates.skippedTokenless,
		"observed_at", candidates.observedAt)
}

func nextAccountHealthCandidate(candidates []config.Account, cursor *string) (config.Account, bool) {
	if len(candidates) == 0 {
		return config.Account{}, false
	}
	start := 0
	if cursor != nil && *cursor != "" {
		for i, acc := range candidates {
			if acc.Identifier() == *cursor {
				start = (i + 1) % len(candidates)
				break
			}
		}
	}
	selected := candidates[start]
	if cursor != nil {
		*cursor = selected.Identifier()
	}
	return selected, true
}

func checkAccountHealth(parent context.Context, store *config.Store, pool *account.Pool, resolver *auth.Resolver, client accountHealthChecker, acc config.Account) {
	started := time.Now()
	accountFingerprint := accountHealthFingerprint(acc.Identifier())
	ctx, cancel := context.WithTimeout(parent, accountHealthCheckTimeout)
	defer cancel()
	token, err := client.CheckAccountHealth(ctx, acc)
	if err != nil {
		var healthErr *auth.AccountHealthError
		if errors.As(err, &healthErr) {
			resolver.MarkAccountHealth(acc.Identifier(), healthErr)
			config.Logger.Warn("[account_health_monitor] unhealthy", "account_fingerprint", accountFingerprint, "state", healthErr.State, "code", healthErr.Code, "auto_disabled", healthErr.State == account.HealthPermanentlyBanned || healthErr.State == account.HealthInvalidCredentials, "elapsed_ms", time.Since(started).Milliseconds())
			return
		}
		config.Logger.Warn("[account_health_monitor] check failed", "account_fingerprint", accountFingerprint, "error", err, "elapsed_ms", time.Since(started).Milliseconds())
		return
	}
	if current, ok := pool.AccountHealth(acc.Identifier()); ok && preserveMonitorTransientHealth(current, time.Now()) {
		config.Logger.Info("[account_health_monitor] healthy endpoint did not clear active runtime cooldown", "account_fingerprint", accountFingerprint, "state", current.State, "until", current.Until, "elapsed_ms", time.Since(started).Milliseconds())
	} else {
		pool.ClearHealth(acc.Identifier())
		if err := store.ClearAccountCooldown(acc.Identifier()); err != nil {
			config.Logger.Warn("[account_health_monitor] clear persisted cooldown failed", "account_fingerprint", accountFingerprint, "error", err)
		}
	}
	if token != "" && token != acc.Token {
		if err := store.UpdateAccountToken(acc.Identifier(), token); err != nil {
			config.Logger.Warn("[account_health_monitor] token persistence failed", "account_fingerprint", accountFingerprint, "error", err)
		}
	}
	config.Logger.Info("[account_health_monitor] healthy", "account_fingerprint", accountFingerprint, "elapsed_ms", time.Since(started).Milliseconds())
}

func preserveMonitorTransientHealth(health account.Health, now time.Time) bool {
	if health.State != account.HealthTemporarilyMuted && health.State != account.HealthRateLimited {
		return false
	}
	return health.Until.After(now)
}

func accountHealthFingerprint(accountID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accountID)))
	return hex.EncodeToString(sum[:8])
}
