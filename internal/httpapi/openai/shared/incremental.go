package shared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

type PinnedCompletionCaller interface {
	CallCompletionPinned(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string) (*http.Response, error)
}

func LogIncrementalRequestContext(surface string, a *auth.RequestAuth, req promptcompat.StandardRequest, wireRequestBytes int) {
	messageJSON, _ := json.Marshal(req.Messages)
	sum := sha256.Sum256(messageJSON)
	formatSum := sha256.Sum256([]byte(req.IncrementalFormatPrompt))
	roles := make([]string, 0, len(req.Messages))
	for _, raw := range req.Messages {
		message, _ := raw.(map[string]any)
		role, _ := message["role"].(string)
		role = strings.TrimSpace(role)
		if role == "" {
			role = "unknown"
		}
		roles = append(roles, role)
	}
	sessionKey := ""
	accountFingerprint := ""
	if a != nil {
		sessionKey = strings.TrimSpace(a.SessionKey)
		accountFingerprint = incrementalAccountFingerprint(a.AccountID)
	}
	config.Logger.Info("[incremental] request context",
		"surface", surface,
		"session_key", sessionKey,
		"account_fingerprint", accountFingerprint,
		"variant", req.IncrementalVariant(),
		"model", req.ResolvedModel,
		"wire_request_bytes", wireRequestBytes,
		"message_count", len(req.Messages),
		"message_roles", strings.Join(roles, ","),
		"canonical_messages_sha256", hex.EncodeToString(sum[:8]),
		"full_prompt_units", promptcompat.PromptUnits(req.FinalPrompt),
		"latest_user_units", promptcompat.PromptUnits(req.LatestUserText),
		"format_prompt_units", promptcompat.PromptUnits(req.IncrementalFormatPrompt),
		"format_prompt_sha256", hex.EncodeToString(formatSum[:8]),
		"format_prompt_present", strings.TrimSpace(req.IncrementalFormatPrompt) != "",
		"tool_count", len(req.ToolNames),
		"stream", req.Stream,
		"thinking", req.Thinking,
		"search", req.Search)
}

func PrepareIncrementalRequest(store *upstreamsession.Store, ds any, autoDeleteMode string, a *auth.RequestAuth, req promptcompat.StandardRequest, messages []any) (*upstreamsession.Lease, string, bool) {
	return PrepareIncrementalRequestWithSettings(store, ds, autoDeleteMode, a, req, messages, config.DefaultPromptLimitSettings())
}

func PrepareIncrementalRequestWithSettings(store *upstreamsession.Store, ds any, autoDeleteMode string, a *auth.RequestAuth, req promptcompat.StandardRequest, messages []any, cfg config.PromptLimitSettings) (*upstreamsession.Lease, string, bool) {
	if store == nil || a == nil || !strings.EqualFold(strings.TrimSpace(autoDeleteMode), "none") {
		return nil, "", false
	}
	if _, ok := ds.(PinnedCompletionCaller); !ok {
		return nil, "", false
	}
	scope := IncrementalScope(a, req)
	lease, ok := store.PrepareWithMaxTurns(scope, messages, cfg.IncrementalMaxTurns)
	if !ok {
		diagnostics := store.Diagnose(scope, messages)
		config.Logger.Info("[incremental] cache miss",
			"surface", scope.Surface,
			"session_key", scope.SessionKey,
			"account_fingerprint", incrementalAccountFingerprint(scope.AccountID),
			"variant", scope.Variant,
			"message_count", len(messages),
			"invalid_input", diagnostics.InvalidInput,
			"branches", diagnostics.Branches,
			"busy_branches", diagnostics.Busy,
			"not_extendable_branches", diagnostics.NotExtendable,
			"request_prefix_mismatches", diagnostics.RequestPrefixMismatch,
			"response_prefix_mismatches", diagnostics.ResponsePrefixMismatch,
			"extendable_branches", diagnostics.Extendable,
			"expected_response_shape", diagnostics.ExpectedResponseShape,
			"current_response_shape", diagnostics.CurrentResponseShape,
			"expected_response_hash", diagnostics.ExpectedResponseHash,
			"current_response_hash", diagnostics.CurrentResponseHash)
		return nil, "", false
	}
	if lease.Rotate {
		return lease, "", true
	}
	prompt := promptcompat.BuildIncrementalPrompt(lease.DeltaMessages, req.IncrementalFormatPrompt, req.Thinking)
	if strings.TrimSpace(prompt) == "" {
		lease.Invalidate()
		return nil, "", false
	}
	return lease, prompt, true
}

func incrementalAccountFingerprint(accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(accountID))
	return hex.EncodeToString(sum[:8])
}

// ApplyIncrementalSessionRotation builds the first prompt for a replacement
// upstream session. It explicitly carries the forced response-format block and
// the configured recent history window; the canonical Messages remain intact
// so the next client turn can still be matched against its exact original
// branch rather than the gateway's transport-only rollover representation.
func ApplyIncrementalSessionRotation(req *promptcompat.StandardRequest, lease *upstreamsession.Lease, cfg config.PromptLimitSettings) (dropped int, ok bool) {
	if req == nil || lease == nil || !lease.Rotate || cfg.IncrementalMaxTurns <= 0 {
		return 0, false
	}
	keepRecent := cfg.IncrementalRotationKeepRecent
	if keepRecent <= 0 {
		keepRecent = 1
	}
	rotatedMessages, changed := promptcompat.CompressMessages(req.Messages, cfg.KeepSystemMessage, keepRecent)
	if !changed {
		rotatedMessages = req.Messages
	}
	prompt := promptcompat.BuildIncrementalPrompt(rotatedMessages, req.IncrementalFormatPrompt, req.Thinking)
	if strings.TrimSpace(prompt) == "" {
		return 0, false
	}
	req.FinalPrompt = prompt
	req.IncrementalSessionRotated = true
	return len(req.Messages) - len(rotatedMessages), true
}

func IncrementalScope(a *auth.RequestAuth, req promptcompat.StandardRequest) upstreamsession.Scope {
	if a == nil {
		return upstreamsession.Scope{}
	}
	identity := strings.TrimSpace(a.AccountID)
	if identity == "" {
		identity = strings.TrimSpace(a.CallerID)
	}
	return upstreamsession.Scope{
		CallerID:   strings.TrimSpace(a.CallerID),
		SessionKey: strings.TrimSpace(a.SessionKey),
		AccountID:  identity,
		Surface:    strings.TrimSpace(req.Surface),
		Variant:    req.IncrementalVariant(),
	}
}

func CallPinnedCompletion(ctx context.Context, ds any, a *auth.RequestAuth, payload map[string]any, powResp string) (*http.Response, error) {
	pinned, ok := ds.(PinnedCompletionCaller)
	if !ok {
		return nil, &IncrementalUnavailableError{}
	}
	return pinned.CallCompletionPinned(ctx, a, payload, powResp)
}

func IsPinnedCompletionPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	parent, ok := payload["parent_message_id"]
	return ok && parent != nil
}

type IncrementalUnavailableError struct{}

func (*IncrementalUnavailableError) Error() string {
	return "incremental pinned completion is unavailable"
}
