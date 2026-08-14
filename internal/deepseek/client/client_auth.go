package client

import (
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

func (c *Client) Login(ctx context.Context, acc config.Account) (string, error) {
	if strings.TrimSpace(acc.Password) == "" {
		healthErr := &auth.AccountHealthError{
			State:   account.HealthInvalidCredentials,
			Message: "password is required to obtain a new upstream token",
		}
		c.markLoginAccountHealth(acc, healthErr)
		return "", healthErr
	}
	clients := c.requestClientsForAccount(acc)
	payload := map[string]any{
		"password":  strings.TrimSpace(acc.Password),
		"device_id": "deepseek_web_to_api",
		"os":        "android",
	}
	if email := strings.TrimSpace(acc.Email); email != "" {
		payload["email"] = email
	} else if mobile := strings.TrimSpace(acc.Mobile); mobile != "" {
		loginMobile, areaCode := normalizeMobileForLogin(mobile)
		payload["mobile"] = loginMobile
		payload["area_code"] = areaCode
	} else {
		return "", errors.New("missing email/mobile")
	}
	resp, err := c.postJSON(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekLoginURL, dsprotocol.BaseHeaders, payload)
	if err != nil {
		return "", err
	}
	code := intFrom(resp["code"])
	data, _ := resp["data"].(map[string]any)
	bizCode := intFrom(data["biz_code"])
	bizMsg, _ := data["biz_msg"].(string)
	msg, _ := resp["msg"].(string)
	if healthErr := loginAccountHealthErrorFromResponse(code, bizCode, msg, bizMsg); healthErr != nil {
		c.markLoginAccountHealth(acc, healthErr)
		return "", healthErr
	}
	if code != 0 {
		return "", fmt.Errorf("login failed: %v", resp["msg"])
	}
	if bizCode != 0 {
		return "", fmt.Errorf("login failed: %v", bizMsg)
	}
	bizData, _ := data["biz_data"].(map[string]any)
	user, _ := bizData["user"].(map[string]any)
	if healthErr := accountHealthErrorFromUser(user); healthErr != nil {
		c.markLoginAccountHealth(acc, healthErr)
		return "", healthErr
	}
	token, _ := user["token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", errors.New("missing login token")
	}
	return token, nil
}

func (c *Client) CheckAccountHealth(ctx context.Context, acc config.Account) (string, error) {
	token := strings.TrimSpace(acc.Token)
	if token == "" {
		return c.Login(ctx, acc)
	}
	authCtx := &auth.RequestAuth{
		DeepSeekToken: token,
		AccountID:     acc.Identifier(),
		Account:       acc,
	}
	ctx = auth.WithAuth(ctx, authCtx)
	clients := c.requestClientsForAccount(acc)
	resp, status, err := c.getJSONWithStatus(ctx, clients.regular, dsprotocol.DeepSeekCurrentUserURL, c.authHeaders(token))
	if err != nil {
		return token, err
	}
	code, bizCode, msg, bizMsg := extractResponseStatus(resp)
	if rateErr := accountRateLimitError(status, code, bizCode, msg, bizMsg, ""); rateErr != nil {
		c.markLoginAccountHealth(acc, rateErr)
		return token, rateErr
	}
	if healthErr := accountHealthErrorFromResponse(code, bizCode, msg, bizMsg); healthErr != nil {
		c.markLoginAccountHealth(acc, healthErr)
		return token, healthErr
	}
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		if healthErr := accountHealthErrorFromUser(extractResponseUser(resp)); healthErr != nil {
			c.markLoginAccountHealth(acc, healthErr)
			return token, healthErr
		}
		return token, nil
	}
	if shouldAttemptRefresh(status, code, bizCode, msg, bizMsg) {
		if strings.TrimSpace(acc.Password) != "" {
			return c.Login(ctx, acc)
		}
		healthErr := &auth.AccountHealthError{
			State:   account.HealthInvalidCredentials,
			Code:    firstNonZero(bizCode, code),
			Message: "stored token rejected and no password is available for refresh",
		}
		c.markLoginAccountHealth(acc, healthErr)
		return token, healthErr
	}
	return token, fmt.Errorf("account health check failed: status=%d code=%d biz_code=%d message=%s", status, code, bizCode, failureMessage(msg, bizMsg, "unknown"))
}

