package config

import (
	// #nosec G505 -- SHA-1 is only used for legacy-compatible, non-secret proxy IDs.
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/proxyuri"
)

type Config struct {
	// ConfigSchemaVersion is persisted by the startup migration layer. A zero
	// value means the file predates schema versioning and is migrated in place.
	ConfigSchemaVersion int                     `json:"config_schema_version,omitempty"`
	Keys                []string                `json:"keys,omitempty"`
	APIKeys             []APIKey                `json:"api_keys,omitempty"`
	Accounts            []Account               `json:"accounts,omitempty"`
	Proxies             []Proxy                 `json:"proxies,omitempty"`
	ProxySubscriptions  []ProxySubscription     `json:"proxy_subscriptions,omitempty"`
	ProxyCore           ProxyCoreConfig         `json:"proxy_core,omitempty"`
	ProxyPolicy         ProxyPolicyConfig       `json:"proxy_policy,omitempty"`
	ModelAliases        map[string]string       `json:"model_aliases,omitempty"`
	Admin               AdminConfig             `json:"admin,omitempty"`
	Server              ServerConfig            `json:"server,omitempty"`
	Storage             StorageConfig           `json:"storage,omitempty"`
	Cache               CacheConfig             `json:"cache,omitempty"`
	Safety              SafetyConfig            `json:"safety,omitempty"`
	Runtime             RuntimeConfig           `json:"runtime,omitempty"`
	Compat              CompatConfig            `json:"compat,omitempty"`
	Responses           ResponsesConfig         `json:"responses,omitempty"`
	Embeddings          EmbeddingsConfig        `json:"embeddings,omitempty"`
	AutoDelete          AutoDeleteConfig        `json:"auto_delete"`
	HistorySplit        HistorySplitConfig      `json:"history_split"`
	CurrentInputFile    CurrentInputFileConfig  `json:"current_input_file,omitempty"`
	ThinkingInjection   ThinkingInjectionConfig `json:"thinking_injection,omitempty"`
	PromptLimit         PromptLimitConfig       `json:"prompt_limit,omitempty"`
	AppUpdate           AppUpdateConfig         `json:"app_update,omitempty"`
	AdditionalFields    map[string]any          `json:"-"`
}

type Account struct {
	Name              string `json:"name,omitempty"`
	Remark            string `json:"remark,omitempty"`
	Email             string `json:"email,omitempty"`
	Mobile            string `json:"mobile,omitempty"`
	Password          string `json:"password,omitempty"`
	Token             string `json:"token,omitempty"`
	ProxyID           string `json:"proxy_id,omitempty"`
	ProxyAutoRoute    bool   `json:"proxy_auto_route,omitempty"`
	Disabled          bool   `json:"disabled,omitempty"`
	DisabledReason    string `json:"disabled_reason,omitempty"`
	DisabledAtUnix    int64  `json:"disabled_at_unix,omitempty"`
	CooldownState     string `json:"cooldown_state,omitempty"`
	CooldownUntilUnix int64  `json:"cooldown_until_unix,omitempty"`
}

const (
	AccountDisabledManual             = "manual"
	AccountDisabledInvalidCredentials = "invalid_credentials"
	AccountDisabledUpstreamBanned     = "upstream_banned"
	AccountCooldownRateLimited        = "rate_limited"
	AccountCooldownTemporarilyMuted   = "temporarily_muted"
)

type APIKey struct {
	Key    string `json:"key"`
	Name   string `json:"name,omitempty"`
	Remark string `json:"remark,omitempty"`
}

