package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

type responsesIncrementalAuthStub struct{}

type responsesSwitchingIncrementalAuthStub struct {
	accountID string
	switches  int
}

func (s *responsesSwitchingIncrementalAuthStub) requestAuth() *auth.RequestAuth {
	accountID := s.accountID
	if accountID == "" {
		accountID = "account-initial"
	}
	return &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-" + accountID,
		CallerID:       "caller:responses-switch",
		AccountID:      accountID,
		SessionKey:     "responses-switch-session",
		TriedAccounts:  map[string]bool{},
	}
}

func (s *responsesSwitchingIncrementalAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s *responsesSwitchingIncrementalAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s *responsesSwitchingIncrementalAuthStub) DetermineWithSession(_ *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (*responsesSwitchingIncrementalAuthStub) Release(_ *auth.RequestAuth) {}

func (s *responsesSwitchingIncrementalAuthStub) SwitchAccount(_ context.Context, a *auth.RequestAuth) bool {
	if s.switches > 0 {
		return false
	}
	s.switches++
	s.accountID = "account-retry"
	a.AccountID = s.accountID
	a.DeepSeekToken = "token-" + s.accountID
	return true
}

type responsesBodyRecordingAuthStub struct {
	bodies      [][]byte
	sessionKeys []string
}

func (s *responsesBodyRecordingAuthStub) requestAuth() *auth.RequestAuth {
	return responsesIncrementalAuthStub{}.requestAuth()
}

func (s *responsesBodyRecordingAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s *responsesBodyRecordingAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s *responsesBodyRecordingAuthStub) DetermineWithSession(_ *http.Request, body []byte) (*auth.RequestAuth, error) {
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	return s.requestAuth(), nil
}

func (s *responsesBodyRecordingAuthStub) DetermineWithSessionKey(_ *http.Request, body []byte, sessionKey string) (*auth.RequestAuth, error) {
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	s.sessionKeys = append(s.sessionKeys, sessionKey)
	a := s.requestAuth()
	a.SessionKey = sessionKey
	return a, nil
}

func (*responsesBodyRecordingAuthStub) Release(_ *auth.RequestAuth) {}

type responsesRotationConfigStub struct{ responsesHistoryConfigStub }

func (responsesRotationConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.IncrementalMaxTurns = 2
	cfg.IncrementalRotationKeepRecent = 1
	return cfg
}

type responsesIncrementalCurrentInputConfigStub struct{ responsesHistoryConfigStub }

func (responsesIncrementalCurrentInputConfigStub) CurrentInputFileEnabled() bool { return true }
func (responsesIncrementalCurrentInputConfigStub) CurrentInputFileMinChars() int { return 0 }
func (responsesIncrementalCurrentInputConfigStub) RemoteFileUploadEnabled() bool { return false }

type responsesIncrementalCurrentInputRotationConfigStub struct {
	responsesIncrementalCurrentInputConfigStub
}

func (responsesIncrementalCurrentInputRotationConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.IncrementalMaxTurns = 25
	cfg.IncrementalRotationKeepRecent = 25
	return cfg
}

type responsesCompactRecoveryConfigStub struct{ responsesHistoryConfigStub }

func (responsesCompactRecoveryConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 1000
	cfg.MaxCharsExpertConfigured = true
	cfg.KeepRecentTurns = 2
	cfg.IncrementalMaxTurns = 25
	return cfg
}

func (responsesIncrementalAuthStub) requestAuth() *auth.RequestAuth {
	return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller:responses-inc", AccountID: "account-1", SessionKey: "responses-session", TriedAccounts: map[string]bool{}}
}

func (s responsesIncrementalAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s responsesIncrementalAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (s responsesIncrementalAuthStub) DetermineWithSession(_ *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.requestAuth(), nil
}

func (responsesIncrementalAuthStub) Release(_ *auth.RequestAuth) {}

type responsesIncrementalDSStub struct {
	createCalls int
	normal      []map[string]any
	pinned      []map[string]any
}

type responsesSwitchingIncrementalDSStub struct {
	normalAccounts []string
	normalSessions []string
	pinnedAccounts []string
	pinnedSessions []string
	pinnedParents  []int
}

type responsesPinned429DSStub struct {
	normalAccounts []string
	normalPayloads []map[string]any
	pinnedAccounts []string
	pinnedError    error
}

func (s *responsesSwitchingIncrementalDSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "session-" + a.AccountID, nil
}

func (*responsesSwitchingIncrementalDSStub) GetPow(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "pow-" + a.AccountID, nil
}

func (*responsesSwitchingIncrementalDSStub) GetPowPinned(_ context.Context, a *auth.RequestAuth) (string, error) {
	return "pow-" + a.AccountID, nil
}

func (*responsesSwitchingIncrementalDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-1", Status: "uploaded"}, nil
}

func (s *responsesSwitchingIncrementalDSStub) CallCompletion(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normalAccounts = append(s.normalAccounts, a.AccountID)
	s.normalSessions = append(s.normalSessions, responseString(payload["chat_session_id"]))
	if len(s.normalAccounts) == 1 {
		return responsesIncrementalSSE(101, ""), nil
	}
	return responsesIncrementalSSE(202, "first response"), nil
}

func (s *responsesSwitchingIncrementalDSStub) CallCompletionPinned(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.pinnedAccounts = append(s.pinnedAccounts, a.AccountID)
	s.pinnedSessions = append(s.pinnedSessions, responseString(payload["chat_session_id"]))
	parent := 0
	switch value := payload["parent_message_id"].(type) {
	case int:
		parent = value
	case float64:
		parent = int(value)
	}
	s.pinnedParents = append(s.pinnedParents, parent)
	return responsesIncrementalSSE(303, "second response"), nil
}

func (*responsesSwitchingIncrementalDSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*responsesSwitchingIncrementalDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func (*responsesPinned429DSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "session-" + a.AccountID, nil
}

func (*responsesPinned429DSStub) GetPow(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	return "pow-" + a.AccountID, nil
}

func (*responsesPinned429DSStub) GetPowPinned(_ context.Context, a *auth.RequestAuth) (string, error) {
	return "pow-" + a.AccountID, nil
}

func (*responsesPinned429DSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-1", Status: "uploaded"}, nil
}

func (s *responsesPinned429DSStub) CallCompletion(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normalAccounts = append(s.normalAccounts, a.AccountID)
	s.normalPayloads = append(s.normalPayloads, cloneResponsesPayload(payload))
	if len(s.normalAccounts) == 1 {
		return responsesIncrementalSSE(501, "first response"), nil
	}
	return responsesIncrementalSSE(502, "replayed response"), nil
}

func (s *responsesPinned429DSStub) CallCompletionPinned(_ context.Context, a *auth.RequestAuth, _ map[string]any, _ string) (*http.Response, error) {
	s.pinnedAccounts = append(s.pinnedAccounts, a.AccountID)
	if s.pinnedError != nil {
		return nil, s.pinnedError
	}
	return nil, &dsclient.RequestFailure{
		Op:         "completion",
		Kind:       dsclient.FailureUpstreamStatus,
		StatusCode: http.StatusTooManyRequests,
		Message:    "rate limited",
	}
}

func (*responsesPinned429DSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*responsesPinned429DSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func (s *responsesIncrementalDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	s.createCalls++
	return "responses-remote-session", nil
}

func (*responsesIncrementalDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (*responsesIncrementalDSStub) GetPowPinned(_ context.Context, _ *auth.RequestAuth) (string, error) {
	return "pow", nil
}

func (*responsesIncrementalDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-1", Status: "uploaded"}, nil
}

func (s *responsesIncrementalDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normal = append(s.normal, cloneResponsesPayload(payload))
	return responsesIncrementalSSE(401, "first response"), nil
}

func (s *responsesIncrementalDSStub) CallCompletionPinned(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.pinned = append(s.pinned, cloneResponsesPayload(payload))
	return responsesIncrementalSSE(402, "second response"), nil
}

func (*responsesIncrementalDSStub) DeleteSessionForToken(_ context.Context, _, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*responsesIncrementalDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func TestResponsesIncrementalReusesSessionAndSendsOnlyDelta(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	firstBody := map[string]any{"model": "deepseek-v4-flash", "input": "first request"}
	firstResponse := serveResponsesIncremental(t, h, firstBody)
	output, ok := firstResponse["output"].([]any)
	if !ok || len(output) == 0 {
		t.Fatalf("missing first response output: %#v", firstResponse)
	}
	secondInput := []any{map[string]any{"role": "user", "content": "first request"}}
	secondInput = append(secondInput, output...)
	secondInput = append(secondInput, map[string]any{"role": "user", "content": "second request"})
	serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": secondInput})

	if ds.createCalls != 1 || len(ds.normal) != 1 || len(ds.pinned) != 1 {
		t.Fatalf("unexpected calls: create=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	payload := ds.pinned[0]
	if payload["chat_session_id"] != "responses-remote-session" {
		t.Fatalf("unexpected session: %#v", payload["chat_session_id"])
	}
	if parent, ok := payload["parent_message_id"].(float64); !ok || int(parent) != 401 {
		t.Fatalf("unexpected parent: %#v", payload["parent_message_id"])
	}
	prompt, _ := payload["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("missing %q in prompt: %q", expected, prompt)
		}
	}
	for _, forbidden := range []string{"first request", "first response"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("unexpected replay of %q: %q", forbidden, prompt)
		}
	}
}

func TestResponsesPreviousResponseReusesIncrementalSessionWithCurrentInputFile(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesIncrementalCurrentInputConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}

	first := serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": "first request",
	})
	responseID, _ := first["id"].(string)
	if responseID == "" {
		t.Fatalf("first response had no id: %#v", first)
	}
	serveResponsesIncremental(t, h, map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": responseID,
		"input":                "second request",
	})

	if ds.createCalls != 1 || len(ds.normal) != 1 || len(ds.pinned) != 1 {
		t.Fatalf("current-input response chain must reuse the original session: create=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	prompt, _ := ds.pinned[0]["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("incremental prompt missing %q: %q", expected, prompt)
		}
	}
	for _, replayed := range []string{"first request", "first response"} {
		if strings.Contains(prompt, replayed) {
			t.Fatalf("incremental prompt replayed %q: %q", replayed, prompt)
		}
	}
}

func TestResponsesPreviousResponseRotatesAfter25TurnsWithCurrentInputFile(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesIncrementalCurrentInputRotationConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}

	previousResponseID := ""
	for turn := 1; turn <= 26; turn++ {
		body := map[string]any{
			"model": "deepseek-v4-flash",
			"input": fmt.Sprintf("turn %d", turn),
		}
		if previousResponseID != "" {
			body["previous_response_id"] = previousResponseID
		}
		response := serveResponsesIncremental(t, h, body)
		previousResponseID, _ = response["id"].(string)
		if previousResponseID == "" {
			t.Fatalf("turn %d response had no id: %#v", turn, response)
		}
	}

	if ds.createCalls != 2 || len(ds.normal) != 2 || len(ds.pinned) != 24 {
		t.Fatalf("expected 25-turn pinned session followed by rotation: create=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	rollover := ds.normal[1]
	if rollover["parent_message_id"] != nil {
		t.Fatalf("rotation must create a root completion: %#v", rollover)
	}
}

func TestResponsesIncrementalRotatesIntoFreshRootSession(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesRotationConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	history := []any{map[string]any{"role": "user", "content": "first request"}}
	first := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": history})
	firstOutput, _ := first["output"].([]any)
	history = append(history, firstOutput...)
	history = append(history, map[string]any{"role": "user", "content": "second request"})
	second := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": history})
	secondOutput, _ := second["output"].([]any)
	history = append(history, secondOutput...)
	history = append(history, map[string]any{"role": "user", "content": "third request"})
	serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": history})

	if ds.createCalls != 2 || len(ds.normal) != 2 || len(ds.pinned) != 1 {
		t.Fatalf("expected full, pinned, rollover calls; creates=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	rollover := ds.normal[1]
	if rollover["parent_message_id"] != nil {
		t.Fatalf("Responses rollover must start at root: %#v", rollover)
	}
	prompt, _ := rollover["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "second response", "third request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Responses rollover prompt missing %q: %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "first request") {
		t.Fatalf("Responses rollover retained compacted history: %q", prompt)
	}
}

func TestResponsesIncrementalRecordsRetryAccountSession(t *testing.T) {
	authStub := &responsesSwitchingIncrementalAuthStub{}
	ds := &responsesSwitchingIncrementalDSStub{}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        authStub,
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	first := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": "first request"})
	firstOutput, _ := first["output"].([]any)
	secondInput := []any{map[string]any{"role": "user", "content": "first request"}}
	secondInput = append(secondInput, firstOutput...)
	secondInput = append(secondInput, map[string]any{"role": "user", "content": "second request"})
	serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": secondInput})

	if authStub.switches != 1 {
		t.Fatalf("expected one empty-output account switch, got %d", authStub.switches)
	}
	if got := ds.normalAccounts; len(got) != 2 || got[0] != "account-initial" || got[1] != "account-retry" {
		t.Fatalf("unexpected normal completion accounts: %#v", got)
	}
	if got := ds.normalSessions; len(got) != 2 || got[0] != "session-account-initial" || got[1] != "session-account-retry" {
		t.Fatalf("retry did not move to its account session: %#v", got)
	}
	if len(ds.pinnedAccounts) != 1 || ds.pinnedAccounts[0] != "account-retry" || ds.pinnedSessions[0] != "session-account-retry" || ds.pinnedParents[0] != 202 {
		t.Fatalf("incremental branch did not retain retry account/session state: accounts=%#v sessions=%#v parents=%#v", ds.pinnedAccounts, ds.pinnedSessions, ds.pinnedParents)
	}
}

func TestResponsesPinned429SwitchesAccountAndReplaysFullContext(t *testing.T) {
	authStub := &responsesSwitchingIncrementalAuthStub{}
	ds := &responsesPinned429DSStub{}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        authStub,
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	first := serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": "first request",
	})
	firstOutput, _ := first["output"].([]any)
	if len(firstOutput) == 0 {
		t.Fatalf("missing first response output: %#v", first)
	}
	secondInput := []any{map[string]any{"role": "user", "content": "first request"}}
	secondInput = append(secondInput, firstOutput...)
	secondInput = append(secondInput, map[string]any{"role": "user", "content": "second request"})
	serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": secondInput,
	})

	if authStub.switches != 1 {
		t.Fatalf("expected one account switch after pinned 429, got %d", authStub.switches)
	}
	if got := ds.pinnedAccounts; len(got) != 1 || got[0] != "account-initial" {
		t.Fatalf("pinned branch should remain on the original account: %#v", got)
	}
	if got := ds.normalAccounts; len(got) != 2 || got[0] != "account-initial" || got[1] != "account-retry" {
		t.Fatalf("full replay did not move to the replacement account: %#v", got)
	}
	fallback := ds.normalPayloads[1]
	if fallback["chat_session_id"] != "session-account-retry" || fallback["parent_message_id"] != nil {
		t.Fatalf("replay must create a new root session: %#v", fallback)
	}
	prompt, _ := fallback["prompt"].(string)
	for _, expected := range []string{"first request", "first response", "second request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("full replay prompt missing %q: %q", expected, prompt)
		}
	}
}

