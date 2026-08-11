package config

import (
	"os"
	"strconv"
	"strings"
)

func (s *Store) ModelAliases() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := DefaultModelAliases()
	for k, v := range s.cfg.ModelAliases {
		key := strings.TrimSpace(lower(k))
		val := strings.TrimSpace(lower(v))
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	return out
}

func (s *Store) CompatWideInputStrictOutput() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Compat.WideInputStrictOutput == nil {
		return true
	}
	return *s.cfg.Compat.WideInputStrictOutput
}

func (s *Store) CompatStripReferenceMarkers() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Compat.StripReferenceMarkers == nil {
		return true
	}
	return *s.cfg.Compat.StripReferenceMarkers
}

func (s *Store) ToolcallMode() string {
	return "feature_match"
}

func (s *Store) ToolcallEarlyEmitConfidence() string {
	return "high"
}

func (s *Store) ResponsesStoreTTLSeconds() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Responses.StoreTTLSeconds > 0 {
		return s.cfg.Responses.StoreTTLSeconds
	}
	return 900
}

func (s *Store) EmbeddingsProvider() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.Embeddings.Provider)
}

func (s *Store) AutoDeleteMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mode := strings.ToLower(strings.TrimSpace(s.cfg.AutoDelete.Mode))
	switch mode {
	case "none", "single", "all":
		return mode
	}
	if s.cfg.AutoDelete.Sessions {
		return "all"
	}
	return "none"
}

// SafetyBlockMessage returns the operator-configured message returned to
// clients when a safety policy blocks a request. Empty string falls back
// to the handler default.
func (s *Store) SafetyBlockMessage() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.Safety.BlockMessage)
}

func (s *Store) AdminPasswordHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.Admin.PasswordHash)
}

func (s *Store) AdminKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.Admin.Key)
}

func (s *Store) AdminJWTSecret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.Admin.JWTSecret)
}

func (s *Store) AdminJWTExpireHours() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Admin.JWTExpireHours > 0 {
		return s.cfg.Admin.JWTExpireHours
	}
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_JWT_EXPIRE_HOURS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return 24
}

func (s *Store) AdminJWTValidAfterUnix() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Admin.JWTValidAfterUnix
}

func (s *Store) RuntimeAccountMaxInflight() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Runtime.AccountMaxInflight > 0 {
		return s.cfg.Runtime.AccountMaxInflight
	}
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ACCOUNT_MAX_INFLIGHT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

func (s *Store) RuntimeAccountMaxQueue(defaultSize int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Runtime.AccountMaxQueue > 0 {
		return s.cfg.Runtime.AccountMaxQueue
	}
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ACCOUNT_MAX_QUEUE")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return n
		}
	}
	if defaultSize < 0 {
		return 0
	}
	return defaultSize
}

func (s *Store) RuntimeGlobalMaxInflight(defaultSize int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Runtime.GlobalMaxInflight > 0 {
		return s.cfg.Runtime.GlobalMaxInflight
	}
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_GLOBAL_MAX_INFLIGHT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	if defaultSize < 0 {
		return 0
	}
	return defaultSize
}

func (s *Store) RuntimeTokenRefreshIntervalHours() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Runtime.TokenRefreshIntervalHours > 0 {
		return s.cfg.Runtime.TokenRefreshIntervalHours
	}
	return 6
}

func (s *Store) RuntimeAccountHealthCheckIntervalMinutes() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Runtime.AccountHealthCheckIntervalMinutes > 0 {
		return s.cfg.Runtime.AccountHealthCheckIntervalMinutes
	}
	return 0
}

func (s *Store) AutoDeleteSessions() bool {
	return s.AutoDeleteMode() != "none"
}

func (s *Store) HistorySplitEnabled() bool {
	return false
}

func (s *Store) HistorySplitTriggerAfterTurns() int {
	return 1
}

func (s *Store) CurrentInputFileEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.CurrentInputFile.Enabled == nil {
		return true
	}
	return *s.cfg.CurrentInputFile.Enabled
}

func (s *Store) CurrentInputFileMinChars() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.CurrentInputFile.MinChars
}

func (s *Store) ThinkingInjectionEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.ThinkingInjection.Enabled == nil {
		return true
	}
	return *s.cfg.ThinkingInjection.Enabled
}

func (s *Store) ThinkingInjectionPrompt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.ThinkingInjection.Prompt)
}

// RemoteFileUploadEnabled reports whether inline attachments and the
// current-input-file transcript should be forwarded to the upstream DeepSeek
// upload_file endpoint. The production default is false because that endpoint
// is per-account rate-limited and dominated the failure rate on busy
// workloads. Operators can opt in via the
// DEEPSEEK_WEB_TO_API_REMOTE_FILE_UPLOAD_ENABLED env var when they have
// headroom; the JSON config field server.remote_file_upload_enabled is also
// honoured for parity with other knobs.
func (s *Store) RemoteFileUploadEnabled() bool {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_REMOTE_FILE_UPLOAD_ENABLED")); raw != "" {
		return parseBoolDefault(raw, false)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Server.RemoteFileUploadEnabled != nil {
		return *s.cfg.Server.RemoteFileUploadEnabled
	}
	return false
}