type Proxy struct {
	ID                  string `json:"id,omitempty"`
	Name                string `json:"name,omitempty"`
	Type                string `json:"type,omitempty"`
	Host                string `json:"host,omitempty"`
	Port                int    `json:"port,omitempty"`
	Username            string `json:"username,omitempty"`
	Password            string `json:"password,omitempty"`
	URI                 string `json:"uri,omitempty"`
	SubscriptionID      string `json:"subscription_id,omitempty"`
	Disabled            bool   `json:"disabled,omitempty"`
	DisabledReason      string `json:"disabled_reason,omitempty"`
	DisabledAtUnix      int64  `json:"disabled_at_unix,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
	LastTestAtUnix      int64  `json:"last_test_at_unix,omitempty"`
	LastTestSuccess     bool   `json:"last_test_success,omitempty"`
	LastLatencyMS       int    `json:"last_latency_ms,omitempty"`
	LastHTTPStatus      int    `json:"last_http_status,omitempty"`
	LastTestError       string `json:"last_test_error,omitempty"`
	LastExitIP          string `json:"last_exit_ip,omitempty"`
	LastCountry         string `json:"last_country,omitempty"`
	LastColo            string `json:"last_colo,omitempty"`
}

type ProxyCoreConfig struct {
	XrayBinaryPath        string `json:"xray_binary_path,omitempty"`
	RuntimeDir            string `json:"runtime_dir,omitempty"`
	StartupTimeoutSeconds int    `json:"startup_timeout_seconds,omitempty"`
	AutoDownloadDisabled  bool   `json:"auto_download_disabled,omitempty"`
	DownloadDir           string `json:"download_dir,omitempty"`
	DownloadVersion       string `json:"download_version,omitempty"`
	// InstalledVersion records the version from the local .version marker after
	// a managed Xray download. It is status metadata, not a requested version.
	InstalledVersion string `json:"installed_version,omitempty"`
}

type ProxyPolicyConfig struct {
	HealthCheckEnabled                *bool  `json:"health_check_enabled,omitempty"`
	AutomaticRoutingEnabled           *bool  `json:"auto_route_enabled,omitempty"`
	HealthCheckIntervalMinutes        int    `json:"health_check_interval_minutes,omitempty"`
	AutoDisableAfterFailures          int    `json:"auto_disable_after_failures,omitempty"`
	AutoEnableOnRecovery              *bool  `json:"auto_enable_on_recovery,omitempty"`
	FallbackProxyID                   string `json:"fallback_proxy_id,omitempty"`
	SubscriptionUpdateIntervalMinutes int    `json:"subscription_update_interval_minutes,omitempty"`
	TestConcurrency                   int    `json:"test_concurrency,omitempty"`
}

type ProxySubscription struct {
	ID                    string `json:"id,omitempty"`
	Name                  string `json:"name,omitempty"`
	URL                   string `json:"url,omitempty"`
	Disabled              bool   `json:"disabled,omitempty"`
	AutoUpdateDisabled    bool   `json:"auto_update_disabled,omitempty"`
	AutoTestDisabled      bool   `json:"auto_test_disabled,omitempty"`
	UpdateIntervalMinutes int    `json:"update_interval_minutes,omitempty"`
	LastUpdatedAtUnix     int64  `json:"last_updated_at_unix,omitempty"`
	LastAttemptAtUnix     int64  `json:"last_attempt_at_unix,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	NodeCount             int    `json:"node_count,omitempty"`
}

const (
	ProxyDisabledManual              = "manual"
	ProxyDisabledHealth              = "health_check_failed"
	ProxyDisabledSubscriptionRemoved = "subscription_removed"
)

func NormalizeProxy(p Proxy) Proxy {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Type = proxyuri.NormalizeType(p.Type)
	p.Host = strings.TrimSpace(p.Host)
	p.Username = strings.TrimSpace(p.Username)
	p.Password = strings.TrimSpace(p.Password)
	p.URI = strings.TrimSpace(p.URI)
	p.SubscriptionID = strings.TrimSpace(p.SubscriptionID)
	p.DisabledReason = strings.TrimSpace(p.DisabledReason)
	p.LastTestError = strings.TrimSpace(p.LastTestError)
	p.LastExitIP = strings.TrimSpace(p.LastExitIP)
	p.LastCountry = strings.ToUpper(strings.TrimSpace(p.LastCountry))
	p.LastColo = strings.ToUpper(strings.TrimSpace(p.LastColo))
	if len([]rune(p.LastTestError)) > 600 {
		p.LastTestError = string([]rune(p.LastTestError)[:600]) + "..."
	}
	if p.ConsecutiveFailures < 0 {
		p.ConsecutiveFailures = 0
	}
	if proxyuri.IsCoreType(p.Type) {
		p.Host = ""
		p.Port = 0
		p.Username = ""
		p.Password = ""
		if node, err := proxyuri.Parse(p.Type, p.URI); err == nil {
			p.Host = node.Address
			p.Port = node.Port
			if p.Name == "" {
				p.Name = node.DisplayName
			}
		}
	} else {
		p.URI = ""
	}
	if p.ID == "" {
		p.ID = StableProxyID(p)
	}
	if p.Name == "" && p.Host != "" && p.Port > 0 {
		p.Name = fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	return p
}

func StableProxyID(p Proxy) string {
	p.Type = proxyuri.NormalizeType(p.Type)
	if proxyuri.IsCoreType(p.Type) {
		sum := sha256.Sum256([]byte(p.Type + "|" + strings.TrimSpace(p.URI)))
		return "proxy_" + hex.EncodeToString(sum[:6])
	}
	// #nosec G401 -- preserve existing proxy ID compatibility; this is not a security boundary.
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(p.Type)) + "|" + strings.ToLower(strings.TrimSpace(p.Host)) + "|" + fmt.Sprintf("%d", p.Port) + "|" + strings.TrimSpace(p.Username)))
	return "proxy_" + hex.EncodeToString(sum[:6])
}

