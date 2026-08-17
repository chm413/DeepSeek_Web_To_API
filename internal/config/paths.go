package config

import (
	"os"
	"path/filepath"
	"strings"
)

func BaseDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func ResolvePath(envKey, defaultRel string) string {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw != "" {
		if filepath.IsAbs(raw) {
			return raw
		}
		return filepath.Join(BaseDir(), raw)
	}
	return filepath.Join(BaseDir(), defaultRel)
}

func ConfigPath() string {
	if strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH")) == "" && BaseDir() == "/app" {
		return containerDefaultConfigPath()
	}
	return ResolvePath("DEEPSEEK_WEB_TO_API_CONFIG_PATH", "config.json")
}

func containerDefaultConfigPath() string {
	// Container images run as non-root by default. Only use /data when mounted/provisioned.
	// Otherwise keep /app/config.json so admin-side save does not fail on MkdirAll("/data").
	if st, err := os.Stat("/data"); err == nil && st.IsDir() {
		return "/data/config.json"
	}
	return "/app/config.json"
}

func legacyContainerConfigPath() string {
	return "/app/config.json"
}

func shouldTryLegacyContainerConfigPath() bool {
	if BaseDir() != "/app" {
		return false
	}
	configured := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH"))
	if configured == "" {
		return true
	}
	return filepath.Clean(configured) == filepath.Join("/app", "data", "config.json")
}

// legacyContainerConfigFallbackPath returns the immutable-image config path
// when a newer Compose layout explicitly points at /app/data/config.json.
// This prevents an image upgrade from treating an existing /app/config.json
// as a fresh installation before it can be migrated into the mounted volume.
func legacyContainerConfigFallbackPath(destination string) string {
	if filepath.Clean(strings.TrimSpace(destination)) != filepath.Join("/app", "data", "config.json") {
		return ""
	}
	return legacyContainerConfigPath()
}

func RawStreamSampleRoot() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_RAW_STREAM_SAMPLE_ROOT", "tests/raw_stream_samples")
}

func ChatHistoryPath() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_CHAT_HISTORY_PATH", "data/chat_history.json")
}

func AccountsSQLitePath() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH", "data/accounts.sqlite")
}

func TokenUsageSQLitePath() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_TOKEN_USAGE_SQLITE_PATH", "data/token_usage.sqlite")
}

func SafetyWordsSQLitePath() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_SAFETY_WORDS_SQLITE_PATH", "data/safety_words.sqlite")
}

func SafetyIPsSQLitePath() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_SAFETY_IPS_SQLITE_PATH", "data/safety_ips.sqlite")
}

func ResponseCacheDir() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_RESPONSE_CACHE_DIR", "data/response_cache")
}

func StaticAdminDir() string {
	return ResolvePath("DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR", "static/admin")
}
