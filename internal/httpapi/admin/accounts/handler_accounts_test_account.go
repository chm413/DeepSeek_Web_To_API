package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	authn "DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/prompt"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sse"
)

func (h *Handler) testAccount(ctx context.Context, acc config.Account, model, message string) map[string]any {
	start := time.Now()
	identifier := acc.Identifier()
	result := newAccountTestResult(identifier, model, !h.Store.IsEnvBacked())
	defer h.persistAccountTestResult(identifier, result)

	result["phase"] = "token_refresh"
	token, err := h.DS.Login(ctx, acc)
	if err != nil {
		h.recordAccountHealth(identifier, err, result)
		setAccountTestFailure(result, "token_refresh", err)
		return result
	}
	h.clearAccountHealth(identifier)
	result["account_state"] = account.HealthHealthy
	result["token_refreshed"] = true
	if err := h.Store.UpdateAccountToken(acc.Identifier(), token); err != nil {
		result["config_warning"] = "登录成功，但 token 持久化失败（仅保存在内存，重启后会丢失）: " + err.Error()
	}

	authCtx := &authn.RequestAuth{UseConfigToken: true, DeepSeekToken: token, AccountID: identifier, Account: acc}
	proxyCtx := authn.WithAuth(ctx, authCtx)
	sessionID, err := h.ensureTestSession(proxyCtx, authCtx, acc, result)
	if err != nil {
		return result
	}
	h.updateSessionCount(proxyCtx, identifier, token, result)

	if strings.TrimSpace(message) == "" {
		result["success"] = true
		result["phase"] = "complete"
		result["message"] = withConfigWarning("Token 刷新成功（登录与会话创建成功）", result)
		result["response_time"] = int(time.Since(start).Milliseconds())
		return result
	}
	h.runAccountCompletion(proxyCtx, authCtx, sessionID, model, message, start, result)
	return result
}

func (h *Handler) recordAccountHealth(identifier string, err error, result map[string]any) {
	var healthErr *authn.AccountHealthError
	if !errors.As(err, &healthErr) {
		return
	}
	result["account_state"] = healthErr.State
	result["error_code"] = healthErr.Code
	if !healthErr.Until.IsZero() {
		result["health_until"] = healthErr.Until
	}
	recorder, canRecord := h.Pool.(interface {
		MarkTemporaryMute(string, time.Time, string)
		MarkPermanentlyBanned(string, string)
		MarkInvalidCredentials(string, string)
	})
	switch healthErr.State {
	case account.HealthTemporarilyMuted:
		if canRecord {
			recorder.MarkTemporaryMute(identifier, healthErr.Until, healthErr.Error())
		}
	case account.HealthPermanentlyBanned:
		if canRecord {
			recorder.MarkPermanentlyBanned(identifier, healthErr.Error())
		}
		if err := h.Store.SetAccountDisabled(identifier, true, config.AccountDisabledUpstreamBanned); err != nil {
			result["config_warning"] = "account ban detected but automatic disable persistence failed: " + err.Error()
		} else {
			result["auto_disabled"] = true
		}
	case account.HealthInvalidCredentials:
		if canRecord {
			recorder.MarkInvalidCredentials(identifier, healthErr.Error())
		}
		if err := h.Store.SetAccountDisabled(identifier, true, config.AccountDisabledInvalidCredentials); err != nil {
			result["config_warning"] = "invalid credentials detected but automatic disable persistence failed: " + err.Error()
		} else {
			result["auto_disabled"] = true
		}
	}
}

func (h *Handler) clearAccountHealth(identifier string) {
	if clearer, ok := h.Pool.(interface{ ClearHealth(string) }); ok {
		clearer.ClearHealth(identifier)
	}
}

func newAccountTestResult(identifier, model string, configWritable bool) map[string]any {
	return map[string]any{
		"account":         identifier,
		"success":         false,
		"response_time":   0,
		"message":         "",
		"model":           model,
		"config_writable": configWritable,
		"config_warning":  "",
		"phase":           "token_refresh",
		"failure_reason":  "",
	}
}

func (h *Handler) persistAccountTestResult(identifier string, result map[string]any) {
	status := "failed"
	if ok, _ := result["success"].(bool); ok {
		status = "ok"
	}
	resultStore, ok := h.Store.(interface {
		UpdateAccountTestResult(string, config.AccountTestResult) error
	})
	if !ok {
		_ = h.Store.UpdateAccountTestStatus(identifier, status)
		return
	}
	_ = resultStore.UpdateAccountTestResult(identifier, config.AccountTestResult{
		Status:         status,
		Phase:          accountTestString(result["phase"]),
		FailureReason:  accountTestString(result["failure_reason"]),
		ErrorCode:      accountTestInt(result["error_code"]),
		HTTPStatus:     accountTestInt(result["http_status"]),
		AccountState:   accountTestString(result["account_state"]),
		AutoDisabled:   accountTestBool(result["auto_disabled"]),
		ConfigWarning:  accountTestString(result["config_warning"]),
		ResponseTimeMs: accountTestInt(result["response_time"]),
	})
}

