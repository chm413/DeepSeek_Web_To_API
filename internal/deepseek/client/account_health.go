package client

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

const (
	loginUserBannedCode              = 10
	upstreamUserBannedCode           = 40012
	upstreamAccountDisabledCode      = 90008
	upstreamMutedCode                = 50006
	defaultMuteMessage               = "account temporarily muted by upstream"
	defaultBannedMessage             = "account permanently banned by upstream"
	defaultInvalidCredentialsMessage = "account credentials rejected by upstream"
	defaultRateLimitMessage          = "account temporarily rate limited by upstream"
)

const maxRateLimitCooldown = 15 * time.Minute
const defaultRateLimitStateCooldown = time.Minute

func accountHealthErrorFromResponse(code, bizCode int, msg, bizMsg string) *auth.AccountHealthError {
	return accountHealthErrorFromCodes(code, bizCode, msg, bizMsg, false)
}

func loginAccountHealthErrorFromResponse(code, bizCode int, msg, bizMsg string) *auth.AccountHealthError {
	return accountHealthErrorFromCodes(code, bizCode, msg, bizMsg, true)
}

func accountHealthErrorFromCodes(code, bizCode int, msg, bizMsg string, includeLoginBan bool) *auth.AccountHealthError {
	message := strings.TrimSpace(bizMsg)
	if message == "" {
		message = strings.TrimSpace(msg)
	}
	switch {
	case (includeLoginBan && (code == loginUserBannedCode || bizCode == loginUserBannedCode)) ||
		code == upstreamUserBannedCode || bizCode == upstreamUserBannedCode ||
		code == upstreamAccountDisabledCode || bizCode == upstreamAccountDisabledCode ||
		explicitPermanentBanMessage(message):
		if message == "" {
			message = defaultBannedMessage
		}
		return &auth.AccountHealthError{State: account.HealthPermanentlyBanned, Code: firstNonZero(bizCode, code), Message: message}
	case includeLoginBan && loginCredentialsRejected(msg, bizMsg):
		if message == "" {
			message = defaultInvalidCredentialsMessage
		}
		return &auth.AccountHealthError{State: account.HealthInvalidCredentials, Code: firstNonZero(bizCode, code), Message: message}
	case code == upstreamMutedCode || bizCode == upstreamMutedCode || explicitMuteMessage(message):
		if message == "" {
			message = defaultMuteMessage
		}
		return &auth.AccountHealthError{State: account.HealthTemporarilyMuted, Code: firstNonZero(bizCode, code), Message: message}
	default:
		return nil
	}
}

