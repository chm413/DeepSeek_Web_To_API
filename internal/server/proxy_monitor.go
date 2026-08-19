package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/proxyservice"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func startProxyMonitor(ctx context.Context, store *config.Store, pool *account.Pool, ds *dsclient.Client) {
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
		status := xrayproxy.ProbeWithStore(initialCtx, store)
		cancel()
		if status.Available {
			config.Logger.Info("[proxy_monitor] xray ready", "version", status.Version, "binary_path", status.BinaryPath)
		} else {
			config.Logger.Warn("[proxy_monitor] xray unavailable", "error", status.Error)
		}
		reconcileAndSyncProxyRoutes(ctx, store, pool, ds)

		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var lastHealthCheck time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-syncRequests:
				reconcileAndSyncProxyRoutes(ctx, store, pool, ds)
			case now := <-ticker.C:
				refreshDueSubscriptions(ctx, store, pool, ds, now)
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

func reconcileAndSyncProxyRoutes(parent context.Context, store *config.Store, pool *account.Pool, ds *dsclient.Client) {
	changes, err := proxyservice.ReconcileAutoRoutes(store)
	if err != nil {
		config.Logger.Warn("[proxy_router] automatic route reconciliation failed", "error", err)
	}
	syncAssignedProxyRoutes(parent, store)
	if len(changes) == 0 {
		return
	}
	if pool != nil {
		pool.Reset()
	}
	config.Logger.Info("[proxy_router] automatic routes changed", "accounts", len(changes), "available_nodes", len(proxyservice.AvailableRoutePool(store.Snapshot())))
	reloginProxyRouteChanges(parent, store, pool, ds, changes)
}

func reloginProxyRouteChanges(parent context.Context, store *config.Store, pool *account.Pool, ds *dsclient.Client, changes []proxyservice.AutoRouteChange) {
	if store == nil || ds == nil || len(changes) == 0 {
		return
	}
	jobs := make(chan proxyservice.AutoRouteChange)
	var workers sync.WaitGroup
	workerCount := 4
	if len(changes) < workerCount {
		workerCount = len(changes)
	}
	for index := 0; index < workerCount; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for change := range jobs {
				if strings.TrimSpace(change.ToProxyID) == "" {
					config.Logger.Warn("[proxy_router] account is waiting for an available route", "account", change.AccountID, "from_proxy_id", change.FromProxyID, "reason", change.Reason)
					continue
				}
				accountConfig, ok := store.FindAccount(change.AccountID)
				if !ok || accountConfig.Disabled || strings.TrimSpace(accountConfig.Password) == "" {
					config.Logger.Warn("[proxy_router] automatic relogin skipped", "account", change.AccountID, "proxy_id", change.ToProxyID, "account_found", ok)
					continue
				}
				loginCtx, cancel := context.WithTimeout(parent, 45*time.Second)
				token, loginErr := ds.Login(loginCtx, accountConfig)
				cancel()
				if loginErr != nil {
					config.Logger.Warn("[proxy_router] relogin after route change failed", "account", change.AccountID, "from_proxy_id", change.FromProxyID, "to_proxy_id", change.ToProxyID, "reason", change.Reason, "error", loginErr)
					continue
				}
				if err := store.UpdateAccountToken(change.AccountID, token); err != nil {
					config.Logger.Warn("[proxy_router] persist relogin token failed", "account", change.AccountID, "proxy_id", change.ToProxyID, "error", err)
					continue
				}
				config.Logger.Info("[proxy_router] relogin after route change succeeded", "account", change.AccountID, "from_proxy_id", change.FromProxyID, "to_proxy_id", change.ToProxyID, "reason", change.Reason)
			}
		}()
	}
	for _, change := range changes {
		select {
		case jobs <- change:
		case <-parent.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
	if pool != nil {
		pool.Reset()
	}
}

func syncAssignedProxyRoutes(parent context.Context, store *config.Store) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	if err := xrayproxy.SyncAssignedWithStore(ctx, store); err != nil {
		config.Logger.Warn("[proxy_monitor] xray route sync failed", "error", err)
		return
	}
	config.Logger.Info("[proxy_monitor] xray routes synchronized", "routes", xrayproxy.Default().RouteCount(), "processes", xrayproxy.Default().Count())
}

func refreshDueSubscriptions(parent context.Context, store *config.Store, pool *account.Pool, ds *dsclient.Client, now time.Time) {
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
			var commitErr *proxyservice.SubscriptionRefreshCommitError
			if len(result.RouteChanges) > 0 || errors.As(err, &commitErr) {
				// RefreshSubscription may have committed the new assignments and
				// cleared account tokens before Xray synchronization failed. Do not
				// discard those changes; retry synchronization and relogin now.
				config.Logger.Warn("[proxy_monitor] subscription refresh partially committed", "subscription_id", subscription.ID, "error", err, "route_changes", len(result.RouteChanges))
				if pool != nil {
					pool.Reset()
				}
				syncAssignedProxyRoutes(parent, store)
				reloginProxyRouteChanges(parent, store, pool, ds, result.RouteChanges)
			} else {
				config.Logger.Warn("[proxy_monitor] subscription refresh failed", "subscription_id", subscription.ID, "error", err)
			}
			continue
		}
		config.Logger.Info("[proxy_monitor] subscription refreshed", "subscription_id", subscription.ID, "nodes", result.NodeCount, "added", result.Added, "updated", result.Updated, "removed", result.Removed, "invalid", result.Invalid)
		if len(result.RouteChanges) > 0 {
			if pool != nil {
				pool.Reset()
			}
			config.Logger.Info("[proxy_monitor] subscription route assignments changed", "subscription_id", subscription.ID, "accounts", len(result.RouteChanges))
			reloginProxyRouteChanges(parent, store, pool, ds, result.RouteChanges)
		}
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
