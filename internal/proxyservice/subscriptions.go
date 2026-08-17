package proxyservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxysubscription"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

type Store interface {
	Snapshot() config.Config
	Update(func(*config.Config) error) error
}

type SubscriptionRefreshResult struct {
	SubscriptionID    string   `json:"subscription_id"`
	NodeCount         int      `json:"node_count"`
	Added             int      `json:"added"`
	Updated           int      `json:"updated"`
	Removed           int      `json:"removed"`
	Disabled          int      `json:"disabled"`
	SkippedDuplicates int      `json:"skipped_duplicates"`
	Invalid           int      `json:"invalid"`
	Warnings          []string `json:"warnings,omitempty"`
}

func RefreshSubscription(ctx context.Context, store Store, subscriptionID string) (SubscriptionRefreshResult, error) {
	if store == nil {
		return SubscriptionRefreshResult{}, errors.New("proxy subscription store is nil")
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	snapshot := store.Snapshot()
	subscription, ok := findSubscription(snapshot.ProxySubscriptions, subscriptionID)
	if !ok {
		return SubscriptionRefreshResult{}, errors.New("proxy subscription not found")
	}
	now := time.Now().Unix()
	body, err := proxysubscription.Fetch(ctx, subscription.URL)
	if err != nil {
		_ = recordSubscriptionFailure(store, subscriptionID, now, err)
		return SubscriptionRefreshResult{}, err
	}
	parsed, err := proxysubscription.Parse(body, subscriptionID)
	if err != nil {
		_ = recordSubscriptionFailure(store, subscriptionID, now, err)
		return SubscriptionRefreshResult{}, err
	}
	result := SubscriptionRefreshResult{
		SubscriptionID: subscriptionID,
		NodeCount:      len(parsed.Proxies),
		Invalid:        parsed.Invalid,
		Warnings:       parsed.Warnings,
	}
	err = store.Update(func(cfg *config.Config) error {
		index := subscriptionIndex(cfg.ProxySubscriptions, subscriptionID)
		if index < 0 {
			return errors.New("proxy subscription not found")
		}
		existing := make(map[string]config.Proxy)
		existingBySemanticKey := make(map[string]config.Proxy)
		configuredSemanticKeys := make(map[string]struct{})
		assigned := make(map[string]bool)
		for _, account := range cfg.Accounts {
			assigned[strings.TrimSpace(account.ProxyID)] = true
		}
		for _, configured := range cfg.Proxies {
			proxy := config.NormalizeProxy(configured)
			if semanticKey, semanticErr := proxysubscription.SemanticKey(proxy); semanticErr == nil {
				configuredSemanticKeys[semanticKey] = struct{}{}
				if proxy.SubscriptionID == subscriptionID {
					existingBySemanticKey[semanticKey] = proxy
				}
			}
			if proxy.SubscriptionID == subscriptionID {
				existing[proxy.ID] = proxy
			}
		}
		incomingIDs := make(map[string]struct{}, len(parsed.Proxies))
		updatedNodeIDs := make(map[string]struct{}, len(parsed.Proxies))
		updatedNodes := make([]config.Proxy, 0, len(parsed.Proxies))
		for _, proxy := range parsed.Proxies {
			incomingIDs[proxy.ID] = struct{}{}
			previous, exists := existing[proxy.ID]
			if !exists {
				semanticKey, semanticErr := proxysubscription.SemanticKey(proxy)
				if semanticErr == nil {
					if semanticPrevious, semanticExists := existingBySemanticKey[semanticKey]; semanticExists {
						// Keep the prior ID so assigned accounts remain attached when a
						// semantically identical node has a different URI representation.
						proxy.ID = semanticPrevious.ID
						incomingIDs[proxy.ID] = struct{}{}
						previous = semanticPrevious
						exists = true
					} else if _, duplicate := configuredSemanticKeys[semanticKey]; duplicate {
						result.SkippedDuplicates++
						continue
					} else {
						configuredSemanticKeys[semanticKey] = struct{}{}
					}
				}
			}
			if exists {
				proxy.Disabled = previous.Disabled
				proxy.DisabledReason = previous.DisabledReason
				proxy.DisabledAtUnix = previous.DisabledAtUnix
				proxy.ConsecutiveFailures = previous.ConsecutiveFailures
				proxy.LastTestAtUnix = previous.LastTestAtUnix
				proxy.LastTestSuccess = previous.LastTestSuccess
				proxy.LastLatencyMS = previous.LastLatencyMS
				proxy.LastHTTPStatus = previous.LastHTTPStatus
				proxy.LastTestError = previous.LastTestError
				proxy.LastExitIP = previous.LastExitIP
				proxy.LastCountry = previous.LastCountry
				proxy.LastColo = previous.LastColo
				if proxy.DisabledReason == config.ProxyDisabledSubscriptionRemoved {
					proxy.Disabled = false
					proxy.DisabledReason = ""
					proxy.DisabledAtUnix = 0
				}
			}
			if _, alreadyIncluded := updatedNodeIDs[proxy.ID]; alreadyIncluded {
				result.SkippedDuplicates++
				continue
			}
			updatedNodeIDs[proxy.ID] = struct{}{}
			if exists {
				result.Updated++
			} else {
				result.Added++
			}
			updatedNodes = append(updatedNodes, proxy)
		}
		if result.SkippedDuplicates > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"skipped %d node(s) already present in another subscription or manual proxy configuration",
				result.SkippedDuplicates,
			))
		}

		kept := make([]config.Proxy, 0, len(cfg.Proxies)+len(updatedNodes))
		for _, configured := range cfg.Proxies {
			proxy := config.NormalizeProxy(configured)
			if proxy.SubscriptionID != subscriptionID {
				// Never rewrite manually managed or other-subscription nodes while
				// refreshing this subscription. In particular, an assigned node that
				// matches an incoming semantic key remains authoritative.
				kept = append(kept, configured)
				continue
			}
			if _, exists := incomingIDs[proxy.ID]; exists {
				continue
			}
			if assigned[proxy.ID] {
				configured.Disabled = true
				configured.DisabledReason = config.ProxyDisabledSubscriptionRemoved
				configured.DisabledAtUnix = now
				kept = append(kept, configured)
				result.Disabled++
			} else {
				result.Removed++
			}
		}
		cfg.Proxies = append(kept, updatedNodes...)
		cfg.ProxySubscriptions[index].LastAttemptAtUnix = now
		cfg.ProxySubscriptions[index].LastUpdatedAtUnix = now
		cfg.ProxySubscriptions[index].LastError = ""
		cfg.ProxySubscriptions[index].NodeCount = len(parsed.Proxies)
		return config.ValidateConfig(*cfg)
	})
	if err != nil {
		_ = recordSubscriptionFailure(store, subscriptionID, now, err)
		return SubscriptionRefreshResult{}, err
	}
	if syncErr := xrayproxy.SyncAssigned(ctx, store.Snapshot()); syncErr != nil {
		return result, fmt.Errorf("subscription updated but xray route sync failed: %w", syncErr)
	}
	return result, nil
}

func recordSubscriptionFailure(store Store, subscriptionID string, now int64, cause error) error {
	return store.Update(func(cfg *config.Config) error {
		index := subscriptionIndex(cfg.ProxySubscriptions, subscriptionID)
		if index < 0 {
			return errors.New("proxy subscription not found")
		}
		cfg.ProxySubscriptions[index].LastAttemptAtUnix = now
		cfg.ProxySubscriptions[index].LastError = truncateError(cause.Error(), 600)
		return nil
	})
}

func findSubscription(subscriptions []config.ProxySubscription, id string) (config.ProxySubscription, bool) {
	index := subscriptionIndex(subscriptions, id)
	if index < 0 {
		return config.ProxySubscription{}, false
	}
	return subscriptions[index], true
}

func subscriptionIndex(subscriptions []config.ProxySubscription, id string) int {
	id = strings.TrimSpace(id)
	for i := range subscriptions {
		if strings.TrimSpace(subscriptions[i].ID) == id {
			return i
		}
	}
	return -1
}

func truncateError(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}
