package shared

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type rootSessionFailoverAuthStub struct{ switches int }

func (s *rootSessionFailoverAuthStub) SwitchAccount(_ context.Context, a *auth.RequestAuth) bool {
	if s.switches > 0 {
		return false
	}
	s.switches++
	a.AccountID = "account-replay"
	a.DeepSeekToken = "token-replay"
	return true
}

type rootSessionFailoverDSStub struct {
	createAccounts       []string
	pinnedCreateAccounts []string
	create429ForInitial  bool
	powAccounts          []string
	deleted              []string
	limitAccounts        []string
}

func (s *rootSessionFailoverDSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	s.createAccounts = append(s.createAccounts, a.AccountID)
	return "session-" + a.AccountID, nil
}

func (s *rootSessionFailoverDSStub) CreateSessionPinned(ctx context.Context, a *auth.RequestAuth) (string, error) {
	s.pinnedCreateAccounts = append(s.pinnedCreateAccounts, a.AccountID)
	if s.create429ForInitial && a.AccountID == "account-initial" {
		return "", &dsclient.RequestFailure{
			Op:         "create session",
			Kind:       dsclient.FailureUpstreamStatus,
			StatusCode: http.StatusTooManyRequests,
			Message:    "rate limited",
		}
	}
	return s.CreateSession(ctx, a, 1)
}

func (*rootSessionFailoverDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "unexpected-fallback-pow", nil
}

func (s *rootSessionFailoverDSStub) GetPowPinned(_ context.Context, a *auth.RequestAuth) (string, error) {
	s.powAccounts = append(s.powAccounts, a.AccountID)
	if a.AccountID == "account-initial" {
		return "", &dsclient.RequestFailure{
			Op:         "get pow",
			Kind:       dsclient.FailureUpstreamStatus,
			StatusCode: http.StatusTooManyRequests,
			Message:    "rate limited",
		}
	}
	return "pow-" + a.AccountID, nil
}

func (*rootSessionFailoverDSStub) CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error) {
	return nil, nil
}

func (*rootSessionFailoverDSStub) CallCompletionRootPinned(context.Context, *auth.RequestAuth, map[string]any, string) (*http.Response, error) {
	return nil, nil
}

func (s *rootSessionFailoverDSStub) GetModelInputLimits(_ context.Context, a *auth.RequestAuth) (config.ModelInputLimits, error) {
	s.limitAccounts = append(s.limitAccounts, a.AccountID)
	return config.ModelInputLimits{Default: 1000, Expert: 1000}, nil
}

func (s *rootSessionFailoverDSStub) DeleteSessionForToken(_ context.Context, token, sessionID string) (*dsclient.DeleteSessionResult, error) {
	s.deleted = append(s.deleted, token+":"+sessionID)
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*rootSessionFailoverDSStub) DeleteAllSessionsForToken(context.Context, string) error {
	return nil
}

func TestPrepareRootSessionWithPinnedPowReplaysOnAnotherAccount(t *testing.T) {
	ds := &rootSessionFailoverDSStub{}
	switcher := &rootSessionFailoverAuthStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "account-initial",
		DeepSeekToken:  "token-initial",
		TriedAccounts:  map[string]bool{},
	}
	cfg := config.DefaultPromptLimitSettings()
	req := promptcompat.StandardRequest{
		Surface:       "test",
		ResolvedModel: "deepseek-v4-flash",
		FinalPrompt:   strings.Repeat("safe prompt ", 20),
	}

	prepared, err := PrepareRootSessionWithPinnedPow(context.Background(), ds, switcher, a, req, cfg)
	if err != nil {
		t.Fatalf("prepare root session: %v", err)
	}
	if prepared.SessionID != "session-account-replay" || prepared.Pow != "pow-account-replay" {
		t.Fatalf("unexpected replay root preparation: %#v", prepared)
	}
	if switcher.switches != 1 || a.AccountID != "account-replay" {
		t.Fatalf("expected one account switch, switches=%d account=%q", switcher.switches, a.AccountID)
	}
	if got := strings.Join(ds.createAccounts, ","); got != "account-initial,account-replay" {
		t.Fatalf("root sessions were not rebuilt in account order: %q", got)
	}
	if got := strings.Join(ds.powAccounts, ","); got != "account-initial,account-replay" {
		t.Fatalf("PoW did not stay paired with each root session: %q", got)
	}
	if len(ds.deleted) != 1 || !strings.Contains(ds.deleted[0], "token-initial:session-account-initial") {
		t.Fatalf("abandoned root session was not deleted using its original token: %#v", ds.deleted)
	}
	if got := ds.limitAccounts; len(got) != 1 || got[0] != "account-replay" {
		t.Fatalf("replacement account prompt limit was not refreshed: %#v", got)
	}
}

