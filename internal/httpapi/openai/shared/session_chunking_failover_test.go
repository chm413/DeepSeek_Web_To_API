package shared

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type chunkingFailoverAuthStub struct{ switches int }

func (s *chunkingFailoverAuthStub) SwitchAccount(_ context.Context, a *auth.RequestAuth) bool {
	if s.switches > 0 {
		return false
	}
	s.switches++
	a.AccountID = "account-replay"
	a.DeepSeekToken = "token-replay"
	return true
}

type chunkingFailoverDSStub struct {
	createCount   map[string]int
	messageID     int
	failed        bool
	failure       error
	mainPrompts   []string
	mainAccount   []string
	rawAccounts   []string
	limitAccounts []string
	deleted       []string
}

func (s *chunkingFailoverDSStub) CreateSession(_ context.Context, a *auth.RequestAuth, _ int) (string, error) {
	if s.createCount == nil {
		s.createCount = map[string]int{}
	}
	s.createCount[a.AccountID]++
	if s.createCount[a.AccountID]%2 == 1 {
		return "planner-" + a.AccountID, nil
	}
	return "main-" + a.AccountID, nil
}

func (*chunkingFailoverDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "pow", nil
}

func (*chunkingFailoverDSStub) GetPowPinned(context.Context, *auth.RequestAuth) (string, error) {
	return "pow", nil
}

func (s *chunkingFailoverDSStub) GetModelInputLimits(_ context.Context, a *auth.RequestAuth) (config.ModelInputLimits, error) {
	s.limitAccounts = append(s.limitAccounts, a.AccountID)
	if a.AccountID == "account-replay" {
		return config.ModelInputLimits{Default: 7000, Expert: 7000}, nil
	}
	return config.ModelInputLimits{Default: 8000, Expert: 8000}, nil
}

func (s *chunkingFailoverDSStub) CallCompletionPinned(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.messageID++
	prompt, _ := payload["prompt"].(string)
	if strings.HasPrefix(fmt.Sprint(payload["chat_session_id"]), "planner-") {
		return chunkingFailoverSSE(s.messageID, fmt.Sprintf(`{"offset_utf16":%d}`, chunkingFailoverPlannerMax(prompt)), false), nil
	}
	s.mainPrompts = append(s.mainPrompts, prompt)
	s.mainAccount = append(s.mainAccount, a.AccountID)
	thinking, _ := payload["thinking_enabled"].(bool)
	return chunkingFailoverSSE(s.messageID, "started", thinking), nil
}

func (s *chunkingFailoverDSStub) CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string, _ int) (*http.Response, error) {
	return s.CallCompletionPinned(ctx, a, payload, pow)
}

func (s *chunkingFailoverDSStub) CallCompletionRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string, _ int) (*http.Response, error) {
	s.rawAccounts = append(s.rawAccounts, a.AccountID)
	return s.CallCompletionPinned(ctx, a, payload, pow)
}

func (s *chunkingFailoverDSStub) CallCompletionPinnedRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string) (*http.Response, error) {
	s.rawAccounts = append(s.rawAccounts, a.AccountID)
	prompt, _ := payload["prompt"].(string)
	if !s.failed && a.AccountID == "account-initial" && strings.Contains(prompt, "[OVERSIZED_REQUEST_CONTROL") {
		s.failed = true
		if s.failure != nil {
			return nil, s.failure
		}
		return nil, &dsclient.RequestFailure{Op: "completion", Kind: dsclient.FailureUpstreamStatus, StatusCode: http.StatusTooManyRequests, Message: "rate limited"}
	}
	return s.CallCompletionPinned(ctx, a, payload, pow)
}

