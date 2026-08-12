package client

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	powpkg "DeepSeek_Web_To_API/pow"
)

type completionSwitchDoer struct {
	seenTokens   []string
	seenSessions []string
}

func (d *completionSwitchDoer) Do(req *http.Request) (*http.Response, error) {
	d.seenTokens = append(d.seenTokens, strings.TrimSpace(req.Header.Get("authorization")))
	d.seenSessions = append(d.seenSessions, requestBodyString(req))
	if strings.Contains(req.Header.Get("authorization"), "token-1") {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"busy"}`)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: {\"v\":\"ok\"}\n" + "data: [DONE]\n")),
		Request:    req,
	}, nil
}

type completionSwitchRegularDoer struct{}

func (completionSwitchRegularDoer) Do(req *http.Request) (*http.Response, error) {
	switch req.URL.String() {
	case dsprotocol.DeepSeekCreateSessionURL:
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"ok","data":{"biz_code":0,"biz_data":{"id":"session-2"}}}`)),
			Request:    req,
		}, nil
	case dsprotocol.DeepSeekCreatePowURL:
		challengeHash := powpkg.DeepSeekHashV1([]byte(powpkg.BuildPrefix("salt", 1712345678) + "0"))
		body := `{"code":0,"msg":"ok","data":{"biz_code":0,"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` +
			hex.EncodeToString(challengeHash[:]) +
			`","salt":"salt","expire_at":1712345678,"difficulty":1,"signature":"sig","target_path":"` +
			dsprotocol.DeepSeekCompletionTargetPath + `"}}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	default:
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected url"}`)),
			Request:    req,
		}, nil
	}
}

func TestCallCompletionSwitchesAccountAfterFailedAttempt(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		if acc.Email == "acc2@example.com" {
			return "token-2", nil
		}
		return "token-1", nil
	})
	doer := &completionSwitchDoer{}
	client := &Client{
		Auth:       resolver,
		Store:      store,
		regular:    completionSwitchRegularDoer{},
		stream:     doer,
		fallbackS:  &http.Client{},
		fallback:   &http.Client{},
		maxRetries: 3,
	}
	first, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("missing first account")
	}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-1",
		AccountID:      "acc1@example.com",
		Account:        first,
		TriedAccounts:  map[string]bool{},
	}
	resp, err := client.CallCompletion(context.Background(), a, map[string]any{"chat_session_id": "s"}, "pow", 2)
	if err != nil {
		t.Fatalf("CallCompletion returned error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if a.AccountID != "acc2@example.com" {
		t.Fatalf("expected switched account, got %q", a.AccountID)
	}
	if len(doer.seenTokens) != 2 || !strings.Contains(doer.seenTokens[1], "token-2") {
		t.Fatalf("expected retry with second token, saw %#v", doer.seenTokens)
	}
	if len(doer.seenSessions) != 2 || !strings.Contains(doer.seenSessions[1], "session-2") {
		t.Fatalf("expected retry payload to use new session, saw %#v", doer.seenSessions)
	}
}

func TestCallCompletionSwitchesAccountAfterHTTP200BusinessMute(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	doer := &completionSwitchDoer{}
	doerResponse := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		doer.seenTokens = append(doer.seenTokens, strings.TrimSpace(req.Header.Get("authorization")))
		if strings.Contains(req.Header.Get("authorization"), "token-1") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("{\"code\":0,\"msg\":\"\",\"data\":{\"biz_code\":5,\"biz_msg\":\"user is muted\",\"biz_data\":null}}")),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("data: {\"p\":\"response/content\",\"v\":\"ok\"}\n" + "data: [DONE]\n")),
			Request:    req,
		}, nil
	})
	client := &Client{
		Auth:       resolver,
		Store:      store,
		regular:    completionSwitchRegularDoer{},
		stream:     doerResponse,
		fallbackS:  &http.Client{},
		fallback:   &http.Client{},
		maxRetries: 3,
	}
	first, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("missing first account")
	}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-1",
		AccountID:      "acc1@example.com",
		Account:        first,
		TriedAccounts:  map[string]bool{},
	}
	resp, err := client.CallCompletion(context.Background(), a, map[string]any{"chat_session_id": "s"}, "pow", 2)
	if err != nil {
		t.Fatalf("CallCompletion returned error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read completion body: %v", err)
	}
	if !strings.Contains(string(body), `"v":"ok"`) {
		t.Fatalf("unexpected replayed completion body: %q", body)
	}
	if a.AccountID != "acc2@example.com" {
		t.Fatalf("expected switched account, got %q", a.AccountID)
	}
	health, ok := pool.AccountHealth("acc1@example.com")
	if !ok || health.State != account.HealthTemporarilyMuted {
		t.Fatalf("expected first account to be temporarily muted, got %#v, %v", health, ok)
	}
	if len(doer.seenTokens) != 2 {
		t.Fatalf("expected two completion attempts, got %#v", doer.seenTokens)
	}
}