func TestPrepareRootSessionPinnedCreate429SwitchesBeforeCreatingReplacement(t *testing.T) {
	ds := &rootSessionFailoverDSStub{create429ForInitial: true}
	switcher := &rootSessionFailoverAuthStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "account-initial",
		DeepSeekToken:  "token-initial",
		TriedAccounts:  map[string]bool{},
	}
	req := promptcompat.StandardRequest{Surface: "test", ResolvedModel: "deepseek-v4-flash", FinalPrompt: "safe prompt"}
	prepared, err := PrepareRootSessionWithPinnedPow(context.Background(), ds, switcher, a, req, config.DefaultPromptLimitSettings())
	if err != nil {
		t.Fatalf("prepare root after create 429: %v", err)
	}
	if prepared.SessionID != "session-account-replay" || prepared.Pow != "pow-account-replay" {
		t.Fatalf("unexpected replacement root: %#v", prepared)
	}
	if got := strings.Join(ds.pinnedCreateAccounts, ","); got != "account-initial,account-replay" {
		t.Fatalf("pinned create did not switch before replacement creation: %q", got)
	}
	if got := strings.Join(ds.createAccounts, ","); got != "account-replay" {
		t.Fatalf("initial account must not create a root after its 429: %q", got)
	}
	if switcher.switches != 1 || a.AccountID != "account-replay" {
		t.Fatalf("expected one switch to replacement account, switches=%d account=%q", switcher.switches, a.AccountID)
	}
}

func TestPrepareRootSessionWithPinnedPowRejectsReplacementLimit(t *testing.T) {
	ds := &rootSessionFailoverDSStub{}
	switcher := &rootSessionFailoverAuthStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "account-initial",
		DeepSeekToken:  "token-initial",
		TriedAccounts:  map[string]bool{},
	}
	cfg := config.DefaultPromptLimitSettings()
	req := promptcompat.StandardRequest{
		Surface:       "test",
		ResolvedModel: "deepseek-v4-flash",
		FinalPrompt:   strings.Repeat("x", 1001),
	}

	_, err := PrepareRootSessionWithPinnedPow(context.Background(), ds, switcher, a, req, cfg)
	if message, ok := RootSessionPromptLimitMessage(err); !ok || message == "" {
		t.Fatalf("replacement account limit must be surfaced as a prompt error, got %v", err)
	}
	if replacementCfg, ok := RootSessionPromptLimitSettings(err); !ok || replacementCfg.MaxCharsDefault != 1000 || replacementCfg.MaxCharsExpert != 1000 {
		t.Fatalf("replacement account limit settings were not preserved: %#v ok=%v", replacementCfg, ok)
	}
	if len(ds.deleted) != 1 || !strings.Contains(ds.deleted[0], "token-initial:session-account-initial") {
		t.Fatalf("abandoned oversized root session was not deleted: %#v", ds.deleted)
	}
}

func TestRestartRootSessionAfterSessionCapacityKeepsAccount(t *testing.T) {
	ds := &rootSessionFailoverDSStub{}
	switcher := &rootSessionFailoverAuthStub{}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		AccountID:      "account-initial",
		DeepSeekToken:  "token-initial",
		TriedAccounts:  map[string]bool{},
	}
	cfg := config.DefaultPromptLimitSettings()
	req := promptcompat.StandardRequest{Surface: "test", ResolvedModel: "deepseek-v4-flash", FinalPrompt: "small prompt"}
	err := &dsclient.RequestFailure{
		Op:             "completion",
		Kind:           dsclient.FailureUpstreamStatus,
		StatusCode:     http.StatusTooManyRequests,
		RateLimitScope: dsclient.RateLimitScopeSessionCapacity,
		Message:        "maximum conversation turns reached",
	}
	_, restarted, restartErr := RestartRootSessionAfterPinnedFailure(context.Background(), ds, switcher, a, req, cfg, "session-account-initial", err)
	if restartErr != nil || !restarted {
		t.Fatalf("expected same-account root restart, restarted=%v err=%v", restarted, restartErr)
	}
	if switcher.switches != 0 || a.AccountID != "account-initial" {
		t.Fatalf("session capacity must not switch accounts: switches=%d account=%q", switcher.switches, a.AccountID)
	}
	if len(ds.deleted) != 1 || !strings.Contains(ds.deleted[0], "token-initial:session-account-initial") {
		t.Fatalf("exhausted root session was not deleted with its owner token: %#v", ds.deleted)
	}
}
