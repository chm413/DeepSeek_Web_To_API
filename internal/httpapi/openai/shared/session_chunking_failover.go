package shared

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

// PinnedBranchAccountSwitcher is intentionally small so every protocol
// handler can move a failed pinned branch to a fresh managed account without
// depending on the concrete resolver implementation.
type PinnedBranchAccountSwitcher interface {
	SwitchAccount(context.Context, *auth.RequestAuth) bool
}

// ShouldReplayPinnedBranch reports failures for which a child of an existing
// upstream conversation must never be retried on another account. The caller
// has to rebuild a root session from the full canonical prompt instead.
func ShouldReplayPinnedBranch(err error) bool {
	if err == nil {
		return false
	}
	var healthErr *auth.AccountHealthError
	if errors.As(err, &healthErr) {
		switch healthErr.State {
		case account.HealthRateLimited, account.HealthTemporarilyMuted, account.HealthInvalidCredentials, account.HealthPermanentlyBanned:
			return true
		}
	}
	detail := CompletionErrorDetail(err)
	return detail.Status == http.StatusTooManyRequests || detail.Status == http.StatusUnauthorized || detail.Status == http.StatusForbidden
}

// SwitchManagedAccountForPinnedBranch switches only when the failed branch is
// unsafe to continue. A direct DeepSeek token has no managed account pool, so
// it is deliberately left untouched.
func SwitchManagedAccountForPinnedBranch(ctx context.Context, resolver any, a *auth.RequestAuth, err error) bool {
	if a == nil || !a.UseConfigToken || IsSessionCapacityRateLimit(err) || !ShouldReplayPinnedBranch(err) {
		return false
	}
	switcher, ok := resolver.(PinnedBranchAccountSwitcher)
	if !ok {
		return false
	}
	previousID := strings.TrimSpace(a.AccountID)
	previousToken := strings.TrimSpace(a.DeepSeekToken)
	if !switcher.SwitchAccount(ctx, a) {
		return false
	}
	return strings.TrimSpace(a.AccountID) != previousID || strings.TrimSpace(a.DeepSeekToken) != previousToken
}

// TryPrepareRootSessionChunkingWithFailover commits an oversized full prompt
// to a fresh upstream root. If a later fragment is rate-limited, the partial
// branch is discarded and the complete canonical prompt is replayed on the
// next managed account. It must only be used for root requests: an existing
// parent can belong to a different account and cannot be moved safely.
func TryPrepareRootSessionChunkingWithFailover(ctx context.Context, ds any, resolver any, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings) (*SessionChunkingPreparation, error) {
	sessionCapacityReplays := 0
	transientBranchReplays := 0
	for {
		attemptCfg := cfg
		beforeID, beforeToken := rootSessionAuthIdentity(a)
		prepared, err := TryPrepareSessionChunking(ctx, ds, a, req, cfg, "", 0)
		afterID, afterToken := rootSessionAuthIdentity(a)
		accountChanged := beforeID != afterID || beforeToken != afterToken
		if accountChanged {
			cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
			if err == nil && prepared != nil {
				oldLimit := promptcompat.LimitForModel(attemptCfg, promptcompat.EffectiveModel(req))
				newLimit := promptcompat.LimitForModel(cfg, promptcompat.EffectiveModel(req))
				if newLimit > 0 && (oldLimit <= 0 || newLimit < oldLimit) {
					deleteAbandonedRootSession(ctx, ds, afterID, afterToken, prepared.SessionID)
					config.Logger.Warn("[prompt_limit] root chunk preparation changed account with a lower input limit; rebuilding every fragment",
						"surface", req.Surface,
						"model", req.ResolvedModel,
						"from_limit_units", oldLimit,
						"to_limit_units", newLimit,
						"original_prompt_units", promptcompat.PromptUnits(req.FinalPrompt))
					continue
				}
			}
		}
		if err == nil {
			if prepared != nil {
				prepared.PromptLimit = cfg
			}
			return prepared, nil
		}
		if IsSessionCapacityRateLimit(err) {
			if sessionCapacityReplays >= 1 {
				return nil, err
			}
			sessionCapacityReplays++
			config.Logger.Warn("[prompt_limit] root chunk branch reached upstream session capacity; restarting on the same account",
				"surface", req.Surface,
				"model", req.ResolvedModel,
				"account", a.AccountID,
				"original_prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
				"reason", CompletionErrorDetail(err).Code)
			continue
		}
		if IsRetryableSessionChunkingFailure(err) {
			if transientBranchReplays >= 1 {
				return nil, err
			}
			transientBranchReplays++
			config.Logger.Warn("[prompt_limit] transient same-session fragment failure; rebuilding complete root branch",
				"surface", req.Surface,
				"model", req.ResolvedModel,
				"account", a.AccountID,
				"original_prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
				"replay", transientBranchReplays,
				"max_replays", 1,
				"error", err)
			continue
		}
		if !SwitchManagedAccountForPinnedBranch(ctx, resolver, a, err) {
			return nil, err
		}
		cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
		sessionCapacityReplays = 0
		transientBranchReplays = 0
		config.Logger.Warn("[prompt_limit] restarting same-session prompt chunks on another account",
			"surface", req.Surface,
			"model", req.ResolvedModel,
			"original_prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
			"reason", CompletionErrorDetail(err).Code)
	}
}

// RestartRootSessionChunkingAfterPinnedFailure starts over from the complete
// root prompt after the final pinned completion is rejected. Account-scoped
// failures move to a replacement account. A conversation-capacity failure
// rotates the exhausted branch once on the same account, since it must not
// consume or cool down an otherwise healthy account. Returning false means
// the original error should be surfaced unchanged.
func RestartRootSessionChunkingAfterPinnedFailure(ctx context.Context, ds any, resolver any, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings, previous *SessionChunkingPreparation, err error) (*SessionChunkingPreparation, bool, error) {
	if IsSessionCapacityRateLimit(err) {
		if previous == nil || previous.SessionCapacityRestarted {
			return nil, false, nil
		}
		accountID, token := rootSessionAuthIdentity(a)
		deleteAbandonedRootSession(ctx, ds, accountID, token, previous.SessionID)
		config.Logger.Warn("[prompt_limit] rotating exhausted final chunk branch on the same account",
			"surface", req.Surface,
			"model", req.ResolvedModel,
			"account", accountID,
			"original_prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
			"reason", CompletionErrorDetail(err).Code)
		prepared, prepareErr := TryPrepareRootSessionChunkingWithFailover(ctx, ds, resolver, a, req, cfg)
		if prepared != nil {
			prepared.SessionCapacityRestarted = true
		}
		if prepareErr == nil && prepared == nil {
			prepareErr = errors.New("same-session prompt replay no longer requires chunking")
		}
		return prepared, true, prepareErr
	}
	if !SwitchManagedAccountForPinnedBranch(ctx, resolver, a, err) {
		return nil, false, nil
	}
	// Input ceilings are account-scoped upstream metadata. Never reuse the
	// failed account's budget when building the replacement root branch.
	cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
	config.Logger.Warn("[prompt_limit] replaying full prompt after pinned branch failure",
		"surface", req.Surface,
		"model", req.ResolvedModel,
		"original_prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
		"reason", CompletionErrorDetail(err).Code)
	prepared, prepareErr := TryPrepareRootSessionChunkingWithFailover(ctx, ds, resolver, a, req, cfg)
	if prepareErr == nil && prepared == nil {
		prepareErr = errors.New("same-session prompt replay no longer requires chunking")
	}
	return prepared, true, prepareErr
}
