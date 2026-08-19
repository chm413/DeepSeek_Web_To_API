package proxyservice

import (
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

func TestReconcileAutoRoutesIsStickyAndBalancesEnabledAccounts(t *testing.T) {
	enabled := true
	now := time.Now().Unix()
	store := &memoryStore{cfg: config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled},
		Proxies: []config.Proxy{
			{ID: "node-a", Type: "socks5", Host: "127.0.0.1", Port: 1080, LastTestAtUnix: now, LastTestSuccess: true, LastLatencyMS: 80},
			{ID: "node-b", Type: "socks5", Host: "127.0.0.1", Port: 1081, LastTestAtUnix: now, LastTestSuccess: true, LastLatencyMS: 30},
		},
		Accounts: []config.Account{
			{Email: "manual@example.com", Password: "pwd", ProxyID: "node-b"},
			{Email: "manual2@example.com", Password: "pwd", ProxyID: "node-b"},
			{Email: "sticky@example.com", Password: "pwd", ProxyID: "node-a", ProxyAutoRoute: true, Token: "keep"},
			{Email: "new@example.com", Password: "pwd", ProxyAutoRoute: true, Token: "old"},
			{Email: "disabled@example.com", Password: "pwd", ProxyID: "node-a", Disabled: true},
		},
	}}

	changes, err := ReconcileAutoRoutes(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].AccountID != "new@example.com" || changes[0].ToProxyID != "node-a" || changes[0].Reason != "unassigned" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	snapshot := store.Snapshot()
	if snapshot.Accounts[2].ProxyID != "node-a" || snapshot.Accounts[2].Token != "keep" {
		t.Fatalf("valid sticky route changed: %#v", snapshot.Accounts[2])
	}
	if snapshot.Accounts[3].ProxyID != "node-a" || snapshot.Accounts[3].Token != "" {
		t.Fatalf("new automatic route not assigned with token invalidation: %#v", snapshot.Accounts[3])
	}
}

func TestReconcileAutoRoutesMovesOnlyFailedAssignments(t *testing.T) {
	enabled := true
	now := time.Now().Unix()
	store := &memoryStore{cfg: config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled},
		Proxies: []config.Proxy{
			{ID: "failed", Type: "socks5", Host: "127.0.0.1", Port: 1080, LastTestAtUnix: now, LastTestSuccess: false},
			{ID: "healthy", Type: "socks5", Host: "127.0.0.1", Port: 1081, LastTestAtUnix: now, LastTestSuccess: true, LastLatencyMS: 50},
		},
		Accounts: []config.Account{
			{Email: "auto@example.com", Password: "pwd", ProxyID: "failed", ProxyAutoRoute: true, Token: "stale"},
			{Email: "manual@example.com", Password: "pwd", ProxyID: "failed", Token: "manual-token"},
		},
	}}

	changes, err := ReconcileAutoRoutes(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].FromProxyID != "failed" || changes[0].ToProxyID != "healthy" || changes[0].Reason != "node_unavailable" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	snapshot := store.Snapshot()
	if snapshot.Accounts[0].ProxyID != "healthy" || snapshot.Accounts[0].Token != "" {
		t.Fatalf("failed automatic route not moved: %#v", snapshot.Accounts[0])
	}
	if snapshot.Accounts[1].ProxyID != "failed" || snapshot.Accounts[1].Token != "manual-token" {
		t.Fatalf("manual route should stay untouched: %#v", snapshot.Accounts[1])
	}
}

func TestReconcileAutoRoutesKeepsAssignedNodeWhenNoHealthyReplacementExists(t *testing.T) {
	enabled := true
	store := &memoryStore{cfg: config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled},
		Proxies: []config.Proxy{
			{ID: "failed", Type: "socks5", Host: "127.0.0.1", Port: 1080, LastTestAtUnix: time.Now().Unix(), LastTestSuccess: false},
		},
		Accounts: []config.Account{{Email: "auto@example.com", Password: "pwd", Token: "keep", ProxyID: "failed", ProxyAutoRoute: true}},
	}}
	changes, err := ReconcileAutoRoutes(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("missing replacement must not clear an egress assignment: %#v", changes)
	}
	account := store.Snapshot().Accounts[0]
	if account.ProxyID != "failed" || account.Token != "keep" {
		t.Fatalf("missing replacement mutated account route: %#v", account)
	}
}

func TestReassignDeletedProxyRoutesUsesFallbackAndHealthyAutomaticRoute(t *testing.T) {
	enabled := true
	now := time.Now().Unix()
	cfg := config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled, FallbackProxyID: "fallback"},
		Proxies: []config.Proxy{
			{ID: "retired", Type: "socks5", Host: "127.0.0.1", Port: 1080},
			{ID: "fallback", Type: "socks5", Host: "127.0.0.1", Port: 1081},
			{ID: "healthy", Type: "socks5", Host: "127.0.0.1", Port: 1082, LastTestAtUnix: now, LastTestSuccess: true},
		},
		Accounts: []config.Account{
			{Email: "manual@example.com", Password: "pwd", Token: "manual-token", ProxyID: "retired"},
			{Email: "automatic@example.com", Password: "pwd", Token: "automatic-token", ProxyID: "retired", ProxyAutoRoute: true},
		},
	}

	changes, err := ReassignDeletedProxyRoutes(&cfg, map[string]struct{}{"retired": {}})
	if err != nil {
		t.Fatalf("reassign routes: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("unexpected route changes: %#v", changes)
	}
	if cfg.Accounts[0].ProxyID != "fallback" || cfg.Accounts[0].Token != "" {
		t.Fatalf("manual account did not move to fallback: %#v", cfg.Accounts[0])
	}
	if cfg.Accounts[1].ProxyID != "healthy" || cfg.Accounts[1].Token != "" {
		t.Fatalf("automatic account did not move to healthy replacement: %#v", cfg.Accounts[1])
	}
	for _, change := range changes {
		if change.ToProxyID == "" {
			t.Fatalf("deletion produced a direct route: %#v", change)
		}
	}
}

func TestReassignDeletedProxyRoutesRejectsAutomaticDeletionWithoutReplacementAtomically(t *testing.T) {
	enabled := true
	cfg := config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled},
		Proxies:     []config.Proxy{{ID: "retired", Type: "socks5", Host: "127.0.0.1", Port: 1080}},
		Accounts:    []config.Account{{Email: "automatic@example.com", Password: "pwd", Token: "keep", ProxyID: "retired", ProxyAutoRoute: true}},
	}
	before := cfg.Accounts[0]
	if _, err := ReassignDeletedProxyRoutes(&cfg, map[string]struct{}{"retired": {}}); err == nil {
		t.Fatal("expected missing replacement to reject deletion")
	}
	if cfg.Accounts[0] != before || len(cfg.Proxies) != 1 || cfg.Proxies[0].ID != "retired" {
		t.Fatalf("failed route plan mutated config: before=%#v after=%#v", before, cfg)
	}
}

func TestAvailableRoutePoolRejectsStaleSuccessfulProxy(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).Unix()
	cfg := config.Config{
		Proxies: []config.Proxy{{ID: "stale", Type: "socks5", Host: "127.0.0.1", Port: 1080, LastTestAtUnix: old, LastTestSuccess: true}},
	}
	if pool := AvailableRoutePool(cfg); len(pool) != 0 {
		t.Fatalf("stale successful proxy remained routable: %#v", pool)
	}
}