func TestResponsesPinnedSessionCapacityRebuildsFullContextOnSameAccount(t *testing.T) {
	authStub := &responsesSwitchingIncrementalAuthStub{}
	ds := &responsesPinned429DSStub{pinnedError: &dsclient.RequestFailure{
		Op:             "completion",
		Kind:           dsclient.FailureUpstreamStatus,
		StatusCode:     http.StatusTooManyRequests,
		RateLimitScope: dsclient.RateLimitScopeSessionCapacity,
		Message:        "maximum conversation turns reached",
	}}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        authStub,
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	first := serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": "first request",
	})
	firstOutput, _ := first["output"].([]any)
	secondInput := []any{map[string]any{"role": "user", "content": "first request"}}
	secondInput = append(secondInput, firstOutput...)
	secondInput = append(secondInput, map[string]any{"role": "user", "content": "second request"})
	serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": secondInput,
	})

	if authStub.switches != 0 {
		t.Fatalf("session capacity must not switch accounts, got %d switches", authStub.switches)
	}
	if got := ds.pinnedAccounts; len(got) != 1 || got[0] != "account-initial" {
		t.Fatalf("pinned branch should stay on the original account: %#v", got)
	}
	if got := ds.normalAccounts; len(got) != 2 || got[0] != "account-initial" || got[1] != "account-initial" {
		t.Fatalf("full replay should rebuild a root on the same account: %#v", got)
	}
	fallback := ds.normalPayloads[1]
	if fallback["chat_session_id"] != "session-account-initial" || fallback["parent_message_id"] != nil {
		t.Fatalf("same-account replay must create a new root session: %#v", fallback)
	}
	prompt, _ := fallback["prompt"].(string)
	for _, expected := range []string{"first request", "first response", "second request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("full replay prompt missing %q: %q", expected, prompt)
		}
	}
}

