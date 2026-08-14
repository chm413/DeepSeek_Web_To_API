package proxyservice

import (
	"errors"
	"sort"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
)

type AutoRouteChange struct {
	AccountID   string `json:"account_id"`
	FromProxyID string `json:"from_proxy_id,omitempty"`
	ToProxyID   string `json:"to_proxy_id,omitempty"`
	Reason      string `json:"reason"`
}

type RoutePoolNode struct {
	ProxyID            string `json:"proxy_id"`
	LatencyMS          int    `json:"latency_ms"`
	Country            string `json:"country,omitempty"`
	Colo               string `json:"colo,omitempty"`
	ExitIP             string `json:"exit_ip,omitempty"`
	AssignedAccounts   int    `json:"assigned_accounts"`
	AutoRoutedAccounts int    `json:"auto_routed_accounts"`
}

func ProxyAssignmentCounts(cfg config.Config) (map[string]int, map[string]int) {
	assigned := make(map[string]int)
	automatic := make(map[string]int)
	for _, account := range cfg.Accounts {
		if account.Disabled {
			continue
		}
		proxyID := strings.TrimSpace(account.ProxyID)
		if proxyID == "" {
			continue
		}
		assigned[proxyID]++
		if account.ProxyAutoRoute {
			automatic[proxyID]++
		}
	}
	return assigned, automatic
}

func AvailableRoutePool(cfg config.Config) []RoutePoolNode {
	assigned, automatic := ProxyAssignmentCounts(cfg)
	nodes := make([]RoutePoolNode, 0, len(cfg.Proxies))
	for _, raw := range cfg.Proxies {
		proxy := config.NormalizeProxy(raw)
		if !proxyAvailableForRouting(proxy) {
			continue
		}
		nodes = append(nodes, RoutePoolNode{
			ProxyID:            proxy.ID,
			LatencyMS:          proxy.LastLatencyMS,
			Country:            proxy.LastCountry,
			Colo:               proxy.LastColo,
			ExitIP:             proxy.LastExitIP,
			AssignedAccounts:   assigned[proxy.ID],
			AutoRoutedAccounts: automatic[proxy.ID],
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].AssignedAccounts != nodes[j].AssignedAccounts {
			return nodes[i].AssignedAccounts < nodes[j].AssignedAccounts
		}
		leftLatency := effectiveRouteLatency(nodes[i].LatencyMS)
		rightLatency := effectiveRouteLatency(nodes[j].LatencyMS)
		if leftLatency != rightLatency {
			return leftLatency < rightLatency
		}
		return nodes[i].ProxyID < nodes[j].ProxyID
	})
	return nodes
}

// ReconcileAutoRoutes keeps valid assignments sticky. It only assigns an
// unassigned account or moves one whose current node has left the latest
// successful-test pool. Changing egress invalidates the old login token.
func ReconcileAutoRoutes(store Store) ([]AutoRouteChange, error) {
	if store == nil {
		return nil, errors.New("proxy route store is nil")
	}
	snapshot := store.Snapshot()
	if !snapshot.ProxyPolicy.AutoRouteEnabled() {
		return nil, nil
	}
	if len(planAutoRouteChanges(snapshot)) == 0 {
		return nil, nil
	}
	var changes []AutoRouteChange
	err := store.Update(func(cfg *config.Config) error {
		changes = applyAutoRouteChanges(cfg)
		return config.ValidateConfig(*cfg)
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}

func planAutoRouteChanges(cfg config.Config) []AutoRouteChange {
	clone := cfg.Clone()
	return applyAutoRouteChanges(&clone)
}

func applyAutoRouteChanges(cfg *config.Config) []AutoRouteChange {
	if cfg == nil || !cfg.ProxyPolicy.AutoRouteEnabled() {
		return nil
	}
	available := make(map[string]config.Proxy)
	for _, raw := range cfg.Proxies {
		proxy := config.NormalizeProxy(raw)
		if proxyAvailableForRouting(proxy) {
			available[proxy.ID] = proxy
		}
	}
	counts, _ := ProxyAssignmentCounts(*cfg)
	indices := make([]int, 0, len(cfg.Accounts))
	for index, account := range cfg.Accounts {
		if account.Disabled || !account.ProxyAutoRoute || strings.TrimSpace(account.Password) == "" {
			continue
		}
		indices = append(indices, index)
	}
	sort.Slice(indices, func(i, j int) bool {
		return cfg.Accounts[indices[i]].Identifier() < cfg.Accounts[indices[j]].Identifier()
	})

	changes := make([]AutoRouteChange, 0)
	for _, index := range indices {
		account := &cfg.Accounts[index]
		currentID := strings.TrimSpace(account.ProxyID)
		if _, valid := available[currentID]; valid {
			continue
		}
		nextID := leastAssignedRoute(available, counts)
		if nextID == currentID {
			continue
		}
		account.ProxyID = nextID
		account.Token = ""
		reason := "unassigned"
		if currentID != "" {
			reason = "node_unavailable"
		}
		changes = append(changes, AutoRouteChange{
			AccountID:   account.Identifier(),
			FromProxyID: currentID,
			ToProxyID:   nextID,
			Reason:      reason,
		})
		if nextID != "" {
			counts[nextID]++
		}
	}
	return changes
}

func leastAssignedRoute(available map[string]config.Proxy, counts map[string]int) string {
	ids := make([]string, 0, len(available))
	for id := range available {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if counts[ids[i]] != counts[ids[j]] {
			return counts[ids[i]] < counts[ids[j]]
		}
		leftLatency := effectiveRouteLatency(available[ids[i]].LastLatencyMS)
		rightLatency := effectiveRouteLatency(available[ids[j]].LastLatencyMS)
		if leftLatency != rightLatency {
			return leftLatency < rightLatency
		}
		return ids[i] < ids[j]
	})
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func proxyAvailableForRouting(proxy config.Proxy) bool {
	return !proxy.Disabled && proxy.LastTestAtUnix > 0 && proxy.LastTestSuccess
}

func effectiveRouteLatency(latency int) int {
	if latency <= 0 {
		return int(^uint(0) >> 1)
	}
	return latency
}
