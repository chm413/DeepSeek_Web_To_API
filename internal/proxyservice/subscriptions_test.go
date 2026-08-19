package proxyservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func useTestSubscriptionFetcher(t *testing.T) {
	t.Helper()
	previous := fetchSubscription
	fetchSubscription = func(ctx context.Context, rawURL string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		return io.ReadAll(response.Body)
	}
	t.Cleanup(func() { fetchSubscription = previous })
}

func TestRefreshSubscriptionImportsUpdatesAndMigratesRemovedAssignedNode(t *testing.T) {
	useTestSubscriptionFetcher(t)
	var version atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if version.Load() == 0 {
			_, _ = fmt.Fprintln(w, "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none#first")
			return
		}
		_, _ = fmt.Fprintln(w, "hysteria2://password@example.net:443#second")
	}))
	defer server.Close()

	store := &memoryStore{cfg: config.Config{
		ProxySubscriptions: []config.ProxySubscription{{ID: "sub-a", Name: "Airport", URL: server.URL}},
		Proxies:            []config.Proxy{{ID: "fallback", Type: "socks5", Host: "127.0.0.1", Port: 1080}},
		ProxyPolicy:        config.ProxyPolicyConfig{FallbackProxyID: "fallback"},
	}}
	first, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if first.Added != 1 || first.NodeCount != 1 {
		t.Fatalf("unexpected first refresh result: %#v", first)
	}
	snapshot := store.Snapshot()
	if len(snapshot.Proxies) != 2 {
		t.Fatalf("expected imported subscription node, got %#v", snapshot.Proxies)
	}
	removedID := ""
	for _, proxy := range snapshot.Proxies {
		if proxy.SubscriptionID == "sub-a" {
			removedID = proxy.ID
		}
	}
	if removedID == "" {
		t.Fatalf("subscription node was not imported: %#v", snapshot.Proxies)
	}
	store.mu.Lock()
	store.cfg.Accounts = []config.Account{{Email: "user@example.com", ProxyID: removedID, Token: "stale-token"}}
	store.mu.Unlock()

	version.Store(1)
	second, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.Added != 1 || second.Removed != 1 || second.Disabled != 0 || len(second.RouteChanges) != 1 {
		t.Fatalf("unexpected second refresh result: %#v", second)
	}
	snapshot = store.Snapshot()
	if len(snapshot.Proxies) != 2 {
		t.Fatalf("expected fallback and current nodes, got %#v", snapshot.Proxies)
	}
	for _, proxy := range snapshot.Proxies {
		if proxy.ID == removedID {
			t.Fatalf("removed subscription node was retained: %#v", snapshot.Proxies)
		}
	}
	account := snapshot.Accounts[0]
	if account.ProxyID != "fallback" || account.Token != "" {
		t.Fatalf("removed account route was not migrated safely: %#v", account)
	}
}

func TestRefreshSubscriptionSanitizesFetchFailure(t *testing.T) {
	previous := fetchSubscription
	secretURL := "https://user:secret@example.com/subscription?token=private-token"
	fetchSubscription = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("fetch subscription: " + secretURL)
	}
	t.Cleanup(func() { fetchSubscription = previous })

	store := &memoryStore{cfg: config.Config{ProxySubscriptions: []config.ProxySubscription{{
		ID: "sub-a", Name: "Airport", URL: secretURL,
	}}}}
	_, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err == nil {
		t.Fatal("expected fetch failure")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("returned error leaked subscription credential: %v", err)
	}
	lastError := store.Snapshot().ProxySubscriptions[0].LastError
	if strings.Contains(lastError, "secret") || strings.Contains(lastError, "private-token") || strings.Contains(lastError, "example.com") {
		t.Fatalf("persisted error leaked subscription credential: %q", lastError)
	}
}

func TestRefreshSubscriptionRejectsStaleURLWithoutOverwritingNewConfig(t *testing.T) {
	previous := fetchSubscription
	store := &memoryStore{cfg: config.Config{ProxySubscriptions: []config.ProxySubscription{{
		ID:   "sub-a",
		Name: "Airport",
		URL:  "https://old.example.invalid/subscription",
	}}}}
	fetchSubscription = func(context.Context, string) ([]byte, error) {
		if err := store.Update(func(cfg *config.Config) error {
			cfg.ProxySubscriptions[0].URL = "https://new.example.invalid/subscription"
			return nil
		}); err != nil {
			return nil, err
		}
		return []byte("vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none#old"), nil
	}
	t.Cleanup(func() { fetchSubscription = previous })

	_, err := RefreshSubscription(context.Background(), store, "sub-a")
	var staleErr *SubscriptionRefreshStaleError
	if !errors.As(err, &staleErr) {
		t.Fatalf("expected stale refresh error, got %v", err)
	}
	snapshot := store.Snapshot()
	if snapshot.ProxySubscriptions[0].URL != "https://new.example.invalid/subscription" {
		t.Fatalf("new subscription URL was overwritten: %#v", snapshot.ProxySubscriptions[0])
	}
	if snapshot.ProxySubscriptions[0].LastError != "" || len(snapshot.Proxies) != 0 {
		t.Fatalf("stale refresh mutated the replacement configuration: %#v", snapshot)
	}
}