func extractResponseUser(resp map[string]any) map[string]any {
	data, _ := resp["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	user, _ := bizData["user"].(map[string]any)
	if user != nil {
		return user
	}
	user, _ = data["user"].(map[string]any)
	return user
}

func (c *Client) CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	clients := c.requestClientsForAuth(ctx, a)
	attempts := 0
	refreshed := false
	for attempts < maxAttempts {
		headers := c.authHeaders(a.DeepSeekToken)
		resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekCreateSessionURL, headers, map[string]any{"agent": "chat"})
		if err != nil {
			if IsClientCancelledError(err) || IsUpstreamTimeoutError(err) {
				return "", requestFailureWithOp("create session", err)
			}
			config.Logger.Warn("[create_session] request error", "error", err, "account", a.AccountID)
			attempts++
			continue
		}
		code, bizCode, msg, bizMsg := extractResponseStatus(resp)
		c.markAccountRateLimited(a, status, code, bizCode, msg, bizMsg, "")
		if status == http.StatusOK && code == 0 && bizCode == 0 {
			sessionID := extractCreateSessionID(resp)
			if sessionID != "" {
				return sessionID, nil
			}
		}
		if healthErr := c.markAccountHealth(a, code, bizCode, msg, bizMsg); healthErr != nil {
			if a.UseConfigToken && c.Auth.SwitchAccount(ctx, a) {
				clients = c.requestClientsForAuth(ctx, a)
				refreshed = false
				attempts++
				continue
			}
			return "", healthErr
		}
		config.Logger.Warn("[create_session] failed", "status", status, "code", code, "biz_code", bizCode, "msg", msg, "biz_msg", bizMsg, "use_config_token", a.UseConfigToken, "account", a.AccountID)
		if a.UseConfigToken {
			if !refreshed && shouldAttemptRefresh(status, code, bizCode, msg, bizMsg) {
				if c.Auth.RefreshToken(ctx, a) {
					refreshed = true
					continue
				}
			}
			if c.Auth.SwitchAccount(ctx, a) {
				refreshed = false
				attempts++
				continue
			}
		}
		attempts++
	}
	return "", errors.New("create session failed")
}

func (c *Client) GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error) {
	return c.GetPowForTarget(ctx, a, dsprotocol.DeepSeekCompletionTargetPath, maxAttempts)
}

func (c *Client) GetPowForTarget(ctx context.Context, a *auth.RequestAuth, targetPath string, maxAttempts int) (string, error) {
	return c.getPowForTarget(ctx, a, targetPath, maxAttempts, true)
}

// GetPowPinned obtains a completion PoW without switching the selected
// account. It is required after a chat_session_id has been created because a
// session belongs to exactly one upstream account.
func (c *Client) GetPowPinned(ctx context.Context, a *auth.RequestAuth) (string, error) {
	return c.getPowForTarget(ctx, a, dsprotocol.DeepSeekCompletionTargetPath, 3, false)
}

func (c *Client) getPowForTarget(ctx context.Context, a *auth.RequestAuth, targetPath string, maxAttempts int, allowAccountSwitch bool) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		targetPath = dsprotocol.DeepSeekCompletionTargetPath
	}
	clients := c.requestClientsForAuth(ctx, a)
	attempts := 0
	refreshed := false
	lastFailureKind := FailureUnknown
	lastFailureMessage := ""
	for attempts < maxAttempts {
		headers := c.authHeaders(a.DeepSeekToken)
		resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekCreatePowURL, headers, map[string]any{"target_path": targetPath})
		if err != nil {
			if IsClientCancelledError(err) || IsUpstreamTimeoutError(err) {
				return "", requestFailureWithOp("get pow", err)
			}
			config.Logger.Warn("[get_pow] request error", "error", err, "account", a.AccountID, "target_path", targetPath)
			lastFailureKind = FailureUnknown
			lastFailureMessage = err.Error()
			attempts++
			continue
		}
		code, bizCode, msg, bizMsg := extractResponseStatus(resp)
		c.markAccountRateLimited(a, status, code, bizCode, msg, bizMsg, "")
		if status == http.StatusOK && code == 0 && bizCode == 0 {
			data, _ := resp["data"].(map[string]any)
			bizData, _ := data["biz_data"].(map[string]any)
			challenge, _ := bizData["challenge"].(map[string]any)
			answer, err := ComputePow(ctx, challenge)
			if err != nil {
				attempts++
				continue
			}
			return BuildPowHeader(challenge, answer)
		}
		if healthErr := c.markAccountHealth(a, code, bizCode, msg, bizMsg); healthErr != nil {
			if allowAccountSwitch && a.UseConfigToken && c.Auth.SwitchAccount(ctx, a) {
				clients = c.requestClientsForAuth(ctx, a)
				refreshed = false
				attempts++
				continue
			}
			return "", healthErr
		}
		config.Logger.Warn("[get_pow] failed", "status", status, "code", code, "biz_code", bizCode, "msg", msg, "biz_msg", bizMsg, "use_config_token", a.UseConfigToken, "account", a.AccountID, "target_path", targetPath)
		lastFailureMessage = failureMessage(msg, bizMsg, "get pow failed")
		if isTokenInvalid(status, code, bizCode, msg, bizMsg) || isAuthIndicativeBizFailure(msg, bizMsg) {
			lastFailureKind = authFailureKind(a.UseConfigToken)
		} else {
			lastFailureKind = FailureUnknown
		}
		if a.UseConfigToken {
			if !refreshed && shouldAttemptRefresh(status, code, bizCode, msg, bizMsg) {
				if c.Auth.RefreshToken(ctx, a) {
					refreshed = true
					continue
				}
			}
			if allowAccountSwitch && c.Auth.SwitchAccount(ctx, a) {
				refreshed = false
				attempts++
				continue
			}
		}
		attempts++
	}
	if lastFailureKind != FailureUnknown {
		return "", &RequestFailure{Op: "get pow", Kind: lastFailureKind, Message: lastFailureMessage}
	}
	return "", errors.New("get pow failed")
}

