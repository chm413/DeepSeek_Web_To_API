package chat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/safetyllm"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
)

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	// Read body first so we can compute a session-affinity key before
	// acquiring an account from the pool. Same Claude Code / Codex
	// conversation → same account, preserving upstream session context.
	r.Body = http.MaxBytesReader(w, r.Body, openAIGeneralMaxSize)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if errors.Is(err, requestbody.ErrInvalidUTF8Body) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: invalid utf-8 request body")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json")
		return
	}

	callerAuth, err := h.Auth.DetermineCaller(r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	historyStdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, requestTraceID(r))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	historyStdReq = shared.ApplyThinkingInjection(h.Store, historyStdReq)
	historySession := startQueuedChatHistory(h.ChatHistory, r, callerAuth, historyStdReq)

	a, err := h.Auth.DetermineWithSession(r, rawBody)
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		if historySession != nil {
			historySession.error(status, detail, "error", "", "")
		}
		writeOpenAIError(w, status, detail)
		return
	}
	if historySession != nil {
		historySession.bindAuth(a)
	}
	var sessionID string
	defer func() {
		h.autoDeleteRemoteSession(r.Context(), a, sessionID)
		h.Auth.Release(a)
	}()

	r = r.WithContext(auth.WithAuth(r.Context(), a))
	if err := h.preprocessInlineFileInputs(r.Context(), a, req); err != nil {
		if historySession != nil {
			historySession.error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIInlineFileError(w, err)
		return
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, requestTraceID(r))
	if err != nil {
		if historySession != nil {
			historySession.error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	stdReq = shared.ApplyThinkingInjection(h.Store, stdReq)
	shared.LogIncrementalRequestContext("chat.completions", a, stdReq, len(rawBody))
	incrementalBaseReq := stdReq
	promptLimit := h.Store.PromptLimitSnapshot()
	if h.tryIncrementalChat(w, r, a, &stdReq, promptLimit, historySession, &sessionID) {
		return
	}
	// Trim the oldest turns before CIF runs. CIF collapses Messages into a
	// single synthetic user message, so once it has run there are no turn
	// boundaries left to drop — compressing afterwards would silently no-op.
	dynamicLimitApplied := false
	promptLimit, dynamicLimitApplied, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "chat.completions", "error", err)
	}
	if !stdReq.IncrementalSessionRotated {
		if dropped, ok := shared.CompressPromptBeforeCIF(promptLimit, &stdReq); ok {
			limit, expert := shared.PromptLimitForModel(promptLimit, stdReq.ResolvedModel)
			config.Logger.Info("[prompt_limit] compressed history before CIF",
				"surface", "chat.completions", "model", stdReq.ResolvedModel,
				"expert", expert, "limit_chars", limit,
				"dropped_messages", dropped, "prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "dynamic_upstream_limit", dynamicLimitApplied)
		}
	}
	if errMsg := shared.EnforcePromptLimitBeforeCIF(promptLimit, stdReq, h.Store.RemoteFileUploadEnabled()); errMsg != "" {
		config.Logger.Info("[prompt_limit] rejected before CIF",
			"surface", "chat.completions", "model", stdReq.ResolvedModel,
			"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "auto_compress_enabled", promptLimit.AutoCompressEnable)
		if historySession != nil {
			historySession.error(http.StatusRequestEntityTooLarge, errMsg, "error", "prompt_too_large", "")
		}
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
		return
	}
	cifStartedAt := time.Now()
	stdReq, err = h.applyCurrentInputFile(r.Context(), a, stdReq)
	cifDuration := time.Since(cifStartedAt)
	if err != nil {
		status, message := mapCurrentInputFileError(err)
		if historySession != nil {
			historySession.error(status, message, "error", "", "")
		}
		writeOpenAIError(w, status, message)
		return
	}
	recordCurrentInputMetrics(stdReq, cifDuration)
	if historySession != nil {
		historySession.updateHistoryText(stdReq.HistoryText)
		historySession.updateCurrentInputState(stdReq)
	}

	// v1.0.14: LLM-based binary safety check after auth + CIF assembly so
	// the audited text matches what we'd send upstream. Pre-CIF we'd miss
	// content the operator considers part of the conversation; post-CIF
	// we get the full prompt the model would actually see.
	if shared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, shared.PickAuditText(stdReq.LatestUserText, stdReq.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return
	}

	// Final gate. Expert/Pro models take a smaller upstream context than the
	// flash tier, and CIF inlines the whole transcript into one message, so the
	// assembled prompt can still exceed the ceiling even after compression.
	// Reject here rather than letting upstream return an opaque empty output.
	var chunkedPrompt *shared.SessionChunkingPreparation
	if shared.EnforcePromptLimit(promptLimit, stdReq) != "" && promptLimit.SessionChunkingEnable {
		chunkedPrompt, err = shared.TryPrepareRootSessionChunkingWithFailover(r.Context(), h.DS, h.Auth, a, stdReq, promptLimit)
		if err != nil {
			config.Logger.Error("[prompt_limit] same-session chunking failed",
				"surface", "chat.completions", "model", stdReq.ResolvedModel,
				"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "error", err)
			if shared.IsSessionCapacityRateLimit(err) {
				writeCompletionCallError(w, historySession, err, "", "")
				return
			}
			if historySession != nil {
				historySession.error(http.StatusBadGateway, err.Error(), "error", "session_chunking_failed", "")
			}
			writeOpenAIError(w, http.StatusBadGateway, "same-session prompt chunking failed: "+err.Error())
			return
		}
		if chunkedPrompt != nil {
			sessionID = chunkedPrompt.SessionID
			promptLimit = chunkedPrompt.PromptLimit
		}
	}
	if chunkedPrompt == nil && shared.EnforcePromptLimit(promptLimit, stdReq) != "" && promptLimit.ProFlashCompressionEnable {
		compressed, ok, compressErr := shared.TryFlashCompressPrompt(r.Context(), h.DS, a, stdReq, promptLimit, h.Store.AutoDeleteMode())
		if compressErr != nil {
			config.Logger.Warn("[prompt_limit] Flash compression failed; returning original overflow",
				"surface", "chat.completions", "model", stdReq.ResolvedModel, "error", compressErr)
		} else if ok {
			stdReq = compressed
			config.Logger.Info("[prompt_limit] compressed Pro history with Flash",
				"surface", "chat.completions", "model", stdReq.ResolvedModel,
				"thinking", stdReq.Thinking, "prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt))
		}
	}
	if chunkedPrompt == nil {
		if errMsg := shared.EnforcePromptLimit(promptLimit, stdReq); errMsg != "" {
			if historySession != nil {
				historySession.error(http.StatusRequestEntityTooLarge, errMsg, "error", "prompt_too_large", "")
			}
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
			return
		}
	}

	var (
		pow     string
		payload map[string]any
		resp    *http.Response
	)
rootDispatch:
	for {
		if chunkedPrompt == nil {
			sessionCapacityRetried := false
			restartAsChunks := false
			for {
				root, rootErr := shared.PrepareRootSessionWithPinnedPow(r.Context(), h.DS, h.Auth, a, stdReq, promptLimit)
				if rootErr != nil {
					if message, limited := shared.RootSessionPromptLimitMessage(rootErr); limited {
						if replacementCfg, ok := shared.RootSessionPromptLimitSettings(rootErr); ok && replacementCfg.SessionChunkingEnable {
							chunkedPrompt, err = shared.TryPrepareRootSessionChunkingWithFailover(r.Context(), h.DS, h.Auth, a, stdReq, replacementCfg)
							if err == nil && chunkedPrompt != nil {
								promptLimit = chunkedPrompt.PromptLimit
								restartAsChunks = true
								break
							}
							if err != nil {
								config.Logger.Error("[prompt_limit] replacement-account chunking failed",
									"surface", "chat.completions", "model", stdReq.ResolvedModel,
									"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "error", err)
								if shared.IsSessionCapacityRateLimit(err) {
									writeCompletionCallError(w, historySession, err, "", "")
									return
								}
							}
						}
						if historySession != nil {
							historySession.error(http.StatusRequestEntityTooLarge, message, "error", "prompt_too_large", "")
						}
						writeOpenAIError(w, http.StatusRequestEntityTooLarge, message)
						return
					}
					if shared.RootSessionErrorIsPow(rootErr) {
						writePowCallError(w, historySession, rootErr)
					} else {
						writeSessionCallError(w, historySession, rootErr)
					}
					return
				}
				promptLimit = root.PromptLimit
				sessionID = root.SessionID
				pow = root.Pow
				payload = stdReq.CompletionPayload(sessionID)
				resp, err = shared.CallRootSessionPinnedCompletion(r.Context(), h.DS, a, payload, pow)
				if err == nil {
					break
				}
				if shared.IsSessionCapacityRateLimit(err) {
					if sessionCapacityRetried {
						break
					}
					sessionCapacityRetried = true
				}
				nextCfg, restarted, restartErr := shared.RestartRootSessionAfterPinnedFailure(r.Context(), h.DS, h.Auth, a, stdReq, promptLimit, sessionID, err)
				if !restarted {
					break
				}
				if message, limited := shared.RootSessionPromptLimitMessage(restartErr); limited {
					if replacementCfg, ok := shared.RootSessionPromptLimitSettings(restartErr); ok && replacementCfg.SessionChunkingEnable {
						chunkedPrompt, err = shared.TryPrepareRootSessionChunkingWithFailover(r.Context(), h.DS, h.Auth, a, stdReq, replacementCfg)
						if err == nil && chunkedPrompt != nil {
							promptLimit = chunkedPrompt.PromptLimit
							restartAsChunks = true
							break
						}
						if err != nil {
							config.Logger.Error("[prompt_limit] replacement-account chunking failed",
								"surface", "chat.completions", "model", stdReq.ResolvedModel,
								"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "error", err)
							if shared.IsSessionCapacityRateLimit(err) {
								writeCompletionCallError(w, historySession, err, "", "")
								return
							}
						}
					}
					if historySession != nil {
						historySession.error(http.StatusRequestEntityTooLarge, message, "error", "prompt_too_large", "")
					}
					writeOpenAIError(w, http.StatusRequestEntityTooLarge, message)
					return
				}
				if restartErr != nil {
					err = restartErr
					break
				}
				promptLimit = nextCfg
			}
			if restartAsChunks {
				continue rootDispatch
			}
			break rootDispatch
		}
		for {
			pow, err = shared.GetPinnedPow(r.Context(), h.DS, a)
			if err != nil {
				next, restarted, replayErr := shared.RestartRootSessionChunkingAfterPinnedFailure(r.Context(), h.DS, h.Auth, a, stdReq, promptLimit, chunkedPrompt, err)
				if restarted {
					if replayErr != nil {
						err = replayErr
						break
					}
					chunkedPrompt = next
					continue
				}
				powDetail := shared.PowErrorDetail(err)
				if powDetail.Code != "authentication_failed" {
					writePowCallError(w, historySession, err)
					return
				}
				if !a.UseConfigToken {
					a.MarkDirectTokenInvalid()
				}
				if historySession != nil {
					historySession.error(http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).", "error", "", "")
				}
				writeOpenAIError(w, http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).")
				return
			}
			payload = stdReq.CompletionPayload(chunkedPrompt.SessionID)
			payload["prompt"] = chunkedPrompt.FinalWirePrompt
			payload["parent_message_id"] = chunkedPrompt.ParentMessageID
			resp, err = shared.CallPinnedCompletion(r.Context(), h.DS, a, payload, pow)
			if err == nil {
				sessionID = chunkedPrompt.SessionID
				break
			}
			next, restarted, replayErr := shared.RestartRootSessionChunkingAfterPinnedFailure(r.Context(), h.DS, h.Auth, a, stdReq, promptLimit, chunkedPrompt, err)
			if !restarted {
				break
			}
			if replayErr != nil {
				err = replayErr
				break
			}
			chunkedPrompt = next
		}
		break rootDispatch
	}
	if nextSessionID := strings.TrimSpace(asString(payload["chat_session_id"])); nextSessionID != "" {
		sessionID = nextSessionID
	}
	if err != nil {
		if !a.UseConfigToken && shared.CompletionErrorDetail(err).Status == http.StatusUnauthorized {
			a.MarkDirectTokenInvalid()
		}
		writeCompletionCallError(w, historySession, err, "", "")
		return
	}
	refFileTokens := stdReq.RefFileTokens
	var outcome chatCompletionOutcome
	if stdReq.Stream {
		outcome = h.handleStreamWithRetry(w, r, a, resp, payload, pow, sessionID, stdReq.ResponseModel, stdReq.FinalPrompt, stdReq, promptLimit, refFileTokens, stdReq.Thinking, stdReq.ExposeReasoning, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice.IsRequired(), historySession, &sessionID)
	} else {
		outcome = h.handleNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, sessionID, stdReq.ResponseModel, stdReq.FinalPrompt, stdReq, promptLimit, refFileTokens, stdReq.Thinking, stdReq.ExposeReasoning, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice.IsRequired(), historySession, &sessionID)
	}
	if outcome.sessionID != "" {
		sessionID = outcome.sessionID
	}
	h.recordFullChatIncrementalState(a, incrementalBaseReq, sessionID, outcome)
}

func (h *Handler) tryIncrementalChat(w http.ResponseWriter, r *http.Request, a *auth.RequestAuth, stdReq *promptcompat.StandardRequest, promptLimit config.PromptLimitSettings, historySession *chatHistorySession, activeSessionID *string) bool {
	if stdReq == nil {
		return false
	}
	autoDeleteMode := h.Store.AutoDeleteMode()
	lease, incrementalPrompt, ok := shared.PrepareIncrementalRequestWithSettings(h.Incremental, h.DS, autoDeleteMode, a, *stdReq, stdReq.Messages, promptLimit)
	if !ok {
		return false
	}
	defer func() {
		if lease != nil {
			lease.Invalidate()
		}
	}()
	if lease.Rotate {
		dropped, applied := shared.ApplyIncrementalSessionRotation(stdReq, lease, promptLimit)
		if !applied {
			return false
		}
		config.Logger.Info("[incremental] rotating upstream session",
			"surface", "chat.completions", "session_key", a.SessionKey,
			"previous_session_id", lease.SessionID, "completed_turns", lease.TurnCount,
			"configured_max_turns", promptLimit.IncrementalMaxTurns,
			"dropped_messages", dropped, "rotation_prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
			"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt))
		lease.Invalidate()
		lease = nil
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		return false
	}
	fullPrompt := stdReq.FinalPrompt
	fullRootReq := *stdReq
	stdReq.FinalPrompt = incrementalPrompt
	if activeSessionID != nil {
		*activeSessionID = lease.SessionID
	}
	config.Logger.Info("[incremental] reused upstream session",
		"surface", "chat.completions", "session_key", a.SessionKey,
		"parent_message_id", lease.ParentMessageID,
		"match_mode", lease.MatchMode,
		"retained_messages", len(stdReq.Messages)-len(lease.DeltaMessages),
		"delta_messages", len(lease.DeltaMessages),
		"full_prompt_units", promptcompat.PromptUnits(fullPrompt),
		"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt),
		"incremental_prompt_units", promptcompat.PromptUnits(incrementalPrompt))

	var err error
	promptLimit, _, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
	if err != nil {
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "chat.completions.incremental", "error", err)
	}
	if shared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, shared.PickAuditText(stdReq.LatestUserText, stdReq.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return true
	}
	var chunkedPrompt *shared.SessionChunkingPreparation
	if shared.EnforcePromptLimit(promptLimit, *stdReq) != "" && promptLimit.SessionChunkingEnable {
		chunkedPrompt, err = shared.TryPrepareSessionChunking(r.Context(), h.DS, a, *stdReq, promptLimit, lease.SessionID, lease.ParentMessageID)
		if err != nil {
			if shared.IsSessionCapacityRateLimit(err) || shared.IsRetryableSessionChunkingFailure(err) {
				reason := "existing upstream session reached capacity during chunk preparation"
				if shared.IsRetryableSessionChunkingFailure(err) && !shared.IsSessionCapacityRateLimit(err) {
					reason = "incremental chunk branch lost before an upstream fragment commit"
				}
				config.Logger.Warn("[incremental] chunk preparation cannot safely advance the retained branch; rebuilding full context",
					"surface", "chat.completions", "session_key", a.SessionKey,
					"turn_count", lease.TurnCount, "reason", reason, "error", err)
				if activeSessionID != nil {
					*activeSessionID = ""
				}
				stdReq.FinalPrompt = fullPrompt
				return false
			}
			if shared.SwitchManagedAccountForPinnedBranch(r.Context(), h.Auth, a, err) {
				config.Logger.Warn("[incremental] pinned chunk preparation failed; rebuilding full context on another account",
					"surface", "chat.completions", "session_key", a.SessionKey,
					"error", err)
				if activeSessionID != nil {
					*activeSessionID = ""
				}
				stdReq.FinalPrompt = fullPrompt
				return false
			}
			if historySession != nil {
				historySession.error(http.StatusBadGateway, err.Error(), "error", "session_chunking_failed", "")
			}
			writeOpenAIError(w, http.StatusBadGateway, "same-session prompt chunking failed: "+err.Error())
			return true
		}
	}
	if chunkedPrompt == nil {
		if errMsg := shared.EnforcePromptLimit(promptLimit, *stdReq); errMsg != "" {
			if historySession != nil {
				historySession.error(http.StatusRequestEntityTooLarge, errMsg, "error", "prompt_too_large", "")
			}
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
			return true
		}
	}
	// Every incremental request still targets a child of lease.SessionID,
	// including an ordinary short delta. It must not use a PoW path that can
	// silently change accounts before the pinned branch is abandoned.
	pow, err := shared.GetPinnedPow(r.Context(), h.DS, a)
	if err != nil {
		if shared.IsSessionCapacityRateLimit(err) {
			config.Logger.Warn("[incremental] existing upstream session reached capacity while acquiring pinned PoW; rebuilding full context on the same account",
				"surface", "chat.completions", "session_key", a.SessionKey,
				"turn_count", lease.TurnCount, "error", err)
			if activeSessionID != nil {
				*activeSessionID = ""
			}
			stdReq.FinalPrompt = fullPrompt
			return false
		}
		if shared.SwitchManagedAccountForPinnedBranch(r.Context(), h.Auth, a, err) {
			config.Logger.Warn("[incremental] pinned PoW failed; rebuilding full context on another account",
				"surface", "chat.completions", "session_key", a.SessionKey,
				"error", err)
			if activeSessionID != nil {
				*activeSessionID = ""
			}
			stdReq.FinalPrompt = fullPrompt
			return false
		}
		powDetail := shared.PowErrorDetail(err)
		if powDetail.Stopped || powDetail.Status == http.StatusGatewayTimeout {
			writePowCallError(w, historySession, err)
			return true
		}
		if !a.UseConfigToken {
			a.MarkDirectTokenInvalid()
		}
		if historySession != nil {
			historySession.error(http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).", "error", "", "")
		}
		writeOpenAIError(w, http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).")
		return true
	}
	payload := stdReq.CompletionPayload(lease.SessionID)
	payload["parent_message_id"] = lease.ParentMessageID
	if chunkedPrompt != nil {
		payload["prompt"] = chunkedPrompt.FinalWirePrompt
		payload["parent_message_id"] = chunkedPrompt.ParentMessageID
	}
	resp, err := shared.CallPinnedCompletion(r.Context(), h.DS, a, payload, pow)
	if err != nil {
		if shared.IsSessionCapacityRateLimit(err) {
			config.Logger.Warn("[incremental] existing upstream session reached capacity; rebuilding full context on the same account",
				"surface", "chat.completions", "session_key", a.SessionKey,
				"turn_count", lease.TurnCount, "error", err)
		} else {
			switched := shared.SwitchManagedAccountForPinnedBranch(r.Context(), h.Auth, a, err)
			config.Logger.Warn("[incremental] pinned completion failed; falling back to full replay",
				"surface", "chat.completions", "session_key", a.SessionKey, "switched_account", switched, "error", err)
		}
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		stdReq.FinalPrompt = fullPrompt
		return false
	}
	var outcome chatCompletionOutcome
	if stdReq.Stream {
		outcome = h.handleStreamWithRetry(w, r, a, resp, payload, pow, lease.SessionID, stdReq.ResponseModel, stdReq.FinalPrompt, fullRootReq, promptLimit, stdReq.RefFileTokens, stdReq.Thinking, stdReq.ExposeReasoning, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice.IsRequired(), historySession, activeSessionID)
	} else {
		outcome = h.handleNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, lease.SessionID, stdReq.ResponseModel, stdReq.FinalPrompt, fullRootReq, promptLimit, stdReq.RefFileTokens, stdReq.Thinking, stdReq.ExposeReasoning, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice.IsRequired(), historySession, activeSessionID)
	}
	if outcome.success {
		if outcome.replayedRoot {
			stdReq.FinalPrompt = fullPrompt
			h.recordFullChatIncrementalState(a, *stdReq, outcome.sessionID, outcome)
		} else {
			lease.Complete(shared.IncrementalScope(a, *stdReq), stdReq.Messages, outcome.responseMessages, lease.SessionID, outcome.responseMessageID)
			lease = nil
		}
	}
	return true
}

func (h *Handler) recordFullChatIncrementalState(a *auth.RequestAuth, stdReq promptcompat.StandardRequest, sessionID string, outcome chatCompletionOutcome) {
	if h == nil || h.Incremental == nil || h.Store == nil || !outcome.success || !strings.EqualFold(strings.TrimSpace(h.Store.AutoDeleteMode()), "none") {
		return
	}
	scope := shared.IncrementalScope(a, stdReq)
	h.Incremental.Record(scope, stdReq.Messages, outcome.responseMessages, sessionID, outcome.responseMessageID)
	config.Logger.Info("[incremental] recorded upstream branch",
		"surface", "chat.completions", "session_key", scope.SessionKey,
		"variant", scope.Variant,
		"parent_message_id", outcome.responseMessageID,
		"request_messages", len(stdReq.Messages), "response_messages", len(outcome.responseMessages),
		"full_prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
		"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt))
}

func (h *Handler) handleNonStream(w http.ResponseWriter, resp *http.Response, completionID, model, finalPrompt string, refFileTokens int, thinkingEnabled, exposeReasoning, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) {
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if historySession != nil {
			historySession.error(resp.StatusCode, string(body), "error", "", "")
		}
		writeOpenAIError(w, resp.StatusCode, string(body))
		return
	}
	result := sse.CollectStream(resp, thinkingEnabled, true)

	stripReferenceMarkers := h.compatStripReferenceMarkers()
	finalThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	finalText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if searchEnabled {
		finalText = replaceCitationMarkersWithLinks(finalText, result.CitationLinks)
	}
	detected := detectAssistantToolCalls(result.Text, finalText, result.Thinking, result.ToolDetectionThinking, toolNames)
	if shouldWriteUpstreamEmptyOutputError(finalText, finalThinking) && len(detected.Calls) == 0 {
		status, message, code := upstreamEmptyOutputDetail(result.ContentFilter, finalText, finalThinking)
		if historySession != nil {
			historySession.error(status, message, code, finalThinking, finalText)
		}
		writeUpstreamEmptyOutputError(w, finalText, finalThinking, result.ContentFilter)
		return
	}
	respBody := openaifmt.BuildChatCompletionWithToolCallsVisibility(completionID, model, finalPrompt, finalThinking, finalText, detected.Calls, toolsRaw, exposeReasoning)
	addRefFileTokensToUsage(respBody, refFileTokens)
	finishReason := "stop"
	if choices, ok := respBody["choices"].([]map[string]any); ok && len(choices) > 0 {
		if fr, _ := choices[0]["finish_reason"].(string); strings.TrimSpace(fr) != "" {
			finishReason = fr
		}
	}
	if historySession != nil {
		historySession.success(http.StatusOK, finalThinking, finalText, finishReason, openaifmt.BuildChatUsageForModel(model, finalPrompt, finalThinking, finalText, refFileTokens))
	}
	writeJSON(w, http.StatusOK, respBody)
}

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request, resp *http.Response, completionID, model, finalPrompt string, refFileTokens int, thinkingEnabled, exposeReasoning, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if historySession != nil {
			historySession.error(resp.StatusCode, string(body), "error", "", "")
		}
		writeOpenAIError(w, resp.StatusCode, string(body))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_, canFlush := w.(http.Flusher)
	if !canFlush {
		config.Logger.Warn("[stream] response writer does not support flush; streaming may be buffered")
	}

	created := time.Now().Unix()
	bufferToolContent := len(toolNames) > 0
	emitEarlyToolDeltas := h.toolcallFeatureMatchEnabled() && h.toolcallEarlyEmitHighConfidence()
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	initialType := "text"
	if thinkingEnabled {
		initialType = "thinking"
	}

	streamRuntime := newChatStreamRuntime(
		w,
		rc,
		canFlush,
		completionID,
		created,
		model,
		finalPrompt,
		thinkingEnabled,
		exposeReasoning,
		searchEnabled,
		stripReferenceMarkers,
		toolNames,
		toolsRaw,
		false,
		bufferToolContent,
		emitEarlyToolDeltas,
	)
	streamRuntime.refFileTokens = refFileTokens

	streamengine.ConsumeSSE(streamengine.ConsumeConfig{
		Context:             r.Context(),
		Body:                resp.Body,
		ThinkingEnabled:     thinkingEnabled,
		InitialType:         initialType,
		KeepAliveInterval:   time.Duration(dsprotocol.KeepAliveTimeout) * time.Second,
		IdleTimeout:         time.Duration(dsprotocol.StreamIdleTimeout) * time.Second,
		MaxKeepAliveNoInput: dsprotocol.MaxKeepaliveCount,
	}, streamengine.ConsumeHooks{
		OnKeepAlive: func() {
			streamRuntime.sendKeepAlive()
		},
		OnParsed: func(parsed sse.LineResult) streamengine.ParsedDecision {
			decision := streamRuntime.onParsed(parsed)
			if historySession != nil {
				historySession.progress(streamRuntime.thinking.String(), streamRuntime.text.String())
			}
			return decision
		},
		OnFinalize: func(reason streamengine.StopReason, scannerErr error) {
			if failure, failed := streamengine.ClassifyTerminalFailure(reason, scannerErr); failed {
				config.Logger.Warn("[stream] upstream stream terminated abnormally",
					"surface", "chat.completions", "stop_reason", reason,
					"status", failure.Status, "code", failure.Code, "error", scannerErr)
				streamRuntime.sendFailedChunk(failure.Status, failure.Message, failure.Code)
			} else if string(reason) == "content_filter" {
				streamRuntime.finalize("content_filter", false)
			} else {
				streamRuntime.finalize("stop", false)
			}
			if historySession == nil {
				return
			}
			if streamRuntime.finalErrorMessage != "" {
				historySession.error(streamRuntime.finalErrorStatus, streamRuntime.finalErrorMessage, streamRuntime.finalErrorCode, streamRuntime.thinking.String(), streamRuntime.text.String())
				return
			}
			historySession.success(http.StatusOK, streamRuntime.finalThinking, streamRuntime.finalText, streamRuntime.finalFinishReason, streamRuntime.finalUsage)
		},
		OnContextDone: func() {
			if historySession != nil {
				historySession.stopped(streamRuntime.thinking.String(), streamRuntime.text.String(), string(streamengine.StopReasonContextCancelled))
			}
		},
	})
}
