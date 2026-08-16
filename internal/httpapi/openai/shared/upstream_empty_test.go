package shared

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type emptyRetryPinnedDSStub struct {
	created        []string
	normalPowCalls int
	pinnedPowCalls int
}

func (s *emptyRetryPinnedDSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	s.created = append(s.created, a.AccountID)
	return "session-" + a.AccountID, nil
}

func (s *emptyRetryPinnedDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	s.normalPowCalls++
	return "unsafe-normal-pow", nil
}

func (s *emptyRetryPinnedDSStub) GetPowPinned(_ context.Context, a *auth.RequestAuth) (string, error) {
	s.pinnedPowCalls++
	return "pinned-pow-" + a.AccountID, nil
}

func (*emptyRetryPinnedDSStub) UploadFile(context.Context, *auth.RequestAuth, dsclient.UploadFileRequest, int) (*dsclient.UploadFileResult, error) {
	return nil, nil
}

func (*emptyRetryPinnedDSStub) CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error) {
	return nil, nil
}

func (*emptyRetryPinnedDSStub) DeleteSessionForToken(context.Context, string, string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*emptyRetryPinnedDSStub) DeleteAllSessionsForToken(context.Context, string) error { return nil }

type emptyRetryAccountSwitcherStub struct{ switched bool }

func (s *emptyRetryAccountSwitcherStub) SwitchAccount(_ context.Context, a *auth.RequestAuth) bool {
	if s.switched {
		return false
	}
	s.switched = true
	a.AccountID = "retry-account"
	a.DeepSeekToken = "retry-token"
	return true
}

func TestPrepareEmptyOutputRetryPinsNewRootAndClearsParent(t *testing.T) {
	ds := &emptyRetryPinnedDSStub{}
	switcher := &emptyRetryAccountSwitcherStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "initial-account",
		DeepSeekToken:  "initial-token",
		TriedAccounts:  map[string]bool{},
	}
	base := map[string]any{
		"chat_session_id": "session-initial-account",
	}
	retry := map[string]any{
		"chat_session_id":   "session-initial-account",
		"parent_message_id": 101,
	}
	activeSessionID := "session-initial-account"

	pow, ok := PrepareEmptyOutputRetry(context.Background(), switcher, ds, a, base, retry, "initial-pow", "responses", false, 1, nil, &activeSessionID)
	if !ok {
		t.Fatal("expected retry setup to succeed")
	}
	if got, want := pow, "pinned-pow-retry-account"; got != want {
		t.Fatalf("retry pow = %q, want %q", got, want)
	}
	if ds.normalPowCalls != 0 || ds.pinnedPowCalls != 1 {
		t.Fatalf("expected only pinned PoW, normal=%d pinned=%d", ds.normalPowCalls, ds.pinnedPowCalls)
	}
	if len(ds.created) != 1 || ds.created[0] != "retry-account" {
		t.Fatalf("retry root was created for the wrong account: %#v", ds.created)
	}
	for name, payload := range map[string]map[string]any{"base": base, "retry": retry} {
		if payload["chat_session_id"] != "session-retry-account" {
			t.Fatalf("%s session was not replaced: %#v", name, payload)
		}
		if parent, ok := payload["parent_message_id"]; !ok || parent != nil {
			t.Fatalf("%s retained a parent from the prior account: %#v", name, payload)
		}
	}
	if activeSessionID != "session-retry-account" {
		t.Fatalf("active session = %q", activeSessionID)
	}
}

func TestPrepareEmptyOutputRetryPinsExistingRootPow(t *testing.T) {
	ds := &emptyRetryPinnedDSStub{}
	a := &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}
	pow, ok := PrepareEmptyOutputRetry(context.Background(), nil, ds, a, map[string]any{"chat_session_id": "root"}, nil, "initial-pow", "responses", false, 1, nil, nil)
	if !ok || pow != "pinned-pow-account" {
		t.Fatalf("existing root retry = (%q, %v)", pow, ok)
	}
	if ds.normalPowCalls != 0 || ds.pinnedPowCalls != 1 {
		t.Fatalf("expected only pinned PoW, normal=%d pinned=%d", ds.normalPowCalls, ds.pinnedPowCalls)
	}
}

