package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
)

func TestAccountHealthErrorFromResponseUsesExplicitCodes(t *testing.T) {
	if err := loginAccountHealthErrorFromResponse(10, 0, "", ""); err == nil || err.State != account.HealthPermanentlyBanned {
		t.Fatalf("expected permanent ban error, got %#v", err)
	}
	if err := accountHealthErrorFromResponse(10, 0, "", ""); err != nil {
		t.Fatalf("generic code 10 must not imply a permanent ban, got %#v", err)
	}
	if err := accountHealthErrorFromResponse(0, 50006, "", ""); err == nil || err.State != account.HealthTemporarilyMuted {
		t.Fatalf("expected temporary mute error, got %#v", err)
	}
	if err := accountHealthErrorFromResponse(429, 0, "rate limited", ""); err != nil {
		t.Fatalf("429 must not be treated as account health, got %#v", err)
	}
	if err := loginAccountHealthErrorFromResponse(0, 0, "PASSWORD_OR_USER_NAME_IS_WRONG", ""); err == nil || err.State != account.HealthInvalidCredentials {
		t.Fatalf("expected invalid credential error, got %#v", err)
	}
}

func TestAccountHealthErrorFromUserParsesMuteUntil(t *testing.T) {
	until := float64(time.Now().Add(time.Hour).Unix())
	err := accountHealthErrorFromUser(map[string]any{
		"chat": map[string]any{"is_muted": 1, "mute_until": until},
	})
	if err == nil || err.State != account.HealthTemporarilyMuted {
		t.Fatalf("expected temporary mute, got %#v", err)
	}
	if err.Until.IsZero() || err.Until.Unix() != int64(until) {
		t.Fatalf("unexpected mute expiry: %v", err.Until)
	}
}

func TestCompletion429MarksManagedAccountTemporarilyRateLimited(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"limited@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) { return acc.Token, nil })
	client := &Client{Store: store, Auth: resolver}
	acc, _ := store.FindAccount("limited@example.com")
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: acc.Identifier(), Account: acc, DeepSeekToken: acc.Token}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"120"}},
		Body:       io.NopCloser(strings.NewReader(`{"code":429,"msg":"too many requests"}`)),
	}
	err := client.completionStatusFailure(a, resp)
	var failure *RequestFailure
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected normal 429 request failure, got %#v", err)
	}
	health, ok := pool.AccountHealth(acc.Identifier())
	if !ok || health.State != account.HealthRateLimited {
		t.Fatalf("expected temporary rate-limit state, got %#v, %v", health, ok)
	}
	remaining := time.Until(health.Until)
	if remaining < 110*time.Second || remaining > 125*time.Second {
		t.Fatalf("unexpected Retry-After cooldown: %v", remaining)
	}
	updated, _ := store.FindAccount(acc.Identifier())
	if updated.Disabled {
		t.Fatal("429 must not persistently disable the account")
	}
}

func TestCompletionBodyMarksSSEMuteWithoutChangingBytes(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"muted@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	client := &Client{Auth: resolver}
	acc, ok := store.FindAccount("muted@example.com")
	if !ok {
		t.Fatal("missing test account")
	}
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: acc.Identifier(), Account: acc}
	until := time.Now().Add(time.Hour).Unix()
	body := fmt.Sprintf("data: {\"code\":50006,\"data\":{\"mute_until\":%d}}\n", until)
	wrapped := client.wrapAccountHealthBody(a, io.NopCloser(strings.NewReader(body)))
	got, err := io.ReadAll(wrapped)
	if err != nil {
		t.Fatalf("read wrapped body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("health wrapper changed SSE bytes: %q", got)
	}
	health, ok := pool.AccountHealth(acc.Identifier())
	if !ok || health.State != account.HealthTemporarilyMuted {
		t.Fatalf("expected SSE mute to mark account, got %#v, %v", health, ok)
	}
}