func TestCallCompletionDisablesAndSwitchesAfterHTTP200BusinessCode90008(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"banned@example.com","password":"pwd","token":"token-1"},
			{"email":"healthy@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	completionDoer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		body := "data: {\"p\":\"response/content\",\"v\":\"ok\"}\n" + "data: [DONE]\n"
		if strings.Contains(req.Header.Get("authorization"), "token-1") {
			body = "{\"code\":0,\"data\":{\"biz_code\":90008,\"biz_msg\":\"\"}}"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	client := &Client{
		Auth:       resolver,
		Store:      store,
		regular:    completionSwitchRegularDoer{},
		stream:     completionDoer,
		fallbackS:  &http.Client{},
		fallback:   &http.Client{},
		maxRetries: 3,
	}
	first, ok := store.FindAccount("banned@example.com")
	if !ok {
		t.Fatal("missing first account")
	}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-1",
		AccountID:      first.Identifier(),
		Account:        first,
		TriedAccounts:  map[string]bool{},
	}
	resp, err := client.CallCompletion(context.Background(), a, map[string]any{"chat_session_id": "s"}, "pow", 2)
	if err != nil {
		t.Fatalf("CallCompletion returned error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if a.AccountID != "healthy@example.com" {
		t.Fatalf("expected healthy account after ban, got %q", a.AccountID)
	}
	updated, ok := store.FindAccount("banned@example.com")
	if !ok || !updated.Disabled || updated.DisabledReason != config.AccountDisabledUpstreamBanned {
		t.Fatalf("expected banned account to be persistently disabled, got %#v, %v", updated, ok)
	}
}

func TestCallCompletionReturnsUpstreamStatusFailure(t *testing.T) {
	t.Parallel()

	client := &Client{
		stream: encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"busy"}`)),
				Request:    req,
			}, nil
		}),
		fallbackS:  &http.Client{},
		maxRetries: 1,
	}

	resp, err := client.CallCompletion(context.Background(), &auth.RequestAuth{DeepSeekToken: "x"}, map[string]any{"chat_session_id": "s"}, "pow", 1)
	if resp != nil {
		t.Fatalf("expected nil response on exhausted upstream status, got %#v", resp)
	}
	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RequestFailure, got %T %v", err, err)
	}
	if failure.Kind != FailureUpstreamStatus || failure.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unexpected failure: %#v", failure)
	}
	if !strings.Contains(failure.Message, "busy") {
		t.Fatalf("expected upstream body in failure, got %q", failure.Message)
	}
}

func TestCompletionStatusFailureClassifiesManagedUnauthorized(t *testing.T) {
	t.Parallel()

	client := &Client{}
	a := &auth.RequestAuth{UseConfigToken: true, DeepSeekToken: "expired-token"}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"code":40001,"msg":"token expired"}`)),
	}
	err := client.completionStatusFailure(a, resp)
	if !IsManagedUnauthorizedError(err) {
		t.Fatalf("expected managed unauthorized classification, got %T: %v", err, err)
	}
}

// rateLimitFailoverDoer returns 429 for the first `failuresBeforeSuccess`
// requests, then 200 OK with a minimal SSE stream. It records every call so
// the test can inspect which token was used at each attempt.
type rateLimitFailoverDoer struct {
	failuresBeforeSuccess int
	calls                 int
	seenTokens            []string
}