func TestTryPrepareRootSessionChunkingSessionCapacityStaysOnAccount(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &chunkingFailoverDSStub{failure: &dsclient.RequestFailure{
		Op:             "completion",
		Kind:           dsclient.FailureUpstreamStatus,
		StatusCode:     http.StatusTooManyRequests,
		RateLimitScope: dsclient.RateLimitScopeSessionCapacity,
		Message:        "maximum conversation turns reached",
	}}
	switcher := &chunkingFailoverAuthStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 8000
	cfg.SessionChunkingEnable = true
	cfg.SessionChunkingTargetRatio = 0.85
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: "account-initial", DeepSeekToken: "token-initial", TriedAccounts: map[string]bool{}}
	req := promptcompat.StandardRequest{
		Surface:                 "test",
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("A complete paragraph that must remain intact after a session capacity error.\n\n", 160),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}

	prepared, err := TryPrepareRootSessionChunkingWithFailover(context.Background(), ds, switcher, a, req, cfg)
	if err != nil || prepared == nil {
		t.Fatalf("expected same-account chunk replay, prepared=%#v err=%v", prepared, err)
	}
	if switcher.switches != 0 || a.AccountID != "account-initial" {
		t.Fatalf("session capacity must not switch accounts: switches=%d account=%q", switcher.switches, a.AccountID)
	}
	if len(ds.limitAccounts) != 0 {
		t.Fatalf("same-account session replay must not re-resolve another account limit: %#v", ds.limitAccounts)
	}
}

func (s *chunkingFailoverDSStub) DeleteSessionForToken(_ context.Context, token, sessionID string) (*dsclient.DeleteSessionResult, error) {
	s.deleted = append(s.deleted, token+":"+sessionID)
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*chunkingFailoverDSStub) DeleteAllSessionsForToken(context.Context, string) error { return nil }

func TestTryPrepareRootSessionChunkingWithFailoverReplaysCompletePrompt(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &chunkingFailoverDSStub{}
	switcher := &chunkingFailoverAuthStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 8000
	cfg.SessionChunkingEnable = true
	cfg.SessionChunkingTargetRatio = 0.85
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	original := strings.Repeat("A complete paragraph that must remain intact after a rate limit.\n\n", 160)
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: "account-initial", DeepSeekToken: "token-initial", TriedAccounts: map[string]bool{}}
	req := promptcompat.StandardRequest{Surface: "test", ResolvedModel: "deepseek-v4-pro", FinalPrompt: original, IncrementalFormatPrompt: "Return exactly one JSON object."}

	prepared, err := TryPrepareRootSessionChunkingWithFailover(context.Background(), ds, switcher, a, req, cfg)
	if err != nil {
		t.Fatalf("prepare root chunks with failover: %v", err)
	}
	if prepared == nil || prepared.SessionID != "main-account-replay" {
		t.Fatalf("unexpected replay preparation: %#v", prepared)
	}
	if switcher.switches != 1 || a.AccountID != "account-replay" {
		t.Fatalf("expected one managed account switch, switches=%d account=%q", switcher.switches, a.AccountID)
	}
	if got := ds.limitAccounts; len(got) != 1 || got[0] != "account-replay" {
		t.Fatalf("replacement account limit was not re-resolved: %#v", got)
	}
	if prepared.ChunkCount < 5 {
		t.Fatalf("replacement account's lower input ceiling was not used: chunks=%d", prepared.ChunkCount)
	}
	if !containsString(ds.rawAccounts, "account-initial") || !containsString(ds.rawAccounts, "account-replay") {
		t.Fatalf("chunk commits did not use both accounts: %#v", ds.rawAccounts)
	}

	fragments := make([]string, 0, prepared.ChunkCount)
	for index, prompt := range ds.mainPrompts {
		if ds.mainAccount[index] != "account-replay" {
			continue
		}
		if fragment, ok := extractChunkingFailoverFragment(prompt); ok {
			fragments = append(fragments, fragment)
		}
	}
	if fragment, ok := extractChunkingFailoverFragment(prepared.FinalWirePrompt); ok {
		fragments = append(fragments, fragment)
	}
	if reconstructed := strings.Join(fragments, ""); reconstructed != original {
		t.Fatalf("replayed fragments did not reconstruct full prompt: got=%d want=%d", promptcompat.PromptUnits(reconstructed), promptcompat.PromptUnits(original))
	}
}