func (c *Config) ClearAccountTokens() {
	if c == nil {
		return
	}
	for i := range c.Accounts {
		c.Accounts[i].Token = ""
	}
}

func (c *Config) NormalizeCredentials() {
	if c == nil {
		return
	}
	normalizedAPIKeys := normalizeAPIKeys(c.APIKeys)
	if len(normalizedAPIKeys) > 0 {
		c.APIKeys = normalizedAPIKeys
		c.Keys = apiKeysToStrings(c.APIKeys)
	} else {
		c.Keys = normalizeKeys(c.Keys)
		c.APIKeys = apiKeysFromStrings(c.Keys, nil)
	}

	for i := range c.Accounts {
		c.Accounts[i] = NormalizeAccountIdentity(c.Accounts[i])
		c.Accounts[i].Name = strings.TrimSpace(c.Accounts[i].Name)
		c.Accounts[i].Remark = strings.TrimSpace(c.Accounts[i].Remark)
		c.Accounts[i].CooldownState, c.Accounts[i].CooldownUntilUnix = normalizeAccountCooldown(c.Accounts[i].CooldownState, c.Accounts[i].CooldownUntilUnix)
	}
	for i := range c.Proxies {
		c.Proxies[i] = NormalizeProxy(c.Proxies[i])
	}
	for i := range c.ProxySubscriptions {
		c.ProxySubscriptions[i].ID = strings.TrimSpace(c.ProxySubscriptions[i].ID)
		c.ProxySubscriptions[i].Name = strings.TrimSpace(c.ProxySubscriptions[i].Name)
		c.ProxySubscriptions[i].URL = strings.TrimSpace(c.ProxySubscriptions[i].URL)
		c.ProxySubscriptions[i].LastError = strings.TrimSpace(c.ProxySubscriptions[i].LastError)
	}
	c.ProxyCore.XrayBinaryPath = strings.TrimSpace(c.ProxyCore.XrayBinaryPath)
	c.ProxyCore.RuntimeDir = strings.TrimSpace(c.ProxyCore.RuntimeDir)
	c.ProxyCore.DownloadDir = strings.TrimSpace(c.ProxyCore.DownloadDir)
	c.ProxyCore.DownloadVersion = strings.TrimSpace(c.ProxyCore.DownloadVersion)
	c.ProxyCore.InstalledVersion = strings.TrimSpace(c.ProxyCore.InstalledVersion)
	c.ProxyPolicy.FallbackProxyID = strings.TrimSpace(c.ProxyPolicy.FallbackProxyID)

	c.normalizeModelAliases()
}

// DropInvalidAccounts removes accounts that cannot be addressed by admin APIs
// (no email and no normalizable mobile). This prevents legacy token-only
// records from becoming orphaned empty entries after token stripping.
func (c *Config) DropInvalidAccounts() {
	if c == nil || len(c.Accounts) == 0 {
		return
	}
	kept := make([]Account, 0, len(c.Accounts))
	for _, acc := range c.Accounts {
		if acc.Identifier() == "" {
			continue
		}
		kept = append(kept, acc)
	}
	c.Accounts = kept
}