func (d *rateLimitFailoverDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.seenTokens = append(d.seenTokens, strings.TrimSpace(req.Header.Get("authorization")))
	if d.calls <= d.failuresBeforeSuccess {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"rate limited"}`)),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: {\"v\":\"ok\"}\n" + "data: [DONE]\n")),
		Request:    req,
	}, nil
}

// TestCallCompletion429FailsOverAcrossWholePoolBeyondMaxAttempts proves the
// v1.0.12 contract: 429 from upstream triggers an account switch but does
// NOT consume the maxAttempts budget. Three accounts, the first two return
// 429, the third returns 200. With maxAttempts=2 (the historical default
// before this fix surfaced) the legacy behavior would have exhausted the
// budget after the second 429 and propagated 429 to the client; the new
// behavior keeps switching until the third account answers 200.
func TestCallCompletion429FailsOverAcrossWholePoolBeyondMaxAttempts(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"},
			{"email":"acc3@example.com","password":"pwd","token":"token-3"}
		],
		"runtime":{"account_max_inflight":1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		switch acc.Email {
		case "acc1@example.com":
			return "token-1", nil
		case "acc2@example.com":
			return "token-2", nil
		case "acc3@example.com":
			return "token-3", nil
		}
		return "token-x", nil
	})
	doer := &rateLimitFailoverDoer{failuresBeforeSuccess: 2}
	client := &Client{
		Auth:       resolver,
		Store:      store,
		regular:    completionSwitchRegularDoer{},
		stream:     doer,
		fallbackS:  &http.Client{},
		fallback:   &http.Client{},
		maxRetries: 3,
	}
	first, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("missing first account")
	}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-1",
		AccountID:      "acc1@example.com",
		Account:        first,
		TriedAccounts:  map[string]bool{},
	}
	resp, err := client.CallCompletion(context.Background(), a, map[string]any{"chat_session_id": "s"}, "pow", 2)
	if err != nil {
		t.Fatalf("CallCompletion returned error: %v (seenTokens=%v)", err, doer.seenTokens)
	}
	defer func() { _ = resp.Body.Close() }()
	if doer.calls != 3 {
		t.Fatalf("expected 3 upstream attempts (2 x 429 + 1 x 200), got %d", doer.calls)
	}
	if a.AccountID != "acc3@example.com" {
		t.Fatalf("expected to land on acc3 after fail-over, got %q (tried=%v)", a.AccountID, a.TriedAccounts)
	}
}

// TestCallCompletion429PropagatesWhenPoolExhausted confirms the fail-over is
// not infinite: when every account has been tried and they all 429, the
// client receives the upstream 429 — we don't loop forever or mask a real
// fleet-wide rate-limit.
func TestCallCompletion429PropagatesWhenPoolExhausted(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":1}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := auth.NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		if acc.Email == "acc2@example.com" {
			return "token-2", nil
		}
		return "token-1", nil
	})
	doer := &rateLimitFailoverDoer{failuresBeforeSuccess: 1000} // never succeed
	client := &Client{
		Auth:       resolver,
		Store:      store,
		regular:    completionSwitchRegularDoer{},
		stream:     doer,
		fallbackS:  &http.Client{},
		fallback:   &http.Client{},
		maxRetries: 3,
	}
	first, ok := store.FindAccount("acc1@example.com")
	if !ok {
		t.Fatal("missing first account")
	}
	a := &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "token-1",
		AccountID:      "acc1@example.com",
		Account:        first,
		TriedAccounts:  map[string]bool{},
	}
	resp, err := client.CallCompletion(context.Background(), a, map[string]any{"chat_session_id": "s"}, "pow", 2)
	if resp != nil {
		t.Fatalf("expected nil response after fleet-wide 429, got %#v", resp)
	}
	var failure *RequestFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected RequestFailure, got %T %v", err, err)
	}
	if failure.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected propagated 429, got %d (msg=%q)", failure.StatusCode, failure.Message)
	}
	if doer.calls < 2 {
		t.Fatalf("expected at least one fail-over before propagation, got %d calls", doer.calls)
	}
}

func TestStreamPostDoesNotFallbackAfterContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &Client{}

	resp, err := client.streamPost(ctx, encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}), "https://example.test/sse", nil, map[string]any{"prompt": "hi"})

	if resp != nil {
		t.Fatalf("expected nil response, got %#v", resp)
	}
	if !IsClientCancelledError(err) {
		t.Fatalf("expected client-cancelled error, got %T %v", err, err)
	}
}

func requestBodyString(req *http.Request) string {
	if req == nil || req.Body == nil {
		return ""
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return ""
	}
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	return string(body)
}
