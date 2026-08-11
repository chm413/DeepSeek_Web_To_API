package claude

import (
	"context"
	"net/http"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

type AuthResolver interface {
	Determine(req *http.Request) (*auth.RequestAuth, error)
	Release(a *auth.RequestAuth)
}

type DeepSeekCaller interface {
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error)
	// Issue #20 fix: /v1/messages must clean up remote sessions when the
	// operator enables the WebUI auto-delete toggle. Pulled into the
	// claude-side interface so deps_injection can mock it; the production
	// dsclient.Client already implements both methods.
	DeleteSessionForToken(ctx context.Context, token string, sessionID string) (*dsclient.DeleteSessionResult, error)
	DeleteAllSessionsForToken(ctx context.Context, token string) error
}

type ConfigReader interface {
	ModelAliases() map[string]string
	CompatStripReferenceMarkers() bool
	// Issue #20 fix: handler reads the snapshot to decide whether to
	// schedule an upstream session delete after the request completes.
	AutoDeleteMode() string
	// SafetyBlockMessage returns the operator-configured response body
	// when v1.0.14+ LLM safety review blocks a request.
	SafetyBlockMessage() string
	PromptLimitSnapshot() config.PromptLimitSettings
}

type OpenAIChatRunner interface {
	ChatCompletions(w http.ResponseWriter, r *http.Request)
}

var _ AuthResolver = (*auth.Resolver)(nil)
var _ DeepSeekCaller = (*dsclient.Client)(nil)
var _ ConfigReader = (*config.Store)(nil)
