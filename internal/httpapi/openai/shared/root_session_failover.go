package shared

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type rootSessionCaller interface {
	CreateSession(context.Context, *auth.RequestAuth, int) (string, error)
	GetPow(context.Context, *auth.RequestAuth, int) (string, error)
	CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error)
}

type rootPinnedSessionCreator interface {
	CreateSessionPinned(context.Context, *auth.RequestAuth) (string, error)
}

type rootPinnedCompletionCaller interface {
	CallCompletionRootPinned(context.Context, *auth.RequestAuth, map[string]any, string) (*http.Response, error)
}

type rootSessionStage string

const (
	rootSessionStageCreate rootSessionStage = "create"
	rootSessionStagePow    rootSessionStage = "pow"
)

// RootSessionError identifies whether a root-session setup failure happened
// while creating a conversation or acquiring a PoW for that same account.
type RootSessionError struct {
	Stage rootSessionStage
	Err   error
}

func (e *RootSessionError) Error() string {
	if e == nil || e.Err == nil {
		return "root session setup failed"
	}
	return fmt.Sprintf("root session %s: %v", e.Stage, e.Err)
}

func (e *RootSessionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RootSessionPromptLimitError means an account change lowered the live
// upstream ceiling below a prompt that was valid for the previous account.
// PromptLimit lets the protocol handler retry the final canonical prompt with
// same-session chunking rather than discarding a request that can still fit
// once it is transported in bounded fragments.
type RootSessionPromptLimitError struct {
	Message     string
	PromptLimit config.PromptLimitSettings
}

func (e *RootSessionPromptLimitError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "prompt exceeds replacement account input limit"
	}
	return e.Message
}

func rootSessionErrorStage(err error) rootSessionStage {
	var rootErr *RootSessionError
	if errors.As(err, &rootErr) {
		return rootErr.Stage
	}
	return ""
}

// RootSessionErrorIsPow reports whether root setup failed after a session was
// created, while obtaining a fixed-account proof of work.
func RootSessionErrorIsPow(err error) bool {
	return rootSessionErrorStage(err) == rootSessionStagePow
}

// RootSessionPromptLimitMessage returns a client-safe limit message when a
// root replay cannot fit the selected replacement account.
func RootSessionPromptLimitMessage(err error) (string, bool) {
	var limitErr *RootSessionPromptLimitError
	if !errors.As(err, &limitErr) {
		return "", false
	}
	return limitErr.Error(), true
}

// RootSessionPromptLimitSettings returns the replacement account limit that
// rejected a root preparation. It is only meaningful with a matching limit
// error and is intentionally not inferred from a generic 413.
func RootSessionPromptLimitSettings(err error) (config.PromptLimitSettings, bool) {
	var limitErr *RootSessionPromptLimitError
	if !errors.As(err, &limitErr) {
		return config.PromptLimitSettings{}, false
	}
	return limitErr.PromptLimit, true
}

func rootSessionAuthIdentity(a *auth.RequestAuth) (string, string) {
	if a == nil {
		return "", ""
	}
	return strings.TrimSpace(a.AccountID), strings.TrimSpace(a.DeepSeekToken)
}

func refreshRootSessionPromptLimits(ctx context.Context, ds any, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings) config.PromptLimitSettings {
	refreshedCfg, applied, err := ResolveDynamicPromptLimits(ctx, ds, a, cfg)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic limit refresh after root account switch failed; retaining prior limit",
			"surface", req.Surface,
			"model", req.ResolvedModel,
			"error", err)
		return cfg
	}
	if applied {
		return refreshedCfg
	}
	return cfg
}

func rootSessionLimitError(cfg config.PromptLimitSettings, req promptcompat.StandardRequest) error {
	if message := EnforcePromptLimit(cfg, req); message != "" {
		return &RootSessionPromptLimitError{Message: message, PromptLimit: cfg}
	}
	return nil
}

func deleteAbandonedRootSession(ctx context.Context, ds any, accountID, token, sessionID string) {
	if deleter, ok := ds.(SessionDeleter); ok {
		AutoDeleteRemoteSession(ctx, deleter, "single", accountID, token, sessionID)
	}
}

// GetRootSessionPinnedPow uses the fixed-account PoW endpoint whenever the
// upstream client supports it. The ordinary fallback is retained only for
// alternate/test callers that do not expose the pinned capability.
func GetRootSessionPinnedPow(ctx context.Context, ds any, a *auth.RequestAuth) (string, error) {
	if pinned, ok := ds.(PinnedPowCaller); ok {
		return pinned.GetPowPinned(ctx, a)
	}
	caller, ok := ds.(interface {
		GetPow(context.Context, *auth.RequestAuth, int) (string, error)
	})
	if !ok {
		return "", &IncrementalUnavailableError{}
	}
	return caller.GetPow(ctx, a, 3)
}

// CreateRootSessionPinned uses the fixed-account create endpoint whenever the
// upstream client provides it. The fallback is retained for test and alternate
// callers that only expose the legacy create method.
func CreateRootSessionPinned(ctx context.Context, ds any, a *auth.RequestAuth) (string, error) {
	if pinned, ok := ds.(rootPinnedSessionCreator); ok {
		return pinned.CreateSessionPinned(ctx, a)
	}
	caller, ok := ds.(interface {
		CreateSession(context.Context, *auth.RequestAuth, int) (string, error)
	})
	if !ok {
		return "", &IncrementalUnavailableError{}
	}
	return caller.CreateSession(ctx, a, 3)
}

