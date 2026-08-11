package shared

import (
	"context"
	"net/http"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/util"
)

const (
	// UploadMaxSize limits total multipart request body size (100 MiB).
	UploadMaxSize = 100 << 20
	// GeneralMaxSize limits total JSON request body size (100 MiB).
	GeneralMaxSize = 100 << 20
)

type AuthResolver interface {
	Determine(req *http.Request) (*auth.RequestAuth, error)
	DetermineWithSession(req *http.Request, body []byte) (*auth.RequestAuth, error)
	DetermineCaller(req *http.Request) (*auth.RequestAuth, error)
	Release(a *auth.RequestAuth)
}

type DeepSeekCaller interface {
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	UploadFile(ctx context.Context, a *auth.RequestAuth, req dsclient.UploadFileRequest, maxAttempts int) (*dsclient.UploadFileResult, error)
	CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error)
	DeleteSessionForToken(ctx context.Context, token string, sessionID string) (*dsclient.DeleteSessionResult, error)
	DeleteAllSessionsForToken(ctx context.Context, token string) error
}

// DynamicPromptLimitProvider is optional so protocol tests and alternate
// upstream clients can continue to work with the static fallback settings.
type DynamicPromptLimitProvider interface {
	GetModelInputLimits(ctx context.Context, a *auth.RequestAuth) (config.ModelInputLimits, error)
}

type ConfigReader interface {
	ModelAliases() map[string]string
	CompatWideInputStrictOutput() bool
	CompatStripReferenceMarkers() bool
	ToolcallMode() string
	ToolcallEarlyEmitConfidence() string
	ResponsesStoreTTLSeconds() int
	EmbeddingsProvider() string
	AutoDeleteMode() string
	AutoDeleteSessions() bool
	HistorySplitEnabled() bool
	HistorySplitTriggerAfterTurns() int
	CurrentInputFileEnabled() bool
	CurrentInputFileMinChars() int
	ThinkingInjectionEnabled() bool
	ThinkingInjectionPrompt() string
	// SafetyBlockMessage returns the message rendered to a client when
	// the v1.0.14+ LLM safety reviewer blocks a request. Empty falls
	// back to the handler default.
	SafetyBlockMessage() string
	// RemoteFileUploadEnabled controls whether the service forwards inline
	// attachments (and the current-input-file transcript) to the upstream
	// DeepSeek upload_file endpoint. When false (the production default),
	// attachments are inlined as text directly in the prompt instead. The
	// upstream endpoint is heavily rate-limited per account and was the
	// dominant cause of production failure rate; operators can opt back in
	// via DEEPSEEK_WEB_TO_API_REMOTE_FILE_UPLOAD_ENABLED=true if they
	// have headroom.
	RemoteFileUploadEnabled() bool

	// Prompt limit and auto-compression settings (v1.0.14+)
	// PromptLimitSnapshot returns every prompt_limit knob read under one
	// lock. A single snapshot must be taken per request and threaded through
	// both the compress and enforce phases: reading the knobs twice lets a
	// concurrent config write reject a request that was never offered the
	// chance to compress.
	PromptLimitSnapshot() config.PromptLimitSettings
}

type Deps struct {
	Store       ConfigReader
	Auth        AuthResolver
	DS          DeepSeekCaller
	ChatHistory *chathistory.Store
}

func CompatStripReferenceMarkers(store ConfigReader) bool {
	if store == nil {
		return true
	}
	return store.CompatStripReferenceMarkers()
}

var WriteJSON = util.WriteJSON

var _ AuthResolver = (*auth.Resolver)(nil)
var _ DeepSeekCaller = (*dsclient.Client)(nil)
var _ ConfigReader = (*config.Store)(nil)
