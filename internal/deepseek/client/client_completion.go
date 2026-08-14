package client

import (
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	trans "DeepSeek_Web_To_API/internal/deepseek/transport"
)

const completionFailureBodyLimit = 4096
const completionSSEPreludeLimit = 64 * 1024

var errNoCompletionSwitchCandidate = errors.New("no completion switch candidate")

func (c *Client) CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error) {
	return c.callCompletion(ctx, a, payload, powResp, maxAttempts, true, true)
}

func (c *Client) CallCompletionPinned(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string) (*http.Response, error) {
	return c.callCompletion(ctx, a, payload, powResp, 1, false, true)
}

// CallCompletionRaw returns the upstream completion stream without the
// auto-continue pipe. Callers that deliberately interrupt generation need
// Close on the returned body to reach the underlying HTTP stream immediately.
func (c *Client) CallCompletionRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error) {
	return c.callCompletion(ctx, a, payload, powResp, maxAttempts, true, false)
}

func (c *Client) CallCompletionPinnedRaw(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string) (*http.Response, error) {
	return c.callCompletion(ctx, a, payload, powResp, 1, false, false)
}

func (c *Client) callCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int, allowAccountSwitch, autoContinue bool) (*http.Response, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	if failure := requestContextFailure("completion", ctx, nil); failure != nil {
		return nil, failure
	}
	clients := c.requestClientsForAuth(ctx, a)
	headers := c.authHeaders(a.DeepSeekToken)
	headers["x-ds-pow-response"] = powResp
	attempts := 0
	refreshed := false
	var lastErr error
	for attempts < maxAttempts {
		captureSession := c.capture.Start("deepseek_completion", dsprotocol.DeepSeekCompletionURL, a.AccountID, payload)
		resp, err := c.streamPost(ctx, clients.stream, dsprotocol.DeepSeekCompletionURL, headers, payload)
		if err != nil {
			lastErr = transportFailure("completion", ctx, err)
			config.Logger.Warn("[completion] request failed", "account", accountIDForLog(a), "failure_kind", requestFailureKind(lastErr), "error", err)
			if !completionFailureRetryable(lastErr) {
				return nil, lastErr
			}
			attempts++
			if attempts >= maxAttempts {
				break
			}
			if allowAccountSwitch {
				if switchErr := c.switchCompletionAccount(ctx, a, &clients, &headers, payload); switchErr == nil {
					continue
				} else if !errors.Is(switchErr, errNoCompletionSwitchCandidate) {
					config.Logger.Warn("[completion] switch account failed", "account", accountIDForLog(a), "error", switchErr)
					return nil, firstError(switchErr, lastErr)
				}
			}
			if err := sleepCompletionRetry(ctx, time.Second); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			resp.Body = c.wrapAccountHealthBody(a, resp.Body)
			if captureSession != nil {
				resp.Body = captureSession.WrapBody(resp.Body, resp.StatusCode)
			}
			healthErr, inspectErr := c.inspectCompletionSSEPrelude(a, resp)
			if inspectErr != nil {
				lastErr = transportFailure("completion", ctx, inspectErr)
				config.Logger.Warn("[completion] SSE prelude inspection failed", "account", accountIDForLog(a), "failure_kind", requestFailureKind(lastErr), "error", inspectErr)
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
				if !completionFailureRetryable(lastErr) {
					return nil, lastErr
				}
				attempts++
				if attempts >= maxAttempts {
					break
				}
				if allowAccountSwitch {
					if switchErr := c.switchCompletionAccount(ctx, a, &clients, &headers, payload); switchErr == nil {
						continue
					} else if !errors.Is(switchErr, errNoCompletionSwitchCandidate) {
						return nil, firstError(switchErr, lastErr)
					}
				}
				continue
			}
			if healthErr != nil {
				lastErr = healthErr
				config.Logger.Warn("[completion] upstream SSE reported account health failure", "account", accountIDForLog(a), "state", healthErr.State, "code", healthErr.Code, "error", healthErr)
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
				if allowAccountSwitch && a.UseConfigToken && c.hasCompletionSwitchCandidate(a) {
					if switchErr := c.switchCompletionAccount(ctx, a, &clients, &headers, payload); switchErr == nil {
						continue
					} else if !errors.Is(switchErr, errNoCompletionSwitchCandidate) {
						return nil, firstError(switchErr, lastErr)
					}
				}
				break
			}
			if autoContinue {
				resp = c.wrapCompletionWithAutoContinue(ctx, a, payload, powResp, resp)
			}
			return resp, nil
		}
		if captureSession != nil {
			resp.Body = captureSession.WrapBody(resp.Body, resp.StatusCode)
		}
		lastErr = c.completionStatusFailure(a, resp)
		config.Logger.Warn("[completion] upstream returned non-OK status", "account", accountIDForLog(a), "status", resp.StatusCode, "failure_kind", requestFailureKind(lastErr), "error", lastErr)
		if resp.Body != nil {
			if err := resp.Body.Close(); err != nil {
				config.Logger.Warn("[completion] close upstream response body failed", "account", accountIDForLog(a), "status", resp.StatusCode, "error", err)
			}
		}
		var healthErr *auth.AccountHealthError
		if errors.As(lastErr, &healthErr) {
			if allowAccountSwitch && a.UseConfigToken && c.hasCompletionSwitchCandidate(a) {
				if switchErr := c.switchCompletionAccount(ctx, a, &clients, &headers, payload); switchErr == nil {
					continue
				} else if !errors.Is(switchErr, errNoCompletionSwitchCandidate) {
					return nil, firstError(switchErr, lastErr)
				}
			}
			break
		}
		if !refreshed && IsManagedUnauthorizedError(lastErr) && c.Auth != nil && c.Auth.RefreshToken(ctx, a) {
			refreshed = true
			clients = c.requestClientsForAuth(ctx, a)
			headers = c.authHeaders(a.DeepSeekToken)
			headers["x-ds-pow-response"] = powResp
			continue
		}
		// 429 from upstream means the account is rate-limited right now.
		// Other accounts in the pool may have headroom — fail over to a
		// fresh one WITHOUT consuming the maxAttempts budget. Only when
		// the pool is exhausted (every managed account has been tried)
		// or the caller is on a direct token (no pool to switch to) do we
		// fall through to the normal "count this as a real attempt and
		// possibly back off" path. Without this special case the chat
		// handler's hard-coded maxAttempts=3 would surface 429 to the
		// client even when the operator's pool has dozens of idle
		// accounts — the historical pain on /admin/metrics/overview's
		// failure rate.
		if allowAccountSwitch && resp.StatusCode == http.StatusTooManyRequests && a.UseConfigToken && c.hasCompletionSwitchCandidate(a) {
			if switchErr := c.switchCompletionAccount(ctx, a, &clients, &headers, payload); switchErr == nil {
				config.Logger.Info("[completion] 429 fail-over to next account", "from", accountIDForLog(a), "tried", len(a.TriedAccounts))
				continue
			} else if !errors.Is(switchErr, errNoCompletionSwitchCandidate) {
				config.Logger.Warn("[completion] 429 switch account failed", "account", accountIDForLog(a), "error", switchErr)
				return nil, firstError(switchErr, lastErr)
			}
			// fall through to normal attempts-counted path below.
		}
		attempts++
		if attempts >= maxAttempts {
			break
		}
		if allowAccountSwitch {
			if switchErr := c.switchCompletionAccount(ctx, a, &clients, &headers, payload); switchErr == nil {
				continue
			} else if !errors.Is(switchErr, errNoCompletionSwitchCandidate) {
				config.Logger.Warn("[completion] switch account failed", "account", accountIDForLog(a), "error", switchErr)
				return nil, firstError(switchErr, lastErr)
			}
		}
		if err := sleepCompletionRetry(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &RequestFailure{Op: "completion", Kind: FailureUnknown, Message: "completion failed"}
}

func (c *Client) inspectCompletionSSEPrelude(a *auth.RequestAuth, resp *http.Response) (*auth.AccountHealthError, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	reader := bufio.NewReaderSize(resp.Body, completionSSEPreludeLimit)
	var replay bytes.Buffer
	for replay.Len() <= completionSSEPreludeLimit {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			replay.Write(line)
		}
		trimmed := strings.TrimSpace(string(line))
		payload, candidate := completionPreludePayload(trimmed)
		if candidate {
			if payload != "" && payload != "[DONE]" {
				var chunk map[string]any
				if json.Unmarshal([]byte(payload), &chunk) == nil {
					if healthErr := accountHealthErrorFromSSEChunk(chunk); healthErr != nil {
						if c != nil && c.Auth != nil && a != nil && a.UseConfigToken && strings.TrimSpace(a.AccountID) != "" {
							c.Auth.MarkAccountHealth(a.AccountID, healthErr)
						}
						resp.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(replay.Bytes()), reader), Closer: resp.Body}
						return healthErr, nil
					}
				}
			}
			resp.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(replay.Bytes()), reader), Closer: resp.Body}
			return nil, nil
		}
		if err != nil {
			resp.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(replay.Bytes()), reader), Closer: resp.Body}
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
	}
	resp.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(replay.Bytes()), reader), Closer: resp.Body}
	return nil, nil
}

