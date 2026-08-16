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

type summaryCompactionDSStub struct {
	prompts  []string
	deleted  []string
	response func() *http.Response
}

type summaryPinnedCompactionDSStub struct {
	summaryCompactionDSStub
	normalPowCalls        int
	pinnedPowCalls        int
	normalCompletionCalls int
	pinnedCompletionCalls int
}

func (s *summaryCompactionDSStub) CreateSession(context.Context, *auth.RequestAuth, int) (string, error) {
	return "summary-session", nil
}

func (*summaryCompactionDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	return "pow", nil
}

func (s *summaryPinnedCompactionDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	s.normalPowCalls++
	return "unsafe-normal-pow", nil
}

func (s *summaryPinnedCompactionDSStub) GetPowPinned(context.Context, *auth.RequestAuth) (string, error) {
	s.pinnedPowCalls++
	return "pinned-pow", nil
}

func (s *summaryCompactionDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.prompts = append(s.prompts, strings.TrimSpace(payload["prompt"].(string)))
	if s.response != nil {
		return s.response(), nil
	}
	return summaryCompactionSSE("Preserve active requirements, exact identifiers, decisions, and unresolved work."), nil
}

func (s *summaryPinnedCompactionDSStub) CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string, attempts int) (*http.Response, error) {
	s.normalCompletionCalls++
	return s.summaryCompactionDSStub.CallCompletion(ctx, a, payload, pow, attempts)
}

func (s *summaryPinnedCompactionDSStub) CallCompletionRootPinned(ctx context.Context, a *auth.RequestAuth, payload map[string]any, pow string) (*http.Response, error) {
	s.pinnedCompletionCalls++
	return s.summaryCompactionDSStub.CallCompletion(ctx, a, payload, pow, 1)
}

func (s *summaryCompactionDSStub) DeleteSessionForToken(_ context.Context, _ string, sessionID string) (*dsclient.DeleteSessionResult, error) {
	s.deleted = append(s.deleted, sessionID)
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*summaryCompactionDSStub) DeleteAllSessionsForToken(context.Context, string) error { return nil }

func TestTrySummaryCompactPromptReplacesRollingSummary(t *testing.T) {
	ds := &summaryCompactionDSStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.KeepRecentTurns = 1
	messages := []any{
		map[string]any{"role": "system", "content": "permanent system instruction"},
		map[string]any{"role": "user", "content": "old-marker " + strings.Repeat("old requirement ", 200)},
		map[string]any{"role": "assistant", "content": strings.Repeat("old answer ", 200)},
		map[string]any{"role": "user", "content": "latest-marker continue the implementation"},
		map[string]any{"role": "assistant", "content": "latest completed result"},
	}
	req := promptcompat.StandardRequest{
		RequestedModel: "deepseek-v4-flash",
		ResolvedModel:  "deepseek-v4-flash",
		ResponseModel:  "deepseek-v4-flash",
		Messages:       messages,
		ToolChoice:     promptcompat.DefaultToolChoicePolicy(),
	}
	req.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", req.ToolChoice, false)
	a := &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}

	first, firstStats, ok, err := TrySummaryCompactPrompt(context.Background(), ds, a, req, cfg, 6000)
	if err != nil || !ok {
		t.Fatalf("first summary compact: ok=%v err=%v", ok, err)
	}
	if firstStats.AfterStateBytes >= firstStats.BeforeStateBytes || firstStats.AfterPromptUnits >= firstStats.BeforePromptUnits {
		t.Fatalf("first compact did not shrink: %+v", firstStats)
	}
	firstPrompt := first.FinalPrompt
	if strings.Contains(firstPrompt, "old-marker") || !strings.Contains(firstPrompt, "latest-marker") || !strings.Contains(firstPrompt, summaryCompactionMarker) {
		t.Fatalf("unexpected first compact prompt: %s", firstPrompt)
	}

	secondMessages := append(cloneSummaryMessages(first.Messages),
		map[string]any{"role": "user", "content": "new-marker " + strings.Repeat("new work ", 120)},
		map[string]any{"role": "assistant", "content": "new completed result"},
	)
	first.Messages = secondMessages
	first.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(secondMessages, nil, "", first.ToolChoice, false)
	second, _, ok, err := TrySummaryCompactPrompt(context.Background(), ds, a, first, cfg, 6000)
	if err != nil || !ok {
		t.Fatalf("second summary compact: ok=%v err=%v", ok, err)
	}
	generated := 0
	for _, item := range second.Messages {
		if isGeneratedSummary(item) {
			generated++
		}
	}
	if generated != 1 {
		t.Fatalf("rolling compact nested summaries: generated=%d messages=%#v", generated, second.Messages)
	}
	if len(ds.prompts) != 2 || !strings.Contains(ds.prompts[1], summaryCompactionMarker) {
		t.Fatalf("second compact did not merge the prior summary: %#v", ds.prompts)
	}
	if len(ds.deleted) != 2 {
		t.Fatalf("summary sessions were not cleaned up: %#v", ds.deleted)
	}
}