func TestLoginReturnsPermanentBanForTopLevelCodeTen(t *testing.T) {
	client := &Client{
		regular: doerFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != dsprotocol.DeepSeekLoginURL {
				t.Fatalf("unexpected login URL: %s", req.URL)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":10,"msg":"USER_IS_BANNED","data":{}}`)),
			}, nil
		}),
		fallback: &http.Client{},
	}
	_, err := client.Login(context.Background(), config.Account{Email: "banned@example.com", Password: "pwd"})
	var healthErr *auth.AccountHealthError
	if !errors.As(err, &healthErr) || healthErr.State != account.HealthPermanentlyBanned {
		t.Fatalf("expected permanent ban error, got %T: %v", err, err)
	}
}

func TestLoginInvalidCredentialsAutomaticallyDisablesManagedAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"invalid@example.com","password":"wrong"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		return "", errors.New("unused")
	})
	client := &Client{
		Store: store,
		Auth:  resolver,
		regular: doerFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"biz_code":10001,"biz_msg":"PASSWORD_OR_USER_NAME_IS_WRONG"}}`)),
			}, nil
		}),
		fallback: &http.Client{},
	}

	_, err := client.Login(context.Background(), config.Account{Email: "invalid@example.com", Password: "wrong"})
	var healthErr *auth.AccountHealthError
	if !errors.As(err, &healthErr) || healthErr.State != account.HealthInvalidCredentials {
		t.Fatalf("expected invalid credential health error, got %T: %v", err, err)
	}
	acc, ok := store.FindAccount("invalid@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != config.AccountDisabledInvalidCredentials {
		t.Fatalf("expected automatic invalid credential disable, got %#v, %v", acc, ok)
	}
}

func TestCheckAccountHealthRejectsUnrefreshableInvalidToken(t *testing.T) {
	client := &Client{
		regular: doerFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":40001,"msg":"token expired"}`)),
			}, nil
		}),
		fallback: &http.Client{},
	}
	_, err := client.CheckAccountHealth(context.Background(), config.Account{Email: "token-only@example.com", Token: "expired-token"})
	var healthErr *auth.AccountHealthError
	if !errors.As(err, &healthErr) || healthErr.State != account.HealthInvalidCredentials {
		t.Fatalf("expected unrefreshable token to be invalid credentials, got %#v, %v", healthErr, err)
	}
}

func TestSessionEndpointBanAutomaticallyDisablesManagedAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"banned@example.com","token":"token-1"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) { return acc.Token, nil })
	client := &Client{
		Store: store,
		Auth:  resolver,
		regular: doerFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"biz_code":40012,"biz_msg":"USER_IS_BANNED"}}`)),
			}, nil
		}),
		fallback: &http.Client{},
	}
	acc, _ := store.FindAccount("banned@example.com")
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: acc.Identifier(), Account: acc, DeepSeekToken: acc.Token}
	_, err := client.GetSessionCount(context.Background(), a, 1)
	var healthErr *auth.AccountHealthError
	if !errors.As(err, &healthErr) || healthErr.State != account.HealthPermanentlyBanned {
		t.Fatalf("expected session endpoint ban, got %#v, %v", healthErr, err)
	}
	updated, ok := store.FindAccount("banned@example.com")
	if !ok || !updated.Disabled || updated.DisabledReason != config.AccountDisabledUpstreamBanned {
		t.Fatalf("expected session endpoint to auto-disable account, got %#v, %v", updated, ok)
	}
}

func TestForTokenAuthFailureRefreshesAndAutomaticallyDisablesRejectedCredentials(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"refresh-rejected@example.com","password":"wrong","token":"expired-token"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	var client *Client
	resolver := auth.NewResolver(store, pool, func(ctx context.Context, acc config.Account) (string, error) {
		return client.Login(ctx, acc)
	})
	client = &Client{
		Store: store,
		Auth:  resolver,
		regular: doerFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":40001,"msg":"token expired"}`
			status := http.StatusUnauthorized
			if req.URL.String() == dsprotocol.DeepSeekLoginURL {
				body = `{"code":0,"data":{"biz_code":10001,"biz_msg":"PASSWORD_OR_USER_NAME_IS_WRONG"}}`
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
		fallback: &http.Client{},
	}
	acc, _ := store.FindAccount("refresh-rejected@example.com")
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: acc.Identifier(), Account: acc, DeepSeekToken: acc.Token}
	ctx := auth.WithAuth(context.Background(), a)

	_, err := client.GetSessionCountForToken(ctx, acc.Token)
	if err == nil {
		t.Fatal("expected rejected refresh to fail")
	}
	updated, ok := store.FindAccount(acc.Identifier())
	if !ok || !updated.Disabled || updated.DisabledReason != config.AccountDisabledInvalidCredentials {
		t.Fatalf("expected rejected refresh to auto-disable account, got %#v, %v", updated, ok)
	}
}

