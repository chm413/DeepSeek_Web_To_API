package responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

type responsesAutoCompressConfigStub struct{ responsesHistoryConfigStub }

func (responsesAutoCompressConfigStub) RemoteFileUploadEnabled() bool { return false }

func (responsesAutoCompressConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.Enabled = true
	cfg.AutoCompressEnable = true
	cfg.KeepRecentTurns = 6
	cfg.KeepSystemMessage = true
	return cfg
}

type responsesRejectOverflowConfigStub struct{ responsesHistoryConfigStub }

func (responsesRejectOverflowConfigStub) RemoteFileUploadEnabled() bool { return false }

func (responsesRejectOverflowConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.Enabled = true
	cfg.AutoCompressEnable = false
	cfg.ProFlashCompressionEnable = false
	cfg.SessionChunkingEnable = false
	return cfg
}

type responsesPromptLimitDSStub struct {
	responsesIncrementalDSStub
	limitCalls int
}

type responsesPinned429LimitConfigStub struct{ responsesHistoryConfigStub }

func (responsesPinned429LimitConfigStub) RemoteFileUploadEnabled() bool { return false }

func (responsesPinned429LimitConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.Enabled = true
	cfg.AutoCompressEnable = false
	cfg.ProFlashCompressionEnable = false
	cfg.SessionChunkingEnable = false
	return cfg
}

type responsesPinned429LimitDSStub struct{ responsesPinned429DSStub }

func (*responsesPinned429LimitDSStub) GetModelInputLimits(_ context.Context, a *auth.RequestAuth) (config.ModelInputLimits, error) {
	if a.AccountID == "account-retry" {
		return config.ModelInputLimits{Default: 1000, Expert: 1000}, nil
	}
	return config.ModelInputLimits{Default: 10000, Expert: 10000}, nil
}

type responsesSummaryConfigStub struct{ responsesHistoryConfigStub }

func (responsesSummaryConfigStub) PromptLimitSnapshot() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsDefault = 8000
	cfg.MaxCharsDefaultConfigured = true
	cfg.SummaryCompactionEnable = true
	cfg.SummaryCompactionThreshold = 0.5
	cfg.KeepRecentTurns = 1
	return cfg
}

type responsesSummaryDSStub struct {
	responsesIncrementalDSStub
}

func (s *responsesSummaryDSStub) GetModelInputLimits(context.Context, *auth.RequestAuth) (config.ModelInputLimits, error) {
	return config.ModelInputLimits{Default: 2621440, Expert: 163840}, nil
}

func (s *responsesSummaryDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.normal = append(s.normal, cloneResponsesPayload(payload))
	prompt, _ := payload["prompt"].(string)
	if strings.Contains(prompt, "CONVERSATION TO COMPACT") {
		return responsesIncrementalSSE(301, "Preserve the project requirements and completed decisions."), nil
	}
	return responsesIncrementalSSE(401, "main response"), nil
}

func (s *responsesPromptLimitDSStub) GetModelInputLimits(context.Context, *auth.RequestAuth) (config.ModelInputLimits, error) {
	s.limitCalls++
	return config.ModelInputLimits{Default: 2621440, Expert: 163840}, nil
}

func TestResponsesExpertOverflowAutoCompressesBeforeUpstream(t *testing.T) {
	input := []any{map[string]any{"role": "system", "content": "retain system instructions"}}
	for turn := 0; turn < 30; turn++ {
		userText := strings.Repeat("u", 4000)
		assistantText := strings.Repeat("a", 4000)
		if turn == 0 {
			userText = "oldest-marker-" + userText
		}
		if turn == 29 {
			userText = "newest-marker-" + userText
		}
		input = append(input,
			map[string]any{"role": "user", "content": userText},
			map[string]any{"role": "assistant", "content": assistantText},
		)
	}

	ds := &responsesPromptLimitDSStub{}
	h := &Handler{
		Store: responsesAutoCompressConfigStub{},
		Auth:  responsesIncrementalAuthStub{},
		DS:    ds,
	}
	serveResponsesIncremental(t, h, map[string]any{
		"model":  "deepseek-v4-pro",
		"input":  input,
		"stream": false,
	})

	if ds.limitCalls == 0 {
		t.Fatal("dynamic upstream prompt limit was not queried")
	}
	if len(ds.normal) != 1 {
		t.Fatalf("expected one upstream completion, got %d", len(ds.normal))
	}
	prompt, _ := ds.normal[0]["prompt"].(string)
	if units := promptcompat.PromptUnits(prompt); units > 163840 {
		t.Fatalf("compressed Responses prompt still exceeds provider limit: %d", units)
	}
	if strings.Contains(prompt, "oldest-marker") {
		t.Fatal("oldest history survived automatic compression")
	}
	if !strings.Contains(prompt, "newest-marker") || !strings.Contains(prompt, "retain system instructions") {
		t.Fatal("automatic compression dropped required recent or system content")
	}
}

