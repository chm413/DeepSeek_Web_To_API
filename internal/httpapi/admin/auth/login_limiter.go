package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	// Login requests contain only a credential and a small expiry option.  Keep
	// this limit independent from the much larger public API body limits.
	maxAdminLoginBodyBytes = 8 << 10

	loginFailureThreshold = 3
	loginBackoffBase      = time.Second
	loginBackoffMax       = time.Minute
	loginStateTTL         = 15 * time.Minute
	loginStateMaxEntries  = 4096
)

type loginLimitState struct {
	failures    int
	lastFailure time.Time
	blockedTill time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]loginLimitState
	now     func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		entries: make(map[string]loginLimitState),
		now:     time.Now,
	}
}

func (l *loginLimiter) clock() time.Time {
	if l == nil || l.now == nil {
		return time.Now()
	}
	return l.now()
}

func (l *loginLimiter) bucketKeys(ip, credential string) []string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	keys := []string{"ip:" + ip}
	if strings.TrimSpace(credential) == "" {
		return keys
	}
	sum := sha256.Sum256([]byte(credential))
	// Never retain the raw credential in process memory or diagnostics.
	fingerprint := hex.EncodeToString(sum[:8])
	// Keep both a global credential bucket and an IP+credential bucket. The
	// former prevents source-address rotation from bypassing backoff; the
	// latter prevents one noisy address from affecting every caller.
	return append(keys, "credential:"+fingerprint, "credential-ip:"+ip+":"+fingerprint)
}

func (l *loginLimiter) check(ip, credential string) time.Duration {
	if l == nil {
		return 0
	}
	now := l.clock()
	keys := l.bucketKeys(ip, credential)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	var wait time.Duration
	for _, key := range keys {
		if state, ok := l.entries[key]; ok && state.blockedTill.After(now) {
			if d := state.blockedTill.Sub(now); d > wait {
				wait = d
			}
		}
	}
	return wait
}

func (l *loginLimiter) failure(ip, credential string) time.Duration {
	if l == nil {
		return 0
	}
	now := l.clock()
	keys := l.bucketKeys(ip, credential)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	var wait time.Duration
	for _, key := range keys {
		state := l.entries[key]
		if state.lastFailure.IsZero() || now.Sub(state.lastFailure) > loginStateTTL {
			state.failures = 0
		}
		state.failures++
		state.lastFailure = now
		if d := loginBackoff(state.failures); d > 0 {
			state.blockedTill = now.Add(d)
			if d > wait {
				wait = d
			}
		}
		l.entries[key] = state
	}
	return wait
}

func (l *loginLimiter) success(ip, credential string) {
	if l == nil {
		return
	}
	keys := l.bucketKeys(ip, credential)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		delete(l.entries, key)
	}
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	if len(l.entries) <= loginStateMaxEntries {
		for key, state := range l.entries {
			if !state.lastFailure.IsZero() && now.Sub(state.lastFailure) > loginStateTTL && !state.blockedTill.After(now) {
				delete(l.entries, key)
			}
		}
		return
	}
	// Keep the limiter bounded if an attacker rotates source addresses. Evict
	// the oldest states instead of resetting active backoff for every caller.
	for len(l.entries) > loginStateMaxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, state := range l.entries {
			stamp := state.lastFailure
			if stamp.IsZero() {
				stamp = state.blockedTill
			} else if !state.blockedTill.IsZero() && state.blockedTill.Before(stamp) {
				stamp = state.blockedTill
			}
			if oldestKey == "" || stamp.Before(oldest) {
				oldestKey, oldest = key, stamp
			}
		}
		if oldestKey == "" {
			break
		}
		delete(l.entries, oldestKey)
	}
}

func loginBackoff(failures int) time.Duration {
	if failures < loginFailureThreshold {
		return 0
	}
	exponent := failures - loginFailureThreshold
	if exponent > 6 {
		exponent = 6
	}
	d := loginBackoffBase << exponent
	if d > loginBackoffMax {
		return loginBackoffMax
	}
	return d
}

func (h *Handler) getLoginLimiter() *loginLimiter {
	if h == nil {
		return nil
	}
	h.loginLimiterMu.Lock()
	defer h.loginLimiterMu.Unlock()
	if h.loginLimiter == nil {
		h.loginLimiter = newLoginLimiter()
	}
	return h.loginLimiter
}