func (h *Handler) ensureTestSession(ctx context.Context, a *authn.RequestAuth, acc config.Account, result map[string]any) (string, error) {
	result["phase"] = "session_create"
	sessionID, err := h.DS.CreateSession(ctx, a, 1)
	if err == nil {
		return sessionID, nil
	}
	result["session_error"] = err.Error()
	result["phase"] = "token_refresh_retry"
	newToken, loginErr := h.DS.Login(ctx, acc)
	if loginErr != nil {
		h.recordAccountHealth(acc.Identifier(), loginErr, result)
		setAccountTestFailure(result, "token_refresh_retry", loginErr)
		return "", loginErr
	}
	a.DeepSeekToken = newToken
	result["token_refreshed"] = true
	if err := h.Store.UpdateAccountToken(acc.Identifier(), newToken); err != nil {
		result["config_warning"] = "刷新 token 成功，但 token 持久化失败（仅保存在内存，重启后会丢失）: " + err.Error()
	}
	result["phase"] = "session_create_retry"
	sessionID, err = h.DS.CreateSession(ctx, a, 1)
	if err != nil {
		setAccountTestFailure(result, "session_create_retry", err)
		return "", err
	}
	return sessionID, nil
}

func (h *Handler) updateSessionCount(ctx context.Context, identifier, token string, result map[string]any) {
	sessionStats, sessionErr := h.DS.GetSessionCountForToken(ctx, token)
	if sessionErr != nil || sessionStats == nil {
		return
	}
	sessionCount := sessionStats.FirstPageCount
	result["session_count"] = sessionCount
	_ = h.Store.UpdateAccountSessionCount(identifier, sessionCount)
}

func (h *Handler) runAccountCompletion(ctx context.Context, a *authn.RequestAuth, sessionID string, model, message string, start time.Time, result map[string]any) {
	result["phase"] = "model_validation"
	resolvedModel, thinking, search, ok := h.resolveAccountTestModel(model)
	if !ok {
		// v1.0.10: strict allowlist — admin "test account" must NOT bypass
		// the model gate. Disabled (deepseek-v4-vision) or unknown model
		// IDs short-circuit here so the operator gets the same rejection
		// message a real client would.
		setAccountTestFailureText(result, "model_validation", "模型未启用或未支持: "+model)
		result["response_time"] = int(time.Since(start).Milliseconds())
		return
	}
	model = resolvedModel
	result["phase"] = "pow"
	pow, err := h.DS.GetPow(ctx, a, 1)
	if err != nil {
		setAccountTestFailure(result, "pow", err)
		return
	}
	payload := promptcompat.StandardRequest{
		ResolvedModel: model,
		FinalPrompt:   prompt.MessagesPrepare([]map[string]any{{"role": "user", "content": message}}),
		Thinking:      thinking,
		Search:        search,
	}.CompletionPayload(sessionID)
	result["phase"] = "completion"
	resp, err := h.DS.CallCompletion(ctx, a, payload, pow, 1)
	if err != nil {
		setAccountTestFailure(result, "completion", err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		reason, code := accountTestHTTPFailure(resp.StatusCode, body)
		setAccountTestFailureText(result, "completion", reason)
		result["http_status"] = resp.StatusCode
		if code != 0 {
			result["error_code"] = code
		}
		return
	}
	collected := sse.CollectStream(resp, thinking, true)
	result["success"] = true
	result["phase"] = "complete"
	result["response_time"] = int(time.Since(start).Milliseconds())
	if collected.Text != "" {
		result["message"] = collected.Text
	} else {
		result["message"] = "（无回复内容）"
	}
	if collected.Thinking != "" {
		result["thinking"] = collected.Thinking
	}
}

func setAccountTestFailure(result map[string]any, phase string, err error) {
	reason := "unknown error"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		reason = strings.TrimSpace(err.Error())
	}
	setAccountTestFailureText(result, phase, reason)
}

func setAccountTestFailureText(result map[string]any, phase, reason string) {
	phase = strings.TrimSpace(phase)
	reason = strings.TrimSpace(reason)
	result["success"] = false
	result["phase"] = phase
	result["failure_reason"] = reason
	result["message"] = phase + ": " + reason
}

func accountTestHTTPFailure(status int, raw []byte) (string, int) {
	fallback := fmt.Sprintf("HTTP %d", status)
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return fallback, 0
	}
	code := accountTestInt(payload["biz_code"])
	if code == 0 {
		code = accountTestInt(payload["code"])
	}
	for _, key := range []string{"biz_msg", "msg", "message", "detail"} {
		if message := accountTestString(payload[key]); message != "" {
			return fmt.Sprintf("HTTP %d: %s", status, message), code
		}
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		if code == 0 {
			code = accountTestInt(nested["code"])
		}
		if message := accountTestString(nested["message"]); message != "" {
			return fmt.Sprintf("HTTP %d: %s", status, message), code
		}
	}
	return fallback, code
}

func accountTestString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case account.HealthState:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}

func accountTestInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func accountTestBool(value any) bool {
	result, _ := value.(bool)
	return result
}

// resolveAccountTestModel returns (resolvedModel, thinking, search, ok).
// ok=false means the requested id is unknown OR explicitly blocked
// (deepseek-v4-vision in v1.0.10) — callers must NOT proxy to the upstream
// in that case.
func (h *Handler) resolveAccountTestModel(model string) (string, bool, bool, bool) {
	resolvedModel, resolved := config.ResolveModel(modelAliasSnapshotReader{
		aliases: h.Store.Snapshot().ModelAliases,
	}, model)
	if !resolved {
		return model, false, false, false
	}
	thinking, search, ok := config.GetModelConfig(resolvedModel)
	if !ok {
		return resolvedModel, false, false, false
	}
	return resolvedModel, thinking, search, true
}

func withConfigWarning(message string, result map[string]any) string {
	warning, _ := result["config_warning"].(string)
	if strings.TrimSpace(warning) == "" {
		return message
	}
	return message + "；" + warning
}