type emptyRetryReplayDSStub struct {
	pinnedCalls         []string
	rootCalls           []string
	deleted             []string
	failInitialRootCall bool
	failInitialCapacity bool
}

type emptyRetryLimitDSStub struct {
	emptyRetryPinnedDSStub
	limitAccounts []string
	rootCalls     int
}

func (s *emptyRetryLimitDSStub) GetModelInputLimits(_ context.Context, a *auth.RequestAuth) (config.ModelInputLimits, error) {
	s.limitAccounts = append(s.limitAccounts, a.AccountID)
	return config.ModelInputLimits{Default: 10, Expert: 10}, nil
}

func (s *emptyRetryLimitDSStub) CallCompletionRootPinned(context.Context, *auth.RequestAuth, map[string]any, string) (*http.Response, error) {
	s.rootCalls++
	return nil, nil
}

func (*emptyRetryReplayDSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "session-" + a.AccountID, nil
}

func (*emptyRetryReplayDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "unsafe-normal-pow", nil
}

func (*emptyRetryReplayDSStub) GetPowPinned(_ context.Context, a *auth.RequestAuth) (string, error) {
	return "pinned-pow-" + a.AccountID, nil
}

func (s *emptyRetryReplayDSStub) CallCompletionPinned(_ context.Context, a *auth.RequestAuth, _ map[string]any, _ string) (*http.Response, error) {
	s.pinnedCalls = append(s.pinnedCalls, a.AccountID)
	return nil, &dsclient.RequestFailure{Op: "completion", Kind: dsclient.FailureUpstreamStatus, StatusCode: http.StatusTooManyRequests, Message: "rate limited"}
}

func (s *emptyRetryReplayDSStub) CallCompletionRootPinned(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.rootCalls = append(s.rootCalls, a.AccountID+":"+payloadSessionID(payload))
	if s.failInitialRootCall && a.AccountID == "initial-account" {
		return nil, &dsclient.RequestFailure{Op: "completion", Kind: dsclient.FailureUpstreamStatus, StatusCode: http.StatusTooManyRequests, Message: "rate limited"}
	}
	if s.failInitialCapacity && a.AccountID == "initial-account" && len(s.rootCalls) == 1 {
		return nil, &dsclient.RequestFailure{Op: "completion", Kind: dsclient.FailureUpstreamStatus, StatusCode: http.StatusTooManyRequests, RateLimitScope: dsclient.RateLimitScopeSessionCapacity, Message: "maximum conversation turns reached"}
	}
	body := "data: {\"p\":\"response/content\",\"v\":\"replayed\"}\n" + "data: [DONE]\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (*emptyRetryReplayDSStub) UploadFile(context.Context, *auth.RequestAuth, dsclient.UploadFileRequest, int) (*dsclient.UploadFileResult, error) {
	return nil, nil
}

func (*emptyRetryReplayDSStub) CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error) {
	return nil, nil
}

func (s *emptyRetryReplayDSStub) DeleteSessionForToken(_ context.Context, token, sessionID string) (*dsclient.DeleteSessionResult, error) {
	s.deleted = append(s.deleted, token+":"+sessionID)
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*emptyRetryReplayDSStub) DeleteAllSessionsForToken(context.Context, string) error { return nil }