func TestResponsesExpiredCompactSlidingWindowReusesRecoveredSession(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	h := &Handler{
		Store:       responsesCompactRecoveryConfigStub{},
		Auth:        responsesIncrementalAuthStub{},
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	expired := map[string]any{"type": "compaction", "encrypted_content": localCompactionHandlePrefix + "expired"}
	oldUser := map[string]any{"role": "user", "content": strings.Repeat("old-user-", 250)}
	oldAssistant := map[string]any{"role": "assistant", "content": strings.Repeat("old-answer-", 250)}
	secondUser := map[string]any{"role": "user", "content": "second request"}
	secondAssistant := map[string]any{"role": "assistant", "content": "second answer"}
	thirdUser := map[string]any{"role": "user", "content": "third request"}

	firstInput := []any{expired, oldUser, oldAssistant, secondUser, secondAssistant, thirdUser}
	first := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-pro", "input": firstInput})
	firstOutput, _ := first["output"].([]any)
	if len(firstOutput) == 0 {
		t.Fatalf("missing recovered response output: %#v", first)
	}

	fourthUser := map[string]any{"role": "user", "content": "fourth request"}
	secondInput := []any{expired, oldUser, oldAssistant, secondUser, secondAssistant, thirdUser}
	secondInput = append(secondInput, firstOutput...)
	secondInput = append(secondInput, fourthUser)
	serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-pro", "input": secondInput})

	if ds.createCalls != 1 || len(ds.normal) != 1 || len(ds.pinned) != 1 {
		t.Fatalf("expired compact window must reuse the recovered session: creates=%d normal=%d pinned=%d", ds.createCalls, len(ds.normal), len(ds.pinned))
	}
	prompt, _ := ds.pinned[0]["prompt"].(string)
	for _, expected := range []string{"Incremental response format requirements", "fourth request"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("recovered incremental prompt missing %q: %q", expected, prompt)
		}
	}
	for _, replayed := range []string{"old-user-", "second request", "third request", "first response"} {
		if strings.Contains(prompt, replayed) {
			t.Fatalf("recovered incremental prompt replayed %q: %q", replayed, prompt)
		}
	}
}

