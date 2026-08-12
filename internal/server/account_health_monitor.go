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
	accountHealthWorkers      = 4
)

type accountHealthChecker interface {
	CheckAccountHealth(context.Context, config.Account) (string, error)
}

func startAccountHealthMonitor(ctx context.Context, store *config.Store, pool *account.Pool, resolver *auth.Resolver, client accountHealthChecker, interval time.Duration) {
	if interval <= 0 || store == nil || pool == nil || resolver == nil || client == nil {
		return
	}
	config.Logger.Info("[account_health_monitor] started", "interval", interval, "workers", accountHealthWorkers)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				config.Logger.Info("[account_health_monitor] stopped")
				return
			case <-ticker.C:
				runAccountHealthChecksOnce(ctx, store, pool, resolver, client)
			}
		}
	}()
}

func runAccountHealthChecksOnce(ctx context.Context, store *config.Store, pool *account.Pool, resolver *auth.Resolver, client accountHealthChecker) {
	accounts := store.Accounts()
	enabled := make([]config.Account, 0, len(accounts))
	for _, acc := range accounts {
		if !acc.Disabled {
			enabled = append(enabled, acc)
		}
	}
	started := time.Now()
	config.Logger.Info("[account_health_monitor] check started", "enabled_accounts", len(enabled), "disabled_accounts", len(accounts)-len(enabled))
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
	config.Logger.Info("[account_health_monitor] check completed", "accounts", len(enabled), "elapsed_ms", time.Since(started).Milliseconds())
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