func TestCallEmptyOutputRetryReplaysCanonicalRootAfterPinned429(t *testing.T) {
	ds := &emptyRetryReplayDSStub{}
	switcher := &emptyRetryAccountSwitcherStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "initial-account",
		DeepSeekToken:  "initial-token",
		TriedAccounts:  map[string]bool{},
	}
	rootReq := promptcompat.StandardRequest{
		Surface:       "responses",
		ResolvedModel: "deepseek-v4-flash",
		FinalPrompt:   "complete canonical prompt",
	}
	base := rootReq.CompletionPayload("session-initial-account")
	retry := rootReq.CompletionPayload("session-initial-account")
	base["parent_message_id"] = 99
	retry["parent_message_id"] = 101
	activeSessionID := "session-initial-account"

	result, err := CallEmptyOutputRetry(context.Background(), ds, switcher, a, base, retry, "pinned-pow-initial-account", rootReq, config.DefaultPromptLimitSettings(), &activeSessionID)
	if err != nil {
		t.Fatalf("call empty retry: %v", err)
	}
	if result.Response == nil || result.Response.StatusCode != http.StatusOK || !result.ReplayedRoot {
		t.Fatalf("unexpected replay result: %#v", result)
	}
	if result.Pow != "pinned-pow-retry-account" || result.SessionID != "session-retry-account" || a.AccountID != "retry-account" {
		t.Fatalf("wrong replay account/session state: result=%#v account=%q", result, a.AccountID)
	}
	if got := strings.Join(ds.pinnedCalls, ","); got != "initial-account" {
		t.Fatalf("pinned retry calls = %q", got)
	}
	if got := strings.Join(ds.rootCalls, ","); got != "retry-account:session-retry-account" {
		t.Fatalf("root replay calls = %q", got)
	}
	if len(ds.deleted) != 1 || ds.deleted[0] != "initial-token:session-initial-account" {
		t.Fatalf("old root was not deleted with its original token: %#v", ds.deleted)
	}
	for name, payload := range map[string]map[string]any{"base": base, "retry": retry} {
		if payloadSessionID(payload) != "session-retry-account" || payload["prompt"] != rootReq.FinalPrompt {
			t.Fatalf("%s did not receive the canonical replacement root: %#v", name, payload)
		}
		if parent, ok := payload["parent_message_id"]; !ok || parent != nil {
			t.Fatalf("%s retained a stale parent: %#v", name, payload)
		}
	}
	if activeSessionID != "session-retry-account" {
		t.Fatalf("active session = %q", activeSessionID)
	}
}

func TestCallEmptyOutputRetryChecksReplacementAccountLimit(t *testing.T) {
	ds := &emptyRetryLimitDSStub{}
	switcher := &emptyRetryAccountSwitcherStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "initial-account",
		DeepSeekToken:  "initial-token",
		TriedAccounts:  map[string]bool{},
	}
	rootReq := promptcompat.StandardRequest{
		Surface:       "responses",
		ResolvedModel: "deepseek-v4-flash",
		FinalPrompt:   strings.Repeat("x", 11),
	}
	base := rootReq.CompletionPayload("session-initial-account")
	retry := rootReq.CompletionPayload("session-initial-account")
	retry["parent_message_id"] = 101
	pow, prepared := PrepareEmptyOutputRetry(context.Background(), switcher, ds, a, base, retry, "initial-pow", "responses", false, 1, nil, nil)
	if !prepared || pow != "pinned-pow-retry-account" {
		t.Fatalf("retry preparation = (%q, %v)", pow, prepared)
	}
	_, err := CallEmptyOutputRetry(context.Background(), ds, switcher, a, base, retry, pow, rootReq, config.DefaultPromptLimitSettings(), nil)
	if message, limited := RootSessionPromptLimitMessage(err); !limited || message == "" {
		t.Fatalf("replacement account limit was not surfaced: %v", err)
	}
	if ds.rootCalls != 0 {
		t.Fatalf("oversized replacement root reached completion: %d", ds.rootCalls)
	}
	if got := strings.Join(ds.limitAccounts, ","); got != "retry-account" {
		t.Fatalf("replacement limit lookup = %q", got)
	}
}