func TestResponsesPreviousResponseAuthenticatesExpandedCanonicalInput(t *testing.T) {
	ds := &responsesIncrementalDSStub{}
	authRecorder := &responsesBodyRecordingAuthStub{}
	h := &Handler{
		Store:       responsesHistoryConfigStub{},
		Auth:        authRecorder,
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	first := serveResponsesIncremental(t, h, map[string]any{"model": "deepseek-v4-flash", "input": "first request"})
	responseID, _ := first["id"].(string)
	if responseID == "" {
		t.Fatalf("first response had no id: %#v", first)
	}
	serveResponsesIncremental(t, h, map[string]any{
		"model":                "deepseek-v4-flash",
		"previous_response_id": responseID,
		"input":                "second request",
	})
	if len(authRecorder.bodies) != 2 {
		t.Fatalf("session auth bodies=%d, want 2", len(authRecorder.bodies))
	}
	if len(authRecorder.sessionKeys) != 1 || authRecorder.sessionKeys[0] != "responses-session" {
		t.Fatalf("previous_response_id did not inherit the original session key: %#v", authRecorder.sessionKeys)
	}
	expanded := string(authRecorder.bodies[1])
	for _, expected := range []string{"first request", "first response", "second request"} {
		if !strings.Contains(expanded, expected) {
			t.Fatalf("expanded session body missing %q: %s", expected, expanded)
		}
	}
}

func serveResponsesIncremental(t *testing.T, h *Handler, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func responsesIncrementalSSE(messageID int, text string) *http.Response {
	id, _ := json.Marshal(messageID)
	content, _ := json.Marshal(text)
	body := `data: {"response_message_id":` + string(id) + "}\n" +
		`data: {"p":"response/content","v":` + string(content) + "}\n" +
		"data: [DONE]\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func cloneResponsesPayload(payload map[string]any) map[string]any {
	b, _ := json.Marshal(payload)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
