package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

func TestPinnedPowPreservesHTTP429ForBranchRecovery(t *testing.T) {
	doer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"rate limited","data":{"biz_code":0}}`)),
			Request:    req,
		}, nil
	})
	client := &Client{regular: doer, fallback: &http.Client{}, maxRetries: 1}
	_, err := client.GetPowPinned(context.Background(), &auth.RequestAuth{DeepSeekToken: "direct-token"})
	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected structured request failure, got %T: %v", err, err)
	}
	if failure.Kind != FailureUpstreamStatus || failure.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("pinned 429 was not preserved: %#v", failure)
	}
}

func TestCreateSessionPreservesHTTP429AfterPoolExhaustion(t *testing.T) {
	doer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"rate limited","data":{"biz_code":0}}`)),
			Request:    req,
		}, nil
	})
	client := &Client{regular: doer, fallback: &http.Client{}, maxRetries: 1}
	_, err := client.CreateSession(context.Background(), &auth.RequestAuth{DeepSeekToken: "direct-token"}, 1)
	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected structured request failure, got %T: %v", err, err)
	}
	if failure.Kind != FailureUpstreamStatus || failure.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("create-session 429 was not preserved: %#v", failure)
	}
}

func TestGetPowRateLimitFailoverDoesNotSpendAttemptBudget(t *testing.T) {
	client, a, seen := newManagedRateLimitFailoverClient(t)
	pow, err := client.GetPow(context.Background(), a, 1)
	if err != nil || pow == "" {
		t.Fatalf("expected replacement account PoW, pow=%q err=%v", pow, err)
	}
	assertManagedRateLimitFailover(t, a, *seen)
}

func TestCreateSessionRateLimitFailoverDoesNotSpendAttemptBudget(t *testing.T) {
	client, a, seen := newManagedRateLimitFailoverClient(t)
	sessionID, err := client.CreateSession(context.Background(), a, 1)
	if err != nil || sessionID == "" {
		t.Fatalf("expected replacement account session, session=%q err=%v", sessionID, err)
	}
	assertManagedRateLimitFailover(t, a, *seen)
}

func TestGetPowSessionCapacityDoesNotSwitchManagedAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"initial@example.com","password":"pwd","token":"token-1"},
			{"email":"replacement@example.com","password":"pwd","token":"token-2"}
		]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) { return acc.Token, nil })
	doer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":429,"msg":"maximum conversation turns reached"}`)),
			Request:    req,
		}, nil
	})
	client := &Client{Auth: resolver, Store: store, regular: doer, fallback: &http.Client{}, maxRetries: 1}
	initial, ok := store.FindAccount("initial@example.com")
	if !ok {
		t.Fatal("missing initial account")
	}
	a := &auth.RequestAuth{UseConfigToken: true, AccountID: initial.Identifier(), Account: initial, DeepSeekToken: initial.Token, TriedAccounts: map[string]bool{}}
	_, err := client.GetPow(context.Background(), a, 1)
	var failure *RequestFailure
	if !errors.As(err, &failure) || failure.RateLimitScope != RateLimitScopeSessionCapacity {
		t.Fatalf("expected session-capacity failure, got %#v", err)
	}
	if a.AccountID != initial.Identifier() {
		t.Fatalf("session capacity must keep the same account, got %q", a.AccountID)
	}
	if health, ok := pool.AccountHealth(initial.Identifier()); ok && health.State == account.HealthRateLimited {
		t.Fatalf("session capacity must not mark account rate limited: %#v", health)
	}
}

func newManagedRateLimitFailoverClient(t *testing.T) (*Client, *auth.RequestAuth, *[]string) {
	t.Helper()
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"limited@example.com","password":"pwd","token":"token-1"},
			{"email":"available@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		if acc.Email == "available@example.com" {
			return "token-2", nil
		}
		return "token-1", nil
	})
	seen := make([]string, 0, 2)
	doer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.Header.Get("authorization"))
		if strings.Contains(req.Header.Get("authorization"), "token-1") {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"rate limited","data":{"biz_code":0}}`)),
				Request:    req,
			}, nil
		}
		return completionSwitchRegularDoer{}.Do(req)
	})
	client := &Client{
		Auth:       resolver,
		Store:      store,
		regular:    doer,
		fallback:   &http.Client{},
		maxRetries: 1,
	}
	first, ok := store.FindAccount("limited@example.com")
	if !ok {
		t.Fatal("missing limited account")
	}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-1",
		AccountID:      first.Identifier(),
		Account:        first,
		TriedAccounts:  map[string]bool{},
	}
	return client, a, &seen
}

func assertManagedRateLimitFailover(t *testing.T, a *auth.RequestAuth, seen []string) {
	t.Helper()
	if a.AccountID != "available@example.com" {
		t.Fatalf("429 did not move to the available account: %q", a.AccountID)
	}
	if len(seen) != 2 || !strings.Contains(seen[1], "token-2") {
		t.Fatalf("new account was not attempted after the first 429: %#v", seen)
	}
}
