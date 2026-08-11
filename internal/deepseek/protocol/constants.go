package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	DeepSeekHost                 = "chat.deepseek.com"
	DeepSeekLoginURL             = "https://chat.deepseek.com/api/v0/users/login"
	DeepSeekCurrentUserURL       = "https://chat.deepseek.com/api/v0/users/current"
	DeepSeekClientSettingsURL    = "https://chat.deepseek.com/api/v0/client/settings?scope=model"
	DeepSeekCreateSessionURL     = "https://chat.deepseek.com/api/v0/chat_session/create"
	DeepSeekCreatePowURL         = "https://chat.deepseek.com/api/v0/chat/create_pow_challenge"
	DeepSeekCompletionURL        = "https://chat.deepseek.com/api/v0/chat/completion"
	DeepSeekContinueURL          = "https://chat.deepseek.com/api/v0/chat/continue"
	DeepSeekUploadFileURL        = "https://chat.deepseek.com/api/v0/file/upload_file"
	DeepSeekFetchFilesURL        = "https://chat.deepseek.com/api/v0/file/fetch_files"
	DeepSeekFetchSessionURL      = "https://chat.deepseek.com/api/v0/chat_session/fetch_page"
	DeepSeekDeleteSessionURL     = "https://chat.deepseek.com/api/v0/chat_session/delete"
	DeepSeekDeleteAllSessionsURL = "https://chat.deepseek.com/api/v0/chat_session/delete_all"
	DeepSeekCompletionTargetPath = "/api/v0/chat/completion"
	DeepSeekUploadTargetPath     = "/api/v0/file/upload_file"
)

var defaultStaticBaseHeaders = map[string]string{
	"Host":           "chat.deepseek.com",
	"Accept":         "application/json",
	"Content-Type":   "application/json",
	"accept-charset": "UTF-8",
}

var defaultSkipContainsPatterns = []string{
	"quasi_status",
	"elapsed_secs",
	"token_usage",
	"pending_fragment",
	"conversation_mode",
	"fragments/-1/status",
	"fragments/-2/status",
	"fragments/-3/status",
}

var defaultSkipExactPaths = []string{
	"response/search_status",
}

var ClientVersion string
var BaseHeaders = map[string]string{}
var SkipContainsPatterns = cloneStringSlice(defaultSkipContainsPatterns)
var SkipExactPathSet = toStringSet(defaultSkipExactPaths)

type clientConstants struct {
	Name            string `json:"name"`
	Platform        string `json:"platform"`
	Version         string `json:"version"`
	AndroidAPILevel string `json:"android_api_level"`
	Locale          string `json:"locale"`
}

type sharedConstants struct {
	Client              clientConstants   `json:"client"`
	BaseHeaders         map[string]string `json:"base_headers"`
	SkipContainsPattern []string          `json:"skip_contains_patterns"`
	SkipExactPaths      []string          `json:"skip_exact_paths"`
}

//go:embed constants_shared.json
var sharedConstantsJSON []byte

func init() {
	cfg := sharedConstants{}
	if err := json.Unmarshal(sharedConstantsJSON, &cfg); err != nil {
		panic(fmt.Errorf("load DeepSeek shared constants: %w", err))
	}
	applySharedConstants(cfg)
}

func applySharedConstants(cfg sharedConstants) {
	client := normalizeClientConstants(cfg.Client)
	ClientVersion = client.Version
	BaseHeaders = buildBaseHeaders(client, cfg.BaseHeaders)
	SkipContainsPatterns = cloneStringSlice(defaultSkipContainsPatterns)
	if len(cfg.SkipContainsPattern) > 0 {
		SkipContainsPatterns = cloneStringSlice(cfg.SkipContainsPattern)
	}
	SkipExactPathSet = toStringSet(defaultSkipExactPaths)
	if len(cfg.SkipExactPaths) > 0 {
		SkipExactPathSet = toStringSet(cfg.SkipExactPaths)
	}
}

func normalizeClientConstants(in clientConstants) clientConstants {
	if in.Name == "" {
		in.Name = "DeepSeek"
	}
	if in.Platform == "" {
		in.Platform = "android"
	}
	if in.AndroidAPILevel == "" {
		in.AndroidAPILevel = "35"
	}
	if in.Locale == "" {
		in.Locale = "zh_CN"
	}
	return in
}

func buildBaseHeaders(client clientConstants, overrides map[string]string) map[string]string {
	out := cloneStringMap(defaultStaticBaseHeaders)
	for k, v := range overrides {
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if client.Name != "" && client.Version != "" {
		userAgent := client.Name + "/" + client.Version
		if client.Platform == "android" && client.AndroidAPILevel != "" {
			userAgent += " Android/" + client.AndroidAPILevel
		}
		out["User-Agent"] = userAgent
	}
	if client.Platform != "" {
		out["x-client-platform"] = client.Platform
	}
	if client.Version != "" {
		out["x-client-version"] = client.Version
	}
	if client.Locale != "" {
		out["x-client-locale"] = client.Locale
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSlice(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func toStringSet(in []string) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		out[v] = struct{}{}
	}
	return out
}

var (
	KeepAliveTimeout = envInt("DEEPSEEK_WEB_TO_API_STREAM_KEEPALIVE_INTERVAL_SECONDS", 5)
	// StreamIdleTimeout is the SSE-stream "no content received" cutoff,
	// matched against the global HTTP total timeout (7200s default since
	// v1.0.6). Long-reasoning models on Pro tiers can hold a stream open
	// for the full 2-hour ceiling between thinking + tool-use loops; the
	// previous 1800s default chopped that mid-flight.
	StreamIdleTimeout = envInt("DEEPSEEK_WEB_TO_API_STREAM_IDLE_TIMEOUT_SECONDS", 7200)
	// MaxKeepaliveCount caps consecutive keep-alive frames before the
	// stream is treated as dead. Scaled with StreamIdleTimeout: 7200s /
	// 5s keep-alive interval = 1440 frames.
	MaxKeepaliveCount = envInt("DEEPSEEK_WEB_TO_API_STREAM_MAX_KEEPALIVE_NO_CONTENT", 1440)
)

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
