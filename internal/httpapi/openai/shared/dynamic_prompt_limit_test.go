package shared

import (
	"context"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

type dynamicPromptLimitStub struct{}

func (dynamicPromptLimitStub) GetModelInputLimits(context.Context, *auth.RequestAuth) (config.ModelInputLimits, error) {
	return config.ModelInputLimits{Default: 2621440, Expert: 163840}, nil
}

func TestResolveDynamicPromptLimitsKeepsLowerOperatorSafetyCeilings(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsDefault = 380000
	cfg.MaxCharsExpert = 150000
	cfg.MaxCharsDefaultConfigured = true
	cfg.MaxCharsExpertConfigured = true
	cfg.ProFlashCompressionTarget = 150000
	got, applied, err := ResolveDynamicPromptLimits(context.Background(), dynamicPromptLimitStub{}, &auth.RequestAuth{DeepSeekToken: "token"}, cfg)
	if err != nil || !applied {
		t.Fatalf("resolve dynamic limits: applied=%v err=%v", applied, err)
	}
	if got.MaxCharsDefault != 380000 || got.MaxCharsExpert != 150000 {
		t.Fatalf("operator safety limits were raised: %+v", got)
	}

	cfg.MaxCharsExpert = 200000
	cfg.ProFlashCompressionTarget = 250000
	got, applied, err = ResolveDynamicPromptLimits(context.Background(), dynamicPromptLimitStub{}, &auth.RequestAuth{DeepSeekToken: "token"}, cfg)
	if err != nil || !applied {
		t.Fatalf("resolve dynamic hard cap: applied=%v err=%v", applied, err)
	}
	if got.MaxCharsExpert != 163840 || got.ProFlashCompressionTarget != 163840 {
		t.Fatalf("upstream hard cap was not enforced: %+v", got)
	}
}

func TestResolveDynamicPromptLimitsRaisesEmpiricalFallbackToProviderCapacity(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	if cfg.MaxCharsDefaultConfigured || cfg.MaxCharsExpertConfigured {
		t.Fatalf("defaults must not look like operator ceilings: %+v", cfg)
	}
	got, applied, err := ResolveDynamicPromptLimits(context.Background(), dynamicPromptLimitStub{}, &auth.RequestAuth{DeepSeekToken: "token"}, cfg)
	if err != nil || !applied {
		t.Fatalf("resolve dynamic limits: applied=%v err=%v", applied, err)
	}
	if got.MaxCharsDefault != 2621440 || got.MaxCharsExpert != 163840 {
		t.Fatalf("provider capacity was not adopted: %+v", got)
	}
	if got.MaxCharsDefault < 1000000 {
		t.Fatalf("resolved default tier cannot carry 1M input: %+v", got)
	}
	if message := promptcompat.LimitExceededMessage(got, strings.Repeat("x", 1000000), "deepseek-v4-flash"); message != "" {
		t.Fatalf("1M input was rejected after dynamic resolution: %s", message)
	}
	if message := promptcompat.LimitExceededMessage(got, strings.Repeat("x", 2621440), "deepseek-v4-flash"); message != "" {
		t.Fatalf("exact provider limit was rejected: %s", message)
	}
	if message := promptcompat.LimitExceededMessage(got, strings.Repeat("x", 2621441), "deepseek-v4-flash"); message == "" {
		t.Fatal("provider limit + 1 must be rejected")
	}
}

func TestResolveDynamicPromptLimitsFallsBackForUnsupportedClient(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	got, applied, err := ResolveDynamicPromptLimits(context.Background(), struct{}{}, &auth.RequestAuth{DeepSeekToken: "token"}, cfg)
	if err != nil || applied {
		t.Fatalf("unsupported provider should use static fallback: applied=%v err=%v", applied, err)
	}
	if got != cfg {
		t.Fatalf("fallback changed settings: got=%+v want=%+v", got, cfg)
	}
}

func TestEnforcePromptLimitBeforeCIFOnlyRejectsInlineOverflow(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsDefault = 10
	req := promptcompat.StandardRequest{ResolvedModel: "deepseek-v4-flash", FinalPrompt: strings.Repeat("x", 11)}
	if message := EnforcePromptLimitBeforeCIF(cfg, req, false); message == "" {
		t.Fatal("inline CIF overflow should be rejected before transcript rewriting")
	}
	if message := EnforcePromptLimitBeforeCIF(cfg, req, true); message != "" {
		t.Fatalf("remote file mode may shrink the live prompt: %s", message)
	}
	cfg.ProFlashCompressionEnable = true
	if message := EnforcePromptLimitBeforeCIF(cfg, req, false); message != "" {
		t.Fatalf("Flash compression must retain its later reduction opportunity: %s", message)
	}
}

func TestApplyResponsesCompactThresholdUsesExpertBudget(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 163840
	cfg.ProFlashCompressionTarget = 150000
	req := map[string]any{
		"context_management": map[string]any{"compact_threshold": 0.85},
	}

	got, applied := ApplyResponsesCompactThreshold(req, cfg, "deepseek-v4-pro")
	if !applied {
		t.Fatal("expected compact threshold to apply")
	}
	if got.MaxCharsExpert != 139264 {
		t.Fatalf("expert threshold mismatch: got %d want %d", got.MaxCharsExpert, 139264)
	}
	if got.MaxCharsDefault != cfg.MaxCharsDefault {
		t.Fatalf("default limit changed: got %d want %d", got.MaxCharsDefault, cfg.MaxCharsDefault)
	}
	if got.ProFlashCompressionTarget != 139264 {
		t.Fatalf("pro-to-flash target was not clamped: got %d want %d", got.ProFlashCompressionTarget, 139264)
	}
}

func TestApplyResponsesCompactThresholdUsesDefaultBudget(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsDefault = 200000
	cfg.MaxCharsExpert = 100000
	req := map[string]any{
		"context_management": map[string]any{"compact_threshold": "0.5"},
	}

	got, applied := ApplyResponsesCompactThreshold(req, cfg, "deepseek-v4-flash")
	if !applied {
		t.Fatal("expected compact threshold to apply")
	}
	if got.MaxCharsDefault != 100000 {
		t.Fatalf("default threshold mismatch: got %d want %d", got.MaxCharsDefault, 100000)
	}
	if got.MaxCharsExpert != cfg.MaxCharsExpert {
		t.Fatalf("expert limit changed: got %d want %d", got.MaxCharsExpert, cfg.MaxCharsExpert)
	}
}

func TestApplyResponsesCompactThresholdRejectsInvalidValues(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	values := []any{nil, 0, -0.5, 1, 1.1, "0.5trailing", "not-a-number"}
	for _, value := range values {
		req := map[string]any{
			"context_management": map[string]any{"compact_threshold": value},
		}
		got, applied := ApplyResponsesCompactThreshold(req, cfg, "deepseek-v4-pro")
		if applied {
			t.Fatalf("invalid threshold %v was applied", value)
		}
		if got != cfg {
			t.Fatalf("invalid threshold %v changed settings: got=%+v want=%+v", value, got, cfg)
		}
	}
}

func TestApplyResponsesCompactThresholdIsExplicitWhenAutoCompressionIsDisabled(t *testing.T) {
	cfg := config.DefaultPromptLimitSettings()
	cfg.AutoCompressEnable = false
	req := map[string]any{
		"context_management": map[string]any{"compact_threshold": 0.5},
	}

	got, applied := ApplyResponsesCompactThreshold(req, cfg, "deepseek-v4-pro")
	if !applied {
		t.Fatal("explicit compact_threshold should apply when automatic compression is disabled")
	}
	if got.MaxCharsExpert != cfg.MaxCharsExpert/2 || !got.AutoCompressEnable {
		t.Fatalf("explicit compact threshold did not create a one-request budget: got=%+v", got)
	}
}
