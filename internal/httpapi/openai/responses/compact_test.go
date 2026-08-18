package responses

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type responsesCompactionErrorDSStub struct {
	responsesHistoryDSStub
	err error
}

func (s responsesCompactionErrorDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return nil, s.err
}

func TestCompactionStateUsesSlidingIdleTTL(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	store := newResponseStore(time.Minute)
	store.compactionTTL = 10 * time.Minute
	store.now = func() time.Time { return now }

	handle := store.putCompaction("caller", []any{map[string]any{"role": "user", "content": "context"}})
	if handle == "" {
		t.Fatal("expected compaction handle")
	}
	now = now.Add(9 * time.Minute)
	if _, ok := store.getCompaction("caller", handle); !ok {
		t.Fatal("expected state before idle TTL")
	}
	now = now.Add(9 * time.Minute)
	if _, ok := store.getCompaction("caller", handle); !ok {
		t.Fatal("expected successful read to extend idle TTL")
	}
	now = now.Add(11 * time.Minute)
	if _, ok := store.getCompaction("caller", handle); ok {
		t.Fatal("expected state to expire after the renewed idle TTL")
	}
}

func TestCompactionHandleInheritsToolContractWhenOmitted(t *testing.T) {
	h := &Handler{responses: newResponseStore(time.Minute)}
	owner := "caller:compact-tools"
	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "read_workspace",
			"description": "Read a workspace file.",
			"parameters":  map[string]any{"type": "object"},
		},
	}}
	handle := h.getResponseStore().putCompactionState(owner, []any{
		map[string]any{"role": "user", "content": "inspect the repository"},
	}, tools, true, "required", true)
	if handle == "" {
		t.Fatal("expected compaction handle")
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"input": []any{
			map[string]any{"type": "compaction", "encrypted_content": handle},
			map[string]any{"role": "user", "content": "continue"},
		},
	}
	if err := h.expandLocalCompactionState(owner, req); err != nil {
		t.Fatalf("expand compact state: %v", err)
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(responsesHistoryConfigStub{}, req, "")
	if err != nil {
		t.Fatalf("normalize expanded request: %v", err)
	}
	if !stdReq.ToolChoice.IsRequired() {
		t.Fatalf("expected inherited required tool policy, got %#v", stdReq.ToolChoice)
	}
	for _, expected := range []string{"Tool: read_workspace", "Read a workspace file", "MUST call at least one tool"} {
		if !strings.Contains(stdReq.IncrementalFormatPrompt, expected) {
			t.Fatalf("missing %q from expanded tool contract: %q", expected, stdReq.IncrementalFormatPrompt)
		}
	}
}

func TestMissingCompactionStateKeepsFreshTail(t *testing.T) {
	h := &Handler{responses: newResponseStore(time.Minute)}
	req := map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": "retained old window"},
			map[string]any{"type": "compaction", "encrypted_content": localCompactionHandlePrefix + "missing"},
			map[string]any{"role": "user", "content": "fresh turn"},
		},
	}
	if err := h.expandLocalCompactionState("test-caller", req); err != nil {
		t.Fatalf("expand missing state: %v", err)
	}
	items, ok := req["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected only fresh tail, got %#v", req["input"])
	}
	item, _ := items[0].(map[string]any)
	if item["content"] != "fresh turn" {
		t.Fatalf("unexpected fresh item: %#v", item)
	}
}

func TestRecoveredCompactionFreshTailIsExplicitlyCompressed(t *testing.T) {
	h := &Handler{responses: newResponseStore(time.Minute)}
	req := map[string]any{
		"input": []any{
			map[string]any{"type": "compaction", "encrypted_content": localCompactionHandlePrefix + "expired"},
			map[string]any{"role": "user", "content": strings.Repeat("old", 300)},
			map[string]any{"role": "assistant", "content": strings.Repeat("answer", 300)},
			map[string]any{"role": "user", "content": "continue development"},
		},
	}
	recovered, err := h.expandLocalCompactionStateWithRecovery("caller", req)
	if err != nil {
		t.Fatalf("expand missing state: %v", err)
	}
	messages := promptcompat.NormalizeResponsesInputAsMessages(req["input"])
	stdReq := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-pro", Messages: messages}
	stdReq.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", promptcompat.DefaultToolChoicePolicy(), false)
	limit := config.DefaultPromptLimitSettings()
	limit.Enabled = true
	limit.MaxCharsExpert = 1000
	limit.KeepRecentTurns = 1
	dropped, compressed := h.recoverExpiredCompaction(&stdReq, recovered, limit)
	if !compressed || dropped == 0 {
		t.Fatalf("expected explicit recovery compression, compressed=%v dropped=%d", compressed, dropped)
	}
	if got := promptcompat.ExtractLatestUserText(stdReq.Messages); got != "continue development" {
		t.Fatalf("recovery compression lost latest user instruction: %q", got)
	}
	if !strings.Contains(stdReq.FinalPrompt, "Incremental response format requirements") {
		t.Fatalf("recovery prompt lost forced output format: %q", stdReq.FinalPrompt)
	}
}

