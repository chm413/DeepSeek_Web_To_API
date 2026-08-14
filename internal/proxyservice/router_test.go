package proxyservice

import (
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func TestReconcileAutoRoutesIsStickyAndBalancesEnabledAccounts(t *testing.T) {
	enabled := true
	store := &memoryStore{cfg: config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled},
		Proxies: []config.Proxy{
			{ID: "node-a", Type: "socks5", Host: "127.0.0.1", Port: 1080, LastTestAtUnix: 10, LastTestSuccess: true, LastLatencyMS: 80},
			{ID: "node-b", Type: "socks5", Host: "127.0.0.1", Port: 1081, LastTestAtUnix: 10, LastTestSuccess: true, LastLatencyMS: 30},
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
	store := &memoryStore{cfg: config.Config{
		ProxyPolicy: config.ProxyPolicyConfig{AutomaticRoutingEnabled: &enabled},
		Proxies: []config.Proxy{
			{ID: "failed", Type: "socks5", Host: "127.0.0.1", Port: 1080, LastTestAtUnix: 20, LastTestSuccess: false},
			{ID: "healthy", Type: "socks5", Host: "127.0.0.1", Port: 1081, LastTestAtUnix: 20, LastTestSuccess: true, LastLatencyMS: 50},
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
