package proxyservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/proxyuri"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

type ConnectivityTester func(context.Context, config.Proxy, config.ProxyCoreConfig) map[string]any

type ProxyTestResult struct {
	ProxyID        string `json:"proxy_id"`
	ProxyType      string `json:"proxy_type"`
	Success        bool   `json:"success"`
	Message        string `json:"message"`
	ResponseTime   int    `json:"response_time"`
	StatusCode     int    `json:"status_code,omitempty"`
	AutoDisabled   bool   `json:"auto_disabled,omitempty"`
	AutoEnabled    bool   `json:"auto_enabled,omitempty"`
	FailureCount   int    `json:"failure_count"`
	Disabled       bool   `json:"disabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	ExitIP         string `json:"exit_ip,omitempty"`
	Country        string `json:"country,omitempty"`
	Colo           string `json:"colo,omitempty"`
}

func TestProxies(ctx context.Context, store Store, proxyIDs []string, concurrency int, tester ConnectivityTester) ([]ProxyTestResult, error) {
	if store == nil {
		return nil, errors.New("proxy test store is nil")
	}
	if tester == nil {
		tester = dsclient.TestProxyConnectivityWithCore
	}
	snapshot := store.Snapshot()
	selected := selectProxies(snapshot.Proxies, proxyIDs)
	if len(selected) == 0 {
		return []ProxyTestResult{}, nil
	}
	if concurrency <= 0 {
		concurrency = snapshot.ProxyPolicy.Concurrency()
	}
	if concurrency > 32 {
		concurrency = 32
	}
	coreSpecs := make([]xrayproxy.Spec, 0)
	for _, proxy := range selected {
		if proxyuri.IsCoreType(proxy.Type) {
			coreSpecs = append(coreSpecs, xrayproxy.Spec{ID: proxy.ID, Type: proxy.Type, URI: proxy.URI})
		}
	}
	if len(coreSpecs) > 0 {
		if _, err := xrayproxy.Default().EnsureMany(ctx, coreSpecs, xrayproxy.SettingsFromStore(store)); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if syncErr := xrayproxy.SyncAssignedWithStore(cleanupCtx, store); syncErr != nil {
				config.Logger.Warn("[proxy_test] restore assigned xray routes after startup failure", "error", syncErr)
			}
			return nil, err
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := xrayproxy.SyncAssignedWithStore(cleanupCtx, store); err != nil {
				config.Logger.Warn("[proxy_test] restore assigned xray routes failed", "error", err)
			}
		}()
	}

	jobs := make(chan config.Proxy)
	results := make(chan ProxyTestResult, len(selected))
	var workers sync.WaitGroup
	for i := 0; i < concurrency && i < len(selected); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for proxy := range jobs {
				payload := tester(ctx, proxy, snapshot.ProxyCore)
				results <- proxyTestResult(proxy, payload)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, proxy := range selected {
			select {
			case jobs <- proxy:
			case <-ctx.Done():
				return
			}
		}
	}()
	workers.Wait()
	close(results)
	out := make([]ProxyTestResult, 0, len(selected))
	for result := range results {
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProxyID < out[j].ProxyID })
	if err := applyTestResults(store, out); err != nil {
		return nil, err
	}
	if err := xrayproxy.SyncAssignedWithStore(ctx, store); err != nil {
		return out, fmt.Errorf("proxy tests completed but xray route sync failed: %w", err)
	}
	return out, nil
}

func ScheduledProxyIDs(cfg config.Config) []string {
	out := make([]string, 0, len(cfg.Proxies))
	for _, proxy := range cfg.Proxies {
		proxy = config.NormalizeProxy(proxy)
		if proxy.Disabled && proxy.DisabledReason != config.ProxyDisabledHealth {
			continue
		}
		out = append(out, proxy.ID)
	}
	return out
}

func applyTestResults(store Store, results []ProxyTestResult) error {
	now := time.Now().Unix()
	return store.Update(func(cfg *config.Config) error {
		byID := make(map[string]*ProxyTestResult, len(results))
		for i := range results {
			byID[results[i].ProxyID] = &results[i]
		}
		threshold := cfg.ProxyPolicy.DisableAfterFailures()
		for i := range cfg.Proxies {
			proxy := config.NormalizeProxy(cfg.Proxies[i])
			result := byID[proxy.ID]
			if result == nil {
				continue
			}
			proxy.LastTestAtUnix = now
			proxy.LastTestSuccess = result.Success
			proxy.LastLatencyMS = result.ResponseTime
			proxy.LastHTTPStatus = result.StatusCode
			proxy.LastTestError = ""
			if result.Success {
				proxy.ConsecutiveFailures = 0
				if proxy.Disabled && proxy.DisabledReason == config.ProxyDisabledHealth && cfg.ProxyPolicy.EnableOnRecovery() {
					proxy.Disabled = false
					proxy.DisabledReason = ""
					proxy.DisabledAtUnix = 0
					result.AutoEnabled = true
				}
			} else {
				proxy.ConsecutiveFailures++
				proxy.LastTestError = truncateError(result.Message, 600)
				if !proxy.Disabled && proxy.ConsecutiveFailures >= threshold {
					proxy.Disabled = true
					proxy.DisabledReason = config.ProxyDisabledHealth
					proxy.DisabledAtUnix = now
					result.AutoDisabled = true
				}
			}
			if result.ExitIP != "" {
				proxy.LastExitIP = result.ExitIP
			}
			if result.Country != "" {
				proxy.LastCountry = result.Country
			}
			if result.Colo != "" {
				proxy.LastColo = result.Colo
			}
			result.FailureCount = proxy.ConsecutiveFailures
			result.Disabled = proxy.Disabled
			result.DisabledReason = proxy.DisabledReason
			cfg.Proxies[i] = proxy
		}
		return config.ValidateConfig(*cfg)
	})
}

func proxyTestResult(proxy config.Proxy, payload map[string]any) ProxyTestResult {
	return ProxyTestResult{
		ProxyID:      proxy.ID,
		ProxyType:    proxy.Type,
		Success:      valueBool(payload["success"]),
		Message:      strings.TrimSpace(fmt.Sprintf("%v", payload["message"])),
		ResponseTime: valueInt(payload["response_time"]),
		StatusCode:   valueInt(payload["status_code"]),
		ExitIP:       valueString(payload["exit_ip"]),
		Country:      strings.ToUpper(valueString(payload["country"])),
		Colo:         strings.ToUpper(valueString(payload["colo"])),
	}
}

func selectProxies(proxies []config.Proxy, ids []string) []config.Proxy {
	wanted := map[string]struct{}{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	all := len(wanted) == 0
	out := make([]config.Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		proxy = config.NormalizeProxy(proxy)
		if all {
			out = append(out, proxy)
			continue
		}
		if _, exists := wanted[proxy.ID]; exists {
			out = append(out, proxy)
		}
	}
	return out
}

func valueBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func valueInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func valueString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}