func (c *Config) normalizeModelAliases() {
	if c == nil {
		return
	}

	aliases := map[string]string{}
	for k, v := range c.ModelAliases {
		key := strings.TrimSpace(lower(k))
		val := strings.TrimSpace(lower(v))
		if key == "" || val == "" {
			continue
		}
		aliases[key] = val
	}
	if len(aliases) == 0 {
		c.ModelAliases = nil
	} else {
		c.ModelAliases = aliases
	}
}

type CompatConfig struct {
	WideInputStrictOutput *bool `json:"wide_input_strict_output,omitempty"`
	StripReferenceMarkers *bool `json:"strip_reference_markers,omitempty"`
}

type AdminConfig struct {
	Key               string `json:"key,omitempty"`
	PasswordHash      string `json:"password_hash,omitempty"`
	JWTSecret         string `json:"jwt_secret,omitempty"`
	JWTExpireHours    int    `json:"jwt_expire_hours,omitempty"`
	JWTValidAfterUnix int64  `json:"jwt_valid_after_unix,omitempty"`
}

type ServerConfig struct {
	Port                    string `json:"port,omitempty"`
	BindAddr                string `json:"bind_addr,omitempty"`
	LogLevel                string `json:"log_level,omitempty"`
	StaticAdminDir          string `json:"static_admin_dir,omitempty"`
	AutoBuildWebUI          *bool  `json:"auto_build_webui,omitempty"`
	HTTPTotalTimeoutSeconds int    `json:"http_total_timeout_seconds,omitempty"`
	RemoteFileUploadEnabled *bool  `json:"remote_file_upload_enabled,omitempty"`
}

type StorageConfig struct {
	DataDir               string `json:"data_dir,omitempty"`
	AccountsSQLitePath    string `json:"accounts_sqlite_path,omitempty"`
	ChatHistoryPath       string `json:"chat_history_path,omitempty"`
	ChatHistorySQLitePath string `json:"chat_history_sqlite_path,omitempty"`
	TokenUsageSQLitePath  string `json:"token_usage_sqlite_path,omitempty"`
	SafetyWordsSQLitePath string `json:"safety_words_sqlite_path,omitempty"`
	SafetyIPsSQLitePath   string `json:"safety_ips_sqlite_path,omitempty"`
	RawStreamSampleRoot   string `json:"raw_stream_sample_root,omitempty"`
}

type CacheConfig struct {
	Response ResponseCacheConfig `json:"response,omitempty"`
}

type ResponseCacheConfig struct {
	Dir              string `json:"dir,omitempty"`
	MemoryTTLSeconds int    `json:"memory_ttl_seconds,omitempty"`
	DiskTTLSeconds   int    `json:"disk_ttl_seconds,omitempty"`
	MaxBodyBytes     int64  `json:"max_body_bytes,omitempty"`
	MemoryMaxBytes   int64  `json:"memory_max_bytes,omitempty"`
	DiskMaxBytes     int64  `json:"disk_max_bytes,omitempty"`
	SemanticKey      *bool  `json:"semantic_key,omitempty"`
}

type SafetyConfig struct {
	Enabled                *bool    `json:"enabled,omitempty"`
	BlockMessage           string   `json:"block_message,omitempty"`
	BlockedIPs             []string `json:"blocked_ips,omitempty"`
	AllowedIPs             []string `json:"allowed_ips,omitempty"`
	BlockedConversationIDs []string `json:"blocked_conversation_ids,omitempty"`
	// BannedContent / BannedRegex / Jailbreak / DisabledBuiltinRules are
	// retained as JSON fields for backward-compat with v1.0.13- configs
	// but are NOT consumed by requestguard as of v1.0.14. Content-level
	// review is performed by SafetyLLMCheck below.
	BannedContent        []string             `json:"banned_content,omitempty"`
	BannedRegex          []string             `json:"banned_regex,omitempty"`
	Jailbreak            JailbreakConfig      `json:"jailbreak,omitempty"`
	DisabledBuiltinRules []string             `json:"disabled_builtin_rules,omitempty"`
	AutoBan              SafetyAutoBanConfig  `json:"auto_ban,omitempty"`
	LLMCheck             SafetyLLMCheckConfig `json:"llm_check,omitempty"`
}

