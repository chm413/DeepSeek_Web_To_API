package proxyservice

import (
	"sync"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

type memoryStore struct {
	mu  sync.Mutex
	cfg config.Config
}

func (s *memoryStore) Snapshot() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

func (s *memoryStore) Update(fn func(*config.Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	if err := fn(&next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

func TestApplyTestResultsAutoDisablesAndRecoversHealthFailures(t *testing.T) {
	store := &memoryStore{cfg: config.Config{
		Proxies:     []config.Proxy{{ID: "proxy-a", Name: "A", Type: "socks5", Host: "127.0.0.1", Port: 1080, ConsecutiveFailures: 2}},
		ProxyPolicy: config.ProxyPolicyConfig{AutoDisableAfterFailures: 3},
	}}

	failed := []ProxyTestResult{{ProxyID: "proxy-a", Success: false, Message: "dial failed"}}
	if err := applyTestResults(store, failed); err != nil {
		t.Fatalf("apply failed result: %v", err)
	}
	proxy := store.Snapshot().Proxies[0]
	if !proxy.Disabled || proxy.DisabledReason != config.ProxyDisabledHealth || proxy.ConsecutiveFailures != 3 {
		t.Fatalf("expected automatic health disable, got %#v", proxy)
	}
	if !failed[0].AutoDisabled || !failed[0].Disabled {
		t.Fatalf("expected result to expose auto-disable, got %#v", failed[0])
	}

	passed := []ProxyTestResult{{ProxyID: "proxy-a", Success: true, Message: "ok", ResponseTime: 25}}
	if err := applyTestResults(store, passed); err != nil {
		t.Fatalf("apply recovery result: %v", err)
	}
	proxy = store.Snapshot().Proxies[0]
	if proxy.Disabled || proxy.DisabledReason != "" || proxy.ConsecutiveFailures != 0 || proxy.LastLatencyMS != 25 {
		t.Fatalf("expected automatic recovery, got %#v", proxy)
	}
	if !passed[0].AutoEnabled {
		t.Fatalf("expected result to expose auto-enable, got %#v", passed[0])
	}
}

func TestApplyTestResultsDoesNotEnableManuallyDisabledProxy(t *testing.T) {
	store := &memoryStore{cfg: config.Config{Proxies: []config.Proxy{{
		ID: "proxy-a", Type: "socks5", Host: "127.0.0.1", Port: 1080,
		Disabled: true, DisabledReason: config.ProxyDisabledManual,
	}}}}
	results := []ProxyTestResult{{ProxyID: "proxy-a", Success: true, Message: "ok"}}
	if err := applyTestResults(store, results); err != nil {
		t.Fatalf("apply result: %v", err)
	}
	proxy := store.Snapshot().Proxies[0]
	if !proxy.Disabled || proxy.DisabledReason != config.ProxyDisabledManual || results[0].AutoEnabled {
		t.Fatalf("manual disable must be preserved, proxy=%#v result=%#v", proxy, results[0])
	}
}