// CallRootSessionPinnedCompletion keeps a root completion on the account that
// created its session. The fallback only serves alternate/test callers that
// do not implement the root-pinned extension; dsclient.Client always uses it.
func CallRootSessionPinnedCompletion(ctx context.Context, ds any, a *auth.RequestAuth, payload map[string]any, pow string) (*http.Response, error) {
	if pinned, ok := ds.(rootPinnedCompletionCaller); ok {
		return pinned.CallCompletionRootPinned(ctx, a, payload, pow)
	}
	caller, ok := ds.(interface {
		CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error)
	})
	if !ok {
		return nil, &IncrementalUnavailableError{}
	}
	return caller.CallCompletion(ctx, a, payload, pow, 1)
}

// RootSessionPreparation is a fresh upstream root whose session and PoW are
// guaranteed to belong to the same selected account.
type RootSessionPreparation struct {
	SessionID   string
	Pow         string
	PromptLimit config.PromptLimitSettings
}

// PrepareRootSessionWithPinnedPow creates a root session, then obtains PoW
// without allowing a hidden account switch. A rate-limited account is safely
// abandoned and the complete root prompt is retried on a replacement account.
func PrepareRootSessionWithPinnedPow(ctx context.Context, ds any, resolver any, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings) (*RootSessionPreparation, error) {
	_, ok := ds.(rootSessionCaller)
	if !ok {
		return nil, &RootSessionError{Stage: rootSessionStageCreate, Err: &IncrementalUnavailableError{}}
	}
	for {
		beforeID, beforeToken := rootSessionAuthIdentity(a)
		sessionID, err := CreateRootSessionPinned(ctx, ds, a)
		if err != nil {
			if SwitchManagedAccountForPinnedBranch(ctx, resolver, a, err) {
				cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
				if limitErr := rootSessionLimitError(cfg, req); limitErr != nil {
					return nil, limitErr
				}
				config.Logger.Warn("[root_session] replaying full root after pinned session-create failure",
					"surface", req.Surface,
					"model", req.ResolvedModel,
					"prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
					"reason", CompletionErrorDetail(err).Code)
				continue
			}
			return nil, &RootSessionError{Stage: rootSessionStageCreate, Err: err}
		}
		sessionID = strings.TrimSpace(sessionID)
		afterID, afterToken := rootSessionAuthIdentity(a)
		if beforeID != afterID || beforeToken != afterToken {
			cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
			if limitErr := rootSessionLimitError(cfg, req); limitErr != nil {
				deleteAbandonedRootSession(ctx, ds, afterID, afterToken, sessionID)
				return nil, limitErr
			}
		}

		pow, err := GetRootSessionPinnedPow(ctx, ds, a)
		if err == nil {
			return &RootSessionPreparation{SessionID: sessionID, Pow: pow, PromptLimit: cfg}, nil
		}
		sessionAccountID, sessionToken := rootSessionAuthIdentity(a)
		if !SwitchManagedAccountForPinnedBranch(ctx, resolver, a, err) {
			return nil, &RootSessionError{Stage: rootSessionStagePow, Err: err}
		}
		deleteAbandonedRootSession(ctx, ds, sessionAccountID, sessionToken, sessionID)
		cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
		if limitErr := rootSessionLimitError(cfg, req); limitErr != nil {
			return nil, limitErr
		}
		config.Logger.Warn("[root_session] replaying full root after pinned PoW failure",
			"surface", req.Surface,
			"model", req.ResolvedModel,
			"prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
			"reason", CompletionErrorDetail(err).Code)
	}
}

// RestartRootSessionAfterPinnedFailure abandons a root after its completion
// was rejected. The caller invokes PrepareRootSessionWithPinnedPow again to
// rebuild the complete canonical prompt under the replacement account.
func RestartRootSessionAfterPinnedFailure(ctx context.Context, ds any, resolver any, a *auth.RequestAuth, req promptcompat.StandardRequest, cfg config.PromptLimitSettings, sessionID string, err error) (config.PromptLimitSettings, bool, error) {
	oldAccountID, oldToken := rootSessionAuthIdentity(a)
	if IsSessionCapacityRateLimit(err) {
		// This is a per-conversation ceiling, not an account cooldown. Drop the
		// exhausted branch and rebuild a root on the same healthy account once.
		deleteAbandonedRootSession(ctx, ds, oldAccountID, oldToken, sessionID)
		cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
		if limitErr := rootSessionLimitError(cfg, req); limitErr != nil {
			return cfg, true, limitErr
		}
		config.Logger.Warn("[root_session] rotating exhausted upstream conversation on same account",
			"surface", req.Surface,
			"model", req.ResolvedModel,
			"prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
			"reason", CompletionErrorDetail(err).Code)
		return cfg, true, nil
	}
	if !SwitchManagedAccountForPinnedBranch(ctx, resolver, a, err) {
		return cfg, false, nil
	}
	deleteAbandonedRootSession(ctx, ds, oldAccountID, oldToken, sessionID)
	cfg = refreshRootSessionPromptLimits(ctx, ds, a, req, cfg)
	if limitErr := rootSessionLimitError(cfg, req); limitErr != nil {
		return cfg, true, limitErr
	}
	config.Logger.Warn("[root_session] replaying full root after pinned completion failure",
		"surface", req.Surface,
		"model", req.ResolvedModel,
		"prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
		"reason", CompletionErrorDetail(err).Code)
	return cfg, true, nil
}
