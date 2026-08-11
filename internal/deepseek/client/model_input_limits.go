package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
)

const modelInputLimitsTTL = 10 * time.Minute

type cachedModelInputLimits struct {
	limits    config.ModelInputLimits
	expiresAt time.Time
}

// GetModelInputLimits reads the live model settings endpoint through the same
// authenticated, proxy-aware transport used by completions. Results are
// cached per upstream credential for a short period because this endpoint is
// account-scoped but the settings change infrequently.
func (c *Client) GetModelInputLimits(ctx context.Context, a *auth.RequestAuth) (config.ModelInputLimits, error) {
	if c == nil || a == nil || strings.TrimSpace(a.DeepSeekToken) == "" {
		return config.ModelInputLimits{}, fmt.Errorf("missing upstream authentication for model settings")
	}
	ctx = auth.WithAuth(ctx, a)
	key := modelInputLimitsCacheKey(a)
	now := time.Now()
	c.modelInputLimitsMu.Lock()
	if item, ok := c.modelInputLimits[key]; ok && now.Before(item.expiresAt) {
		c.modelInputLimitsMu.Unlock()
		return item.limits, nil
	}
	c.modelInputLimitsMu.Unlock()

	clients := c.requestClientsForAuth(ctx, a)
	settingsURL := dsprotocol.DeepSeekClientSettingsURL + "&did=" + url.QueryEscape(modelSettingsDID(a))
	for attempt := 0; attempt < 2; attempt++ {
		body, status, err := c.getJSONWithStatus(ctx, clients.regular, settingsURL, c.authHeaders(a.DeepSeekToken))
		if err != nil {
			return config.ModelInputLimits{}, fmt.Errorf("get model settings: %w", err)
		}
		code, bizCode, msg, bizMsg := extractResponseStatus(body)
		c.markAccountRateLimited(a, status, code, bizCode, msg, bizMsg, "")
		if healthErr := c.markAccountHealth(a, code, bizCode, msg, bizMsg); healthErr != nil {
			return config.ModelInputLimits{}, healthErr
		}
		if status < 200 || status >= 300 || code != 0 || bizCode != 0 {
			if attempt == 0 && c.refreshManagedToken(ctx, a, status, code, bizCode, msg, bizMsg) {
				continue
			}
			return config.ModelInputLimits{}, fmt.Errorf("model settings returned HTTP %d (code=%d, biz_code=%d): %s", status, code, bizCode, firstMessage(msg, bizMsg))
		}
		limits, err := parseModelInputLimits(body)
		if err != nil {
			return config.ModelInputLimits{}, err
		}

		c.modelInputLimitsMu.Lock()
		if c.modelInputLimits == nil {
			c.modelInputLimits = make(map[string]cachedModelInputLimits)
		}
		c.modelInputLimits[key] = cachedModelInputLimits{limits: limits, expiresAt: now.Add(modelInputLimitsTTL)}
		c.modelInputLimitsMu.Unlock()
		return limits, nil
	}
	return config.ModelInputLimits{}, fmt.Errorf("model settings failed after token refresh")
}

// modelSettingsDID mirrors the official web client's persistent UUID-shaped
// `did` query parameter. A deterministic value gives this stateless gateway
// the same stability without storing a browser localStorage identifier.
func modelSettingsDID(a *auth.RequestAuth) string {
	identity := ""
	if a != nil {
		identity = strings.TrimSpace(a.AccountID)
		if identity == "" {
			identity = a.DeepSeekToken
		}
	}
	sum := sha256.Sum256([]byte("deepseek-web-to-api/settings-did/" + identity))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func modelInputLimitsCacheKey(a *auth.RequestAuth) string {
	if id := strings.TrimSpace(a.AccountID); id != "" {
		return "account:" + id
	}
	sum := sha256.Sum256([]byte(a.DeepSeekToken))
	return "token:" + hex.EncodeToString(sum[:])
}

func firstMessage(msg, bizMsg string) string {
	if text := strings.TrimSpace(bizMsg); text != "" {
		return text
	}
	return strings.TrimSpace(msg)
}

func parseModelInputLimits(root map[string]any) (config.ModelInputLimits, error) {
	var limits config.ModelInputLimits
	walkModelInputLimits(root, nil, &limits)
	if limits.Default <= 0 && limits.Expert <= 0 {
		return config.ModelInputLimits{}, fmt.Errorf("model settings did not contain input_character_limit values")
	}
	return limits, nil
}

func walkModelInputLimits(value any, path []string, limits *config.ModelInputLimits) {
	switch v := value.(type) {
	case map[string]any:
		if raw, ok := v["input_character_limit"]; ok {
			limit := intFrom(raw)
			if limit > 0 {
				classifyModelInputLimit(limit, path, v, limits)
			}
		}
		for key, child := range v {
			if key == "input_character_limit" {
				continue
			}
			walkModelInputLimits(child, appendPath(path, key), limits)
		}
	case []any:
		for i, child := range v {
			walkModelInputLimits(child, appendPath(path, fmt.Sprintf("[%d]", i)), limits)
		}
	}
}

func classifyModelInputLimit(limit int, path []string, item map[string]any, limits *config.ModelInputLimits) {
	parts := append([]string(nil), path...)
	for _, key := range []string{"model", "model_id", "model_type", "type", "name", "id"} {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	label := strings.ToLower(strings.Join(parts, "/"))
	switch {
	case strings.Contains(label, "expert"), strings.Contains(label, "pro"):
		if limits.Expert == 0 || limit < limits.Expert {
			limits.Expert = limit
		}
	case strings.Contains(label, "default"), strings.Contains(label, "flash"), strings.Contains(label, "vision"):
		if limits.Default == 0 || limit < limits.Default {
			limits.Default = limit
		}
	}
}

func appendPath(path []string, part string) []string {
	out := make([]string, 0, len(path)+1)
	out = append(out, path...)
	return append(out, part)
}