func TestRestartRootSessionChunkingFinalSessionCapacityStaysOnAccount(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	// This test injects capacity only after preparation; keep the stub's
	// fragment-level failover hook disabled for the initial root.
	ds := &chunkingFailoverDSStub{failed: true}
	switcher := &chunkingFailoverAuthStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 8000
	cfg.SessionChunkingEnable = true
	cfg.SessionChunkingTargetRatio = 0.85
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: "account-initial", DeepSeekToken: "token-initial", TriedAccounts: map[string]bool{}}
	req := promptcompat.StandardRequest{
		Surface:                 "test",
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("A complete paragraph that must remain intact after a final session capacity error.\n\n", 160),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	prepared, err := TryPrepareRootSessionChunkingWithFailover(context.Background(), ds, switcher, a, req, cfg)
	if err != nil || prepared == nil {
		t.Fatalf("prepare root chunks: prepared=%#v err=%v", prepared, err)
	}

	capacityErr := &dsclient.RequestFailure{
		Op:             "completion",
		Kind:           dsclient.FailureUpstreamStatus,
		StatusCode:     http.StatusTooManyRequests,
		RateLimitScope: dsclient.RateLimitScopeSessionCapacity,
		Message:        "maximum conversation turns reached",
	}
	restarted, didRestart, restartErr := RestartRootSessionChunkingAfterPinnedFailure(context.Background(), ds, switcher, a, req, cfg, prepared, capacityErr)
	if restartErr != nil || !didRestart || restarted == nil {
		t.Fatalf("expected same-account final chunk restart, prepared=%#v restarted=%v err=%v", restarted, didRestart, restartErr)
	}
	if !restarted.SessionCapacityRestarted {
		t.Fatal("same-account final chunk restart must be bounded")
	}
	if switcher.switches != 0 || a.AccountID != "account-initial" {
		t.Fatalf("final session capacity must not switch accounts: switches=%d account=%q", switcher.switches, a.AccountID)
	}
	deletedPrepared := false
	for _, deleted := range ds.deleted {
		if strings.Contains(deleted, "token-initial:"+prepared.SessionID) {
			deletedPrepared = true
			break
		}
	}
	if !deletedPrepared {
		t.Fatalf("exhausted final branch was not deleted using its owner token: %#v", ds.deleted)
	}

	_, didRestart, restartErr = RestartRootSessionChunkingAfterPinnedFailure(context.Background(), ds, switcher, a, req, cfg, restarted, capacityErr)
	if restartErr != nil || didRestart {
		t.Fatalf("second capacity failure must be surfaced without another root rebuild: restarted=%v err=%v", didRestart, restartErr)
	}
}

func chunkingFailoverSSE(messageID int, text string, thinking bool) *http.Response {
	path := "response/content"
	if thinking {
		path = "response/thinking_content"
	}
	body := fmt.Sprintf("data: {\"response_message_id\":%d}\ndata: {\"p\":%q,\"v\":%q}\ndata: [DONE]\n", messageID, path, text)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func chunkingFailoverPlannerMax(prompt string) int {
	match := regexp.MustCompile(`ALLOWED_MAX_UTF16=(\d+)`).FindStringSubmatch(prompt)
	if len(match) != 2 {
		return 1
	}
	value, _ := strconv.Atoi(match[1])
	return value
}

func extractChunkingFailoverFragment(prompt string) (string, bool) {
	begin := strings.Index(prompt, "[FRAGMENT_BEGIN")
	if begin < 0 {
		return "", false
	}
	begin = strings.Index(prompt[begin:], "\n") + begin + 1
	end := strings.LastIndex(prompt, "\n[FRAGMENT_END")
	if begin <= 0 || end < begin {
		return "", false
	}
	return prompt[begin:end], true
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
