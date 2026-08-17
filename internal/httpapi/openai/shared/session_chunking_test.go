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
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type sessionChunkingDSStub struct {
	createCount    int
	messageID      int
	mainPrompts    []string
	mainParents    []int
	plannerCalls   int
	deleted        []string
	rawCalls       int
	rawPrompts     []string
	rawSessions    []string
	rawThinking    []bool
	emptyControl   int
	emptyFragment  int
	idOnlyFragment int
	idOnlyControl  int
	uniqueSessions bool
}

func (s *sessionChunkingDSStub) CreateSession(context.Context, *auth.RequestAuth, int) (string, error) {
	s.createCount++
	if s.uniqueSessions {
		if s.createCount%2 == 1 {
			return fmt.Sprintf("planner-session-%d", s.createCount), nil
		}
		return fmt.Sprintf("main-session-%d", s.createCount), nil
	}
	if s.createCount == 1 {
		return "planner-session", nil
	}
	return "main-session", nil
}

func (*sessionChunkingDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "pow", nil
}

func (*sessionChunkingDSStub) GetPowPinned(context.Context, *auth.RequestAuth) (string, error) {
	return "pow", nil
}

func (s *sessionChunkingDSStub) CallCompletionPinned(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string) (*http.Response, error) {
	s.messageID++
	prompt, _ := payload["prompt"].(string)
	if strings.HasPrefix(fmt.Sprint(payload["chat_session_id"]), "planner-session") {
		s.plannerCalls++
		max := plannerAllowedMax(prompt)
		return chunkingTestSSE(s.messageID, fmt.Sprintf(`{"offset_utf16":%d}`, max), false), nil
	}
	s.mainPrompts = append(s.mainPrompts, prompt)
	s.mainParents = append(s.mainParents, payloadParentForTest(payload))
	thinking, _ := payload["thinking_enabled"].(bool)
	return chunkingTestSSE(s.messageID, "started", thinking), nil
}

func (s *sessionChunkingDSStub) CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string, _ int) (*http.Response, error) {
	return s.CallCompletionPinned(ctx, a, payload, pow)
}

func (s *sessionChunkingDSStub) CallCompletionPinnedRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string) (*http.Response, error) {
	return s.callCompletionRaw(ctx, a, payload, pow)
}

func (s *sessionChunkingDSStub) CallCompletionRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string, _ int) (*http.Response, error) {
	return s.callCompletionRaw(ctx, a, payload, pow)
}

func (s *sessionChunkingDSStub) callCompletionRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string) (*http.Response, error) {
	s.rawCalls++
	prompt, _ := payload["prompt"].(string)
	s.rawPrompts = append(s.rawPrompts, prompt)
	s.rawSessions = append(s.rawSessions, fmt.Sprint(payload["chat_session_id"]))
	thinking, _ := payload["thinking_enabled"].(bool)
	s.rawThinking = append(s.rawThinking, thinking)
	if s.emptyFragment > 0 && strings.Contains(prompt, "[OVERSIZED_REQUEST_INTERMEDIATE") {
		s.emptyFragment--
		return chunkingEmptySSE(), nil
	}
	if s.idOnlyFragment > 0 && strings.Contains(prompt, "[OVERSIZED_REQUEST_INTERMEDIATE") {
		s.idOnlyFragment--
		s.messageID++
		return chunkingMessageIDOnlySSE(s.messageID), nil
	}
	if s.idOnlyControl > 0 && strings.Contains(prompt, "[OVERSIZED_REQUEST_CONTROL") {
		s.idOnlyControl--
		s.messageID++
		return chunkingMessageIDOnlySSE(s.messageID), nil
	}
	if s.emptyControl > 0 && strings.Contains(prompt, "[OVERSIZED_REQUEST_CONTROL") {
		s.emptyControl--
		return chunkingEmptySSE(), nil
	}
	return s.CallCompletionPinned(ctx, a, payload, pow)
}

func (s *sessionChunkingDSStub) DeleteSessionForToken(_ context.Context, _ string, sessionID string) (*dsclient.DeleteSessionResult, error) {
	s.deleted = append(s.deleted, sessionID)
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*sessionChunkingDSStub) DeleteAllSessionsForToken(context.Context, string) error { return nil }

