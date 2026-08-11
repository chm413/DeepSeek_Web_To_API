package shared

import (
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/upstreamsession"
)

func TestApplyIncrementalSessionRotationPreservesCanonicalMessages(t *testing.T) {
	req := promptcompat.StandardRequest{
		Messages: []any{
			map[string]any{"role": "user", "content": "old question"},
			map[string]any{"role": "assistant", "content": "old answer"},
			map[string]any{"role": "user", "content": "new question"},
		},
		IncrementalFormatPrompt: "required output format",
	}
	cfg := config.DefaultPromptLimitSettings()
	cfg.IncrementalMaxTurns = 1
	cfg.IncrementalRotationKeepRecent = 1
	lease := &upstreamsession.Lease{Rotate: true, TurnCount: 1}

	dropped, ok := ApplyIncrementalSessionRotation(&req, lease, cfg)
	if !ok || dropped != 1 || !req.IncrementalSessionRotated {
		t.Fatalf("rotation was not applied: ok=%v dropped=%d request=%+v", ok, dropped, req)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("canonical client messages were rewritten: %#v", req.Messages)
	}
	for _, expected := range []string{"required output format", "old answer", "new question"} {
		if !strings.Contains(req.FinalPrompt, expected) {
			t.Fatalf("rotation prompt missing %q: %q", expected, req.FinalPrompt)
		}
	}
	if strings.Contains(req.FinalPrompt, "old question") {
		t.Fatalf("rotation prompt retained dropped history: %q", req.FinalPrompt)
	}
}
