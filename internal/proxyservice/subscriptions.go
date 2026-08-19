package proxyservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	SubscriptionID    string            `json:"subscription_id"`
	NodeCount         int               `json:"node_count"`
	Added             int               `json:"added"`
	Updated           int               `json:"updated"`
	Removed           int               `json:"removed"`
	Disabled          int               `json:"disabled"`
	SkippedDuplicates int               `json:"skipped_duplicates"`
	Invalid           int               `json:"invalid"`
	Warnings          []string          `json:"warnings,omitempty"`
	RouteChanges      []AutoRouteChange `json:"route_changes,omitempty"`
}

// SubscriptionRefreshCommitError indicates that a refresh has already
// committed its configuration and route changes, but the follow-up Xray
// synchronization failed. Callers must still process the RouteChanges
// returned alongside the error (and should expose the operation as partial).
type SubscriptionRefreshCommitError struct {
	err error
}

func (e *SubscriptionRefreshCommitError) Error() string {
	if e == nil || e.err == nil {
		return "subscription updated but route synchronization failed"
	}
	return e.err.Error()
}

func (e *SubscriptionRefreshCommitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// SubscriptionRefreshStaleError means that the subscription was edited while
// a fetch was in progress. Applying the old response would otherwise replace
// nodes for the newly configured URL, so callers should refresh again using
// the current configuration.
type SubscriptionRefreshStaleError struct{}

func (*SubscriptionRefreshStaleError) Error() string {
	return "proxy subscription changed while refresh was in progress"
}

// fetchSubscription is replaceable in package tests so they can exercise
// parsing and route migration with an httptest server without weakening the
// production SSRF boundary in proxysubscription.Fetch.
var fetchSubscription = proxysubscription.Fetch

type subscriptionRefreshGate struct {
	mu   sync.Mutex
	refs int
}

var subscriptionRefreshLocks = struct {
	sync.Mutex
	items map[string]*subscriptionRefreshGate
}{items: make(map[string]*subscriptionRefreshGate)}

// lockSubscriptionRefresh serializes fetch-and-apply work for one
// subscription ID. The reference count removes idle gates again, so repeated
// add/delete operations cannot grow a global lock map indefinitely.
func lockSubscriptionRefresh(subscriptionID string) func() {
	subscriptionRefreshLocks.Lock()
	gate := subscriptionRefreshLocks.items[subscriptionID]
	if gate == nil {
		gate = &subscriptionRefreshGate{}
		subscriptionRefreshLocks.items[subscriptionID] = gate
	}
	gate.refs++
	subscriptionRefreshLocks.Unlock()

	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		subscriptionRefreshLocks.Lock()
		gate.refs--
		if gate.refs == 0 && subscriptionRefreshLocks.items[subscriptionID] == gate {
			delete(subscriptionRefreshLocks.items, subscriptionID)
		}
		subscriptionRefreshLocks.Unlock()
	}
}

