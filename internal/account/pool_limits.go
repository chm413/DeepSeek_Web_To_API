package account

import (
	"os"
	"strconv"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
)

func (p *Pool) ApplyRuntimeLimits(maxInflightPerAccount, maxQueueSize, globalMaxInflight int) {
	if maxInflightPerAccount <= 0 {
		maxInflightPerAccount = 1
	}
	if maxQueueSize < 0 {
		maxQueueSize = 0
	}
	accountCount := 0
	if p.store != nil {
		accountCount = len(p.store.Accounts())
	}
	if globalMaxInflight <= 0 {
		globalMaxInflight = maxInflightPerAccount * accountCount
		if globalMaxInflight <= 0 {
			globalMaxInflight = maxInflightPerAccount
		}
	}
	warnLowGlobalMaxInflight(globalMaxInflight, maxInflightPerAccount, accountCount)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxInflightPerAccount = maxInflightPerAccount
	p.maxQueueSize = maxQueueSize
	p.globalMaxInflight = globalMaxInflight
	p.recommendedConcurrency = defaultRecommendedConcurrency(len(p.queue), p.maxInflightPerAccount)
	p.notifyWaiterLocked()
}

// warnLowGlobalMaxInflight surfaces the most common operator misconfiguration
// behind Issue #19 / "stuck Claude Code parallel tool calls": setting
// global_max_inflight=1 (or 1 with multiple accounts) hard-serializes every
// concurrent request the agent client emits. The model's parallel
// tool_calls block then queues sequentially against a single in-flight
// slot and the operator perceives "ds2api is stuck" while the server is in
// fact healthy. We do not auto-correct (the operator may genuinely want
// throttling — e.g. for risk control), only WARN with a concrete remedy.
func warnLowGlobalMaxInflight(globalMaxInflight, maxInflightPerAccount, accountCount int) {
	if globalMaxInflight <= 0 || maxInflightPerAccount <= 0 || accountCount <= 0 {
		return
	}
	// Severe: global=1 with multiple accounts hard-serializes everything.
	// This is the Issue #19 footgun.
	if globalMaxInflight == 1 && accountCount >= 2 {
		config.Logger.Warn(
			"[account_queue] global_max_inflight=1 hard-serializes ALL concurrent requests across accounts; "+
				"agent clients (Claude Code, etc.) issuing parallel tool_calls will appear to hang. "+
				"Set it to (max_inflight_per_account * account_count) or higher unless you specifically need a global throttle.",
			"global_max_inflight", globalMaxInflight,
			"max_inflight_per_account", maxInflightPerAccount,
			"account_count", accountCount,
			"recommended_global_max_inflight", maxInflightPerAccount*accountCount,
		)
		return
	}
	// Soft: only warn when the cap is so low that fewer than one in-flight
	// slot per account is reachable. Large fleets where the operator has
	// chosen e.g. global=10_000 over per_account*count=11_118 are an
	// intentional throttle, not a misconfiguration — silencing the noise
	// avoids crying wolf at every restart on production.
	if globalMaxInflight < accountCount {
		config.Logger.Warn(
			"[account_queue] global_max_inflight is below account_count; "+
				"on average each account cannot hold even one in-flight slot. "+
				"Increase to at least account_count (or set to 0 for auto = per_account * count).",
			"global_max_inflight", globalMaxInflight,
			"account_count", accountCount,
			"recommended_global_max_inflight", maxInflightPerAccount*accountCount,
		)
	}
}

func maxInflightFromEnv() int {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ACCOUNT_MAX_INFLIGHT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func defaultRecommendedConcurrency(accountCount, maxInflightPerAccount int) int {
	if accountCount <= 0 {
		return 0
	}
	if maxInflightPerAccount <= 0 {
		maxInflightPerAccount = 2
	}
	return accountCount * maxInflightPerAccount
}

func maxQueueFromEnv(defaultSize int) int {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ACCOUNT_MAX_QUEUE")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return n
		}
	}
	if defaultSize < 0 {
		return 0
	}
	return defaultSize
}

func (p *Pool) canAcquireIDLocked(accountID string) bool {
	if accountID == "" {
		return false
	}
	acc, ok := p.store.FindAccount(accountID)
	if !ok || acc.Disabled {
		return false
	}
	if _, blocked := p.accountHealthLocked(accountID); blocked {
		return false
	}
	if p.inUse[accountID] >= p.maxInflightPerAccount {
		return false
	}
	if p.globalMaxInflight > 0 && p.currentInUseLocked() >= p.globalMaxInflight {
		return false
	}
	return true
}

func (p *Pool) currentInUseLocked() int {
	total := 0
	for _, n := range p.inUse {
		total += n
	}
	return total
}