func completionPreludePayload(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "data:") {
		return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
	}
	if strings.HasPrefix(line, "{") {
		return line, true
	}
	return "", false
}

type replayReadCloser struct {
	io.Reader
	io.Closer
}

func (c *Client) switchCompletionAccount(ctx context.Context, a *auth.RequestAuth, clients *requestClients, headers *map[string]string, payload map[string]any) error {
	if c == nil || c.Auth == nil || a == nil || !a.UseConfigToken {
		return errNoCompletionSwitchCandidate
	}
	if !c.hasCompletionSwitchCandidate(a) {
		return errNoCompletionSwitchCandidate
	}
	if !c.Auth.SwitchAccount(ctx, a) {
		if failure := requestContextFailure("completion", ctx, nil); failure != nil {
			return failure
		}
		return errNoCompletionSwitchCandidate
	}
	*clients = c.requestClientsForAuth(ctx, a)
	sessionID, err := c.createCompletionRetrySession(ctx, a, *clients)
	if err != nil {
		return err
	}
	powResp, err := c.getCompletionRetryPow(ctx, a, *clients)
	if err != nil {
		return err
	}
	payload["chat_session_id"] = sessionID
	nextHeaders := c.authHeaders(a.DeepSeekToken)
	nextHeaders["x-ds-pow-response"] = powResp
	*headers = nextHeaders
	return nil
}

