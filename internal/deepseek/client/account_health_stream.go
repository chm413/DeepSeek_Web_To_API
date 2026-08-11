package client

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
)

const maxAccountHealthSSELineBytes = 64 * 1024

type accountHealthReadCloser struct {
	base     io.ReadCloser
	onHealth func(*auth.AccountHealthError)
	pending  []byte
	markOnce sync.Once
}

func (c *Client) wrapAccountHealthBody(a *auth.RequestAuth, body io.ReadCloser) io.ReadCloser {
	if c == nil || c.Auth == nil || a == nil || !a.UseConfigToken || a.AccountID == "" || body == nil {
		return body
	}
	accountID := a.AccountID
	return &accountHealthReadCloser{
		base: body,
		onHealth: func(err *auth.AccountHealthError) {
			c.Auth.MarkAccountHealth(accountID, err)
		},
	}
}

func (r *accountHealthReadCloser) Read(p []byte) (int, error) {
	n, err := r.base.Read(p)
	if n > 0 {
		r.inspect(p[:n])
	}
	if err == io.EOF && len(r.pending) > 0 {
		r.inspectLine(r.pending)
		r.pending = nil
	}
	return n, err
}

func (r *accountHealthReadCloser) Close() error {
	return r.base.Close()
}

func (r *accountHealthReadCloser) inspect(chunk []byte) {
	r.pending = append(r.pending, chunk...)
	for {
		newline := bytes.IndexByte(r.pending, '\n')
		if newline < 0 {
			if len(r.pending) > maxAccountHealthSSELineBytes {
				r.pending = nil
			}
			return
		}
		line := r.pending[:newline]
		r.pending = r.pending[newline+1:]
		r.inspectLine(line)
	}
}

func (r *accountHealthReadCloser) inspectLine(line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	var chunk map[string]any
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return
	}
	if healthErr := accountHealthErrorFromSSEChunk(chunk); healthErr != nil {
		r.markOnce.Do(func() {
			if r.onHealth != nil {
				r.onHealth(healthErr)
			}
		})
	}
}

func accountHealthErrorFromSSEChunk(chunk map[string]any) *auth.AccountHealthError {
	if chunk == nil {
		return nil
	}
	code := numericCode(chunk["code"])
	message := stringField(chunk, "msg", "message")
	data, _ := chunk["data"].(map[string]any)
	if code == 0 && data != nil {
		code = firstNonZero(numericCode(data["code"]), numericCode(data["biz_code"]))
		if message == "" {
			message = stringField(data, "biz_msg", "msg", "message")
		}
	}
	if errData, ok := chunk["error"].(map[string]any); ok {
		if code == 0 {
			code = numericCode(errData["code"])
		}
		if message == "" {
			message = stringField(errData, "message", "msg")
		}
	}

	switch code {
	case upstreamMutedCode:
		until := unixSeconds(valueFromMaps("mute_until", data, chunk))
		if until.IsZero() {
			until = unixSeconds(valueFromMaps("end_at", data, chunk))
		}
		if message == "" {
			message = defaultMuteMessage
		}
		return &auth.AccountHealthError{State: account.HealthTemporarilyMuted, Until: until, Code: code, Message: message}
	case upstreamUserBannedCode:
		if message == "" {
			message = defaultBannedMessage
		}
		return &auth.AccountHealthError{State: account.HealthPermanentlyBanned, Code: code, Message: message}
	default:
		return nil
	}
}

func numericCode(value any) int {
	if code := intFrom(value); code != 0 {
		return code
	}
	if raw, ok := value.(string); ok {
		code, _ := strconv.Atoi(strings.TrimSpace(raw))
		return code
	}
	return 0
}

func stringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func valueFromMaps(key string, maps ...map[string]any) any {
	for _, values := range maps {
		if values == nil {
			continue
		}
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}
