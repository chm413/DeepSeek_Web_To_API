package proxysubscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxSubscriptionBytes = 8 << 20

func Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("subscription URL must use http or https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create subscription request: %w", err)
	}
	req.Header.Set("User-Agent", "Clash.Meta/1.19 DeepSeek-Web-To-API")
	req.Header.Set("Accept", "text/plain, application/yaml, application/json, */*")
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch subscription: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	if len(body) > maxSubscriptionBytes {
		return nil, errors.New("subscription response exceeds 8 MiB")
	}
	return body, nil
}
