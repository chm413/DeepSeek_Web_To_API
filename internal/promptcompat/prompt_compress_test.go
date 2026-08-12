package promptcompat

import (
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func userMsg(text string) any {
	return map[string]any{"role": "user", "content": text}
}

func assistantMsg(text string) any {
	return map[string]any{"role": "assistant", "content": text}
}

func systemMsg(text string) any {
	return map[string]any{"role": "system", "content": text}
}

func toolResultMsg(text string) any {
	return map[string]any{"role": "tool", "content": text, "tool_call_id": "call_x"}
}

// messageContent reads a message's content as a string. Maps are not comparable
// with ==, so identity assertions have to go through the content instead.
func messageContent(item any) string {
	msg, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	return asString(msg["content"])
}

// buildConversation returns 1 system + n user/assistant pairs, each turn padded
// to padChars so the caller can drive the prompt over a chosen ceiling.
func buildConversation(pairs, padChars int) []any {
	out := []any{systemMsg("you are a helpful assistant")}
	for i := 0; i < pairs; i++ {
		out = append(out, userMsg(strings.Repeat("u", padChars)))
		out = append(out, assistantMsg(strings.Repeat("a", padChars)))
	}
	return out
}

func testCfg() config.PromptLimitSettings {
	cfg := config.DefaultPromptLimitSettings()
	cfg.AutoCompressEnable = true
	cfg.MaxCharsDefault = 4000
	cfg.MaxCharsExpert = 1000
	cfg.KeepRecentTurns = 4
	return cfg
}

// TestExpertTierGetsTighterLimit pins the reported symptom: the same prompt is
// accepted on the flash/default tier and rejected on expert/Pro, because the
// expert ceiling is lower. If GetModelType stops reporting "expert" for the Pro
// ids, this catches it.
func TestExpertTierGetsTighterLimit(t *testing.T) {
	t.Parallel()

	cfg := testCfg()
	if got := LimitForModel(cfg, "deepseek-v4-flash"); got != cfg.MaxCharsDefault {
		t.Fatalf("flash limit = %d, want %d", got, cfg.MaxCharsDefault)
	}
	if got := LimitForModel(cfg, "deepseek-v4-pro"); got != cfg.MaxCharsExpert {
		t.Fatalf("pro limit = %d, want %d", got, cfg.MaxCharsExpert)
	}
	if !IsExpertModel("deepseek-v4-pro-search") {
		t.Fatal("deepseek-v4-pro-search must classify as expert")
	}
	if IsExpertModel("deepseek-v4-flash") {
		t.Fatal("deepseek-v4-flash must not classify as expert")
	}
}

func TestLimitDisabledYieldsNoCeiling(t *testing.T) {
	t.Parallel()

	cfg := testCfg()
	cfg.Enabled = false
	if got := LimitForModel(cfg, "deepseek-v4-pro"); got != 0 {
		t.Fatalf("limit with Enabled=false = %d, want 0", got)
	}
	huge := strings.Repeat("x", 500000)
	if msg := LimitExceededMessage(cfg, huge, "deepseek-v4-pro"); msg != "" {
		t.Fatalf("disabled limits must never reject, got %q", msg)
	}
}

func TestCompressMessagesKeepsSystemAndRecentTail(t *testing.T) {
	t.Parallel()

	messages := buildConversation(10, 4) // 1 system + 20 turn messages
	out, ok := CompressMessages(messages, true, 3)
	if !ok {
		t.Fatal("expected compression to report a change")
	}
	// keepRecent=3 → window of 6 messages, plus the system message.
	if len(out) != 7 {
		t.Fatalf("len(out) = %d, want 7", len(out))
	}
	if messageRole(out[0]) != "system" {
		t.Fatalf("out[0] role = %q, want system", messageRole(out[0]))
	}
	// The tail must be the LAST messages of the original, not the first.
	// Compare content rather than the map values themselves — map[string]any is
	// not comparable with ==, so identity checks panic at runtime.
	if got, want := messageContent(out[len(out)-1]), messageContent(messages[len(messages)-1]); got != want {
		t.Fatalf("last kept message = %q, want the original's last message %q", got, want)
	}
}

func TestCompressMessagesNoOpWhenAlreadyShort(t *testing.T) {
	t.Parallel()

	messages := buildConversation(2, 4) // 1 system + 4 turn messages
	out, ok := CompressMessages(messages, true, 4)
	if ok {
		t.Fatal("short conversation must not report compression")
	}
	if len(out) != len(messages) {
		t.Fatalf("len(out) = %d, want %d unchanged", len(out), len(messages))
	}
}

func TestCompactMessagesShrinksConversationInsideConfiguredWindow(t *testing.T) {
	t.Parallel()

	messages := buildConversation(6, 8)
	out, ok := CompactMessages(messages, true, 25)
	if !ok {
		t.Fatal("explicit compaction must shrink a multi-turn conversation")
	}
	if len(out) >= len(messages) {
		t.Fatalf("explicit compact did not shrink: before=%d after=%d", len(messages), len(out))
	}
	if messageRole(out[0]) != "system" {
		t.Fatal("explicit compact lost the leading system message")
	}
	if got, want := messageContent(out[len(out)-1]), messageContent(messages[len(messages)-1]); got != want {
		t.Fatalf("explicit compact lost latest response: got=%q want=%q", got, want)
	}
}

func TestCompactMessagesRejectsSingleIndivisibleTurn(t *testing.T) {
	t.Parallel()

	messages := []any{
		systemMsg("system"),
		userMsg("current request"),
		assistantMsg("tool call"),
		toolResultMsg("tool result"),
		assistantMsg("final response"),
	}
	out, ok := CompactMessages(messages, true, 25)
	if ok {
		t.Fatal("single user turn must not be partially compacted")
	}
	if len(out) != len(messages) {
		t.Fatalf("single user turn changed: before=%d after=%d", len(messages), len(out))
	}
}

func TestCompressMessagesDropsSystemWhenNotKept(t *testing.T) {
	t.Parallel()

	messages := buildConversation(10, 4)
	out, ok := CompressMessages(messages, false, 2)
	if !ok {
		t.Fatal("expected compression")
	}
	for i, item := range out {
		if messageRole(item) == "system" {
			t.Fatalf("out[%d] is a system message but keepSystem=false", i)
		}
	}
}

// TestCompressMessagesDropsOrphanedToolResult pins the tool-pairing invariant:
// slicing history at a fixed offset can land on a `tool` result whose
// originating assistant tool_calls fell outside the window. Emitting that
// orphan produces a malformed exchange, so the leading run must be dropped.
func TestCompressMessagesKeepsCurrentUserToolChain(t *testing.T) {
	t.Parallel()

	messages := []any{
		systemMsg("sys"),
		userMsg("old turn"),
		assistantMsg("old answer"),
		userMsg("continue development"),
		assistantMsg("calling a tool"),
		toolResultMsg("tool output"),
		assistantMsg("final answer"),
	}
	out, ok := CompressMessages(messages, true, 1)
	if !ok {
		t.Fatal("expected compression")
	}
	if got := ExtractLatestUserText(out); got != "continue development" {
		t.Fatalf("latest user instruction was lost: got %q", got)
	}
	if len(out) != 5 {
		t.Fatalf("current user turn was truncated: %#v", out)
	}
}

func TestCompressMessagesKeepsLongCurrentToolChain(t *testing.T) {
	t.Parallel()

	messages := []any{
		systemMsg("sys"),
		userMsg("old"),
		assistantMsg("old answer"),
		userMsg("continue development"),
	}
	for i := 0; i < 40; i++ {
		messages = append(messages, assistantMsg("tool call"), toolResultMsg("large tool output"))
	}
	messages = append(messages, assistantMsg("working"))

	out, ok := CompressMessages(messages, true, 1)
	if !ok {
		t.Fatal("expected compression")
	}
	if got := ExtractLatestUserText(out); got != "continue development" {
		t.Fatalf("latest user instruction was lost: got %q", got)
	}
	if len(out) != len(messages)-2 {
		t.Fatalf("current turn tool chain was truncated: len(out)=%d len(messages)=%d", len(out), len(messages))
	}
}

func TestCompressMessagesKeepsAllSystemMessages(t *testing.T) {
	t.Parallel()

	messages := []any{
		systemMsg("base instructions"),
		systemMsg("format instructions"),
		userMsg("old"), assistantMsg("old answer"),
		userMsg("latest"), assistantMsg("latest answer"),
	}
	out, ok := CompressMessages(messages, true, 1)
	if !ok {
		t.Fatal("expected compression")
	}
	if len(out) != 4 || messageContent(out[0]) != "base instructions" || messageContent(out[1]) != "format instructions" {
		t.Fatalf("system instructions were not preserved: %#v", out)
	}
}

func TestCompressMessagesDoesNotMoveMidConversationSystemMessage(t *testing.T) {
	t.Parallel()

	messages := []any{
		systemMsg("base instructions"),
		userMsg("oldest"), assistantMsg("oldest answer"),
		map[string]any{"role": "system", "content": "mid-conversation note"},
		userMsg("older"), assistantMsg("older answer"),
		userMsg("latest"), assistantMsg("latest answer"),
	}
	out, ok := CompressMessages(messages, true, 1)
	if !ok {
		t.Fatal("expected compression")
	}
	if len(out) != 3 || messageContent(out[0]) != "base instructions" || messageContent(out[1]) != "latest" {
		t.Fatalf("mid-conversation system message was reordered into the prefix: %#v", out)
	}
}

// TestCompressToFitBringsOversizedPromptUnderExpertLimit is the end-to-end
// behaviour for the expert tier: a long conversation that overflows the tighter
// ceiling gets trimmed until the rebuilt prompt fits.
func TestCompressToFitBringsOversizedPromptUnderExpertLimit(t *testing.T) {
	t.Parallel()

	cfg := testCfg()
	messages := buildConversation(40, 200) // ~16k chars, far over the 1000 expert cap
	req := StandardRequest{
		RequestedModel: "deepseek-v4-pro",
		ResolvedModel:  "deepseek-v4-pro",
		Messages:       messages,
	}
	req.FinalPrompt, req.ToolNames = BuildOpenAIPrompt(messages, nil, "", DefaultToolChoicePolicy(), false)
	if len(req.FinalPrompt) <= cfg.MaxCharsExpert {
		t.Fatalf("test setup: prompt %d already under expert cap %d", len(req.FinalPrompt), cfg.MaxCharsExpert)
	}

	out, dropped := CompressToFit(cfg, req)
	if !dropped {
		t.Fatal("expected CompressToFit to drop history")
	}
	if len(out.Messages) >= len(messages) {
		t.Fatalf("message count %d not reduced from %d", len(out.Messages), len(messages))
	}
	if len(out.FinalPrompt) >= len(req.FinalPrompt) {
		t.Fatalf("prompt not shortened: %d -> %d", len(req.FinalPrompt), len(out.FinalPrompt))
	}
	if msg := LimitExceededMessage(cfg, out.FinalPrompt, out.ResolvedModel); msg != "" {
		t.Fatalf("prompt still over the expert limit after compression: %s", msg)
	}
}

func TestCompressToFitLeavesFittingPromptAlone(t *testing.T) {
	t.Parallel()

	cfg := testCfg()
	messages := buildConversation(2, 10)
	req := StandardRequest{ResolvedModel: "deepseek-v4-flash", Messages: messages}
	req.FinalPrompt, _ = BuildOpenAIPrompt(messages, nil, "", DefaultToolChoicePolicy(), false)

	out, dropped := CompressToFit(cfg, req)
	if dropped {
		t.Fatal("a prompt within budget must not be compressed")
	}
	if len(out.Messages) != len(messages) {
		t.Fatalf("messages mutated: %d != %d", len(out.Messages), len(messages))
	}
	if out.FinalPrompt != req.FinalPrompt {
		t.Fatal("FinalPrompt mutated for a prompt within budget")
	}
}

func TestCompressToFitRespectsAutoCompressDisabled(t *testing.T) {
	t.Parallel()

	cfg := testCfg()
	cfg.AutoCompressEnable = false
	messages := buildConversation(40, 200)
	req := StandardRequest{ResolvedModel: "deepseek-v4-pro", Messages: messages}
	req.FinalPrompt, _ = BuildOpenAIPrompt(messages, nil, "", DefaultToolChoicePolicy(), false)

	out, dropped := CompressToFit(cfg, req)
	if dropped {
		t.Fatal("auto_compress_enabled=false must not drop history")
	}
	if len(out.Messages) != len(messages) {
		t.Fatal("messages mutated while auto-compress disabled")
	}
	// The oversized prompt must then be rejected rather than silently sent.
	if msg := LimitExceededMessage(cfg, out.FinalPrompt, out.ResolvedModel); msg == "" {
		t.Fatal("oversized prompt with compression disabled must be reported")
	}
}

func TestEffectiveModelPrefersResolved(t *testing.T) {
	t.Parallel()

	if got := EffectiveModel(StandardRequest{RequestedModel: "gpt-5-pro", ResolvedModel: "deepseek-v4-pro"}); got != "deepseek-v4-pro" {
		t.Fatalf("EffectiveModel = %q, want resolved id", got)
	}
	if got := EffectiveModel(StandardRequest{RequestedModel: "gpt-5-pro"}); got != "gpt-5-pro" {
		t.Fatalf("EffectiveModel = %q, want requested id fallback", got)
	}
}

// TestLimitExceededMessageNamesTheTier keeps the 413 body actionable — it must
// say which ceiling was hit and which knob raises it.
func TestLimitExceededMessageNamesTheTier(t *testing.T) {
	t.Parallel()

	cfg := testCfg()
	over := strings.Repeat("x", cfg.MaxCharsExpert+1)
	msg := LimitExceededMessage(cfg, over, "deepseek-v4-pro")
	if msg == "" {
		t.Fatal("expected a rejection message")
	}
	for _, want := range []string{"expert", "max_chars_expert"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}
	if LimitExceededMessage(cfg, strings.Repeat("x", cfg.MaxCharsExpert), "deepseek-v4-pro") != "" {
		t.Fatal("a prompt exactly at the limit must be accepted")
	}
}

func TestPromptUnitsMatchesJavaScriptStringLength(t *testing.T) {
	t.Parallel()

	if got := PromptUnits("abc中文"); got != 5 {
		t.Fatalf("BMP prompt units = %d, want 5", got)
	}
	if got := PromptUnits("😀"); got != 2 {
		t.Fatalf("emoji prompt units = %d, want 2 UTF-16 code units", got)
	}

	cfg := testCfg()
	cfg.MaxCharsExpert = 2
	if msg := LimitExceededMessage(cfg, "😀", "deepseek-v4-pro"); msg != "" {
		t.Fatalf("emoji exactly at two UTF-16 units must fit, got %q", msg)
	}
	msg := LimitExceededMessage(cfg, "😀a", "deepseek-v4-pro")
	if !strings.Contains(msg, "by 1 units") {
		t.Fatalf("expected exact overflow amount, got %q", msg)
	}
}
