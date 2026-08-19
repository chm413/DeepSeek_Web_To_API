package config

import "time"

const (
	DefaultProxyHealthCheckIntervalMinutes        = 15
	DefaultProxyAutoDisableAfterFailures          = 3
	DefaultProxySubscriptionUpdateIntervalMinutes = 60
	DefaultProxyTestConcurrency                   = 4
)

func (p ProxyPolicyConfig) HealthChecksEnabled() bool {
	return p.HealthCheckEnabled == nil || *p.HealthCheckEnabled
}

func (p ProxyPolicyConfig) AutoRouteEnabled() bool {
	return p.AutomaticRoutingEnabled != nil && *p.AutomaticRoutingEnabled
}

func (p ProxyPolicyConfig) HealthIntervalMinutes() int {
	if p.HealthCheckIntervalMinutes > 0 {
		return p.HealthCheckIntervalMinutes
	}
	return DefaultProxyHealthCheckIntervalMinutes
}

func (p ProxyPolicyConfig) DisableAfterFailures() int {
	if p.AutoDisableAfterFailures > 0 {
		return p.AutoDisableAfterFailures
	}
	return DefaultProxyAutoDisableAfterFailures
}

func (p ProxyPolicyConfig) EnableOnRecovery() bool {
	return p.AutoEnableOnRecovery == nil || *p.AutoEnableOnRecovery
}

func (p ProxyPolicyConfig) SubscriptionIntervalMinutes() int {
	if p.SubscriptionUpdateIntervalMinutes > 0 {
		return p.SubscriptionUpdateIntervalMinutes
	}
	return DefaultProxySubscriptionUpdateIntervalMinutes
}

func (p ProxyPolicyConfig) Concurrency() int {
	if p.TestConcurrency > 0 {
		return p.TestConcurrency
	}
	return DefaultProxyTestConcurrency
}

func (s ProxySubscription) EffectiveUpdateIntervalMinutes(policy ProxyPolicyConfig) int {
	if s.UpdateIntervalMinutes > 0 {
		return s.UpdateIntervalMinutes
	}
	return policy.SubscriptionIntervalMinutes()
}

// ProxyRouteHealthMaxAge is the maximum age of a successful probe that may
// still be used for egress routing. Keep it in config so the router, request
// client, and admin status endpoint apply the same freshness rule.
func ProxyRouteHealthMaxAge(policy ProxyPolicyConfig) time.Duration {
	maxAge := time.Duration(policy.HealthIntervalMinutes()*2) * time.Minute
	if maxAge < 30*time.Minute {
		return 30 * time.Minute
	}
	return maxAge
}

// ProxyAvailableForRouting reports whether a proxy is currently safe to use
// as an automatically selected egress route. Explicit manual routes may use
// a configured fallback, but automatic routes must not keep using a stale
// health result indefinitely.
func ProxyAvailableForRouting(proxy Proxy, policy ProxyPolicyConfig) bool {
	return ProxyAvailableForRoutingAt(proxy, time.Now().Unix(), ProxyRouteHealthMaxAge(policy))
}

// ProxyAvailableForRoutingAt is the deterministic form used by tests and by
// callers that already have a single reference timestamp.
func ProxyAvailableForRoutingAt(proxy Proxy, nowUnix int64, maxAge time.Duration) bool {
	proxy = NormalizeProxy(proxy)
	if proxy.Disabled || proxy.LastTestAtUnix <= 0 || !proxy.LastTestSuccess {
		return false
	}
	if maxAge <= 0 || nowUnix <= proxy.LastTestAtUnix {
		return true
	}
	return nowUnix-proxy.LastTestAtUnix <= int64(maxAge/time.Second)
}