func TestRefreshSubscriptionRejectsRemovedManualRouteWithoutFallback(t *testing.T) {
	useTestSubscriptionFetcher(t)
	var version atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if version.Load() == 0 {
			_, _ = fmt.Fprintln(w, "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none#first")
			return
		}
		_, _ = fmt.Fprintln(w, "hysteria2://password@example.net:443#second")
	}))
	defer server.Close()

	store := &memoryStore{cfg: config.Config{ProxySubscriptions: []config.ProxySubscription{{
		ID: "sub-a", Name: "Airport", URL: server.URL,
	}}}}
	if _, err := RefreshSubscription(context.Background(), store, "sub-a"); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	removedID := store.Snapshot().Proxies[0].ID
	if err := store.Update(func(cfg *config.Config) error {
		cfg.Accounts = []config.Account{{Email: "user@example.com", Password: "pwd", ProxyID: removedID}}
		return nil
	}); err != nil {
		t.Fatalf("seed assigned account: %v", err)
	}
	version.Store(1)
	if _, err := RefreshSubscription(context.Background(), store, "sub-a"); err == nil {
		t.Fatal("expected refresh to reject removal without a fallback")
	}
	snapshot := store.Snapshot()
	if len(snapshot.Proxies) != 1 || snapshot.Proxies[0].ID != removedID || snapshot.Accounts[0].ProxyID != removedID {
		t.Fatalf("failed route migration changed configuration: %#v", snapshot)
	}
}

func TestRefreshSubscriptionPreservesSameSubscriptionNodeState(t *testing.T) {
	useTestSubscriptionFetcher(t)
	var version atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if version.Load() == 0 {
			_, _ = fmt.Fprintln(w, "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls#first")
			return
		}
		_, _ = fmt.Fprintln(w, "vless://11111111-1111-1111-1111-111111111111@example.com:443?security=tls&encryption=none#renamed")
	}))
	defer server.Close()

	store := &memoryStore{cfg: config.Config{ProxySubscriptions: []config.ProxySubscription{{
		ID: "sub-a", Name: "Airport", URL: server.URL,
	}}}}
	if _, err := RefreshSubscription(context.Background(), store, "sub-a"); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	store.mu.Lock()
	store.cfg.Proxies[0].Disabled = true
	store.cfg.Proxies[0].DisabledReason = config.ProxyDisabledHealth
	store.cfg.Proxies[0].DisabledAtUnix = 1710000000
	store.cfg.Proxies[0].ConsecutiveFailures = 3
	store.cfg.Proxies[0].LastTestAtUnix = 1710000001
	store.cfg.Proxies[0].LastTestSuccess = false
	store.cfg.Proxies[0].LastLatencyMS = 123
	store.cfg.Proxies[0].LastTestError = "health state must survive an update"
	store.mu.Unlock()

	version.Store(1)
	result, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if result.Updated != 1 || result.Added != 0 || result.SkippedDuplicates != 0 {
		t.Fatalf("unexpected same-subscription refresh result: %#v", result)
	}
	proxy := store.Snapshot().Proxies[0]
	if !proxy.Disabled || proxy.DisabledReason != config.ProxyDisabledHealth || proxy.ConsecutiveFailures != 3 || proxy.LastLatencyMS != 123 || proxy.LastTestError != "health state must survive an update" {
		t.Fatalf("same-subscription health state was not preserved: %#v", proxy)
	}
}

