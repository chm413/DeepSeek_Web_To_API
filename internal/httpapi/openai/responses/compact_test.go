package responses

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/promptcompat"
)

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

func TestCompactReturnsTenantBoundLocalHandle(t *testing.T) {
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS:    responsesHistoryDSStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "input":[
    {"role":"user","content":"first requirement"},
    {"role":"assistant","content":"first answer"},
    {"role":"user","content":"latest question"}
  ]
}`))
	rec := httptest.NewRecorder()
	h.Compact(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSONBody(t, rec.Body.String())
	output, _ := body["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one compact output item, got %#v", body)
	}
	item, _ := output[0].(map[string]any)
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
	for _, expected := range []string{"first requirement", "first answer", "latest question", "continue from the saved context"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expanded compact state lost %q: %q", expected, prompt)
		}
	}
	if count := strings.Count(prompt, "first requirement"); count != 1 {
		t.Fatalf("retained messages must be replaced rather than duplicated, count=%d prompt=%q", count, prompt)
	}
}

func TestResponsesCompactionTriggerEmitsOneCompactionItem(t *testing.T) {
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth:  responsesHistoryAuthStub{},
		DS:    responsesHistoryDSStub{},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
  "model":"deepseek-v4-flash",
  "stream":true,
  "input":[
    {"role":"user","content":"preserve this context"},
    {"type":"compaction_trigger"}
  ]
}`))
	rec := httptest.NewRecorder()
	h.Responses(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if count := strings.Count(body, "event: response.output_item.done\n"); count != 1 {
		t.Fatalf("expected exactly one output-item completion, got %d body=%s", count, body)
	}
	for _, required := range []string{"\"type\":\"compaction\"", "response.completed", "data: [DONE]"} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %q from compaction stream: %s", required, body)
		}
	}
	if strings.Contains(body, "preserve this context") {
		t.Fatalf("compaction stream must return only opaque state, got %s", body)
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
