package account

import (
	"sort"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

type Pool struct {
	store                  *config.Store
	mu                     sync.Mutex
	queue                  []string
	inUse                  map[string]int
	waiters                []chan struct{}
	health                 map[string]Health
	maxInflightPerAccount  int
	recommendedConcurrency int
	maxQueueSize           int
	globalMaxInflight      int
	Affinity               *Affinity
}

func NewPool(store *config.Store) *Pool {
	maxPer := 2
	if store != nil {
		maxPer = store.RuntimeAccountMaxInflight()
	}
	p := &Pool{
		store:                 store,
		inUse:                 map[string]int{},
		health:                map[string]Health{},
		maxInflightPerAccount: maxPer,
		Affinity:              NewAffinity(),
	}
	p.Reset()
	return p
}

func (p *Pool) Reset() {
	if p == nil || p.store == nil {
		return
	}
	accounts := p.store.Accounts()
	now := time.Now()
	sort.SliceStable(accounts, func(i, j int) bool {
		iHas := accounts[i].Token != ""
		jHas := accounts[j].Token != ""
		if iHas == jHas {
			return i < j
		}
		return iHas
	})
	ids := make([]string, 0, len(accounts))
	persistedHealth := make(map[string]Health, len(accounts))
	for _, a := range accounts {
		if a.Disabled {
			continue
		}
		id := a.Identifier()
		if id != "" {
			ids = append(ids, id)
			if health, ok := persistedCooldownHealth(a, now); ok {
				persistedHealth[id] = health
			}
		}
	}
	if p.store != nil {
		p.maxInflightPerAccount = p.store.RuntimeAccountMaxInflight()
	} else {
		p.maxInflightPerAccount = maxInflightFromEnv()
	}
	recommended := defaultRecommendedConcurrency(len(ids), p.maxInflightPerAccount)
	queueLimit := maxQueueFromEnv(recommended)
	globalLimit := recommended
	if p.store != nil {
		queueLimit = p.store.RuntimeAccountMaxQueue(recommended)
		globalLimit = p.store.RuntimeGlobalMaxInflight(recommended)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drainWaitersLocked()
	p.queue = ids
	p.inUse = map[string]int{}
	if p.health == nil {
		p.health = map[string]Health{}
	}
	active := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		active[id] = struct{}{}
	}
	for id := range p.health {
		if _, ok := active[id]; !ok {
			delete(p.health, id)
			continue
		}
		if persisted, ok := persistedHealth[id]; ok {
			p.health[id] = persisted
			continue
		}
		if health := p.health[id]; isTransientHealth(health.State) && !health.Until.After(now) {
			delete(p.health, id)
		}
	}
	for id, health := range persistedHealth {
		p.health[id] = health
	}
	p.recommendedConcurrency = recommended
	p.maxQueueSize = queueLimit
	p.globalMaxInflight = globalLimit
	config.Logger.Info(
		"[init_account_queue] initialized",
		"total", len(ids),
		"max_inflight_per_account", p.maxInflightPerAccount,
		"global_max_inflight", p.globalMaxInflight,
		"recommended_concurrency", p.recommendedConcurrency,
		"max_queue_size", p.maxQueueSize,
	)
	warnLowGlobalMaxInflight(p.globalMaxInflight, p.maxInflightPerAccount, len(ids))
}

func persistedCooldownHealth(acc config.Account, now time.Time) (Health, bool) {
	until := time.Unix(acc.CooldownUntilUnix, 0)
	if !until.After(now) {
		return Health{}, false
	}
	state := HealthState(acc.CooldownState)
	if state != HealthRateLimited && state != HealthTemporarilyMuted {
		return Health{}, false
	}
	return Health{
		State:     state,
		Until:     until,
		Reason:    "restored persisted cooldown",
		UpdatedAt: now,
	}, true
}

func (p *Pool) Release(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	count := p.inUse[accountID]
	if count <= 0 {
		return
	}
	if count == 1 {
		delete(p.inUse, accountID)
		p.notifyWaiterLocked()
		return
	}
	p.inUse[accountID] = count - 1
	p.notifyWaiterLocked()
}

// AccountInUse reports whether an account currently owns at least one request
// slot. Background checks use this to avoid competing with a real completion
// on the same upstream account.
func (p *Pool) AccountInUse(accountID string) bool {
	if p == nil || accountID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inUse[accountID] > 0
}

func (p *Pool) Status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	available := make([]string, 0, len(p.queue))
	inUseAccounts := make([]string, 0, len(p.inUse))
	inUseSlots := 0
	for _, id := range p.queue {
		if _, blocked := p.accountHealthLocked(id); !blocked && p.inUse[id] < p.maxInflightPerAccount {
			available = append(available, id)
		}
	}
	for id, count := range p.inUse {
		if count > 0 {
			inUseAccounts = append(inUseAccounts, id)
			inUseSlots += count
		}
	}
	health := make(map[string]map[string]any, len(p.health))
	for id, h := range p.health {
		if isTransientHealth(h.State) && !h.Until.After(time.Now()) {
			delete(p.health, id)
			continue
		}
		item := map[string]any{
			"state":      h.State,
			"reason":     h.Reason,
			"updated_at": h.UpdatedAt,
		}
		if !h.Until.IsZero() {
			item["until"] = h.Until
		}
		health[id] = item
	}
	sort.Strings(inUseAccounts)
	allAccounts := p.store.Accounts()
	disabled := 0
	runtimeAccounts := make(map[string]map[string]any, len(allAccounts))
	stateCounts := map[string]int{
		"idle": 0, "busy": 0, "saturated": 0, "disabled": 0,
		"rate_limited": 0, "temporarily_muted": 0,
		"invalid_credentials": 0, "permanently_banned": 0,
	}
	for _, acc := range allAccounts {
		id := acc.Identifier()
		inUse := p.inUse[id]
		availableSlots := p.maxInflightPerAccount - inUse
		if availableSlots < 0 {
			availableSlots = 0
		}
		activityState := "idle"
		if inUse >= p.maxInflightPerAccount {
			activityState = "saturated"
		} else if inUse > 0 {
			activityState = "busy"
		}
		state := activityState
		if acc.Disabled {
			disabled++
			switch acc.DisabledReason {
			case config.AccountDisabledUpstreamBanned:
				state = string(HealthPermanentlyBanned)
			case config.AccountDisabledInvalidCredentials:
				state = string(HealthInvalidCredentials)
			default:
				state = "disabled"
			}
			availableSlots = 0
		} else if currentHealth, blocked := p.accountHealthLocked(id); blocked {
			state = string(currentHealth.State)
			availableSlots = 0
		}
		stateCounts[state]++
		utilization := 0.0
		if p.maxInflightPerAccount > 0 {
			utilization = float64(inUse) * 100 / float64(p.maxInflightPerAccount)
		}
		runtimeAccounts[id] = map[string]any{
			"state":               state,
			"activity_state":      activityState,
			"in_use":              inUse,
			"max_inflight":        p.maxInflightPerAccount,
			"available_slots":     availableSlots,
			"utilization_percent": utilization,
		}
	}
	return map[string]any{
		"available":                len(available),
		"in_use":                   inUseSlots,
		"total":                    len(allAccounts),
		"enabled":                  len(allAccounts) - disabled,
		"disabled":                 disabled,
		"available_accounts":       available,
		"in_use_accounts":          inUseAccounts,
		"max_inflight_per_account": p.maxInflightPerAccount,
		"global_max_inflight":      p.globalMaxInflight,
		"recommended_concurrency":  p.recommendedConcurrency,
		"waiting":                  len(p.waiters),
		"max_queue_size":           p.maxQueueSize,
		"account_health":           health,
		"account_runtime":          runtimeAccounts,
		"state_counts":             stateCounts,
	}
}