func TestRefreshSubscriptionDeduplicatesEquivalentIncomingAliasesForExistingNode(t *testing.T) {
	useTestSubscriptionFetcher(t)
	const existingURI = "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls#existing"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, "vless://11111111-1111-1111-1111-111111111111@EXAMPLE.com:443?encryption=none&security=tls#upper")
		_, _ = fmt.Fprintln(w, "vless://11111111-1111-1111-1111-111111111111@example.COM:443?security=tls&encryption=none#mixed")
	}))
	defer server.Close()

	store := &memoryStore{cfg: config.Config{
		ProxySubscriptions: []config.ProxySubscription{{ID: "sub-a", Name: "Airport", URL: server.URL}},
		Proxies: []config.Proxy{{
			ID:             "existing-node",
			Type:           "vless",
			URI:            existingURI,
			SubscriptionID: "sub-a",
		}},
	}}

	result, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err != nil {
		t.Fatalf("refresh subscription: %v", err)
	}
	if result.Updated != 1 || result.Added != 0 || result.SkippedDuplicates != 1 || result.NodeCount != 2 {
		t.Fatalf("unexpected semantic-alias refresh result: %#v", result)
	}
	snapshot := store.Snapshot()
	if len(snapshot.Proxies) != 1 || snapshot.Proxies[0].ID != "existing-node" {
		t.Fatalf("expected exactly one existing node after semantic dedupe, got %#v", snapshot.Proxies)
	}
}

func TestRefreshSubscriptionSkipsEquivalentExternalNodesWithoutRewritingAssignedProxy(t *testing.T) {
	useTestSubscriptionFetcher(t)
	const manualURI = "vless://11111111-1111-1111-1111-111111111111@manual.example.com:443?encryption=none&security=tls&sni=manual.example.com#Manual"
	const otherSubscriptionURI = "vless://22222222-2222-2222-2222-222222222222@other.example.com:443?encryption=none&security=tls&sni=other.example.com#Other"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, strings.Replace(manualURI, "#Manual", "#From%20subscription", 1))
		_, _ = fmt.Fprintln(w, strings.Replace(otherSubscriptionURI, "#Other", "#From%20subscription", 1))
	}))
	defer server.Close()

	manual := config.Proxy{
		ID:                  "manual-node",
		Name:                "Manual name",
		Type:                "vless",
		URI:                 manualURI,
		Disabled:            true,
		DisabledReason:      config.ProxyDisabledManual,
		DisabledAtUnix:      1710000000,
		ConsecutiveFailures: 4,
		LastTestAtUnix:      1710000001,
		LastTestSuccess:     false,
		LastTestError:       "manual status must remain untouched",
	}
	other := config.Proxy{
		ID:                  "sub-a-node",
		Name:                "Other subscription name",
		Type:                "vless",
		URI:                 otherSubscriptionURI,
		SubscriptionID:      "sub-a",
		Disabled:            true,
		DisabledReason:      config.ProxyDisabledHealth,
		DisabledAtUnix:      1710000010,
		ConsecutiveFailures: 3,
		LastTestAtUnix:      1710000011,
		LastTestSuccess:     false,
		LastTestError:       "other subscription status must remain untouched",
	}
	store := &memoryStore{cfg: config.Config{
		ProxySubscriptions: []config.ProxySubscription{
			{ID: "sub-a", Name: "Existing", URL: "https://example.invalid/a"},
			{ID: "sub-b", Name: "Refreshing", URL: server.URL},
		},
		Proxies: []config.Proxy{manual, other},
		Accounts: []config.Account{
			{Email: "manual@example.com", ProxyID: manual.ID, Disabled: true},
			{Email: "other@example.com", ProxyID: other.ID, Disabled: true},
		},
	}}

	result, err := RefreshSubscription(context.Background(), store, "sub-b")
	if err != nil {
		t.Fatalf("refresh subscription: %v", err)
	}
	if result.Added != 0 || result.SkippedDuplicates != 2 || result.NodeCount != 2 {
		t.Fatalf("unexpected refresh result: %#v", result)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[len(result.Warnings)-1], "skipped 2 node(s)") {
		t.Fatalf("expected duplicate warning, got %#v", result.Warnings)
	}

	snapshot := store.Snapshot()
	if len(snapshot.Proxies) != 2 {
		t.Fatalf("duplicate nodes were added or existing nodes removed: %#v", snapshot.Proxies)
	}
	byID := make(map[string]config.Proxy, len(snapshot.Proxies))
	for _, proxy := range snapshot.Proxies {
		byID[proxy.ID] = proxy
	}
	if got := byID[manual.ID]; got != manual {
		t.Fatalf("manual assigned proxy was rewritten: got %#v want %#v", got, manual)
	}
	if got := byID[other.ID]; got != other {
		t.Fatalf("other subscription assigned proxy was rewritten: got %#v want %#v", got, other)
	}
}
