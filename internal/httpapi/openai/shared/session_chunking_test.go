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

type sessionChunkingDSStub struct {
	createCount  int
	messageID    int
	mainPrompts  []string
	mainParents  []int
	plannerCalls int
	deleted      []string
	rawCalls     int
	rawPrompts   []string
	emptyControl int
}

func (s *sessionChunkingDSStub) CreateSession(context.Context, *auth.RequestAuth, int) (string, error) {
	s.createCount++
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
	if payload["chat_session_id"] == "planner-session" {
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
	s.rawCalls++
	prompt, _ := payload["prompt"].(string)
	s.rawPrompts = append(s.rawPrompts, prompt)
	if s.emptyControl > 0 && strings.Contains(prompt, "[OVERSIZED_REQUEST_CONTROL") {
		s.emptyControl--
		return chunkingEmptySSE(), nil
	}
	return s.CallCompletionPinned(ctx, a, payload, pow)
}

func (s *sessionChunkingDSStub) CallCompletionRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string, _ int) (*http.Response, error) {
	s.rawCalls++
	prompt, _ := payload["prompt"].(string)
	s.rawPrompts = append(s.rawPrompts, prompt)
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