func RefreshSubscription(ctx context.Context, store Store, subscriptionID string) (SubscriptionRefreshResult, error) {
	if store == nil {
		return SubscriptionRefreshResult{}, errors.New("proxy subscription store is nil")
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	defer lockSubscriptionRefresh(subscriptionID)()
	snapshot := store.Snapshot()
	subscription, ok := findSubscription(snapshot.ProxySubscriptions, subscriptionID)
	if !ok {
		return SubscriptionRefreshResult{}, errors.New("proxy subscription not found")
	}
	now := time.Now().Unix()
	body, err := fetchSubscription(ctx, subscription.URL)
	if err != nil {
		safeErr := errors.New(proxysubscription.SanitizeError(err))
		_ = recordSubscriptionFailure(store, subscriptionID, subscription.URL, now, safeErr)
		return SubscriptionRefreshResult{}, safeErr
	}
	parsed, err := proxysubscription.Parse(body, subscriptionID)
	if err != nil {
		safeErr := errors.New(proxysubscription.SanitizeError(err))
		_ = recordSubscriptionFailure(store, subscriptionID, subscription.URL, now, safeErr)
		return SubscriptionRefreshResult{}, safeErr
	}
	result := SubscriptionRefreshResult{
		SubscriptionID: subscriptionID,
		NodeCount:      len(parsed.Proxies),
		Invalid:        parsed.Invalid,
		Warnings:       parsed.Warnings,
	}
	for index, warning := range result.Warnings {
		result.Warnings[index] = proxysubscription.SanitizeError(errors.New(warning))
	}
	err = store.Update(func(cfg *config.Config) error {
		index := subscriptionIndex(cfg.ProxySubscriptions, subscriptionID)
		if index < 0 {
			return errors.New("proxy subscription not found")
		}
		if strings.TrimSpace(cfg.ProxySubscriptions[index].URL) != strings.TrimSpace(subscription.URL) {
			return &SubscriptionRefreshStaleError{}
		}
		existing := make(map[string]config.Proxy)
		existingBySemanticKey := make(map[string]config.Proxy)
		configuredSemanticKeys := make(map[string]struct{})
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
			previous, exists := existing[proxy.ID]
			if !exists {
				semanticKey, semanticErr := proxysubscription.SemanticKey(proxy)
				if semanticErr == nil {
					if semanticPrevious, semanticExists := existingBySemanticKey[semanticKey]; semanticExists {
						// Keep the prior ID so assigned accounts remain attached when a
						// semantically identical node has a different URI representation.
						proxy.ID = semanticPrevious.ID
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
			incomingIDs[proxy.ID] = struct{}{}
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

		removedIDs := make(map[string]struct{})
		for _, configured := range cfg.Proxies {
			proxy := config.NormalizeProxy(configured)
			if proxy.SubscriptionID != subscriptionID {
				continue
			}
			if _, exists := incomingIDs[proxy.ID]; !exists {
				removedIDs[proxy.ID] = struct{}{}
			}
		}
		routeChanges, routeErr := ReassignSubscriptionRemovedRoutes(cfg, removedIDs)
		if routeErr != nil {
			return routeErr
		}
		result.RouteChanges = routeChanges

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
			result.Removed++
		}
		cfg.Proxies = append(kept, updatedNodes...)
		cfg.ProxySubscriptions[index].LastAttemptAtUnix = now
		cfg.ProxySubscriptions[index].LastUpdatedAtUnix = now
		cfg.ProxySubscriptions[index].LastError = ""
		cfg.ProxySubscriptions[index].NodeCount = len(parsed.Proxies)
		return config.ValidateConfig(*cfg)
	})
	if err != nil {
		var staleErr *SubscriptionRefreshStaleError
		if errors.As(err, &staleErr) {
			return SubscriptionRefreshResult{}, staleErr
		}
		safeErr := errors.New(proxysubscription.SanitizeError(err))
		var routeErr *SubscriptionRouteMigrationError
		if errors.As(err, &routeErr) {
			safeErr = &SubscriptionRouteMigrationError{err: safeErr}
		}
		_ = recordSubscriptionFailure(store, subscriptionID, subscription.URL, now, safeErr)
		return SubscriptionRefreshResult{}, safeErr
	}
	if syncErr := xrayproxy.SyncAssignedWithStore(ctx, store); syncErr != nil {
		safeErr := proxysubscription.SanitizeError(syncErr)
		failure := &SubscriptionRefreshCommitError{
			err: fmt.Errorf("subscription updated but xray route sync failed: %s", safeErr),
		}
		_ = recordSubscriptionFailure(store, subscriptionID, subscription.URL, time.Now().Unix(), failure)
		return result, failure
	}
	return result, nil
}

func recordSubscriptionFailure(store Store, subscriptionID, expectedURL string, now int64, cause error) error {
	return store.Update(func(cfg *config.Config) error {
		index := subscriptionIndex(cfg.ProxySubscriptions, subscriptionID)
		if index < 0 {
			return errors.New("proxy subscription not found")
		}
		if strings.TrimSpace(expectedURL) != "" && strings.TrimSpace(cfg.ProxySubscriptions[index].URL) != strings.TrimSpace(expectedURL) {
			// A newer edit owns its own status. Do not attach an old fetch error
			// to the replacement URL.
			return nil
		}
		cfg.ProxySubscriptions[index].LastAttemptAtUnix = now
		if cause == nil {
			cfg.ProxySubscriptions[index].LastError = "subscription request failed"
			return nil
		}
		cfg.ProxySubscriptions[index].LastError = truncateError(proxysubscription.SanitizeError(cause), 600)
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