func (c *Client) hasCompletionSwitchCandidate(a *auth.RequestAuth) bool {
	if c == nil || c.Store == nil || a == nil {
		return true
	}
	current := strings.TrimSpace(a.AccountID)
	for _, acc := range c.Store.Accounts() {
		candidate := strings.TrimSpace(acc.Identifier())
		if candidate == "" || candidate == current || acc.Disabled {
			continue
		}
		if a.TriedAccounts != nil && a.TriedAccounts[candidate] {
			continue
		}
		return true
	}
	return false
}

func (c *Client) createCompletionRetrySession(ctx context.Context, a *auth.RequestAuth, clients requestClients) (string, error) {
	headers := c.authHeaders(a.DeepSeekToken)
	resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekCreateSessionURL, headers, map[string]any{"agent": "chat"})
	if err != nil {
		return "", transportFailure("create session", ctx, err)
	}
	code, bizCode, msg, bizMsg := extractResponseStatus(resp)
	c.markAccountRateLimited(a, status, code, bizCode, msg, bizMsg, "")
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		if sessionID := extractCreateSessionID(resp); sessionID != "" {
			return sessionID, nil
		}
	}
	if healthErr := c.markAccountHealth(a, code, bizCode, msg, bizMsg); healthErr != nil {
		return "", healthErr
	}
	message := failureMessage(msg, bizMsg, "create session failed")
	kind := FailureUpstreamStatus
	if isTokenInvalid(status, code, bizCode, msg, bizMsg) || isAuthIndicativeBizFailure(msg, bizMsg) {
		kind = authFailureKind(a.UseConfigToken)
	}
	return "", &RequestFailure{Op: "create session", Kind: kind, StatusCode: status, Message: message}
}

