package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/config"
)

func newTestResolver(t *testing.T) *Resolver {
	t.Helper()
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd","token":"account-token"}]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	return NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		return "fresh-token", nil
	})
}

func TestDetermineWithXAPIKeyUsesDirectToken(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "direct-token")

	auth, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if auth.UseConfigToken {
		t.Fatalf("expected direct token mode")
	}
	if auth.DeepSeekToken != "direct-token" {
		t.Fatalf("unexpected token: %q", auth.DeepSeekToken)
	}
	if auth.CallerID == "" {
		t.Fatalf("expected caller id to be populated")
	}
}

func TestDetermineWithSessionDirectTokenKeepsStableConversationKey(t *testing.T) {
	r := newTestResolver(t)
	firstReq, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	firstReq.Header.Set("x-api-key", "direct-token")
	first, err := r.DetermineWithSession(firstReq, []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"first question"}]}`))
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}

	secondReq, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	secondReq.Header.Set("x-api-key", "direct-token")
	second, err := r.DetermineWithSession(secondReq, []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"first question"},{"role":"assistant","content":"answer"},{"role":"user","content":"second question"}]}`))
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}

	if first.SessionKey == "" {
		t.Fatal("expected direct-token request to receive a session key")
	}
	if second.SessionKey != first.SessionKey {
		t.Fatalf("expected a stable direct-token conversation key, got %q then %q", first.SessionKey, second.SessionKey)
	}
}

func TestDetermineRejectsRecentlyInvalidDirectToken(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "direct-token")

	a, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	r.MarkDirectTokenInvalid(a)

	if _, err := r.Determine(req); !errors.Is(err, ErrInvalidDirectToken) {
		t.Fatalf("expected cached invalid direct token error, got %v", err)
	}
}

func TestManagedKeyBypassesInvalidDirectTokenCache(t *testing.T) {
	r := newTestResolver(t)
	directReq, _ := http.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	directReq.Header.Set("x-api-key", "managed-key")
	callerID := callerTokenID("managed-key")
	r.invalidDirectAt[callerID] = time.Now()

	a, err := r.Determine(directReq)
	if err != nil {
		t.Fatalf("managed key should bypass direct-token cache: %v", err)
	}
	defer r.Release(a)
	if !a.UseConfigToken {
		t.Fatal("expected managed account auth")
	}
}

func TestInvalidDirectTokenCacheEvictsOldestAtLimit(t *testing.T) {
	r := newTestResolver(t)
	oldestCallerID := "caller:oldest"
	r.invalidDirectAt[oldestCallerID] = time.Now().Add(-time.Minute)
	for i := 0; i < maxInvalidDirectTokenKeys-1; i++ {
		r.invalidDirectAt[fmt.Sprintf("caller:%d", i)] = time.Now()
	}

	r.MarkDirectTokenInvalid(&RequestAuth{CallerID: "caller:new"})

	if len(r.invalidDirectAt) > maxInvalidDirectTokenKeys {
		t.Fatalf("invalid direct token cache grew past limit: %d", len(r.invalidDirectAt))
	}
	if _, ok := r.invalidDirectAt[oldestCallerID]; ok {
		t.Fatal("expected oldest invalid direct token entry to be evicted")
	}
	if _, ok := r.invalidDirectAt["caller:new"]; !ok {
		t.Fatal("expected new invalid direct token entry to be stored")
	}
}

func TestDetermineWithXAPIKeyManagedKeyAcquiresAccount(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "managed-key")

	auth, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	defer r.Release(auth)
	if !auth.UseConfigToken {
		t.Fatalf("expected managed key mode")
	}
	if auth.AccountID != "acc@example.com" {
		t.Fatalf("unexpected account id: %q", auth.AccountID)
	}
	if auth.DeepSeekToken != "fresh-token" {
		t.Fatalf("unexpected account token: %q", auth.DeepSeekToken)
	}
	if auth.CallerID == "" {
		t.Fatalf("expected caller id to be populated")
	}
}

func TestDetermineWithSessionReusesBoundAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"same conversation"}],"stream":true}`)

	req1, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req1.Header.Set("x-api-key", "managed-key")
	a1, err := resolver.DetermineWithSession(req1, body)
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(a1)

	req2, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("x-api-key", "managed-key")
	a2, err := resolver.DetermineWithSession(req2, body)
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}
	defer resolver.Release(a2)

	if a1.AccountID == "" {
		t.Fatal("expected first request to acquire an account")
	}
	if a2.AccountID != a1.AccountID {
		t.Fatalf("expected session-bound account %q, got %q", a1.AccountID, a2.AccountID)
	}
}

func TestDetermineWithSessionHeaderScopeOverridesBodyFingerprint(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	req1, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req1.Header.Set("x-api-key", "managed-key")
	req1.Header.Set(SessionAffinityHeader, "claude:caller")
	a1, err := resolver.DetermineWithSession(req1, []byte(`{"messages":[{"role":"user","content":"first body"}]}`))
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(a1)

	req2, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("x-api-key", "managed-key")
	req2.Header.Set(SessionAffinityHeader, "claude:caller")
	a2, err := resolver.DetermineWithSession(req2, []byte(`{"messages":[{"role":"user","content":"different body"}]}`))
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}
	defer resolver.Release(a2)

	if a2.AccountID != a1.AccountID {
		t.Fatalf("expected header-scoped account %q, got %q", a1.AccountID, a2.AccountID)
	}
}

func TestDetermineWithSessionUsesToolchainConversationHeader(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	req1, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req1.Header.Set("x-api-key", "managed-key")
	req1.Header.Set("X-Codex-Session-ID", "codex-conv-1")
	a1, err := resolver.DetermineWithSession(req1, []byte(`{"messages":[{"role":"user","content":"first"}]}`))
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(a1)

	req2, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("x-api-key", "managed-key")
	req2.Header.Set("X-Codex-Session-ID", "codex-conv-1")
	a2, err := resolver.DetermineWithSession(req2, []byte(`{"messages":[{"role":"user","content":"second"}]}`))
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}
	defer resolver.Release(a2)

	if a2.AccountID != a1.AccountID {
		t.Fatalf("expected conversation-bound account %q, got %q", a1.AccountID, a2.AccountID)
	}
}

func TestDetermineWithSessionDifferentHeaderScopesDistributeAccounts(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	req1, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req1.Header.Set("x-api-key", "managed-key")
	req1.Header.Set(SessionAffinityHeader, "claude:caller:lane:body:agent-a")
	a1, err := resolver.DetermineWithSession(req1, []byte(`{"messages":[{"role":"user","content":"agent a"}]}`))
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(a1)

	req2, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req2.Header.Set("x-api-key", "managed-key")
	req2.Header.Set(SessionAffinityHeader, "claude:caller:lane:body:agent-b")
	a2, err := resolver.DetermineWithSession(req2, []byte(`{"messages":[{"role":"user","content":"agent b"}]}`))
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}
	defer resolver.Release(a2)

	if a1.AccountID == "" || a2.AccountID == "" {
		t.Fatalf("expected both requests to acquire accounts, got %q %q", a1.AccountID, a2.AccountID)
	}
	if a1.AccountID == a2.AccountID {
		t.Fatalf("expected different child agent lanes to distribute accounts, both got %q", a1.AccountID)
	}
}

func TestDetermineWithSessionClaudeRootLanesSpreadAcrossAccounts(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"},
			{"email":"acc3@example.com","password":"pwd","token":"token-3"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	scopes := []string{
		"claude:header:x-claude-code-session-id:sess_abc:lane:body:agent-a",
		"claude:header:x-claude-code-session-id:sess_abc:lane:body:agent-b",
		"claude:header:x-claude-code-session-id:sess_abc:lane:body:agent-c",
	}
	acquired := make([]*RequestAuth, 0, len(scopes))
	defer func() {
		for _, a := range acquired {
			resolver.Release(a)
		}
	}()
	seen := map[string]bool{}
	for _, scope := range scopes {
		req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("x-api-key", "managed-key")
		req.Header.Set(SessionAffinityHeader, scope)
		a, err := resolver.DetermineWithSession(req, []byte(`{"messages":[{"role":"user","content":"subagent"}]}`))
		if err != nil {
			t.Fatalf("determine failed for %s: %v", scope, err)
		}
		acquired = append(acquired, a)
		if seen[a.AccountID] {
			t.Fatalf("expected first wave child lanes to spread across accounts, repeated %q", a.AccountID)
		}
		seen[a.AccountID] = true
	}
}

func TestDetermineWithSessionOverflowsBoundAccountWhenFull(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"},
			{"email":"acc3@example.com","password":"pwd","token":"token-3"}
		],
		"runtime":{"account_max_inflight":2}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	body := []byte(`{"messages":[{"role":"user","content":"same conversation"}]}`)
	acquired := make([]*RequestAuth, 0, 3)
	defer func() {
		for _, a := range acquired {
			resolver.Release(a)
		}
	}()

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("x-api-key", "managed-key")
		req.Header.Set(SessionAffinityHeader, "claude:session:overflow")
		a, err := resolver.DetermineWithSession(req, body)
		if err != nil {
			t.Fatalf("determine %d failed: %v", i+1, err)
		}
		acquired = append(acquired, a)
		if a.AccountID != "acc1@example.com" {
			t.Fatalf("expected first two requests on acc1, got %q", a.AccountID)
		}
	}

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")
	req.Header.Set(SessionAffinityHeader, "claude:session:overflow")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := resolver.DetermineWithSession(req.WithContext(ctx), body)
	if err != nil {
		t.Fatalf("expected third request to overflow instead of queueing: %v", err)
	}
	acquired = append(acquired, a)
	if a.AccountID == "acc1@example.com" {
		t.Fatalf("expected overflow to avoid full account, got %q", a.AccountID)
	}

	status := pool.Status()
	if got := status["in_use"].(int); got != 3 {
		t.Fatalf("expected three in-use slots, got %#v", status["in_use"])
	}
}

func TestSwitchAccountUpdatesSessionAffinityBinding(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":2}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	body := []byte(`{"messages":[{"role":"user","content":"same conversation"}]}`)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")
	req.Header.Set(SessionAffinityHeader, "claude:session:switched")
	a, err := resolver.DetermineWithSession(req, body)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if a.AccountID != "acc1@example.com" {
		t.Fatalf("expected first account, got %q", a.AccountID)
	}
	if !resolver.SwitchAccount(context.Background(), a) {
		t.Fatal("expected switch to succeed")
	}
	if a.AccountID != "acc2@example.com" {
		t.Fatalf("expected switched account, got %q", a.AccountID)
	}
	resolver.Release(a)

	nextReq, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	nextReq.Header.Set("x-api-key", "managed-key")
	nextReq.Header.Set(SessionAffinityHeader, "claude:session:switched")
	next, err := resolver.DetermineWithSession(nextReq, body)
	if err != nil {
		t.Fatalf("determine after switch failed: %v", err)
	}
	defer resolver.Release(next)
	if next.AccountID != "acc2@example.com" {
		t.Fatalf("expected affinity to follow switched account, got %q", next.AccountID)
	}
}

func TestDetermineWithSessionResponsesInputReusesBoundAccount(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	body := []byte(`{"model":"deepseek-v4-flash","instructions":"be stable","input":[{"role":"user","content":"same responses conversation"}],"stream":false}`)

	req1, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	req1.Header.Set("x-api-key", "managed-key")
	a1, err := resolver.DetermineWithSession(req1, body)
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(a1)

	req2, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	req2.Header.Set("x-api-key", "managed-key")
	a2, err := resolver.DetermineWithSession(req2, body)
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}
	defer resolver.Release(a2)

	if a1.AccountID == "" {
		t.Fatal("expected first request to acquire an account")
	}
	if a2.AccountID != a1.AccountID {
		t.Fatalf("expected responses session-bound account %q, got %q", a1.AccountID, a2.AccountID)
	}
}

func TestDetermineWithSessionKeyReusesOriginalResponsesBinding(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})

	firstReq, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	firstReq.Header.Set("x-api-key", "managed-key")
	first, err := resolver.DetermineWithSession(firstReq, []byte(`{"model":"deepseek-v4-flash","input":"first"}`))
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(first)
	if first.SessionKey == "" {
		t.Fatal("first request did not establish a session key")
	}

	secondReq, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	secondReq.Header.Set("x-api-key", "managed-key")
	second, err := resolver.DetermineWithSessionKey(secondReq, []byte(`{"model":"deepseek-v4-flash","previous_response_id":"resp_1","input":"second"}`), first.SessionKey)
	if err != nil {
		t.Fatalf("inherited determine failed: %v", err)
	}
	defer resolver.Release(second)
	if second.SessionKey != first.SessionKey {
		t.Fatalf("session key changed: first=%q second=%q", first.SessionKey, second.SessionKey)
	}
	if second.AccountID != first.AccountID {
		t.Fatalf("inherited session moved accounts: first=%q second=%q", first.AccountID, second.AccountID)
	}
}

func TestDetermineWithSessionDefaultScopeIncludesEndpointPath(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"acc1@example.com","password":"pwd","token":"token-1"},
			{"email":"acc2@example.com","password":"pwd","token":"token-2"}
		],
		"runtime":{"account_max_inflight":8}
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		return acc.Token, nil
	})
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"same first user"}],"stream":false}`)

	req1, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req1.Header.Set("x-api-key", "managed-key")
	a1, err := resolver.DetermineWithSession(req1, body)
	if err != nil {
		t.Fatalf("first determine failed: %v", err)
	}
	defer resolver.Release(a1)

	req2, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	req2.Header.Set("x-api-key", "managed-key")
	a2, err := resolver.DetermineWithSession(req2, body)
	if err != nil {
		t.Fatalf("second determine failed: %v", err)
	}
	defer resolver.Release(a2)

	if a1.AccountID == "" || a2.AccountID == "" {
		t.Fatalf("expected both requests to acquire accounts, got %q %q", a1.AccountID, a2.AccountID)
	}
	if a1.AccountID == a2.AccountID {
		t.Fatalf("expected endpoint-scoped body affinity to use different accounts, both got %q", a1.AccountID)
	}
}