// Regression for the historical Codex Responses failure: a ~215k UTF-16 Pro
// prompt must fail locally before an upstream session or SSE response exists.
func TestResponsesStreamExpertOverflowReturns413BeforeUpstream(t *testing.T) {
	ds := &responsesPromptLimitDSStub{}
	h := &Handler{
		Store: responsesRejectOverflowConfigStub{},
		Auth:  responsesIncrementalAuthStub{},
		DS:    ds,
	}
	body, _ := json.Marshal(map[string]any{
		"model":  "deepseek-v4-pro",
		"input":  strings.Repeat("x", 215000),
		"stream": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected local 413 for oversized stream, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "event: response.") {
		t.Fatalf("oversized request must not start an SSE response: %s", rec.Body.String())
	}
	if ds.createCalls != 0 || len(ds.normal) != 0 {
		t.Fatalf("oversized request reached upstream: create=%d completion=%d", ds.createCalls, len(ds.normal))
	}
	out := decodeJSONBody(t, rec.Body.String())
	errObj, _ := out["error"].(map[string]any)
	if !strings.Contains(asString(errObj["message"]), "163840") {
		t.Fatalf("expected provider limit in overflow error, got %#v", out)
	}
}

func TestResponsesPinned429FallbackRechecksReplacementAccountInputLimit(t *testing.T) {
	authStub := &responsesSwitchingIncrementalAuthStub{}
	ds := &responsesPinned429LimitDSStub{}
	h := &Handler{
		Store:       responsesPinned429LimitConfigStub{},
		Auth:        authStub,
		DS:          ds,
		Incremental: upstreamsession.NewStore(0, 0),
	}
	firstText := strings.Repeat("retained context ", 300)
	first := serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": firstText,
	})
	firstOutput, _ := first["output"].([]any)
	if len(firstOutput) == 0 {
		t.Fatalf("missing first response output: %#v", first)
	}
	secondInput := []any{map[string]any{"role": "user", "content": firstText}}
	secondInput = append(secondInput, firstOutput...)
	secondInput = append(secondInput, map[string]any{"role": "user", "content": "small new request"})
	body, _ := json.Marshal(map[string]any{"model": "deepseek-v4-flash", "input": secondInput})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("replacement account limit must reject the full replay, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if authStub.switches != 1 {
		t.Fatalf("expected a switch after pinned 429, got %d", authStub.switches)
	}
	if len(ds.normalAccounts) != 1 {
		t.Fatalf("oversized full replay was sent upstream: accounts=%#v", ds.normalAccounts)
	}
}

func TestResponsesServerThresholdRunsSummaryBeforeMainCompletion(t *testing.T) {
	ds := &responsesSummaryDSStub{}
	h := &Handler{
		Store: responsesSummaryConfigStub{},
		Auth:  responsesIncrementalAuthStub{},
		DS:    ds,
	}
	response := serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"input": []any{
			map[string]any{"role": "user", "content": "old-marker " + strings.Repeat("old requirement ", 160)},
			map[string]any{"role": "assistant", "content": strings.Repeat("old answer ", 160)},
			map[string]any{"role": "user", "content": "latest-marker continue implementation"},
		},
	})
	if len(ds.normal) != 2 {
		t.Fatalf("expected summary plus main completion, got %d", len(ds.normal))
	}
	summaryPrompt, _ := ds.normal[0]["prompt"].(string)
	mainPrompt, _ := ds.normal[1]["prompt"].(string)
	if !strings.Contains(summaryPrompt, "CONVERSATION TO COMPACT") || !strings.Contains(summaryPrompt, "old-marker") {
		t.Fatalf("unexpected summary request: %q", summaryPrompt)
	}
	if strings.Contains(mainPrompt, "old-marker") || !strings.Contains(mainPrompt, "latest-marker") || !strings.Contains(mainPrompt, "ds2api_summary_v1") {
		t.Fatalf("main request did not use compacted context: %q", mainPrompt)
	}
	assertCompactionPrecedesAssistant(t, response)
}

func TestResponsesRequestTokenThresholdRunsSummaryAndEmitsCompaction(t *testing.T) {
	ds := &responsesSummaryDSStub{}
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesIncrementalAuthStub{},
		DS:    ds,
	}
	response := serveResponsesIncremental(t, h, map[string]any{
		"model": "deepseek-v4-flash",
		"context_management": []any{
			map[string]any{"type": "compaction", "compact_threshold": 100},
		},
		"input": []any{
			map[string]any{"role": "user", "content": "old-marker " + strings.Repeat("old requirement ", 160)},
			map[string]any{"role": "assistant", "content": strings.Repeat("old answer ", 160)},
			map[string]any{"role": "user", "content": "latest-marker continue implementation"},
		},
	})
	if len(ds.normal) != 2 {
		t.Fatalf("expected summary plus main completion, got %d", len(ds.normal))
	}
	assertCompactionPrecedesAssistant(t, response)
}

func TestResponsesRequestTokenThresholdStreamEmitsCompactionBeforeAssistant(t *testing.T) {
	ds := &responsesSummaryDSStub{}
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesIncrementalAuthStub{},
		DS:    ds,
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "stream":true,
  "context_management":[{"type":"compaction","compact_threshold":100}],
  "input":[
    {"role":"user","content":"`+strings.Repeat("old requirement ", 160)+`"},
    {"role":"assistant","content":"`+strings.Repeat("old answer ", 160)+`"},
    {"role":"user","content":"latest-marker continue implementation"}
  ]
}`))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.Responses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	compactionAt := strings.Index(body, `"type":"compaction"`)
	assistantAt := strings.Index(body, `"role":"assistant"`)
	if compactionAt < 0 || assistantAt < 0 || compactionAt >= assistantAt {
		t.Fatalf("compaction item did not precede assistant output: %s", body)
	}
	if !strings.Contains(body, `"output_index":0`) || !strings.Contains(body, `"output_index":1`) {
		t.Fatalf("stream output indices did not reserve index zero for compaction: %s", body)
	}
}

func assertCompactionPrecedesAssistant(t *testing.T, response map[string]any) {
	t.Helper()
	output, _ := response["output"].([]any)
	if len(output) < 2 {
		t.Fatalf("expected compaction and assistant output, got %#v", response)
	}
	first, _ := output[0].(map[string]any)
	second, _ := output[1].(map[string]any)
	if first["type"] != "compaction" || second["role"] != "assistant" {
		t.Fatalf("unexpected output order: %#v", output)
	}
}
