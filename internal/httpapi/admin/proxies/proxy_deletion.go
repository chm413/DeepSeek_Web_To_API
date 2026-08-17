package proxies

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
)

const proxyDeletionReferencePreviewLimit = 25

// proxyDeletionReference identifies the active configuration that must be
// changed before a proxy can be safely deleted. Account identifiers are capped
// so a large deployment cannot turn a failed delete into an oversized response.
type proxyDeletionReference struct {
	ProxyID                     string   `json:"proxy_id"`
	AccountCount                int      `json:"account_count"`
	AccountIdentifiers          []string `json:"account_identifiers,omitempty"`
	AccountIdentifiersTruncated bool     `json:"account_identifiers_truncated,omitempty"`
	FallbackRoute               bool     `json:"fallback_route"`
}

type proxyDeletionConflictError struct {
	References []proxyDeletionReference
}

func (e *proxyDeletionConflictError) Error() string {
	return "proxy deletion is blocked while selected proxies are referenced by accounts or the fallback route"
}

type proxyDeletionRouteError struct {
	err error
}

func (e *proxyDeletionRouteError) Error() string {
	if e == nil || e.err == nil {
		return "proxy deletion cannot safely migrate its assigned routes"
	}
	return e.err.Error()
}

func proxyDeletionReferences(cfg config.Config, wanted map[string]struct{}) []proxyDeletionReference {
	if len(wanted) == 0 {
		return nil
	}

	referencesByID := make(map[string]*proxyDeletionReference, len(wanted))
	for _, proxy := range cfg.Proxies {
		proxy = config.NormalizeProxy(proxy)
		if _, selected := wanted[proxy.ID]; !selected {
			continue
		}
		referencesByID[proxy.ID] = &proxyDeletionReference{ProxyID: proxy.ID}
	}
	for i := range cfg.Accounts {
		proxyID := strings.TrimSpace(cfg.Accounts[i].ProxyID)
		reference, selected := referencesByID[proxyID]
		if !selected {
			continue
		}
		reference.AccountCount++
		if len(reference.AccountIdentifiers) >= proxyDeletionReferencePreviewLimit {
			reference.AccountIdentifiersTruncated = true
			continue
		}
		if identifier := strings.TrimSpace(cfg.Accounts[i].Identifier()); identifier != "" {
			reference.AccountIdentifiers = append(reference.AccountIdentifiers, identifier)
		}
	}

	fallbackID := strings.TrimSpace(cfg.ProxyPolicy.FallbackProxyID)
	if reference, selected := referencesByID[fallbackID]; selected {
		reference.FallbackRoute = true
	}

	references := make([]proxyDeletionReference, 0, len(referencesByID))
	for _, reference := range referencesByID {
		if reference.AccountCount == 0 && !reference.FallbackRoute {
			continue
		}
		sort.Strings(reference.AccountIdentifiers)
		references = append(references, *reference)
	}
	sort.Slice(references, func(i, j int) bool {
		return references[i].ProxyID < references[j].ProxyID
	})
	return references
}

func ensureProxyDeletionAllowed(cfg config.Config, wanted map[string]struct{}) error {
	references := proxyDeletionReferences(cfg, wanted)
	fallbackReferences := make([]proxyDeletionReference, 0, len(references))
	for _, reference := range references {
		if reference.FallbackRoute {
			fallbackReferences = append(fallbackReferences, reference)
		}
	}
	if len(fallbackReferences) > 0 {
		return &proxyDeletionConflictError{References: fallbackReferences}
	}
	return nil
}

// applyProxyDeletionRoutes plans all route moves before removing a node. A
// manual assignment must land on the configured fallback, while an automatic
// assignment must land on another tested route. This prevents a successful
// delete from silently leaving an account on direct egress.
func applyProxyDeletionRoutes(cfg *config.Config, wanted map[string]struct{}) ([]proxyservice.AutoRouteChange, error) {
	if cfg == nil {
		return nil, errors.New("proxy deletion config is nil")
	}
	if err := ensureProxyDeletionAllowed(*cfg, wanted); err != nil {
		return nil, err
	}
	changes, err := proxyservice.ReassignDeletedProxyRoutes(cfg, wanted)
	if err != nil {
		return nil, &proxyDeletionRouteError{err: err}
	}
	kept := make([]config.Proxy, 0, len(cfg.Proxies))
	for _, raw := range cfg.Proxies {
		proxy := config.NormalizeProxy(raw)
		if _, removing := wanted[proxy.ID]; removing {
			continue
		}
		kept = append(kept, proxy)
	}
	cfg.Proxies = kept
	return changes, nil
}

func writeProxyDeletionConflict(w http.ResponseWriter, err error) bool {
	var conflict *proxyDeletionConflictError
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"detail":     conflict.Error(),
			"references": conflict.References,
		})
		return true
	}
	var routing *proxyDeletionRouteError
	if errors.As(err, &routing) {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": routing.Error()})
		return true
	}
	return false
}
