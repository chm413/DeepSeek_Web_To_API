package proxyservice

import (
	"errors"
	"fmt"
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

// ReassignDeletedProxyRoutes moves accounts away from proxies that are about to
// be removed. Manual assignments move to the configured fallback route, while
// automatic assignments are balanced across the remaining healthy route pool.
// The caller removes the selected proxies only after this function succeeds,
// so a deletion can remain atomic when there is no safe replacement route.
func ReassignDeletedProxyRoutes(cfg *config.Config, deleted map[string]struct{}) ([]AutoRouteChange, error) {
	if cfg == nil || len(deleted) == 0 {
		return nil, nil
	}

	wanted := make(map[string]struct{}, len(deleted))
	for id := range deleted {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	remaining := make(map[string]config.Proxy, len(cfg.Proxies))
	available := make(map[string]config.Proxy, len(cfg.Proxies))
	for _, raw := range cfg.Proxies {
		proxy := config.NormalizeProxy(raw)
		if _, removing := wanted[proxy.ID]; removing {
			continue
		}
		remaining[proxy.ID] = proxy
		if proxyAvailableForRouting(proxy) {
			available[proxy.ID] = proxy
		}
	}

	fallbackID := strings.TrimSpace(cfg.ProxyPolicy.FallbackProxyID)
	if fallbackID != "" {
		if _, removing := wanted[fallbackID]; removing {
			return nil, fmt.Errorf("configured fallback proxy %s is selected for deletion", fallbackID)
		}
	}

	manual := make([]int, 0)
	automatic := make([]int, 0)
	for index := range cfg.Accounts {
		account := &cfg.Accounts[index]
		if _, removing := wanted[strings.TrimSpace(account.ProxyID)]; !removing {
			continue
		}
		if account.ProxyAutoRoute {
			automatic = append(automatic, index)
			continue
		}
		manual = append(manual, index)
	}

	if len(manual) > 0 {
		if fallbackID == "" {
			return nil, errors.New("cannot delete assigned proxy without a configured fallback proxy")
		}
		fallback, exists := remaining[fallbackID]
		if !exists {
			return nil, fmt.Errorf("configured fallback proxy %s is unavailable", fallbackID)
		}
		if fallback.Disabled {
			return nil, fmt.Errorf("configured fallback proxy %s is disabled", fallbackID)
		}
	}
	if len(automatic) > 0 && !cfg.ProxyPolicy.AutoRouteEnabled() {
		return nil, errors.New("cannot delete an automatic proxy route while automatic routing is disabled")
	}
	if len(automatic) > 0 && len(available) == 0 {
		return nil, errors.New("cannot delete an automatic proxy route because no tested replacement node is available")
	}

	sort.Slice(manual, func(i, j int) bool {
		return accountSortKey(cfg.Accounts[manual[i]], manual[i]) < accountSortKey(cfg.Accounts[manual[j]], manual[j])
	})
	sort.Slice(automatic, func(i, j int) bool {
		return accountSortKey(cfg.Accounts[automatic[i]], automatic[i]) < accountSortKey(cfg.Accounts[automatic[j]], automatic[j])
	})

	counts := activeAssignmentsExcluding(*cfg, wanted)
	changes := make([]AutoRouteChange, 0, len(manual)+len(automatic))
	move := func(index int, targetID, reason string) {
		account := &cfg.Accounts[index]
		fromID := strings.TrimSpace(account.ProxyID)
		if fromID == targetID {
			return
		}
		account.ProxyID = targetID
		account.Token = ""
		if account.Disabled {
			return
		}
		changes = append(changes, AutoRouteChange{
			AccountID:   account.Identifier(),
			FromProxyID: fromID,
			ToProxyID:   targetID,
			Reason:      reason,
		})
	}

	for _, index := range manual {
		move(index, fallbackID, "node_deleted_fallback")
		if !cfg.Accounts[index].Disabled {
			counts[fallbackID]++
		}
	}
	for _, index := range automatic {
		nextID := leastAssignedRoute(available, counts)
		if nextID == "" {
			return nil, errors.New("cannot delete an automatic proxy route because no tested replacement node is available")
		}
		move(index, nextID, "node_deleted")
		if !cfg.Accounts[index].Disabled {
			counts[nextID]++
		}
	}
	return changes, nil
}

func activeAssignmentsExcluding(cfg config.Config, excluded map[string]struct{}) map[string]int {
	counts := make(map[string]int)
	for _, account := range cfg.Accounts {
		if account.Disabled {
			continue
		}
		proxyID := strings.TrimSpace(account.ProxyID)
		if proxyID == "" {
			continue
		}
		if _, removed := excluded[proxyID]; removed {
			continue
		}
		counts[proxyID]++
	}
	return counts
}

func accountSortKey(account config.Account, index int) string {
	return fmt.Sprintf("%s\x00%08d", account.Identifier(), index)
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
