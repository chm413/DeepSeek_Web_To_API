package server

import (
	"context"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func startProxyMonitor(ctx context.Context, store *config.Store, pool *account.Pool) {
	if store == nil {
		return
	}
	syncRequests := make(chan struct{}, 1)
	store.OnChange(func(config.Config) {
		select {
		case syncRequests <- struct{}{}:
		default:
		}
	})
	go func() {
		config.Logger.Info("[proxy_monitor] started")
		defer config.Logger.Info("[proxy_monitor] stopped")
		initialCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		status := xrayproxy.Probe(initialCtx, xrayproxy.SettingsFromConfig(store.Snapshot().ProxyCore))
		cancel()
		if status.Available {
			config.Logger.Info("[proxy_monitor] xray ready", "version", status.Version, "binary_path", status.BinaryPath)
		} else {
			config.Logger.Warn("[proxy_monitor] xray unavailable", "error", status.Error)
		}
		syncAssignedProxyRoutes(ctx, store)

		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var lastHealthCheck time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-syncRequests:
				syncAssignedProxyRoutes(ctx, store)
			case now := <-ticker.C:
				refreshDueSubscriptions(ctx, store, pool, now)
				policy := store.Snapshot().ProxyPolicy
				if !policy.HealthChecksEnabled() {
					continue
				}
				interval := time.Duration(policy.HealthIntervalMinutes()) * time.Minute
				if !lastHealthCheck.IsZero() && now.Sub(lastHealthCheck) < interval {
					continue
				}
				lastHealthCheck = now
				runScheduledProxyTests(ctx, store, pool)
			}
		}
	}()
}

func syncAssignedProxyRoutes(parent context.Context, store *config.Store) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	if err := xrayproxy.SyncAssigned(ctx, store.Snapshot()); err != nil {
		config.Logger.Warn("[proxy_monitor] xray route sync failed", "error", err)
		return
	}
	config.Logger.Info("[proxy_monitor] xray routes synchronized", "routes", xrayproxy.Default().RouteCount(), "processes", xrayproxy.Default().Count())
}

func refreshDueSubscriptions(parent context.Context, store *config.Store, pool *account.Pool, now time.Time) {
	snapshot := store.Snapshot()
	for _, subscription := range snapshot.ProxySubscriptions {
		if subscription.Disabled || subscription.AutoUpdateDisabled {
			continue
		}
		interval := time.Duration(subscription.EffectiveUpdateIntervalMinutes(snapshot.ProxyPolicy)) * time.Minute
		lastAttempt := time.Unix(subscription.LastAttemptAtUnix, 0)
		if subscription.LastAttemptAtUnix > 0 && now.Sub(lastAttempt) < interval {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		result, err := proxyservice.RefreshSubscription(ctx, store, subscription.ID)
		cancel()
		if err != nil {
			config.Logger.Warn("[proxy_monitor] subscription refresh failed", "subscription_id", subscription.ID, "error", err)
			continue
		}
		config.Logger.Info("[proxy_monitor] subscription refreshed", "subscription_id", subscription.ID, "nodes", result.NodeCount, "added", result.Added, "updated", result.Updated, "removed", result.Removed, "invalid", result.Invalid)
		if pool != nil {
			pool.Reset()
		}
		if subscription.AutoTestDisabled {
			continue
		}
		updated := store.Snapshot()
		ids := make([]string, 0, result.NodeCount)
		for _, proxy := range updated.Proxies {
			if strings.TrimSpace(proxy.SubscriptionID) == subscription.ID {
				ids = append(ids, proxy.ID)
			}
		}
		ctx, cancel = context.WithTimeout(parent, 10*time.Minute)
		testResults, testErr := proxyservice.TestProxies(ctx, store, ids, updated.ProxyPolicy.Concurrency(), nil)
		cancel()
		if testErr != nil {
			config.Logger.Warn("[proxy_monitor] subscription node tests failed", "subscription_id", subscription.ID, "error", testErr)
			continue
		}
		logProxyTestSummary("subscription", subscription.ID, testResults)
	}
}

func runScheduledProxyTests(parent context.Context, store *config.Store, pool *account.Pool) {
	snapshot := store.Snapshot()
	ids := proxyservice.ScheduledProxyIDs(snapshot)
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	results, err := proxyservice.TestProxies(ctx, store, ids, snapshot.ProxyPolicy.Concurrency(), nil)
	if err != nil {
		config.Logger.Warn("[proxy_monitor] scheduled proxy tests failed", "error", err)
		return
	}
	if pool != nil {
		pool.Reset()
	}
	logProxyTestSummary("scheduled", "", results)
}

func logProxyTestSummary(source, subscriptionID string, results []proxyservice.ProxyTestResult) {
	passed := 0
	autoDisabled := 0
	autoEnabled := 0
	for _, result := range results {
		if result.Success {
			passed++
		}
		if result.AutoDisabled {
			autoDisabled++
		}
		if result.AutoEnabled {
			autoEnabled++
		}
	}
	config.Logger.Info("[proxy_monitor] proxy tests completed", "source", source, "subscription_id", subscriptionID, "total", len(results), "passed", passed, "failed", len(results)-passed, "auto_disabled", autoDisabled, "auto_enabled", autoEnabled)
}