func TestDetermineCallerWithManagedKeySkipsAccountAcquire(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodGet, "/v1/responses/resp_1", nil)
	req.Header.Set("x-api-key", "managed-key")

	a, err := r.DetermineCaller(req)
	if err != nil {
		t.Fatalf("determine caller failed: %v", err)
	}
	if a.CallerID == "" {
		t.Fatalf("expected caller id to be populated")
	}
	if a.UseConfigToken {
		t.Fatalf("expected no config-token lease for caller-only auth")
	}
	if a.AccountID != "" {
		t.Fatalf("expected empty account id, got %q", a.AccountID)
	}
}

func TestCallerTokenIDStable(t *testing.T) {
	a := callerTokenID("token-a")
	b := callerTokenID("token-a")
	c := callerTokenID("token-b")
	if a == "" || b == "" || c == "" {
		t.Fatalf("expected non-empty caller ids")
	}
	if a != b {
		t.Fatalf("expected stable caller id, got %q and %q", a, b)
	}
	if a == c {
		t.Fatalf("expected different caller id for different tokens")
	}
}

func TestDetermineMissingToken(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	_, err := r.Determine(req)
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDetermineWithQueryKeyUsesDirectToken(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ALLOW_QUERY_AUTH", "true")
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent?key=direct-query-key", nil)

	a, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if a.UseConfigToken {
		t.Fatalf("expected direct token mode")
	}
	if a.DeepSeekToken != "direct-query-key" {
		t.Fatalf("unexpected token: %q", a.DeepSeekToken)
	}
}

func TestDetermineWithXGoogAPIKeyUsesDirectToken(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", nil)
	req.Header.Set("x-goog-api-key", "goog-header-key")

	a, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if a.UseConfigToken {
		t.Fatalf("expected direct token mode")
	}
	if a.DeepSeekToken != "goog-header-key" {
		t.Fatalf("unexpected token: %q", a.DeepSeekToken)
	}
}

func TestDetermineWithAPIKeyQueryParamUsesDirectToken(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ALLOW_QUERY_AUTH", "true")
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent?api_key=direct-api-key", nil)

	a, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if a.UseConfigToken {
		t.Fatalf("expected direct token mode")
	}
	if a.DeepSeekToken != "direct-api-key" {
		t.Fatalf("unexpected token: %q", a.DeepSeekToken)
	}
}

func TestDetermineHeaderTokenPrecedenceOverQueryKey(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ALLOW_QUERY_AUTH", "true")
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent?key=query-key", nil)
	req.Header.Set("x-api-key", "managed-key")

	a, err := r.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	defer r.Release(a)
	if !a.UseConfigToken {
		t.Fatalf("expected managed key mode from header token")
	}
	if a.AccountID == "" {
		t.Fatalf("expected managed account to be acquired")
	}
}

func TestDetermineWithQueryAuthDisabledByDefault(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent?key=direct-query-key", nil)

	_, err := r.Determine(req)
	if err == nil {
		t.Fatal("expected unauthorized when query auth disabled")
	}
	if err != ErrUnauthorized {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDetermineCallerMissingToken(t *testing.T) {
	r := newTestResolver(t)
	req, _ := http.NewRequest(http.MethodGet, "/v1/responses/resp_1", nil)

	_, err := r.DetermineCaller(req)
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if err != ErrUnauthorized {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDetermineManagedAccountForcesRefreshEverySixHours(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd","token":"seed-token"}]
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("acc@example.com", "seed-token"); err != nil {
		t.Fatalf("update token failed: %v", err)
	}
	pool := account.NewPool(store)

	var loginCount int32
	resolver := NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		n := atomic.AddInt32(&loginCount, 1)
		return "fresh-token-" + string(rune('0'+n)), nil
	})

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")

	a1, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if a1.DeepSeekToken != "seed-token" {
		t.Fatalf("expected initial token without forced refresh, got %q", a1.DeepSeekToken)
	}
	resolver.Release(a1)
	if got := atomic.LoadInt32(&loginCount); got != 0 {
		t.Fatalf("expected no login before refresh interval, got %d", got)
	}

	resolver.mu.Lock()
	resolver.tokenRefreshedAt["acc@example.com"] = time.Now().Add(-7 * time.Hour)
	resolver.mu.Unlock()

	a2, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine after interval failed: %v", err)
	}
	defer resolver.Release(a2)
	if a2.DeepSeekToken != "fresh-token-1" {
		t.Fatalf("expected refreshed token after interval, got %q", a2.DeepSeekToken)
	}
	if got := atomic.LoadInt32(&loginCount); got != 1 {
		t.Fatalf("expected exactly one forced refresh login, got %d", got)
	}
}

func TestDetermineManagedAccountUsesUpdatedRefreshInterval(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[{"email":"acc@example.com","password":"pwd","token":"seed-token"}],
		"runtime":{"token_refresh_interval_hours":6}
	}`)
	store := config.LoadStore()
	if err := store.UpdateAccountToken("acc@example.com", "seed-token"); err != nil {
		t.Fatalf("update token failed: %v", err)
	}
	pool := account.NewPool(store)

	var loginCount int32
	resolver := NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		n := atomic.AddInt32(&loginCount, 1)
		return "fresh-token-" + string(rune('0'+n)), nil
	})

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")

	a1, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	if a1.DeepSeekToken != "seed-token" {
		t.Fatalf("expected initial token without forced refresh, got %q", a1.DeepSeekToken)
	}
	resolver.Release(a1)
	if got := atomic.LoadInt32(&loginCount); got != 0 {
		t.Fatalf("expected no login before runtime update, got %d", got)
	}

	if err := store.Update(func(c *config.Config) error {
		c.Runtime.TokenRefreshIntervalHours = 1
		return nil
	}); err != nil {
		t.Fatalf("update runtime failed: %v", err)
	}

	resolver.mu.Lock()
	resolver.tokenRefreshedAt["acc@example.com"] = time.Now().Add(-2 * time.Hour)
	resolver.mu.Unlock()

	a2, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine after runtime update failed: %v", err)
	}
	defer resolver.Release(a2)
	if a2.DeepSeekToken != "fresh-token-1" {
		t.Fatalf("expected refreshed token after runtime update, got %q", a2.DeepSeekToken)
	}
	if got := atomic.LoadInt32(&loginCount); got != 1 {
		t.Fatalf("expected exactly one login after runtime update, got %d", got)
	}
}

func TestDetermineManagedAccountRetriesOtherAccountOnLoginFailure(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"bad@example.com","password":"pwd"},
			{"email":"good@example.com","password":"pwd","token":"good-token"}
		]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		if acc.Email == "bad@example.com" {
			return "", errors.New("stale account")
		}
		return "fresh-good-token", nil
	})

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")

	a, err := resolver.Determine(req)
	if err != nil {
		t.Fatalf("determine failed: %v", err)
	}
	defer resolver.Release(a)
	if a.AccountID != "good@example.com" {
		t.Fatalf("expected fallback to good account, got %q", a.AccountID)
	}
	if a.DeepSeekToken == "" {
		t.Fatal("expected non-empty token from fallback account")
	}
	if !a.TriedAccounts["bad@example.com"] {
		t.Fatalf("expected bad account to be tracked as tried")
	}
}

func TestDetermineTargetAccountDoesNotFallbackOnLoginFailure(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"bad@example.com","password":"pwd"},
			{"email":"good@example.com","password":"pwd","token":"good-token"}
		]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	resolver := NewResolver(store, pool, func(_ context.Context, acc config.Account) (string, error) {
		if acc.Email == "bad@example.com" {
			return "", errors.New("stale account")
		}
		return "fresh-good-token", nil
	})

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")
	req.Header.Set("X-DeepSeek-Web-To-API-Target-Account", "bad@example.com")

	_, err := resolver.Determine(req)
	if err == nil {
		t.Fatal("expected determine to fail for broken target account")
	}
}

func TestDetermineManagedAccountReturnsLastEnsureErrorWhenAllFail(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["managed-key"],
		"accounts":[
			{"email":"bad1@example.com","password":"pwd"},
			{"email":"bad2@example.com","password":"pwd"}
		]
	}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	ensureErr := errors.New("all credentials stale")
	resolver := NewResolver(store, pool, func(_ context.Context, _ config.Account) (string, error) {
		return "", ensureErr
	})

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "managed-key")

	_, err := resolver.Determine(req)
	if err == nil {
		t.Fatal("expected determine to fail")
	}
	if !errors.Is(err, ensureErr) {
		t.Fatalf("expected ensure error, got %v", err)
	}
	if errors.Is(err, ErrNoAccount) {
		t.Fatalf("expected auth-style ensure error, got ErrNoAccount")
	}
}
