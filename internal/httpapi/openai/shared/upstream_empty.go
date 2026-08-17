package shared

import (
	"context"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

const emptyOutputRetryAccountSwitchAttempts = 3

type EmptyRetryAccountSwitcher interface {
	SwitchAccount(ctx context.Context, a *auth.RequestAuth) bool
}

// EmptyOutputRetryCall is the result of sending a synthetic empty-output
// retry. ReplayedRoot is true only when a pinned retry hit a recoverable
// upstream failure and had to restart from the complete canonical prompt.
type EmptyOutputRetryCall struct {
	Response     *http.Response
	Pow          string
	SessionID    string
	PromptLimit  config.PromptLimitSettings
	ReplayedRoot bool
}

// ShouldWriteUpstreamEmptyOutputError returns true ONLY when the upstream
// produced neither visible text NOR reasoning content. A thinking-only
// response — where the model emitted a reasoning trace but no visible
// text — is no longer treated as "empty"; the reasoning IS content the
// caller can render (DeepSeek Pro reasoning models, especially under
// tool-augmented prompts, intermittently produce thinking-only frames
// that historically were lost as 429 errors). Aligned with upstream
// CJackHwang/ds2api a7522b41 + a299c7d1 but rewritten on top of our
// local empty-retry runtime instead of taking the structural refactor.
func ShouldWriteUpstreamEmptyOutputError(text, thinking string) bool {
	return strings.TrimSpace(text) == "" && strings.TrimSpace(thinking) == ""
}

func UpstreamEmptyOutputDetail(contentFilter bool, text, thinking string) (int, string, string) {
	_ = text
	_ = thinking
	if contentFilter {
		return http.StatusBadRequest, "Upstream content filtered the response and returned no output.", "content_filter"
	}
	return http.StatusTooManyRequests, "Upstream returned no output. Retry later or reduce the input size.", "upstream_empty_output"
}

func WriteUpstreamEmptyOutputError(w http.ResponseWriter, text, thinking string, contentFilter bool) bool {
	if !ShouldWriteUpstreamEmptyOutputError(text, thinking) {
		return false
	}
	status, message, code := UpstreamEmptyOutputDetail(contentFilter, text, thinking)
	WriteOpenAIErrorWithCode(w, status, message, code)
	return true
}

func PrepareEmptyOutputRetry(ctx context.Context, resolver any, ds DeepSeekCaller, a *auth.RequestAuth, basePayload, retryPayload map[string]any, originalPow, surface string, stream bool, retryAttempt int, bindAuth func(*auth.RequestAuth), activeSessionID *string) (string, bool) {
	if ds == nil {
		return originalPow, true
	}
	if IsPinnedCompletionPayload(basePayload) {
		retryPow, powErr := GetPinnedPow(ctx, ds, a)
		if powErr != nil {
			config.Logger.Warn("[openai_empty_retry] pinned retry PoW fetch failed, falling back to original PoW", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "error", powErr)
			return originalPow, true
		}
		return retryPow, true
	}
	if switcher, ok := resolver.(EmptyRetryAccountSwitcher); ok && a != nil && a.UseConfigToken && !IsPinnedCompletionPayload(basePayload) {
		oldAccountID := strings.TrimSpace(a.AccountID)
		for switchAttempt := 1; switchAttempt <= emptyOutputRetryAccountSwitchAttempts; switchAttempt++ {
			if !switcher.SwitchAccount(ctx, a) {
				break
			}
			if bindAuth != nil {
				bindAuth(a)
			}
			sessionID, sessionErr := ds.CreateSession(ctx, a, 3)
			if sessionErr != nil {
				config.Logger.Warn("[openai_empty_retry] retry account session creation failed", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt, "error", sessionErr)
				continue
			}
			sessionID = strings.TrimSpace(sessionID)
			if sessionID == "" {
				config.Logger.Warn("[openai_empty_retry] retry account returned empty session", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt)
				continue
			}
			sessionAccountID := strings.TrimSpace(a.AccountID)
			sessionToken := strings.TrimSpace(a.DeepSeekToken)
			retryPow, powErr := GetRootSessionPinnedPow(ctx, ds, a)
			if powErr != nil {
				AutoDeleteRemoteSession(ctx, ds, "single", sessionAccountID, sessionToken, sessionID)
				config.Logger.Warn("[openai_empty_retry] retry account PoW fetch failed", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt, "error", powErr)
				continue
			}
			setEmptyRetrySessionID(basePayload, retryPayload, sessionID)
			if activeSessionID != nil {
				*activeSessionID = sessionID
			}
			config.Logger.Info("[openai_empty_retry] switched managed account for retry", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt)
			return retryPow, true
		}
		if oldAccountID != "" && strings.TrimSpace(a.AccountID) != "" && strings.TrimSpace(a.AccountID) != oldAccountID {
			config.Logger.Warn("[openai_empty_retry] managed account switch exhausted before retry", "surface", surface, "stream", stream, "retry_attempt", retryAttempt)
			return "", false
		}
		config.Logger.Warn("[openai_empty_retry] no alternate managed account available; retrying current account", "surface", surface, "stream", stream, "retry_attempt", retryAttempt)
	}
	// A non-incremental retry still owns an existing root session. Do not let
	// the regular account-pooling PoW call silently select another account for
	// that session.
	retryPow, powErr := GetRootSessionPinnedPow(ctx, ds, a)
	if powErr != nil {
		config.Logger.Warn("[openai_empty_retry] retry PoW fetch failed, falling back to original PoW", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "error", powErr)
		return originalPow, true
	}
	return retryPow, true
}

// CallEmptyOutputRetry keeps the synthetic retry on the account that owns its
// session. When the retry is rate-limited, it uses the same safe root replay
// as the initial request: abandon the old session, switch only when the error
// is account-scoped, then rebuild the full canonical prompt on a fresh root.
// This avoids combining a session from account A with PoW or completion
// credentials from account B.
func CallEmptyOutputRetry(ctx context.Context, ds any, resolver any, a *auth.RequestAuth, basePayload, retryPayload map[string]any, pow string, rootReq promptcompat.StandardRequest, promptLimit config.PromptLimitSettings, activeSessionID *string) (EmptyOutputRetryCall, error) {
	result := EmptyOutputRetryCall{Pow: pow, PromptLimit: promptLimit}
	sessionCapacityRetried := false
	for {
		if !IsPinnedCompletionPayload(retryPayload) {
			// PrepareEmptyOutputRetry may have deliberately moved an empty
			// response to a newly-created root on another account. Input
			// ceilings are account-scoped, so validate the complete canonical
			// prompt again before issuing that first request on the new root.
			result.PromptLimit = refreshRootSessionPromptLimits(ctx, ds, a, rootReq, result.PromptLimit)
			if limitErr := rootSessionLimitError(result.PromptLimit, rootReq); limitErr != nil {
				// This can happen immediately after PrepareEmptyOutputRetry
				// moves a root to an account with a lower live ceiling. The
				// previous root has not accepted useful content, so discard it
				// and rebuild the complete canonical prompt through the normal
				// chunking path when that operator option is enabled.
				if !result.PromptLimit.SessionChunkingEnable {
					return result, limitErr
				}
				deleteAbandonedRootSession(ctx, ds, a.AccountID, a.DeepSeekToken, payloadSessionID(retryPayload))
				if err := rebuildEmptyRetryChunkedRoot(ctx, ds, resolver, a, basePayload, retryPayload, rootReq, &result, activeSessionID); err != nil {
					return result, err
				}
				continue
			}
		}
		sessionID := strings.TrimSpace(payloadSessionID(retryPayload))
		var (
			resp *http.Response
			err  error
		)
		// A synthetic retry of a full root has a child parent_message_id, but
		// it is still owned by the root account and can use the root-pinned
		// fallback for alternate callers. Only an already-incremental base
		// requires the stricter incremental capability.
		if IsPinnedCompletionPayload(basePayload) {
			resp, err = CallPinnedCompletion(ctx, ds, a, retryPayload, result.Pow)
		} else {
			resp, err = CallRootSessionPinnedCompletion(ctx, ds, a, retryPayload, result.Pow)
		}
		if err == nil {
			result.Response = resp
			result.SessionID = sessionID
			return result, nil
		}

		if IsSessionCapacityRateLimit(err) {
			if sessionCapacityRetried {
				return result, err
			}
			sessionCapacityRetried = true
		}
		nextCfg, restarted, restartErr := RestartRootSessionAfterPinnedFailure(ctx, ds, resolver, a, rootReq, result.PromptLimit, sessionID, err)
		if !restarted {
			return result, err
		}
		if restartErr != nil {
			return result, restartErr
		}
		root, rootErr := PrepareRootSessionWithPinnedPow(ctx, ds, resolver, a, rootReq, nextCfg)
		if rootErr != nil {
			if _, limited := RootSessionPromptLimitMessage(rootErr); limited && nextCfg.SessionChunkingEnable {
				result.PromptLimit = nextCfg
				if err := rebuildEmptyRetryChunkedRoot(ctx, ds, resolver, a, basePayload, retryPayload, rootReq, &result, activeSessionID); err != nil {
					return result, err
				}
				continue
			}
			return result, rootErr
		}
		result.Pow = root.Pow
		result.PromptLimit = root.PromptLimit
		result.ReplayedRoot = true
		setEmptyRetryRootPayload(basePayload, rootReq.CompletionPayload(root.SessionID))
		setEmptyRetryRootPayload(retryPayload, rootReq.CompletionPayload(root.SessionID))
		if activeSessionID != nil {
			*activeSessionID = root.SessionID
		}
		config.Logger.Warn("[openai_empty_retry] replaying complete root after pinned retry failure",
			"surface", rootReq.Surface,
			"model", rootReq.ResolvedModel,
			"prompt_units", promptcompat.PromptUnits(rootReq.FinalPrompt),
			"reason", CompletionErrorDetail(err).Code)
	}
}

func rebuildEmptyRetryChunkedRoot(ctx context.Context, ds any, resolver any, a *auth.RequestAuth, basePayload, retryPayload map[string]any, rootReq promptcompat.StandardRequest, result *EmptyOutputRetryCall, activeSessionID *string) error {
	if result == nil {
		return &RootSessionPromptLimitError{}
	}
	prepared, err := TryPrepareRootSessionChunkingWithFailover(ctx, ds, resolver, a, rootReq, result.PromptLimit)
	if err != nil {
		return err
	}
	if prepared == nil {
		return &RootSessionPromptLimitError{Message: "replacement account input limit still requires a chunked root"}
	}
	pow, err := GetPinnedPow(ctx, ds, a)
	if err != nil {
		return err
	}
	payload := rootReq.CompletionPayload(prepared.SessionID)
	payload["prompt"] = prepared.FinalWirePrompt
	payload["parent_message_id"] = prepared.ParentMessageID
	setEmptyRetryRootPayload(basePayload, payload)
	setEmptyRetryRootPayload(retryPayload, payload)
	result.Pow = pow
	result.PromptLimit = prepared.PromptLimit
	result.ReplayedRoot = true
	if activeSessionID != nil {
		*activeSessionID = prepared.SessionID
	}
	config.Logger.Warn("[openai_empty_retry] rebuilt complete root with session chunks after account limit change",
		"surface", rootReq.Surface,
		"model", rootReq.ResolvedModel,
		"prompt_units", promptcompat.PromptUnits(rootReq.FinalPrompt),
		"chunk_count", prepared.ChunkCount,
		"final_wire_units", prepared.FinalWireUnits)
	return nil
}

func payloadSessionID(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	value, _ := payload["chat_session_id"].(string)
	return value
}

func setEmptyRetryRootPayload(target, source map[string]any) {
	if target == nil {
		return
	}
	for key := range target {
		delete(target, key)
	}
	for key, value := range source {
		target[key] = value
	}
}

func setEmptyRetrySessionID(basePayload, retryPayload map[string]any, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	if basePayload != nil {
		basePayload["chat_session_id"] = sessionID
		basePayload["parent_message_id"] = nil
	}
	if retryPayload != nil {
		retryPayload["chat_session_id"] = sessionID
		retryPayload["parent_message_id"] = nil
	}
}