func TestRecoveredCompactionRejectsWhenFormatAndLatestTurnCannotFit(t *testing.T) {
	h := &Handler{responses: newResponseStore(time.Minute)}
	recovered := recoveredLocalCompaction{Handle: localCompactionHandlePrefix + "expired"}
	messages := []any{
		map[string]any{"role": "user", "content": "latest instruction"},
	}
	stdReq := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-pro", Messages: messages}
	stdReq.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", promptcompat.DefaultToolChoicePolicy(), false)
	originalPrompt := stdReq.FinalPrompt
	limit := config.DefaultPromptLimitSettings()
	limit.Enabled = true
	limit.MaxCharsExpert = 100

	dropped, recoveredOK := h.recoverExpiredCompaction(&stdReq, recovered, limit)
	if recoveredOK || dropped != 0 {
		t.Fatalf("unfit recovery must be rejected, recovered=%v dropped=%d", recoveredOK, dropped)
	}
	if stdReq.FinalPrompt != originalPrompt {
		t.Fatal("failed recovery must leave the normalized request unchanged")
	}
}

func TestCompactReturnsTenantBoundLocalHandle(t *testing.T) {
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS: responsesHistoryDSStub{resp: makeResponsesHistorySSEHTTPResponse(
			`data: {"p":"response/content","v":"Keep the latest requirements and completed decisions."}`,
			`data: [DONE]`,
		)},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "input":[
    {"role":"user","content":"first requirement"},
    {"role":"assistant","content":"first answer"},
    {"role":"user","content":"second requirement"},
    {"role":"assistant","content":"second answer"},
    {"role":"user","content":"latest question"},
    {"role":"assistant","content":"latest answer"}
  ]
}`))
	rec := httptest.NewRecorder()
	h.Compact(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSONBody(t, rec.Body.String())
	if body["object"] != "response.compaction" {
		t.Fatalf("unexpected compact response object: %#v", body)
	}
	output, _ := body["output"].([]any)
	if len(output) < 2 {
		t.Fatalf("expected retained items plus compact state, got %#v", body)
	}
	item, _ := output[len(output)-1].(map[string]any)
	if item["type"] != "compaction" {
		t.Fatalf("unexpected compact item: %#v", item)
	}
	handle, _ := item["encrypted_content"].(string)
	if !strings.HasPrefix(handle, localCompactionHandlePrefix) {
		t.Fatalf("expected local opaque handle, got %q", handle)
	}
	if _, ok := h.getResponseStore().getCompaction("another-caller", handle); ok {
		t.Fatal("local compaction state must not cross caller boundaries")
	}
	stored, ok := h.getResponseStore().getCompaction("caller:responses", handle)
	if !ok {
		t.Fatal("expected compact state for creating caller")
	}
	original := []any{
		map[string]any{"role": "user", "content": "first requirement"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": "second requirement"},
		map[string]any{"role": "assistant", "content": "second answer"},
		map[string]any{"role": "user", "content": "latest question"},
		map[string]any{"role": "assistant", "content": "latest answer"},
	}
	if responseStateSize(stored) >= responseStateSize(original) {
		t.Fatalf("compact state did not shrink: before=%d after=%d", responseStateSize(original), responseStateSize(stored))
	}

	canonicalNext := append(cloneAnySlice(output), map[string]any{"role": "user", "content": "canonical next turn"})
	canonicalReq := map[string]any{"input": canonicalNext}
	if err := h.expandLocalCompactionState("caller:responses", canonicalReq); err != nil {
		t.Fatalf("expand canonical compact output: %v", err)
	}
	canonicalPrompt, _ := promptcompat.BuildOpenAIPrompt(
		promptcompat.NormalizeResponsesInputAsMessages(canonicalReq["input"]), nil, "", promptcompat.DefaultToolChoicePolicy(), false,
	)
	if !strings.Contains(canonicalPrompt, "latest question") || !strings.Contains(canonicalPrompt, "canonical next turn") {
		t.Fatalf("canonical compact output could not be passed as-is: %q", canonicalPrompt)
	}

	followup := map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": "first requirement"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "latest question"},
			map[string]any{"type": "compaction", "encrypted_content": handle},
			map[string]any{"role": "user", "content": "continue from the saved context"},
		},
	}
	if err := h.expandLocalCompactionState("caller:responses", followup); err != nil {
		t.Fatalf("expand local compaction state: %v", err)
	}
	messages := promptcompat.NormalizeResponsesInputAsMessages(followup["input"])
	prompt, _ := promptcompat.BuildOpenAIPrompt(messages, nil, "", promptcompat.DefaultToolChoicePolicy(), false)
	for _, expected := range []string{"latest question", "latest answer", "continue from the saved context"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expanded compact state lost %q: %q", expected, prompt)
		}
	}
	for _, dropped := range []string{"first requirement", "first answer", "second requirement", "second answer"} {
		if strings.Contains(prompt, dropped) {
			t.Fatalf("expanded compact state retained dropped history %q: %q", dropped, prompt)
		}
	}
}

func TestCompactRejectsSingleIndivisibleUserTurn(t *testing.T) {
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS:    responsesHistoryDSStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "input":[{"role":"user","content":"one large indivisible turn"}]
}`))
	rec := httptest.NewRecorder()
	h.Compact(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at least two complete user turns") {
		t.Fatalf("unexpected compact error: %s", rec.Body.String())
	}
}

