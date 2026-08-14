package shared

import (
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

// PromptLimitSnapshot reads the operator's prompt_limit block once, under a
// single lock, and returns the resolved settings.
//
// Callers MUST take one snapshot per request and pass it to both
// CompressPromptBeforeCIF and EnforcePromptLimit. Re-reading the store in each
// phase is a race: a PUT that lowers max_chars_expert between the two phases
// makes the compressor decide "fits, no work needed" and the enforcer then
// reject the very same prompt with a 413 — a spurious failure for a request
// that was never given the chance to compress.
func PromptLimitSnapshot(store ConfigReader) config.PromptLimitSettings {
	if store == nil {
		return config.DefaultPromptLimitSettings()
	}
	return store.PromptLimitSnapshot()
}

// CompressPromptBeforeCIF drops the oldest turns when the assembled prompt
// exceeds the model's character budget.
//
// This MUST run before applyCurrentInputFile. CIF collapses stdReq.Messages
// into a single synthetic user message holding the whole transcript, and once
// that has happened there are no per-turn boundaries left to trim — the
// compressor would see a one-element slice and no-op. Running here means CIF
// builds its transcript from already-trimmed history, so the reduction actually
// reaches upstream.
//
// Returns how many messages were dropped and whether compression fired, so the
// caller can log it. Never fails: a prompt that is still oversized afterwards is
// caught by EnforcePromptLimit once the final prompt is known.
func CompressPromptBeforeCIF(cfg config.PromptLimitSettings, stdReq *promptcompat.StandardRequest) (dropped int, ok bool) {
	if stdReq == nil || !cfg.Enabled {
		return 0, false
	}
	before := len(stdReq.Messages)
	compressed, changed := promptcompat.CompressToFit(cfg, *stdReq)
	if !changed {
		return 0, false
	}
	*stdReq = compressed
	return before - len(compressed.Messages), true
}

// EnforcePromptLimit is the post-CIF backstop. It returns a client-facing
// message when the final prompt still exceeds the model's budget, and "" when
// the request may proceed.
//
// A separate check is required even after CompressPromptBeforeCIF: CIF inlines
// the full history transcript plus its instruction blocks into the prompt, so
// the byte count upstream actually sees is only known here. It can exceed the
// budget even when the pre-CIF message list did not.
func EnforcePromptLimit(cfg config.PromptLimitSettings, stdReq promptcompat.StandardRequest) string {
	if !cfg.Enabled {
		return ""
	}
	return promptcompat.LimitExceededMessage(cfg, stdReq.FinalPrompt, promptcompat.EffectiveModel(stdReq))
}

// EnforcePromptLimitBeforeCIF avoids rebuilding and tokenizing a transcript
// that is already known to exceed the provider limit. This early rejection is
// only valid for inline CIF mode: a real remote file upload can replace the
// prompt body with a short file reference, and Pro-to-Flash compression may
// still intentionally reduce an oversized prompt later in the pipeline.
func EnforcePromptLimitBeforeCIF(cfg config.PromptLimitSettings, stdReq promptcompat.StandardRequest, remoteFileUploadEnabled bool) string {
	if remoteFileUploadEnabled || cfg.ProFlashCompressionEnable || cfg.SessionChunkingEnable {
		return ""
	}
	return EnforcePromptLimit(cfg, stdReq)
}

// PromptLimitForModel reports the character budget applied to a model, and
// whether it was treated as an expert/Pro tier. Exposed for logging.
func PromptLimitForModel(cfg config.PromptLimitSettings, model string) (limit int, expert bool) {
	expert = promptcompat.IsExpertModel(model)
	if expert {
		return cfg.MaxCharsExpert, true
	}
	return cfg.MaxCharsDefault, false
}
