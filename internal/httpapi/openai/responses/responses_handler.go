package responses

import (
	"DeepSeek_Web_To_API/internal/toolcall"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/httpapi/historycapture"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/safetyllm"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
	"DeepSeek_Web_To_API/internal/util"
)

func (h *Handler) GetResponseByID(w http.ResponseWriter, r *http.Request) {
	a, err := h.Auth.DetermineCaller(r)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}

	id := strings.TrimSpace(chi.URLParam(r, "response_id"))
	if id == "" {
		writeOpenAIError(w, http.StatusBadRequest, "response_id is required.")
		return
	}
	owner := responseStoreOwner(a)
	if owner == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	st := h.getResponseStore()
	item, ok := st.get(owner, id)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "Response not found.")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) Responses(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, openAIGeneralMaxSize)
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body too large")
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
	owner := responseStoreOwner(callerAuth)
	traceID := requestTraceID(r)
	previousResponseID := strings.TrimSpace(responseString(req["previous_response_id"]))
	inheritedSessionKey := ""
	if previousResponseID != "" {
		inheritedSessionKey, _ = h.getResponseStore().getSessionKey(owner, previousResponseID)
	}
	compactTriggered := removeCompactionTriggers(req)
	config.Logger.Info("[responses_request] received",
		"trace_id", traceID,
		"owner_fingerprint", responseStateFingerprint(owner),
		"wire_request_bytes", len(rawBody),
		"model", strings.TrimSpace(responseString(req["model"])),
		"stream", util.ToBool(req["stream"]),
		"previous_response_id_fingerprint", responseStateFingerprint(previousResponseID),
		"previous_response_id_present", previousResponseID != "",
		"compaction_triggered", compactTriggered,
		"input_items", responseStateItemCount(req["input"]),
		"input_bytes", responseStateSize(req["input"]),
		"message_items", responseStateItemCount(req["messages"]),
		"message_bytes", responseStateSize(req["messages"]),
		"tools_explicit", requestFieldPresent(req, "tools"),
		"tool_count", responseToolCount(req["tools"]),
		"tool_choice_explicit", requestFieldPresent(req, "tool_choice"),
		"tool_contract_fingerprint", responseToolContractFingerprint(
			req["tools"], requestFieldPresent(req, "tools"),
			req["tool_choice"], requestFieldPresent(req, "tool_choice")),
	)
	recoveredCompaction, err := h.expandLocalCompactionStateWithRecovery(owner, req)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := h.mergePreviousResponseInput(owner, req); err != nil {
		writeOpenAIError(w, http.StatusNotFound, err.Error())
		return
	}
	if compactTriggered {
		h.serveLocalCompaction(w, r, rawBody, req, true)
		return
	}
	compactThresholdTokens, compactThresholdApplied, compactThresholdErr := shared.ResponsesCompactThreshold(req)
	if compactThresholdErr != nil {
		writeOpenAIError(w, http.StatusBadRequest, compactThresholdErr.Error())
		return
	}
	historyStdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, req, traceID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	historyStdReq = shared.ApplyThinkingInjection(h.Store, historyStdReq)
	historySession := historycapture.StartWithStatus(h.ChatHistory, r, callerAuth, historyStdReq, "queued")

	// Authenticate against the expanded canonical request. For a
	// previous_response_id turn the wire body contains only the new input;
	// using it for session affinity would hash a different first user message
	// on every turn and prevent the retained upstream branch from being found.
	sessionBody := rawBody
	if expandedBody, marshalErr := json.Marshal(req); marshalErr == nil {
		sessionBody = expandedBody
	}
	var a *auth.RequestAuth
	if inheritedSessionKey != "" {
		if resolver, ok := h.Auth.(interface {
			DetermineWithSessionKey(req *http.Request, body []byte, sessionKey string) (*auth.RequestAuth, error)
		}); ok {
			a, err = resolver.DetermineWithSessionKey(r, sessionBody, inheritedSessionKey)
		} else {
			a, err = h.Auth.DetermineWithSession(r, sessionBody)
			if a != nil {
				a.SessionKey = inheritedSessionKey
			}
		}
	} else {
		a, err = h.Auth.DetermineWithSession(r, sessionBody)
	}
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		if historySession != nil {
			historySession.Error(status, detail, "error", "", "")
		}
		writeOpenAIError(w, status, detail)
		return
	}
	if historySession != nil {
		historySession.BindAuth(a)
	}
	var sessionID string
	defer func() {
		// Issue #20: /v1/responses must honor the WebUI auto-delete toggle
		// the same way /v1/chat/completions does. The session id may still
		// be empty if CreateSession failed below — the helper handles that.
		shared.AutoDeleteRemoteSession(r.Context(), h.DS, h.Store.AutoDeleteMode(), a.AccountID, a.DeepSeekToken, sessionID)
		h.Auth.Release(a)
	}()
	r = r.WithContext(auth.WithAuth(r.Context(), a))
	owner = responseStoreOwner(a)
	if owner == "" {
		if historySession != nil {
			historySession.Error(http.StatusUnauthorized, "unauthorized", "error", "", "")
		}
		writeOpenAIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := h.preprocessInlineFileInputs(r.Context(), a, req); err != nil {
		if historySession != nil {
			historySession.Error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIInlineFileError(w, err)
		return
	}
	stdReq, err := promptcompat.NormalizeOpenAIResponsesRequest(h.Store, req, traceID)
	if err != nil {
		if historySession != nil {
			historySession.Error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	stdReq = shared.ApplyThinkingInjection(h.Store, stdReq)
	shared.LogIncrementalRequestContext("responses", a, stdReq, len(rawBody))
	incrementalBaseReq := stdReq
	promptLimit := h.Store.PromptLimitSnapshot()
	ordinaryAutoCompress := promptLimit.AutoCompressEnable
	hardPromptLimit := promptLimit
	dynamicLimitApplied := false
	dynamicLimitResolved := false
	summaryHardLimit := 0
	var automaticCompactionItem map[string]any
	if recoveredCompaction.Handle != "" {
		promptLimit, dynamicLimitApplied, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
		dynamicLimitResolved = true
		if err != nil {
			config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses.compact_recovery", "error", err)
		}
		hardPromptLimit = promptLimit
		summaryHardLimit = promptcompat.LimitForModel(promptLimit, promptcompat.EffectiveModel(stdReq))
		if compactThresholdApplied {
			config.Logger.Info("[responses_compact] recognized request token threshold", "surface", "responses.compact_recovery", "model", stdReq.ResolvedModel, "compact_threshold_tokens", compactThresholdTokens)
		}
		if dropped, recovered := h.recoverExpiredCompaction(&stdReq, recoveredCompaction, promptLimit); recovered {
			config.Logger.Info("[responses_compact] rebuilt expired local state from fresh input",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"dropped_messages", dropped,
				"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
				"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt),
				"format_prompt_present", strings.TrimSpace(stdReq.IncrementalFormatPrompt) != "")
			incrementalBaseReq = stdReq
		} else {
			config.Logger.Warn("[responses_compact] fresh tail cannot fit after expired-state recovery",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"message_count", len(stdReq.Messages),
				"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
				"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt),
				"effective_limit_units", promptcompat.LimitForModel(promptLimit, promptcompat.EffectiveModel(stdReq)))
		}
	}
	if !dynamicLimitResolved {
		promptLimit, dynamicLimitApplied, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
		if err != nil {
			config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses", "error", err)
		}
		hardPromptLimit = promptLimit
	}
	if summaryHardLimit <= 0 {
		summaryHardLimit = promptcompat.LimitForModel(promptLimit, promptcompat.EffectiveModel(stdReq))
	}
	summaryTarget := 0
	summaryTrigger := ""
	if compactThresholdApplied {
		renderedTokens := util.CountPromptTokens(stdReq.FinalPrompt, stdReq.ResponseModel)
		summaryTarget = promptcompat.PromptUnits(stdReq.FinalPrompt) * 75 / 100
		config.Logger.Info("[responses_compact] evaluated request token threshold",
			"surface", "responses", "model", stdReq.ResolvedModel,
			"rendered_tokens", renderedTokens, "compact_threshold_tokens", compactThresholdTokens,
			"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt))
		if renderedTokens >= compactThresholdTokens {
			summaryTrigger = "request_compact_threshold"
		}
	} else if promptLimit.SummaryCompactionEnable && summaryHardLimit > 0 {
		threshold := promptLimit.SummaryCompactionThreshold
		if threshold <= 0 || threshold >= 1 {
			threshold = 0.8
		}
		triggerUnits := int(float64(summaryHardLimit) * threshold)
		summaryTarget = triggerUnits * 75 / 100
		if promptcompat.PromptUnits(stdReq.FinalPrompt) > triggerUnits {
			summaryTrigger = "server_threshold"
		}
	}
	if summaryTrigger != "" && !stdReq.IncrementalSessionRotated {
		beforeUnits := promptcompat.PromptUnits(stdReq.FinalPrompt)
		beforeBytes := responseStateSize(stdReq.Messages)
		compactedReq, stats, compacted, compactErr := shared.TrySummaryCompactPrompt(r.Context(), h.DS, a, stdReq, hardPromptLimit, summaryTarget)
		if compactErr != nil || !compacted {
			if compactErr == nil {
				compactErr = shared.ErrSummaryCompactionNotReducible
			}
			detail := summaryCompactionErrorDetail(compactErr)
			config.Logger.Warn("[responses_compact] summary compaction failed",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"trigger", summaryTrigger,
				"target_units", summaryTarget,
				"before_prompt_units", beforeUnits,
				"before_state_bytes", beforeBytes,
				"summary_attempts", stats.Attempts,
				"summary_duration_ms", stats.Duration.Milliseconds(),
				"error", compactErr)
			var healthErr *auth.AccountHealthError
			if summaryTrigger == "request_compact_threshold" || errors.As(compactErr, &healthErr) {
				if historySession != nil {
					historySession.Error(detail.Status, detail.Message, detail.FinishReason, "", "")
				}
				writeOpenAIErrorWithCode(w, detail.Status, detail.Message, detail.Code)
				return
			}
			promptLimit = hardPromptLimit
			promptLimit.AutoCompressEnable = ordinaryAutoCompress
		} else {
			stdReq = compactedReq
			incrementalBaseReq = stdReq
			handle := h.getResponseStore().putCompaction(owner, stdReq.Messages)
			if handle == "" {
				config.Logger.Error("[responses_compact] failed to persist automatic compaction state", "surface", "responses", "model", stdReq.ResolvedModel)
				writeOpenAIError(w, http.StatusInternalServerError, "failed to store automatic compaction state")
				return
			}
			automaticCompactionItem = newLocalCompactionItem(handle)
			config.Logger.Info("[responses_compact] automatic summary compaction completed",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"trigger", summaryTrigger,
				"target_units", summaryTarget,
				"before_messages", stats.BeforeMessages,
				"after_messages", stats.AfterMessages,
				"before_state_bytes", stats.BeforeStateBytes,
				"after_state_bytes", stats.AfterStateBytes,
				"state_reduction_percent", reductionPercent(stats.BeforeStateBytes, stats.AfterStateBytes),
				"before_prompt_units", stats.BeforePromptUnits,
				"after_prompt_units", stats.AfterPromptUnits,
				"prompt_reduction_percent", reductionPercent(stats.BeforePromptUnits, stats.AfterPromptUnits),
				"summary_source_units", stats.SourcePromptUnits,
				"summary_output_units", stats.SummaryUnits,
				"summary_input_tokens", stats.SummaryInputTokens,
				"summary_output_tokens", stats.SummaryOutputTokens,
				"summary_used_hidden_output", stats.UsedThinkingFallback,
				"summary_retained_turns", stats.RetainedTurns,
				"summary_attempts", stats.Attempts,
				"summary_duration_ms", stats.Duration.Milliseconds())
		}
	}
	incrementalAccountID := a.AccountID
	incrementalToken := a.DeepSeekToken
	if automaticCompactionItem == nil && h.tryIncrementalResponses(w, r, a, owner, &stdReq, promptLimit, nil, traceID, historySession, &sessionID) {
		return
	}
	if a.AccountID != incrementalAccountID || a.DeepSeekToken != incrementalToken {
		promptLimit, dynamicLimitApplied, err = shared.ResolveDynamicPromptLimits(r.Context(), h.DS, a, promptLimit)
		if err != nil {
			config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed after incremental account switch; using prior settings", "surface", "responses", "error", err)
		}
	}
	// Trim history BEFORE the current-input-file step: CIF folds the whole
	// transcript into one message, so compressing afterwards has nothing
	// left to drop. See shared.CompressPromptBeforeCIF.
	if !stdReq.IncrementalSessionRotated {
		if dropped, ok := shared.CompressPromptBeforeCIF(promptLimit, &stdReq); ok {
			config.Logger.Info("[prompt_limit] compressed history before CIF",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"dropped_messages", dropped, "prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "dynamic_upstream_limit", dynamicLimitApplied, "compact_threshold", compactThresholdApplied)
		}
	}
	// Keep the client-visible canonical conversation separate from the CIF
	// transport rewrite below. previous_response_id must restore this shape so
	// it can strictly extend the incremental branch recorded for the turn.
	incrementalBaseReq = stdReq
	if errMsg := shared.EnforcePromptLimitBeforeCIF(promptLimit, stdReq, h.Store.RemoteFileUploadEnabled()); errMsg != "" {
		config.Logger.Info("[prompt_limit] rejected before CIF",
			"surface", "responses", "model", stdReq.ResolvedModel,
			"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "auto_compress_enabled", promptLimit.AutoCompressEnable)
		if historySession != nil {
			historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "error", "prompt_too_large", "")
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
			historySession.Error(status, message, "error", "", "")
		}
		writeOpenAIError(w, status, message)
		return
	}
	recordCurrentInputMetrics(stdReq, cifDuration)
	if historySession != nil {
		historySession.UpdateCurrentInputState(stdReq)
	}

	// v1.0.14: LLM-based binary safety check.
	if shared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, shared.PickAuditText(stdReq.LatestUserText, stdReq.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.Error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
		}
	}) {
		return
	}

	// Final gate: CIF inlines the whole transcript into one user message, so
	// the assembled prompt can exceed the ceiling even after the pre-CIF
	// compression above. Nothing further can be trimmed at this point, so a
	// still-oversized prompt is a 413 rather than a silent upstream failure.
	var chunkedPrompt *shared.SessionChunkingPreparation
	if shared.EnforcePromptLimit(promptLimit, stdReq) != "" && promptLimit.SessionChunkingEnable {
		chunkedPrompt, err = shared.TryPrepareRootSessionChunkingWithFailover(r.Context(), h.DS, h.Auth, a, stdReq, promptLimit)
		if err != nil {
			config.Logger.Error("[prompt_limit] same-session chunking failed",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "error", err)
			if shared.IsSessionCapacityRateLimit(err) {
				writeCompletionCallError(w, historySession, err, "", "")
				return
			}
			if historySession != nil {
				historySession.Error(http.StatusBadGateway, err.Error(), "session_chunking_failed", "", "")
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
				"surface", "responses", "model", stdReq.ResolvedModel, "error", compressErr)
		} else if ok {
			stdReq = compressed
			config.Logger.Info("[prompt_limit] compressed Pro history with Flash",
				"surface", "responses", "model", stdReq.ResolvedModel,
				"thinking", stdReq.Thinking, "prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt))
		}
	}
	if chunkedPrompt == nil {
		if errMsg := shared.EnforcePromptLimit(promptLimit, stdReq); errMsg != "" {
			if historySession != nil {
				historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "prompt_too_large", "", "")
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
									"surface", "responses", "model", stdReq.ResolvedModel,
									"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "error", err)
								if shared.IsSessionCapacityRateLimit(err) {
									writeCompletionCallError(w, historySession, err, "", "")
									return
								}
							}
						}
						if historySession != nil {
							historySession.Error(http.StatusRequestEntityTooLarge, message, "prompt_too_large", "", "")
						}
						writeOpenAIError(w, http.StatusRequestEntityTooLarge, message)
						return
					}
					if shared.RootSessionErrorIsPow(rootErr) {
						handlePowError(w, historySession, a, rootErr)
					} else {
						handleCreateSessionError(w, historySession, a, rootErr)
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
								"surface", "responses", "model", stdReq.ResolvedModel,
								"prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt), "error", err)
							if shared.IsSessionCapacityRateLimit(err) {
								writeCompletionCallError(w, historySession, err, "", "")
								return
							}
						}
					}
					if historySession != nil {
						historySession.Error(http.StatusRequestEntityTooLarge, message, "prompt_too_large", "", "")
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
				handlePowError(w, historySession, a, err)
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
	if nextSessionID := strings.TrimSpace(responseString(payload["chat_session_id"])); nextSessionID != "" {
		sessionID = nextSessionID
	}
	if err != nil {
		if !a.UseConfigToken && shared.CompletionErrorDetail(err).Status == http.StatusUnauthorized {
			a.MarkDirectTokenInvalid()
		}
		writeCompletionCallError(w, historySession, err, "", "")
		return
	}

	responseID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// stdReq may now be a current-input-file transport envelope. Persist the
	// canonical pre-CIF input instead; otherwise a follow-up previous_response_id
	// reconstructs a different first message and cannot reuse the pinned session.
	h.getResponseStore().putInputState(owner, responseID, incrementalBaseReq.Messages,
		incrementalBaseReq.ToolsRaw, incrementalBaseReq.HasTools,
		incrementalBaseReq.ToolChoiceRaw, incrementalBaseReq.HasToolChoice)
	h.getResponseStore().putSessionKey(owner, responseID, a.SessionKey)
	refFileTokens := stdReq.RefFileTokens
	var outcome responsesCompletionOutcome
	responsePrefix := localCompactionOutputPrefix(automaticCompactionItem)
	if stdReq.Stream {
		outcome = h.handleResponsesStreamWithRetry(w, r, a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, stdReq, promptLimit, refFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, responsePrefix, traceID, historySession, &sessionID)
	} else {
		outcome = h.handleResponsesNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, stdReq, promptLimit, refFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, responsePrefix, traceID, historySession, &sessionID)
	}
	if outcome.sessionID != "" {
		sessionID = outcome.sessionID
	}
	h.recordFullResponsesIncrementalState(a, incrementalBaseReq, sessionID, outcome)
}

func (h *Handler) tryIncrementalResponses(w http.ResponseWriter, r *http.Request, a *auth.RequestAuth, owner string, stdReq *promptcompat.StandardRequest, promptLimit config.PromptLimitSettings, compactionItem map[string]any, traceID string, historySession *historycapture.Session, activeSessionID *string) bool {
	if stdReq == nil {
		return false
	}
	lease, incrementalPrompt, ok := shared.PrepareIncrementalRequestWithSettings(h.Incremental, h.DS, h.Store.AutoDeleteMode(), a, *stdReq, stdReq.Messages, promptLimit)
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
			"surface", "responses", "session_key", a.SessionKey,
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
		"surface", "responses", "session_key", a.SessionKey,
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
		config.Logger.Warn("[prompt_limit] dynamic upstream limit lookup failed; using static settings", "surface", "responses.incremental", "error", err)
	}
	if shared.RunSafetyCheckAndBlock(r.Context(), h.SafetyLLM, a, shared.PickAuditText(stdReq.LatestUserText, stdReq.FinalPrompt), w, h.Store.SafetyBlockMessage(), func(_ safetyllm.Verdict) {
		if historySession != nil {
			historySession.Error(http.StatusForbidden, "blocked by safety policy", "error", "policy_blocked", "")
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
					"surface", "responses", "session_key", a.SessionKey,
					"turn_count", lease.TurnCount, "reason", reason, "error", err)
				if activeSessionID != nil {
					*activeSessionID = ""
				}
				stdReq.FinalPrompt = fullPrompt
				return false
			}
			if shared.SwitchManagedAccountForPinnedBranch(r.Context(), h.Auth, a, err) {
				config.Logger.Warn("[incremental] pinned chunk preparation failed; rebuilding full context on another account",
					"surface", "responses", "session_key", a.SessionKey,
					"error", err)
				if activeSessionID != nil {
					*activeSessionID = ""
				}
				stdReq.FinalPrompt = fullPrompt
				return false
			}
			if historySession != nil {
				historySession.Error(http.StatusBadGateway, err.Error(), "session_chunking_failed", "", "")
			}
			writeOpenAIError(w, http.StatusBadGateway, "same-session prompt chunking failed: "+err.Error())
			return true
		}
	}
	if chunkedPrompt == nil {
		if errMsg := shared.EnforcePromptLimit(promptLimit, *stdReq); errMsg != "" {
			if historySession != nil {
				historySession.Error(http.StatusRequestEntityTooLarge, errMsg, "prompt_too_large", "", "")
			}
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, errMsg)
			return true
		}
	}
	// The delta is still a child of lease.SessionID even when it does not
	// need chunking. Keep PoW pinned, then rebuild a root branch on 429.
	pow, err := shared.GetPinnedPow(r.Context(), h.DS, a)
	if err != nil {
		if shared.IsSessionCapacityRateLimit(err) {
			config.Logger.Warn("[incremental] existing upstream session reached capacity while acquiring pinned PoW; rebuilding full context on the same account",
				"surface", "responses", "session_key", a.SessionKey,
				"turn_count", lease.TurnCount, "error", err)
			if activeSessionID != nil {
				*activeSessionID = ""
			}
			stdReq.FinalPrompt = fullPrompt
			return false
		}
		if shared.SwitchManagedAccountForPinnedBranch(r.Context(), h.Auth, a, err) {
			config.Logger.Warn("[incremental] pinned PoW failed; rebuilding full context on another account",
				"surface", "responses", "session_key", a.SessionKey,
				"error", err)
			if activeSessionID != nil {
				*activeSessionID = ""
			}
			stdReq.FinalPrompt = fullPrompt
			return false
		}
		handlePowError(w, historySession, a, err)
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
				"surface", "responses", "session_key", a.SessionKey,
				"turn_count", lease.TurnCount, "error", err)
		} else {
			switched := shared.SwitchManagedAccountForPinnedBranch(r.Context(), h.Auth, a, err)
			config.Logger.Warn("[incremental] pinned completion failed; falling back to full replay",
				"surface", "responses", "session_key", a.SessionKey, "switched_account", switched, "error", err)
		}
		if activeSessionID != nil {
			*activeSessionID = ""
		}
		stdReq.FinalPrompt = fullPrompt
		return false
	}
	responseID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	h.getResponseStore().putInputState(owner, responseID, stdReq.Messages,
		stdReq.ToolsRaw, stdReq.HasTools, stdReq.ToolChoiceRaw, stdReq.HasToolChoice)
	h.getResponseStore().putSessionKey(owner, responseID, a.SessionKey)
	var outcome responsesCompletionOutcome
	responsePrefix := localCompactionOutputPrefix(compactionItem)
	if stdReq.Stream {
		outcome = h.handleResponsesStreamWithRetry(w, r, a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, fullRootReq, promptLimit, stdReq.RefFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, responsePrefix, traceID, historySession, activeSessionID)
	} else {
		outcome = h.handleResponsesNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, owner, responseID, stdReq.ResponseModel, stdReq.FinalPrompt, fullRootReq, promptLimit, stdReq.RefFileTokens, stdReq.Thinking, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice, responsePrefix, traceID, historySession, activeSessionID)
	}
	if outcome.success {
		if outcome.replayedRoot {
			stdReq.FinalPrompt = fullPrompt
			h.recordFullResponsesIncrementalState(a, *stdReq, outcome.sessionID, outcome)
		} else {
			lease.Complete(shared.IncrementalScope(a, *stdReq), stdReq.Messages, outcome.responseMessages, lease.SessionID, outcome.responseMessageID)
			lease = nil
		}
	}
	return true
}