func TestTrySummaryCompactPromptRejectsSingleTurnWithoutCallingUpstream(t *testing.T) {
	ds := &summaryCompactionDSStub{}
	messages := []any{map[string]any{"role": "user", "content": strings.Repeat("large", 1000)}}
	req := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-flash", Messages: messages, ToolChoice: promptcompat.DefaultToolChoicePolicy()}
	req.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", req.ToolChoice, false)
	_, _, ok, err := TrySummaryCompactPrompt(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, req, config.DefaultPromptLimitSettings(), 4000)
	if ok || err == nil {
		t.Fatalf("single turn must not compact: ok=%v err=%v", ok, err)
	}
	if len(ds.prompts) != 0 {
		t.Fatalf("single turn unexpectedly called upstream: %d", len(ds.prompts))
	}
}

func TestTrySummaryCompactPromptUsesHiddenOutputWhenVisibleOutputIsEmpty(t *testing.T) {
	ds := &summaryCompactionDSStub{response: func() *http.Response {
		body := `data: {"p":"response/thinking_content","v":"Preserve hidden summary requirements."}` + "\n" + "data: [DONE]\n"
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	}}
	messages := []any{
		map[string]any{"role": "user", "content": strings.Repeat("old requirement ", 100)},
		map[string]any{"role": "assistant", "content": strings.Repeat("old result ", 100)},
		map[string]any{"role": "user", "content": "latest requirement"},
	}
	req := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-flash", ResponseModel: "deepseek-v4-flash", Messages: messages, ToolChoice: promptcompat.DefaultToolChoicePolicy()}
	req.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", req.ToolChoice, false)
	compacted, stats, ok, err := TrySummaryCompactPrompt(context.Background(), ds, &auth.RequestAuth{DeepSeekToken: "token"}, req, config.DefaultPromptLimitSettings(), 4000)
	if err != nil || !ok {
		t.Fatalf("hidden-output summary compact: ok=%v err=%v", ok, err)
	}
	if !stats.UsedThinkingFallback || !strings.Contains(compacted.FinalPrompt, "Preserve hidden summary requirements") {
		t.Fatalf("hidden summary was not used: stats=%+v prompt=%q", stats, compacted.FinalPrompt)
	}
}

func TestTrySummaryCompactPromptPinsRootSession(t *testing.T) {
	ds := &summaryPinnedCompactionDSStub{}
	messages := []any{
		map[string]any{"role": "user", "content": "old requirement " + strings.Repeat("history ", 160)},
		map[string]any{"role": "assistant", "content": strings.Repeat("old result ", 160)},
		map[string]any{"role": "user", "content": "latest requirement"},
	}
	req := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-flash", ResponseModel: "deepseek-v4-flash", Messages: messages, ToolChoice: promptcompat.DefaultToolChoicePolicy()}
	req.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", req.ToolChoice, false)
	_, _, ok, err := TrySummaryCompactPrompt(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, config.DefaultPromptLimitSettings(), 4000)
	if err != nil || !ok {
		t.Fatalf("summary compaction = (%v, %v)", ok, err)
	}
	if ds.normalPowCalls != 0 || ds.normalCompletionCalls != 0 || ds.pinnedPowCalls != 1 || ds.pinnedCompletionCalls != 1 {
		t.Fatalf("expected pinned root path only: %+v", ds)
	}
}

func TestSplitSummaryCompactionWindowKeepsOnlySummaryBaseOpaque(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "permanent"},
		map[string]any{"role": "system", "content": "Compacted conversation summary [" + summaryCompactionMarker + "]: old", "ds2api_compaction_summary": summaryCompactionMarker},
		map[string]any{"role": "user", "content": "recent"},
		map[string]any{"role": "assistant", "content": "answer"},
	}
	base, retained, ok := SplitSummaryCompactionWindow(messages)
	if !ok || len(base) != 2 || len(retained) != 2 {
		t.Fatalf("split=(%#v,%#v,%v)", base, retained, ok)
	}
	if summaryMessageRole(retained[0]) != "user" {
		t.Fatalf("retained window does not start at recent user turn: %#v", retained)
	}
}

func summaryCompactionSSE(text string) *http.Response {
	body := `data: {"p":"response/content","v":"` + text + `"}` + "\n" + "data: [DONE]\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