func TestCompactPreservesAccountHealthError(t *testing.T) {
	healthErr := &auth.AccountHealthError{
		State:   account.HealthTemporarilyMuted,
		Code:    5,
		Message: "user is muted",
	}
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS: responsesCompactionErrorDSStub{
			responsesHistoryDSStub: responsesHistoryDSStub{},
			err:                    healthErr,
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "input":[
    {"role":"user","content":"first requirement"},
    {"role":"assistant","content":"first answer"},
    {"role":"user","content":"latest requirement"}
  ]
}`))
	rec := httptest.NewRecorder()
	h.Compact(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"account_temporarily_muted"`) {
		t.Fatalf("expected structured account health error, got %s", rec.Body.String())
	}
}

func TestResponsesCompactionTriggerEmitsOneCompactionItem(t *testing.T) {
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS: responsesHistoryDSStub{resp: makeResponsesHistorySSEHTTPResponse(
			`data: {"p":"response/content","v":"Preserve the current requirement."}`,
			`data: [DONE]`,
		)},
	}
	reqBody := `{
  "model":"deepseek-v4-flash",
  "stream":true,
  "input":[
    {"role":"user","content":"` + strings.Repeat("old context ", 200) + `"},
    {"role":"assistant","content":"` + strings.Repeat("old answer ", 200) + `"},
    {"role":"user","content":"preserve this context"},
    {"type":"compaction_trigger"}
  ]
}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	h.Responses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if count := strings.Count(body, "event: response.output_item.done\n"); count < 2 {
		t.Fatalf("expected retained items plus compaction completion, got %d body=%s", count, body)
	}
	for _, required := range []string{"\"type\":\"compaction\"", "response.completed", "data: [DONE]"} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %q from compaction stream: %s", required, body)
		}
	}
	if !strings.Contains(body, "preserve this context") {
		t.Fatalf("canonical compact window must retain the recent turn, got %s", body)
	}
}

func TestExpandLocalCompactionStateRejectsExpiredOrForeignHandle(t *testing.T) {
	h := &Handler{responses: newResponseStore(0)}
	if err := h.expandLocalCompactionState("caller:other", map[string]any{
		"input": []any{map[string]any{
			"type":              "compaction",
			"encrypted_content": localCompactionHandlePrefix + "missing",
		}},
	}); err == nil {
		t.Fatal("expected missing local compact state to be rejected")
	}
}
