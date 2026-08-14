package config

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