func (c *Client) authHeaders(token string) map[string]string {
	headers := make(map[string]string, len(dsprotocol.BaseHeaders)+1)
	for k, v := range dsprotocol.BaseHeaders {
		headers[k] = v
	}
	headers["authorization"] = "Bearer " + token
	return headers
}

func isTokenInvalid(status int, code int, bizCode int, msg string, bizMsg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg) + " " + strings.TrimSpace(bizMsg))
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if code == 40001 || code == 40002 || code == 40003 || bizCode == 40001 || bizCode == 40002 || bizCode == 40003 {
		return true
	}
	return strings.Contains(msg, "token") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "expired") ||
		strings.Contains(msg, "not login") ||
		strings.Contains(msg, "login required") ||
		strings.Contains(msg, "invalid jwt")
}

func shouldAttemptRefresh(status int, code int, bizCode int, msg string, bizMsg string) bool {
	if isTokenInvalid(status, code, bizCode, msg, bizMsg) {
		return true
	}
	// Some DeepSeek failures come back as HTTP 200/code=0 but with non-zero biz_code.
	// Only attempt refresh when these biz failures still look auth-related.
	return status == http.StatusOK &&
		code == 0 &&
		bizCode != 0 &&
		isAuthIndicativeBizFailure(msg, bizMsg)
}

func isAuthIndicativeBizFailure(msg string, bizMsg string) bool {
	combined := strings.ToLower(strings.TrimSpace(msg) + " " + strings.TrimSpace(bizMsg))
	authKeywords := []string{
		"auth",
		"authorization",
		"credential",
		"expired",
		"invalid jwt",
		"jwt",
		"login",
		"not login",
		"session expired",
		"token",
		"unauthorized",
		"登录",
		"未登录",
		"认证",
		"凭证",
		"会话过期",
		"令牌",
	}
	for _, keyword := range authKeywords {
		if strings.Contains(combined, keyword) {
			return true
		}
	}
	return false
}

func authFailureKind(useConfigToken bool) FailureKind {
	if useConfigToken {
		return FailureManagedUnauthorized
	}
	return FailureDirectUnauthorized
}

func failureMessage(msg string, bizMsg string, fallback string) string {
	if trimmed := strings.TrimSpace(bizMsg); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(msg); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}

// DeepSeek has returned create-session ids in both biz_data.id and
// biz_data.chat_session.id across observed response variants; accept either.
func extractCreateSessionID(resp map[string]any) string {
	data, _ := resp["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	if sessionID, _ := bizData["id"].(string); strings.TrimSpace(sessionID) != "" {
		return strings.TrimSpace(sessionID)
	}
	if chatSession, ok := bizData["chat_session"].(map[string]any); ok {
		if sessionID, _ := chatSession["id"].(string); strings.TrimSpace(sessionID) != "" {
			return strings.TrimSpace(sessionID)
		}
	}
	return ""
}

func extractResponseStatus(resp map[string]any) (code int, bizCode int, msg string, bizMsg string) {
	code = intFrom(resp["code"])
	msg, _ = resp["msg"].(string)
	data, _ := resp["data"].(map[string]any)
	bizCode = intFrom(data["biz_code"])
	bizMsg, _ = data["biz_msg"].(string)
	if strings.TrimSpace(bizMsg) == "" {
		if bizData, ok := data["biz_data"].(map[string]any); ok {
			bizMsg, _ = bizData["msg"].(string)
		}
	}
	return code, bizCode, msg, bizMsg
}
