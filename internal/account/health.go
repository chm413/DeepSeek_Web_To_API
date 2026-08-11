package account

import "time"

// HealthState describes account-level upstream availability. Transport and
// prompt-limit failures deliberately do not use these states.
type HealthState string

const (
	HealthHealthy            HealthState = "healthy"
	HealthDisabled           HealthState = "disabled"
	HealthRateLimited        HealthState = "rate_limited"
	HealthTemporarilyMuted   HealthState = "temporarily_muted"
	HealthInvalidCredentials HealthState = "invalid_credentials"
	HealthPermanentlyBanned  HealthState = "permanently_banned"
)

type Health struct {
	State     HealthState
	Until     time.Time
	Reason    string
	UpdatedAt time.Time
}

const defaultMuteCooldown = 7 * 24 * time.Hour
const defaultRateLimitCooldown = time.Minute

func (p *Pool) MarkRateLimited(accountID string, until time.Time, reason string) {
	if until.IsZero() {
		until = time.Now().Add(defaultRateLimitCooldown)
	}
	p.markTransientAccount(accountID, HealthRateLimited, until, reason)
}

func (p *Pool) MarkTemporaryMute(accountID string, until time.Time, reason string) {
	if until.IsZero() {
		until = time.Now().Add(defaultMuteCooldown)
	}
	p.markTransientAccount(accountID, HealthTemporarilyMuted, until, reason)
}

func (p *Pool) markTransientAccount(accountID string, state HealthState, until time.Time, reason string) {
	if p == nil || accountID == "" {
		return
	}
	now := time.Now()
	if !until.After(now) {
		p.ClearHealth(accountID)
		return
	}
	p.mu.Lock()
	if p.health == nil {
		p.health = map[string]Health{}
	}
	p.health[accountID] = Health{
		State:     state,
		Until:     until,
		Reason:    reason,
		UpdatedAt: now,
	}
	p.mu.Unlock()
	if p.Affinity != nil {
		p.Affinity.ForgetAccount(accountID)
	}
	p.notifyWaiters()
}

func (p *Pool) MarkPermanentlyBanned(accountID, reason string) {
	p.markBlockedAccount(accountID, HealthPermanentlyBanned, reason)
}

func (p *Pool) MarkInvalidCredentials(accountID, reason string) {
	p.markBlockedAccount(accountID, HealthInvalidCredentials, reason)
}

func (p *Pool) markBlockedAccount(accountID string, state HealthState, reason string) {
	if p == nil || accountID == "" {
		return
	}
	p.mu.Lock()
	if p.health == nil {
		p.health = map[string]Health{}
	}
	p.health[accountID] = Health{
		State:     state,
		Reason:    reason,
		UpdatedAt: time.Now(),
	}
	p.mu.Unlock()
	if p.Affinity != nil {
		p.Affinity.ForgetAccount(accountID)
	}
	p.notifyWaiters()
}

func (p *Pool) ClearHealth(accountID string) {
	if p == nil || accountID == "" {
		return
	}
	p.mu.Lock()
	delete(p.health, accountID)
	p.mu.Unlock()
	p.notifyWaiters()
}

func (p *Pool) AccountHealth(accountID string) (Health, bool) {
	if p == nil || accountID == "" {
		return Health{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.health[accountID]
	if !ok {
		return Health{}, false
	}
	if isTransientHealth(h.State) && !h.Until.After(time.Now()) {
		delete(p.health, accountID)
		return Health{}, false
	}
	return h, true
}

func (p *Pool) accountHealthLocked(accountID string) (Health, bool) {
	h, ok := p.health[accountID]
	if !ok {
		return Health{}, false
	}
	if isTransientHealth(h.State) && !h.Until.After(time.Now()) {
		delete(p.health, accountID)
		return Health{}, false
	}
	return h, true
}

func isTransientHealth(state HealthState) bool {
	return state == HealthTemporarilyMuted || state == HealthRateLimited
}

func (p *Pool) notifyWaiters() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifyWaiterLocked()
}