func TestForTokenAuthFailureRetriesOnceWithRefreshedToken(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"accounts":[{"email":"refresh-ok@example.com","password":"correct","token":"expired-token"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	var client *Client
	resolver := auth.NewResolver(store, pool, func(ctx context.Context, acc config.Account) (string, error) {
		return client.Login(ctx, acc)
	})
	fetchCalls := 0
	client = &Client{
		Store: store,
		Auth:  resolver,
		regular: doerFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case dsprotocol.DeepSeekLoginURL:
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"biz_code":0,"biz_data":{"user":{"token":"fresh-token"}}}}`))}, nil
			default:
				fetchCalls++
				if fetchCalls == 1 {
					return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":40001,"msg":"token expired"}`))}, nil
				}
				if got := req.Header.Get("Authorization"); got != "Bearer fresh-token" {
					t.Fatalf("expected refreshed authorization header, got %q", got)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"biz_code":0,"biz_data":{"chat_sessions":[],"has_more":false}}}`))}, nil
			}
		}),
		fallback: &http.Client{},
	}
	acc, _ := store.FindAccount("refresh-ok@example.com")
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: acc.Identifier(), Account: acc, DeepSeekToken: acc.Token}
	ctx := auth.WithAuth(context.Background(), a)

	stats, err := client.GetSessionCountForToken(ctx, acc.Token)
	if err != nil || stats == nil || !stats.Success || fetchCalls != 2 {
		t.Fatalf("expected one refresh and successful retry, stats=%#v calls=%d err=%v", stats, fetchCalls, err)
	}
	updated, _ := store.FindAccount(acc.Identifier())
	if updated.Token != "fresh-token" || updated.Disabled {
		t.Fatalf("expected refreshed enabled account, got %#v", updated)
	}
}

func TestCheckAccountHealthUsesCurrentUserAndParsesMute(t *testing.T) {
	until := time.Now().Add(time.Hour).Unix()
	client := &Client{
		regular: doerFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || req.URL.String() != dsprotocol.DeepSeekCurrentUserURL {
				t.Fatalf("unexpected health request: %s %s", req.Method, req.URL)
			}
			if req.Header.Get("Authorization") != "Bearer token-1" {
				t.Fatalf("missing account token header: %#v", req.Header)
			}
			body := fmt.Sprintf(`{"code":0,"data":{"biz_code":0,"biz_data":{"user":{"chat":{"is_muted":1,"mute_until":%d}}}}}`, until)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
		fallback: &http.Client{},
	}
	_, err := client.CheckAccountHealth(context.Background(), config.Account{Email: "muted@example.com", Token: "token-1"})
	var healthErr *auth.AccountHealthError
	if !errors.As(err, &healthErr) || healthErr.State != account.HealthTemporarilyMuted || healthErr.Until.Unix() != until {
		t.Fatalf("expected current-user mute error, got %#v, %v", healthErr, err)
	}
}