func TestCallEmptyOutputRetryReplaysRootAfterRootPinned429(t *testing.T) {
	ds := &emptyRetryReplayDSStub{failInitialRootCall: true}
	switcher := &emptyRetryAccountSwitcherStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "initial-account",
		DeepSeekToken:  "initial-token",
		TriedAccounts:  map[string]bool{},
	}
	rootReq := promptcompat.StandardRequest{Surface: "chat.completions", ResolvedModel: "deepseek-v4-flash", FinalPrompt: "complete canonical prompt"}
	base := rootReq.CompletionPayload("session-initial-account")
	retry := rootReq.CompletionPayload("session-initial-account")
	retry["parent_message_id"] = 101

	result, err := CallEmptyOutputRetry(context.Background(), ds, switcher, a, base, retry, "pinned-pow-initial-account", rootReq, config.DefaultPromptLimitSettings(), nil)
	if err != nil || result.Response == nil || !result.ReplayedRoot {
		t.Fatalf("root retry replay = (%#v, %v)", result, err)
	}
	if len(ds.pinnedCalls) != 0 {
		t.Fatalf("full root retry must not use incremental completion: %#v", ds.pinnedCalls)
	}
	if got := strings.Join(ds.rootCalls, ","); got != "initial-account:session-initial-account,retry-account:session-retry-account" {
		t.Fatalf("root pinned calls = %q", got)
	}
	if payloadSessionID(base) != "session-retry-account" || payloadSessionID(retry) != "session-retry-account" || retry["parent_message_id"] != nil {
		t.Fatalf("replacement root payloads = base=%#v retry=%#v", base, retry)
	}
}

func TestCallEmptyOutputRetrySessionCapacityKeepsAccount(t *testing.T) {
	ds := &emptyRetryReplayDSStub{failInitialCapacity: true}
	switcher := &emptyRetryAccountSwitcherStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "initial-account",
		DeepSeekToken:  "initial-token",
		TriedAccounts:  map[string]bool{},
	}
	rootReq := promptcompat.StandardRequest{Surface: "chat.completions", ResolvedModel: "deepseek-v4-flash", FinalPrompt: "complete canonical prompt"}
	base := rootReq.CompletionPayload("session-initial-account")
	retry := rootReq.CompletionPayload("session-initial-account")
	retry["parent_message_id"] = 101

	result, err := CallEmptyOutputRetry(context.Background(), ds, switcher, a, base, retry, "pinned-pow-initial-account", rootReq, config.DefaultPromptLimitSettings(), nil)
	if err != nil || result.Response == nil || !result.ReplayedRoot {
		t.Fatalf("capacity replay = (%#v, %v)", result, err)
	}
	if switcher.switched || a.AccountID != "initial-account" {
		t.Fatalf("session capacity must keep the account: switched=%v account=%q", switcher.switched, a.AccountID)
	}
	if got := strings.Join(ds.rootCalls, ","); got != "initial-account:session-initial-account,initial-account:session-initial-account" {
		t.Fatalf("same-account root calls = %q", got)
	}
}

func TestCallEmptyOutputRetryChunksReplacementRootWhenItsLimitIsLower(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingEnable = true
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	rootReq := promptcompat.StandardRequest{
		Surface:                 "responses",
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("section alpha with a complete boundary.\n\n", 320),
		IncrementalFormatPrompt: "Return one JSON object with the required schema.",
	}
	if promptcompat.PromptUnits(rootReq.FinalPrompt) <= cfg.MaxCharsExpert {
		t.Fatalf("test prompt must require chunking: %d", promptcompat.PromptUnits(rootReq.FinalPrompt))
	}
	a := &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}
	base := rootReq.CompletionPayload("previous-root")
	retry := rootReq.CompletionPayload("previous-root")

	result, err := CallEmptyOutputRetry(context.Background(), ds, nil, a, base, retry, "pow", rootReq, cfg, nil)
	if err != nil || result.Response == nil || !result.ReplayedRoot {
		t.Fatalf("chunked root replay = (%#v, %v)", result, err)
	}
	finalPrompt, _ := base["prompt"].(string)
	if promptcompat.PromptUnits(finalPrompt) > cfg.MaxCharsExpert {
		t.Fatalf("final chunk exceeds replacement limit: %d", promptcompat.PromptUnits(finalPrompt))
	}
	if !IsPinnedCompletionPayload(base) || payloadSessionID(base) == "previous-root" {
		t.Fatalf("replacement root was not converted to a pinned chunk branch: %#v", base)
	}
	if len(ds.mainPrompts) == 0 {
		t.Fatal("chunked root did not issue its final pinned completion")
	}
}