func (c *Client) getCompletionRetryPow(ctx context.Context, a *auth.RequestAuth, clients requestClients) (string, error) {
	headers := c.authHeaders(a.DeepSeekToken)
	resp, status, err := c.postJSONWithStatus(ctx, clients.regular, clients.fallback, dsprotocol.DeepSeekCreatePowURL, headers, map[string]any{"target_path": dsprotocol.DeepSeekCompletionTargetPath})
	if err != nil {
		return "", transportFailure("get pow", ctx, err)
	}
	code, bizCode, msg, bizMsg := extractResponseStatus(resp)
	c.markAccountRateLimited(a, status, code, bizCode, msg, bizMsg, "")
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		data, _ := resp["data"].(map[string]any)
		bizData, _ := data["biz_data"].(map[string]any)
		challenge, _ := bizData["challenge"].(map[string]any)
		answer, err := ComputePow(ctx, challenge)
		if err != nil {
			return "", transportFailure("get pow", ctx, err)
		}
		return BuildPowHeader(challenge, answer)
	}
	if healthErr := c.markAccountHealth(a, code, bizCode, msg, bizMsg); healthErr != nil {
		return "", healthErr
	}
	message := failureMessage(msg, bizMsg, "get pow failed")
	kind := FailureUpstreamStatus
	if isTokenInvalid(status, code, bizCode, msg, bizMsg) || isAuthIndicativeBizFailure(msg, bizMsg) {
		kind = authFailureKind(a.UseConfigToken)
	}
	return "", &RequestFailure{Op: "get pow", Kind: kind, StatusCode: status, Message: message}
}

func (c *Client) streamPost(ctx context.Context, doer trans.Doer, url string, headers map[string]string, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers = c.jsonHeaders(headers)
	clients := c.requestClientsFromContext(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := doer.Do(req)
	if err != nil {
		if failure := requestContextFailure("completion", ctx, err); failure != nil {
			return nil, failure
		}
		config.Logger.Warn("[deepseek] fingerprint stream request failed, fallback to std transport", "url", url, "error", err)
		req2, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if reqErr != nil {
			return nil, reqErr
		}
		for k, v := range headers {
			req2.Header.Set(k, v)
		}
		resp, err = clients.fallbackS.Do(req2)
		if err != nil {
			return nil, err
		}
		if err := decodeResponseBody(resp); err != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				config.Logger.Warn("[deepseek] close fallback stream body failed", "url", url, "error", closeErr)
			}
			return nil, err
		}
		return resp, nil
	}
	if err := decodeResponseBody(resp); err != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			config.Logger.Warn("[deepseek] close stream body failed", "url", url, "error", closeErr)
		}
		return nil, err
	}
	return resp, nil
}

func (c *Client) completionStatusFailure(a *auth.RequestAuth, resp *http.Response) error {
	if resp == nil {
		return &RequestFailure{Op: "completion", Kind: FailureUpstreamStatus, Message: "missing upstream response"}
	}
	message := http.StatusText(resp.StatusCode)
	kind := FailureUpstreamStatus
	code, bizCode := 0, 0
	msg, bizMsg := "", ""
	if resp.Body != nil {
		body, err := io.ReadAll(io.LimitReader(resp.Body, completionFailureBodyLimit+1))
		if err != nil {
			return &RequestFailure{Op: "completion", Kind: FailureUpstreamNetwork, StatusCode: resp.StatusCode, Message: "read upstream error body: " + err.Error(), Cause: err}
		}
		if len(body) > completionFailureBodyLimit {
			body = body[:completionFailureBodyLimit]
		}
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
			message = trimmed
		}
		var parsed map[string]any
		if json.Unmarshal(body, &parsed) == nil {
			code, bizCode, msg, bizMsg = extractResponseStatus(parsed)
			if data, ok := parsed["data"].(map[string]any); ok {
				if code == 0 {
					code = intFrom(data["code"])
				}
				if bizCode == 0 {
					bizCode = intFrom(data["biz_code"])
				}
			}
			if healthErr := c.markAccountHealth(a, code, bizCode, msg, bizMsg); healthErr != nil {
				return healthErr
			}
		}
	}
	c.markAccountRateLimited(a, resp.StatusCode, code, bizCode, msg, bizMsg, resp.Header.Get("Retry-After"))
	if isTokenInvalid(resp.StatusCode, code, bizCode, msg, bizMsg) || isAuthIndicativeBizFailure(msg, bizMsg) {
		kind = authFailureKind(a != nil && a.UseConfigToken)
	}
	return &RequestFailure{Op: "completion", Kind: kind, StatusCode: resp.StatusCode, Message: message}
}

func completionFailureRetryable(err error) bool {
	return !IsClientCancelledError(err) && !IsUpstreamTimeoutError(err)
}

func sleepCompletionRetry(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return requestContextFailure("completion", ctx, nil)
	case <-timer.C:
		return nil
	}
}

func accountIDForLog(a *auth.RequestAuth) string {
	if a == nil {
		return ""
	}
	return a.AccountID
}