func (h *Handler) recordFullResponsesIncrementalState(a *auth.RequestAuth, stdReq promptcompat.StandardRequest, sessionID string, outcome responsesCompletionOutcome) {
	if h == nil || h.Incremental == nil || h.Store == nil || !outcome.success || !strings.EqualFold(strings.TrimSpace(h.Store.AutoDeleteMode()), "none") {
		return
	}
	scope := shared.IncrementalScope(a, stdReq)
	h.Incremental.Record(scope, stdReq.Messages, outcome.responseMessages, sessionID, outcome.responseMessageID)
	config.Logger.Info("[incremental] recorded upstream branch",
		"surface", "responses", "session_key", scope.SessionKey,
		"account_fingerprint", responseStateFingerprint(scope.AccountID),
		"upstream_session_fingerprint", responseStateFingerprint(sessionID),
		"variant", scope.Variant,
		"parent_message_id", outcome.responseMessageID,
		"request_messages", len(stdReq.Messages), "response_messages", len(outcome.responseMessages),
		"full_prompt_units", promptcompat.PromptUnits(stdReq.FinalPrompt),
		"format_prompt_units", promptcompat.PromptUnits(stdReq.IncrementalFormatPrompt))
}

func (h *Handler) handleResponsesNonStream(w http.ResponseWriter, resp *http.Response, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, traceID string) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeOpenAIError(w, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	result := sse.CollectStream(resp, thinkingEnabled, true)
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	sanitizedThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	sanitizedText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if searchEnabled {
		sanitizedText = replaceCitationMarkersWithLinks(sanitizedText, result.CitationLinks)
	}
	textParsed := detectAssistantToolCalls(result.Text, sanitizedText, result.Thinking, result.ToolDetectionThinking, toolNames)
	if len(textParsed.Calls) == 0 && writeUpstreamEmptyOutputError(w, sanitizedText, sanitizedThinking, result.ContentFilter) {
		return
	}
	logResponsesToolPolicyRejection(traceID, toolChoice, textParsed, "text")

	callCount := len(textParsed.Calls)
	if toolChoice.IsRequired() && callCount == 0 {
		writeOpenAIErrorWithCode(w, http.StatusUnprocessableEntity, "tool_choice requires at least one valid tool call.", "tool_choice_violation")
		return
	}

	responseObj := openaifmt.BuildResponseObjectWithToolCalls(responseID, model, finalPrompt, sanitizedThinking, sanitizedText, textParsed.Calls, toolsRaw)
	addRefFileTokensToUsage(responseObj, refFileTokens)
	h.getResponseStore().put(owner, responseID, responseObj)
	writeJSON(w, http.StatusOK, responseObj)
}