func TestTryPrepareSessionChunkingPreservesOriginalAndRepeatsFormat(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })
	ds := &sessionChunkingDSStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	original := strings.Repeat("section alpha with emoji 😀 and complete sentence.\n\n", 420)
	req := promptcompat.StandardRequest{
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             original,
		IncrementalFormatPrompt: "Return one JSON object with the required schema.",
	}
	a := &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}

	prepared, err := TryPrepareSessionChunking(context.Background(), ds, a, req, cfg, "", 0)
	if err != nil {
		t.Fatalf("prepare session chunks: %v", err)
	}
	if prepared == nil || prepared.SessionID != "main-session" || prepared.ParentMessageID <= 0 || prepared.ChunkCount < 2 {
		t.Fatalf("unexpected preparation: %+v", prepared)
	}
	if ds.plannerCalls != prepared.ChunkCount-1 {
		t.Fatalf("planner calls=%d chunks=%d", ds.plannerCalls, prepared.ChunkCount)
	}
	if len(ds.deleted) != 1 || ds.deleted[0] != "planner-session" {
		t.Fatalf("planner cleanup=%#v", ds.deleted)
	}

	allWirePrompts := append(append([]string{}, ds.mainPrompts...), prepared.FinalWirePrompt)
	for index, wirePrompt := range allWirePrompts {
		if !strings.Contains(wirePrompt, "[FINAL_RESPONSE_FORMAT_REQUIREMENTS_REPEAT]") || !strings.Contains(wirePrompt, req.IncrementalFormatPrompt) {
			t.Fatalf("wire prompt %d omitted forced format requirements: %q", index, wirePrompt)
		}
	}
	fragments := make([]string, 0, prepared.ChunkCount)
	for _, wirePrompt := range allWirePrompts {
		if fragment, ok := extractChunkingTestFragment(wirePrompt); ok {
			fragments = append(fragments, fragment)
		}
	}
	if len(fragments) != prepared.ChunkCount {
		t.Fatalf("fragment prompts=%d chunks=%d all main prompts=%d", len(fragments), prepared.ChunkCount, len(ds.mainPrompts))
	}
	if reconstructed := strings.Join(fragments, ""); reconstructed != original {
		t.Fatalf("reconstructed prompt differs: got_units=%d want_units=%d", promptcompat.PromptUnits(reconstructed), promptcompat.PromptUnits(original))
	}
	if got, want := len(ds.mainPrompts), (prepared.ChunkCount-1)*3; got != want {
		t.Fatalf("intermediate upstream turns=%d want=%d", got, want)
	}
	if ds.rawCalls != len(ds.mainPrompts) {
		t.Fatalf("raw completion calls=%d intermediate turns=%d", ds.rawCalls, len(ds.mainPrompts))
	}
	for index, parent := range ds.mainParents {
		if index == 0 {
			if parent != 0 {
				t.Fatalf("first fragment parent=%d want=0", parent)
			}
			continue
		}
		if parent <= 0 {
			t.Fatalf("turn %d did not advance parent: %d", index+1, parent)
		}
	}
	if got, want := prepared.ParentMessageID, ds.messageID; got != want {
		t.Fatalf("final parent=%d latest committed message=%d", got, want)
	}
}