func explicitPermanentBanMessage(message string) bool {
	normalized := normalizedAccountFailureMessage(message)
	patterns := []string{
		"user is banned",
		"account is banned",
		"user has been banned",
		"account has been banned",
		"用户已被封禁",
		"账号已被封禁",
		"账户已被封禁",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func explicitMuteMessage(message string) bool {
	normalized := normalizedAccountFailureMessage(message)
	patterns := []string{
		"user is muted",
		"account is muted",
		"user has been muted",
		"account has been muted",
		"用户已被禁言",
		"账号已被禁言",
		"账户已被禁言",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func normalizedAccountFailureMessage(message string) string {
	return strings.ToLower(strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(message)))
}

func loginCredentialsRejected(msg, bizMsg string) bool {
	combined := strings.ToLower(strings.TrimSpace(msg) + " " + strings.TrimSpace(bizMsg))
	combined = strings.NewReplacer("_", " ", "-", " ").Replace(combined)
	patterns := []string{
		"password or user name is wrong",
		"username or password is wrong",
		"user name or password is wrong",
		"account or password is wrong",
		"email or password is wrong",
		"invalid password",
		"incorrect password",
		"wrong password",
		"用户名或密码",
		"账号或密码",
		"账户或密码",
		"密码错误",
		"密码不正确",
	}
	for _, pattern := range patterns {
		if strings.Contains(combined, pattern) {
			return true
		}
	}
	return false
}

func (c *Client) markAccountHealth(a *auth.RequestAuth, code, bizCode int, msg, bizMsg string) error {
	healthErr := accountHealthErrorFromResponse(code, bizCode, msg, bizMsg)
	if healthErr == nil {
		return nil
	}
	if c == nil || c.Auth == nil || a == nil || strings.TrimSpace(a.AccountID) == "" {
		return healthErr
	}
	if !a.UseConfigToken && c.Store != nil {
		if _, managed := c.Store.FindAccount(a.AccountID); !managed {
			return healthErr
		}
	} else if !a.UseConfigToken {
		return healthErr
	}
	c.Auth.MarkAccountHealth(a.AccountID, healthErr)
	return healthErr
}

func (c *Client) markAccountRateLimited(a *auth.RequestAuth, status, code, bizCode int, msg, bizMsg, retryAfter string, extraMessages ...string) {
	if rateLimitScopeFromResponse(status, code, bizCode, msg, bizMsg, extraMessages...) == RateLimitScopeSessionCapacity {
		return
	}
	healthErr := accountRateLimitError(status, code, bizCode, msg, bizMsg, retryAfter)
	if healthErr == nil {
		return
	}
	if c == nil || c.Auth == nil || a == nil || strings.TrimSpace(a.AccountID) == "" || !a.UseConfigToken {
		return
	}
	if c.Store != nil {
		if _, managed := c.Store.FindAccount(a.AccountID); !managed {
			return
		}
	}
	c.Auth.MarkAccountHealth(a.AccountID, healthErr)
}

func rateLimitScopeFromResponse(status, code, bizCode int, msg, bizMsg string, extraMessages ...string) RateLimitScope {
	if isSessionCapacityRateLimit(status, code, bizCode, msg, bizMsg, extraMessages...) {
		return RateLimitScopeSessionCapacity
	}
	if accountRateLimitError(status, code, bizCode, msg, bizMsg, "") != nil {
		return RateLimitScopeAccount
	}
	return ""
}

func isSessionCapacityRateLimit(status, code, bizCode int, msg, bizMsg string, extraMessages ...string) bool {
	if status != http.StatusTooManyRequests && code != http.StatusTooManyRequests && bizCode != http.StatusTooManyRequests {
		return false
	}
	parts := []string{strings.TrimSpace(msg), strings.TrimSpace(bizMsg)}
	parts = append(parts, extraMessages...)
	message := normalizedAccountFailureMessage(strings.Join(parts, " "))
	if message == "" {
		return false
	}
	patterns := []string{
		"conversation context", "conversation limit", "conversation turn", "conversation has reached",
		"session context", "session limit", "session turn", "session has reached",
		"maximum turns", "maximum messages", "too many messages", "context window",
		"context length", "prompt is too long", "input is too long",
		"会话上下文", "会话轮次", "会话达到上限", "会话已达到上限",
		"对话上下文", "对话轮次", "对话达到上限", "上下文长度", "上下文超限",
	}
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func (c *Client) markAccountRateLimitedFromContext(ctx context.Context, status, code, bizCode int, msg, bizMsg, retryAfter string) {
	a, _ := auth.FromContext(ctx)
	c.markAccountRateLimited(a, status, code, bizCode, msg, bizMsg, retryAfter)
}

func accountRateLimitError(status, code, bizCode int, msg, bizMsg, retryAfter string) *auth.AccountHealthError {
	if status != 429 && code != 429 && bizCode != 429 {
		return nil
	}
	message := strings.TrimSpace(bizMsg)
	if message == "" {
		message = strings.TrimSpace(msg)
	}
	if message == "" {
		message = defaultRateLimitMessage
	}
	return &auth.AccountHealthError{
		State:   account.HealthRateLimited,
		Until:   rateLimitUntil(retryAfter),
		Code:    429,
		Message: message,
	}
}

func rateLimitUntil(retryAfter string) time.Time {
	now := time.Now()
	retryAfter = strings.TrimSpace(retryAfter)
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
		cooldown := time.Duration(seconds) * time.Second
		if cooldown > maxRateLimitCooldown {
			cooldown = maxRateLimitCooldown
		}
		return now.Add(cooldown)
	}
	if parsed, err := time.Parse(http.TimeFormat, retryAfter); err == nil && parsed.After(now) {
		if parsed.Sub(now) > maxRateLimitCooldown {
			return now.Add(maxRateLimitCooldown)
		}
		return parsed
	}
	return now.Add(defaultRateLimitStateCooldown)
}

func (c *Client) markAccountHealthFromContext(ctx context.Context, code, bizCode int, msg, bizMsg string) error {
	a, _ := auth.FromContext(ctx)
	return c.markAccountHealth(a, code, bizCode, msg, bizMsg)
}

func (c *Client) refreshManagedToken(ctx context.Context, a *auth.RequestAuth, status, code, bizCode int, msg, bizMsg string) bool {
	if c == nil || c.Auth == nil || a == nil || !a.UseConfigToken || !shouldAttemptRefresh(status, code, bizCode, msg, bizMsg) {
		return false
	}
	return c.Auth.RefreshToken(ctx, a)
}

func (c *Client) refreshManagedTokenFromContext(ctx context.Context, status, code, bizCode int, msg, bizMsg string) (string, bool) {
	a, _ := auth.FromContext(ctx)
	if !c.refreshManagedToken(ctx, a, status, code, bizCode, msg, bizMsg) {
		return "", false
	}
	return a.DeepSeekToken, true
}

func (c *Client) markLoginAccountHealth(acc config.Account, healthErr *auth.AccountHealthError) {
	if c == nil || c.Auth == nil || healthErr == nil {
		return
	}
	accountID := strings.TrimSpace(acc.Identifier())
	if accountID == "" {
		return
	}
	if c.Store != nil {
		if _, managed := c.Store.FindAccount(accountID); !managed {
			return
		}
	}
	c.Auth.MarkAccountHealth(accountID, healthErr)
}

func accountHealthErrorFromUser(user map[string]any) *auth.AccountHealthError {
	if user == nil {
		return nil
	}
	chat, _ := user["chat"].(map[string]any)
	if !truthy(chat["is_muted"]) {
		return nil
	}
	until := unixSeconds(chat["mute_until"])
	return &auth.AccountHealthError{
		State:   account.HealthTemporarilyMuted,
		Until:   until,
		Code:    upstreamMutedCode,
		Message: defaultMuteMessage,
	}
}

func unixSeconds(value any) time.Time {
	var seconds float64
	switch v := value.(type) {
	case float64:
		seconds = v
	case float32:
		seconds = float64(v)
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	case string:
		seconds, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	if seconds <= 0 {
		return time.Time{}
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * 1e9)
	return time.Unix(whole, nanos)
}

func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || strings.TrimSpace(v) == "1"
	default:
		return false
	}
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
