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

type flashPinnedCompressionDSStub struct {
	normalPowCalls        int
	pinnedPowCalls        int
	normalCompletionCalls int
	pinnedCompletionCalls int
}

func (*flashPinnedCompressionDSStub) CreateSession(context.Context, *auth.RequestAuth, int) (string, error) {
	return "flash-session", nil
}

func (s *flashPinnedCompressionDSStub) GetPow(context.Context, *auth.RequestAuth, int) (string, error) {
	s.normalPowCalls++
	return "unsafe-normal-pow", nil
}

func (s *flashPinnedCompressionDSStub) GetPowPinned(context.Context, *auth.RequestAuth) (string, error) {
	s.pinnedPowCalls++
	return "pinned-pow", nil
}

func (s *flashPinnedCompressionDSStub) CallCompletion(context.Context, *auth.RequestAuth, map[string]any, string, int) (*http.Response, error) {
	s.normalCompletionCalls++
	return nil, nil
}

func (s *flashPinnedCompressionDSStub) CallCompletionRootPinned(context.Context, *auth.RequestAuth, map[string]any, string) (*http.Response, error) {
	s.pinnedCompletionCalls++
	body := "data: {\"p\":\"response/content\",\"v\":\"Short durable summary.\"}\n" + "data: [DONE]\n"
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (*flashPinnedCompressionDSStub) DeleteSessionForToken(context.Context, string, string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (*flashPinnedCompressionDSStub) DeleteAllSessionsForToken(context.Context, string) error {
	return nil
}

func TestTryFlashCompressPromptPinsRootSession(t *testing.T) {
	ds := &flashPinnedCompressionDSStub{}
	cfg := config.DefaultPromptLimitSettings()
	cfg.MaxCharsExpert = 1000
	cfg.MaxCharsDefault = 10000
	cfg.ProFlashCompressionEnable = true
	cfg.ProFlashCompressionTarget = 1000
	messages := []any{
		map[string]any{"role": "user", "content": "old requirement " + strings.Repeat("history ", 180)},
		map[string]any{"role": "assistant", "content": strings.Repeat("old result ", 180)},
		map[string]any{"role": "user", "content": "continue with the latest requirement"},
	}
	req := promptcompat.StandardRequest{
		RequestedModel: "deepseek-v4-pro",
		ResolvedModel:  "deepseek-v4-pro",
		ResponseModel:  "deepseek-v4-pro",
		Messages:       messages,
		ToolChoice:     promptcompat.DefaultToolChoicePolicy(),
	}
	req.FinalPrompt, _ = promptcompat.BuildOpenAIPrompt(messages, nil, "", req.ToolChoice, false)
	if promptcompat.PromptUnits(req.FinalPrompt) <= cfg.MaxCharsExpert {
		t.Fatalf("test prompt does not exceed expert cap: %d", promptcompat.PromptUnits(req.FinalPrompt))
	}

	compressed, ok, err := TryFlashCompressPrompt(context.Background(), ds, &auth.RequestAuth{AccountID: "account", DeepSeekToken: "token"}, req, cfg, "none")
	if err != nil || !ok {
		t.Fatalf("flash compression = (%v, %v)", ok, err)
	}
	if promptcompat.PromptUnits(compressed.FinalPrompt) > cfg.ProFlashCompressionTarget {
		t.Fatalf("compressed prompt exceeds target: %d", promptcompat.PromptUnits(compressed.FinalPrompt))
	}
	if ds.normalPowCalls != 0 || ds.normalCompletionCalls != 0 || ds.pinnedPowCalls != 1 || ds.pinnedCompletionCalls != 1 {
		t.Fatalf("expected pinned root path only: %+v", ds)
	}
}