// SafetyLLMCheckConfig drives the v1.0.14+ binary-verdict LLM safety
// reviewer (internal/safetyllm). When Enabled, every chat / responses /
// messages request runs through deepseek-v4-flash-nothinking after auth
// to get a "violation" / "ok" verdict before reaching upstream.
type SafetyLLMCheckConfig struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	Model           string `json:"model,omitempty"`
	TimeoutMs       int    `json:"timeout_ms,omitempty"`
	FailOpen        *bool  `json:"fail_open,omitempty"`
	CacheTTLSeconds int    `json:"cache_ttl_seconds,omitempty"`
	CacheMaxEntries int    `json:"cache_max_entries,omitempty"`
	MinInputChars   int    `json:"min_input_chars,omitempty"`
	MaxInputChars   int    `json:"max_input_chars,omitempty"`
	MaxConcurrent   int    `json:"max_concurrent,omitempty"`
}

type JailbreakConfig struct {
	Enabled  *bool    `json:"enabled,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

// SafetyAutoBanConfig governs automatic IP blacklisting based on repeated
// safety / banned-word violations. When violation_count for an IP reaches
// Threshold within WindowSeconds, the IP is appended to the safety_ips
// blocked_ips table. Defaults: enabled when safety.enabled, threshold 3,
// window 600s.
type SafetyAutoBanConfig struct {
	Enabled       *bool `json:"enabled,omitempty"`
	Threshold     int   `json:"threshold,omitempty"`
	WindowSeconds int   `json:"window_seconds,omitempty"`
}

type RuntimeConfig struct {
	AccountMaxInflight                int `json:"account_max_inflight,omitempty"`
	AccountMaxQueue                   int `json:"account_max_queue,omitempty"`
	GlobalMaxInflight                 int `json:"global_max_inflight,omitempty"`
	TokenRefreshIntervalHours         int `json:"token_refresh_interval_hours,omitempty"`
	AccountHealthCheckIntervalMinutes int `json:"account_health_check_interval_minutes,omitempty"`
}

type ResponsesConfig struct {
	StoreTTLSeconds int `json:"store_ttl_seconds,omitempty"`
}

type EmbeddingsConfig struct {
	Provider string `json:"provider,omitempty"`
}

type AutoDeleteConfig struct {
	Mode     string `json:"mode,omitempty"`
	Sessions bool   `json:"sessions,omitempty"`
}

type HistorySplitConfig struct {
	Enabled           *bool `json:"enabled,omitempty"`
	TriggerAfterTurns *int  `json:"trigger_after_turns,omitempty"`
}

type CurrentInputFileConfig struct {
	Enabled  *bool `json:"enabled,omitempty"`
	MinChars int   `json:"min_chars,omitempty"`
}

type ThinkingInjectionConfig struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// AppUpdateConfig governs the optional self-update service. The service only
// applies releases inside a container started by the bundled entrypoint; the
// check itself is safe to enable in every deployment.
type AppUpdateConfig struct {
	Enabled              *bool `json:"enabled,omitempty"`
	AutoDownload         *bool `json:"auto_download,omitempty"`
	AutoApply            *bool `json:"auto_apply,omitempty"`
	CheckIntervalMinutes int   `json:"check_interval_minutes,omitempty"`
}

// PromptLimitSettings is a fully-resolved, defaults-applied snapshot of the
// prompt_limit block. It is a plain value type with no pointers so that one
// atomic read (config.Store.PromptLimitSnapshot) can be passed by value
// through a request without any further locking, and so the compress and
// enforce phases of a request provably observe identical settings.
type PromptLimitSettings struct {
	Enabled                             bool
	MaxCharsDefault                     int
	MaxCharsExpert                      int
	MaxCharsDefaultConfigured           bool
	MaxCharsExpertConfigured            bool
	AutoCompressEnable                  bool
	KeepRecentTurns                     int
	KeepSystemMessage                   bool
	ProFlashCompressionEnable           bool
	ProFlashCompressionTarget           int
	SessionChunkingEnable               bool
	SessionChunkingTargetRatio          float64
	SessionChunkingMaxChunks            int
	SessionChunkingCommitTimeoutSeconds int
	SummaryCompactionEnable             bool
	SummaryCompactionThreshold          float64
	// IncrementalMaxTurns is an explicit local rollover policy for the
	// process-local pinned-session cache. Zero means unlimited; it is not an
	// assertion about an undocumented provider-side turn limit.
	IncrementalMaxTurns           int
	IncrementalRotationKeepRecent int
}

// ModelInputLimits contains provider-advertised hard input ceilings by
// upstream tier. Zero means that tier was not present in the settings payload.
type ModelInputLimits struct {
	Default int
	Expert  int
}

// DefaultPromptLimitSettings returns the settings used when the operator has
// not configured prompt_limit at all. Also the fallback when no config Store is
// available (tests, nil store), so behaviour is identical either way.
func DefaultPromptLimitSettings() PromptLimitSettings {
	return PromptLimitSettings{
		Enabled:         true,
		MaxCharsDefault: defaultPromptMaxCharsDefault,
		MaxCharsExpert:  defaultPromptMaxCharsExpert,
		// Automatic history dropping is opt-in. A silent rewrite of an
		// over-limit request can lose context; callers may still request
		// explicit Responses compaction or enable this setting deliberately.
		AutoCompressEnable:                  false,
		KeepRecentTurns:                     defaultPromptKeepRecentTurns,
		KeepSystemMessage:                   true,
		ProFlashCompressionEnable:           false,
		ProFlashCompressionTarget:           defaultPromptMaxCharsExpert,
		SessionChunkingEnable:               false,
		SessionChunkingTargetRatio:          0.85,
		SessionChunkingMaxChunks:            16,
		SessionChunkingCommitTimeoutSeconds: 30,
		SummaryCompactionEnable:             false,
		SummaryCompactionThreshold:          0.8,
		IncrementalMaxTurns:                 0,
		IncrementalRotationKeepRecent:       defaultPromptKeepRecentTurns,
	}
}

// PromptLimitConfig governs prompt size limits before sending to upstream.
// Expert mode (deepseek-v4-pro) has stricter context limits than default mode
// (deepseek-v4-flash). When AutoCompress is enabled, oversized prompts are
// automatically compressed by dropping older conversation turns while
// preserving the system message and most recent turns.
type PromptLimitConfig struct {
	Enabled                             *bool   `json:"enabled,omitempty"`
	MaxCharsDefault                     int     `json:"max_chars_default,omitempty"`                       // limit for flash/default models (default 380000, empirical)
	MaxCharsExpert                      int     `json:"max_chars_expert,omitempty"`                        // limit for pro/expert models (default 150000, empirical reliability knee)
	AutoCompressEnabled                 *bool   `json:"auto_compress_enabled,omitempty"`                   // auto-compress when over limit
	CompressKeepRecent                  int     `json:"compress_keep_recent,omitempty"`                    // recent turns to preserve (default 6)
	CompressKeepSystem                  *bool   `json:"compress_keep_system,omitempty"`                    // always keep system message (default true)
	ProFlashCompressionEnabled          *bool   `json:"pro_flash_compression_enabled,omitempty"`           // use a real Flash request to summarize oversized Pro history
	ProFlashCompressionTargetChars      int     `json:"pro_flash_compression_target_chars,omitempty"`      // target UTF-16 units for the summarized Pro prompt
	SessionChunkingEnabled              *bool   `json:"session_chunking_enabled,omitempty"`                // preserve original prompt by committing bounded fragments to one upstream session
	SessionChunkingTargetRatio          float64 `json:"session_chunking_target_ratio,omitempty"`           // fraction of the active model limit used by each fragment
	SessionChunkingMaxChunks            int     `json:"session_chunking_max_chunks,omitempty"`             // hard safety cap for one request
	SessionChunkingCommitTimeoutSeconds int     `json:"session_chunking_commit_timeout_seconds,omitempty"` // wait for upstream reasoning/content before advancing
	SummaryCompactionEnabled            *bool   `json:"summary_compaction_enabled,omitempty"`              // server-side Flash summary before the model limit
	SummaryCompactionThreshold          float64 `json:"summary_compaction_threshold,omitempty"`            // fraction of the active model window that triggers a summary
	IncrementalMaxTurns                 *int    `json:"incremental_max_turns,omitempty"`                   // explicit local session rollover threshold; 0 disables
	IncrementalRotationKeepRecent       int     `json:"incremental_rotation_keep_recent,omitempty"`        // recent turns retained after rollover
}
