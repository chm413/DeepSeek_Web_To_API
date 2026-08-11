package responses

import (
	"context"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
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

type responsesPromptLimitDSStub struct {
	responsesIncrementalDSStub
	limitCalls int
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