// Default prompt-size ceilings, calibrated from production chat_history
// (deduped by conversation to strip the ~8:1 cross-account retry inflation):
//
//   - expert (deepseek-v4-pro / -pro-search): every deduped request <150k chars
//     succeeded (15/15); above 150k the pass rate fell to ~37% (3/8). This is a
//     soft reliability cliff, NOT a hard rejection — failures surface as
//     upstream_empty_output (retry exhaustion), and one 223k request did
//     succeed. 150k is where reliability was still 100%.
//   - default (deepseek-v4-flash): no observed ceiling — the largest deduped
//     success was 380k with no size-correlated degradation, so the default is
//     set at that observed max.
//
// Sample sizes in the high buckets are small (n=3..5), so treat these as tuned
// defaults, not physical limits; operators override via prompt_limit.max_chars_*.
// See docs/prompt-compatibility.md for the calibration method.
const (
	defaultPromptMaxCharsDefault = 380000
	defaultPromptMaxCharsExpert  = 150000
	defaultPromptKeepRecentTurns = 6
)

// promptLimitLocked resolves the prompt_limit block against its defaults.
// Caller must already hold s.mu (read or write).
func (s *Store) promptLimitLocked() PromptLimitSettings {
	out := DefaultPromptLimitSettings()
	pl := s.cfg.PromptLimit
	if pl.Enabled != nil {
		out.Enabled = *pl.Enabled
	}
	if pl.MaxCharsDefault > 0 {
		out.MaxCharsDefault = pl.MaxCharsDefault
		out.MaxCharsDefaultConfigured = true
	}
	if pl.MaxCharsExpert > 0 {
		out.MaxCharsExpert = pl.MaxCharsExpert
		out.MaxCharsExpertConfigured = true
	}
	if pl.AutoCompressEnabled != nil {
		out.AutoCompressEnable = *pl.AutoCompressEnabled
	}
	if pl.CompressKeepRecent > 0 {
		out.KeepRecentTurns = pl.CompressKeepRecent
	}
	if pl.CompressKeepSystem != nil {
		out.KeepSystemMessage = *pl.CompressKeepSystem
	}
	if pl.ProFlashCompressionEnabled != nil {
		out.ProFlashCompressionEnable = *pl.ProFlashCompressionEnabled
	}
	if pl.ProFlashCompressionTargetChars > 0 {
		out.ProFlashCompressionTarget = pl.ProFlashCompressionTargetChars
	}
	if pl.IncrementalMaxTurns != nil && *pl.IncrementalMaxTurns >= 0 {
		out.IncrementalMaxTurns = *pl.IncrementalMaxTurns
	}
	if pl.IncrementalRotationKeepRecent > 0 {
		out.IncrementalRotationKeepRecent = pl.IncrementalRotationKeepRecent
	}
	return out
}

// PromptLimitSnapshot reads every prompt_limit knob under a SINGLE read lock.
//
// Callers must use this rather than the per-field accessors below when more
// than one field is needed. Reading fields one at a time takes six separate
// locks, so a concurrent Store.Update (hot reload, or a future admin PUT) can
// land between them and yield a torn mix of old and new values. Worse, the
// compress and enforce phases of one request would each re-read: compression
// could see a high ceiling and decline to trim, then enforcement could see a
// freshly-lowered ceiling and reject with 413 a request that was never given
// the chance to shrink. One snapshot per request makes the whole request
// observe one coherent config.
func (s *Store) PromptLimitSnapshot() PromptLimitSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked()
}

// PromptLimitEnabled returns whether prompt size checking is enabled.
func (s *Store) PromptLimitEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked().Enabled
}

// PromptLimitMaxCharsDefault returns the max prompt chars for flash/default models.
func (s *Store) PromptLimitMaxCharsDefault() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked().MaxCharsDefault
}

// PromptLimitMaxCharsExpert returns the max prompt chars for pro/expert models.
func (s *Store) PromptLimitMaxCharsExpert() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked().MaxCharsExpert
}

// PromptLimitAutoCompressEnabled returns whether auto-compression is enabled.
func (s *Store) PromptLimitAutoCompressEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked().AutoCompressEnable
}

// PromptLimitCompressKeepRecent returns number of recent turns to preserve.
func (s *Store) PromptLimitCompressKeepRecent() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked().KeepRecentTurns
}

// PromptLimitCompressKeepSystem returns whether to keep the system message.
func (s *Store) PromptLimitCompressKeepSystem() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promptLimitLocked().KeepSystemMessage
}