func (h *Handler) handleResponsesStream(w http.ResponseWriter, r *http.Request, resp *http.Response, owner, responseID, model, finalPrompt string, refFileTokens int, thinkingEnabled, searchEnabled bool, toolNames []string, toolsRaw any, toolChoice promptcompat.ToolChoicePolicy, traceID string) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		writeOpenAIError(w, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_, canFlush := w.(http.Flusher)

	initialType := "text"
	if thinkingEnabled {
		initialType = "thinking"
	}
	bufferToolContent := len(toolNames) > 0
	emitEarlyToolDeltas := h.toolcallFeatureMatchEnabled() && h.toolcallEarlyEmitHighConfidence()
	stripReferenceMarkers := h.compatStripReferenceMarkers()

	streamRuntime := newResponsesStreamRuntime(
		w,
		rc,
		canFlush,
		responseID,
		model,
		finalPrompt,
		thinkingEnabled,
		searchEnabled,
		stripReferenceMarkers,
		toolNames,
		toolsRaw,
		bufferToolContent,
		emitEarlyToolDeltas,
		toolChoice,
		traceID,
		func(obj map[string]any) {
			h.getResponseStore().put(owner, responseID, obj)
		},
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
		OnParsed: streamRuntime.onParsed,
		OnFinalize: func(reason streamengine.StopReason, scannerErr error) {
			if failure, failed := streamengine.ClassifyTerminalFailure(reason, scannerErr); failed {
				config.Logger.Warn("[stream] upstream stream terminated abnormally",
					"surface", "responses", "stop_reason", reason,
					"status", failure.Status, "code", failure.Code, "error", scannerErr)
				streamRuntime.failResponse(failure.Status, failure.Message, failure.Code)
				return
			}
			if string(reason) == "content_filter" {
				streamRuntime.finalize("content_filter", false)
				return
			}
			streamRuntime.finalize("stop", false)
		},
	})
	writeUnstartedResponsesStreamError(w, streamRuntime)
}

func logResponsesToolPolicyRejection(traceID string, policy promptcompat.ToolChoicePolicy, parsed toolcall.ToolCallParseResult, channel string) {
	rejected := filteredRejectedToolNamesForLog(parsed.RejectedToolNames)
	if !parsed.RejectedByPolicy || len(rejected) == 0 {
		return
	}
	config.Logger.Warn(
		"[responses] rejected tool calls by policy",
		"trace_id", strings.TrimSpace(traceID),
		"channel", channel,
		"tool_choice_mode", policy.Mode,
		"rejected_tool_names", strings.Join(rejected, ","),
	)
}

func filteredRejectedToolNamesForLog(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		switch strings.ToLower(trimmed) {
		case "", "tool_name":
			continue
		default:
			out = append(out, trimmed)
		}
	}
	return out
}
