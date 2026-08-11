package proxyservice

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func TestRefreshSubscriptionImportsUpdatesAndDisablesRemovedAssignedNode(t *testing.T) {
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
	first, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if first.Added != 1 || first.NodeCount != 1 {
		t.Fatalf("unexpected first refresh result: %#v", first)
	}
	snapshot := store.Snapshot()
	if len(snapshot.Proxies) != 1 || snapshot.Proxies[0].SubscriptionID != "sub-a" {
		t.Fatalf("expected imported subscription node, got %#v", snapshot.Proxies)
	}
	removedID := snapshot.Proxies[0].ID
	store.mu.Lock()
	store.cfg.Accounts = []config.Account{{Email: "user@example.com", ProxyID: removedID}}
	store.mu.Unlock()

	version.Store(1)
	second, err := RefreshSubscription(context.Background(), store, "sub-a")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second.Added != 1 || second.Disabled != 1 {
		t.Fatalf("unexpected second refresh result: %#v", second)
	}
	snapshot = store.Snapshot()
	if len(snapshot.Proxies) != 2 {
		t.Fatalf("expected current and retained assigned nodes, got %#v", snapshot.Proxies)
	}
	foundDisabled := false
	for _, proxy := range snapshot.Proxies {
		if proxy.ID == removedID {
			foundDisabled = proxy.Disabled && proxy.DisabledReason == config.ProxyDisabledSubscriptionRemoved
		}
	}
	if !foundDisabled {
		t.Fatalf("expected removed assigned node to be retained disabled, got %#v", snapshot.Proxies)
	}
}