func TestTryPrepareSessionChunkingRetriesEmptyControlStream(t *testing.T) {
	oldTransitionDelay := sessionChunkTransitionDelay
	oldRetryDelay := sessionChunkControlRetryDelay
	sessionChunkTransitionDelay = 0
	sessionChunkControlRetryDelay = 0
	t.Cleanup(func() {
		sessionChunkTransitionDelay = oldTransitionDelay
		sessionChunkControlRetryDelay = oldRetryDelay
	})
	ds := &sessionChunkingDSStub{emptyControl: 1}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	req := promptcompat.StandardRequest{
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("safe paragraph boundary.\n\n", 500),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	prepared, err := TryPrepareSessionChunking(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg, "", 0)
	if err != nil {
		t.Fatalf("prepare chunks after empty control retry: %v", err)
	}
	if prepared == nil || ds.emptyControl != 0 || ds.rawCalls <= len(ds.mainPrompts) {
		t.Fatalf("empty control stream was not retried: prepared=%+v raw=%d committed=%d remaining_empty=%d", prepared, ds.rawCalls, len(ds.mainPrompts), ds.emptyControl)
	}
	for index, prompt := range ds.rawPrompts {
		if !strings.Contains(prompt, "[FINAL_RESPONSE_FORMAT_REQUIREMENTS_REPEAT]") {
			t.Fatalf("raw retry %d omitted format requirements: %q", index, prompt)
		}
		if strings.Contains(prompt, "[OVERSIZED_REQUEST_CONTROL") && !ds.rawThinking[index] {
			t.Fatalf("control retry %d did not require a reasoning acknowledgement: %q", index, prompt)
		}
		if strings.Contains(prompt, "type=probe") && !strings.Contains(prompt, "Checkpoint nonce:") {
			t.Fatalf("probe retry %d omitted its random checkpoint: %q", index, prompt)
		}
	}
}

func TestTryPrepareSessionChunkingVerifiesMessageIDOnlyFragmentWithCheckpoint(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{idOnlyFragment: 1}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	req := promptcompat.StandardRequest{
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("safe paragraph boundary.\n\n", 500),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	prepared, err := TryPrepareSessionChunking(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg, "", 0)
	if err != nil || prepared == nil {
		t.Fatalf("message-id-only fragment was not checkpointed: prepared=%+v err=%v", prepared, err)
	}
	if ds.idOnlyFragment != 0 {
		t.Fatalf("message-id-only fragment was not exercised: %#v", ds)
	}
	foundCheckpoint := false
	for index, prompt := range ds.rawPrompts {
		if strings.Contains(prompt, "type=probe") {
			foundCheckpoint = true
			if !ds.rawThinking[index] {
				t.Fatalf("checkpoint did not require thinking: %q", prompt)
			}
		}
	}
	if !foundCheckpoint {
		t.Fatalf("message-id-only fragment did not issue a checkpoint: %#v", ds.rawPrompts)
	}
}

func TestTryPrepareSessionChunkingAcceptsCompletedMessageIDOnlyControl(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{idOnlyControl: 1}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	req := promptcompat.StandardRequest{
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("safe paragraph boundary.\n\n", 500),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	prepared, err := TryPrepareSessionChunking(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg, "", 0)
	if err != nil || prepared == nil {
		t.Fatalf("message-id-only control was not accepted: prepared=%+v err=%v", prepared, err)
	}
	if ds.idOnlyControl != 0 {
		t.Fatalf("message-id-only control was not exercised: %#v", ds)
	}
}

func TestTryPrepareSessionChunkingDoesNotReplayIndeterminateFragment(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{emptyFragment: 1}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	req := promptcompat.StandardRequest{
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("safe paragraph boundary.\n\n", 500),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	_, err := TryPrepareSessionChunking(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg, "", 0)
	if err == nil || !IsRetryableSessionChunkingFailure(err) {
		t.Fatalf("expected retryable indeterminate fragment error, got %v", err)
	}
	fragmentCalls := 0
	for _, prompt := range ds.rawPrompts {
		if strings.Contains(prompt, "[OVERSIZED_REQUEST_INTERMEDIATE") {
			fragmentCalls++
		}
	}
	if fragmentCalls != 1 {
		t.Fatalf("indeterminate fragment must not be replayed in its original session, calls=%d", fragmentCalls)
	}
	if !containsChunkingTestString(ds.deleted, "main-session") {
		t.Fatalf("failed root session was not discarded: %#v", ds.deleted)
	}
}

func TestTryPrepareSessionChunkingKeepsExistingBranchAfterIndeterminateFragment(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{emptyFragment: 1}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	req := promptcompat.StandardRequest{
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("safe paragraph boundary.\n\n", 500),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	_, err := TryPrepareSessionChunking(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg, "existing-session", 77)
	if err == nil || !IsRetryableSessionChunkingFailure(err) {
		t.Fatalf("expected retryable indeterminate fragment error, got %v", err)
	}
	if containsChunkingTestString(ds.deleted, "existing-session") {
		t.Fatalf("incremental branch must not be deleted or replayed in place: %#v", ds.deleted)
	}
	if len(ds.rawSessions) != 1 || ds.rawSessions[0] != "existing-session" {
		t.Fatalf("unexpected existing branch commits: %#v", ds.rawSessions)
	}
}

func TestSessionChunkIndeterminateDeadlineCanRebuildButCallerCancelCannot(t *testing.T) {
	timeoutErr := newSessionChunkUncommittedError("session", 7, "fragment", "timed out waiting for fragment commit", 0, false, context.DeadlineExceeded, time.Millisecond)
	if !IsRetryableSessionChunkingFailure(timeoutErr) {
		t.Fatalf("local fragment deadline must allow a fresh-root rebuild: %v", timeoutErr)
	}
	cancelledErr := newSessionChunkUncommittedError("session", 7, "fragment", "caller cancelled", 0, false, context.Canceled, time.Millisecond)
	if IsRetryableSessionChunkingFailure(cancelledErr) {
		t.Fatalf("caller cancellation must not rebuild a fresh root: %v", cancelledErr)
	}
}

func TestTryPrepareRootSessionChunkingRebuildsAfterIndeterminateFragment(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{emptyFragment: 1, uniqueSessions: true}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	original := strings.Repeat("safe paragraph boundary.\n\n", 500)
	req := promptcompat.StandardRequest{
		Surface:                 "test",
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             original,
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	prepared, err := TryPrepareRootSessionChunkingWithFailover(context.Background(), ds, nil, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg)
	if err != nil || prepared == nil {
		t.Fatalf("expected rebuilt root after indeterminate fragment, prepared=%#v err=%v", prepared, err)
	}
	if prepared.SessionID != "main-session-4" {
		t.Fatalf("expected a new root session, got %q", prepared.SessionID)
	}
	if !containsChunkingTestString(ds.deleted, "main-session-2") {
		t.Fatalf("indeterminate root was not deleted: %#v", ds.deleted)
	}
	for _, prompt := range append(append([]string{}, ds.mainPrompts...), prepared.FinalWirePrompt) {
		if !strings.Contains(prompt, "[FINAL_RESPONSE_FORMAT_REQUIREMENTS_REPEAT]") {
			t.Fatalf("replayed root omitted forced format requirements: %q", prompt)
		}
	}
}

func TestTryPrepareRootSessionChunkingBoundsIndeterminateReplay(t *testing.T) {
	oldDelay := sessionChunkTransitionDelay
	sessionChunkTransitionDelay = 0
	t.Cleanup(func() { sessionChunkTransitionDelay = oldDelay })

	ds := &sessionChunkingDSStub{emptyFragment: 2, uniqueSessions: true}
	cfg := config.DefaultPromptLimitSettings()
	cfg.SessionChunkingEnable = true
	cfg.MaxCharsExpert = 10000
	cfg.SessionChunkingTargetRatio = 0.9
	cfg.SessionChunkingMaxChunks = 16
	cfg.SessionChunkingCommitTimeoutSeconds = 5
	req := promptcompat.StandardRequest{
		Surface:                 "test",
		ResolvedModel:           "deepseek-v4-pro",
		FinalPrompt:             strings.Repeat("safe paragraph boundary.\n\n", 500),
		IncrementalFormatPrompt: "Return exactly one JSON object.",
	}
	prepared, err := TryPrepareRootSessionChunkingWithFailover(context.Background(), ds, nil, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg)
	if prepared != nil || err == nil || !IsRetryableSessionChunkingFailure(err) {
		t.Fatalf("expected bounded indeterminate failure, prepared=%#v err=%v", prepared, err)
	}
	if ds.createCount != 4 {
		t.Fatalf("expected exactly two root branches (planner + main each), create_count=%d", ds.createCount)
	}
	if !containsChunkingTestString(ds.deleted, "main-session-2") || !containsChunkingTestString(ds.deleted, "main-session-4") {
		t.Fatalf("indeterminate roots were not discarded: %#v", ds.deleted)
	}
}

func payloadParentForTest(payload map[string]any) int {
	switch value := payload["parent_message_id"].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func chunkingEmptySSE() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}
}

func chunkingMessageIDOnlySSE(messageID int) *http.Response {
	body := fmt.Sprintf("data: {\"response_message_id\":%d}\ndata: [DONE]\n", messageID)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestTryPrepareSessionChunkingIsDisabledByDefault(t *testing.T) {
	ds := &sessionChunkingDSStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 100
	prepared, err := TryPrepareSessionChunking(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, promptcompat.StandardRequest{
		ResolvedModel: "deepseek-v4-pro",
		FinalPrompt:   strings.Repeat("x", 1000),
	}, cfg, "", 0)
	if err != nil || prepared != nil || ds.createCount != 0 {
		t.Fatalf("default-disabled result: prepared=%+v err=%v create_count=%d", prepared, err, ds.createCount)
	}
}

func plannerAllowedMax(prompt string) int {
	re := regexp.MustCompile(`ALLOWED_MAX_UTF16=(\d+)`)
	match := re.FindStringSubmatch(prompt)
	if len(match) != 2 {
		return 1
	}
	value, _ := strconv.Atoi(match[1])
	return value
}

func chunkingTestSSE(messageID int, text string, thinking bool) *http.Response {
	path := "response/content"
	if thinking {
		path = "response/thinking_content"
	}
	body := fmt.Sprintf("data: {\"response_message_id\":%d}\ndata: {\"p\":%q,\"v\":%q}\ndata: [DONE]\n", messageID, path, text)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func extractChunkingTestFragment(prompt string) (string, bool) {
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

func containsChunkingTestString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
