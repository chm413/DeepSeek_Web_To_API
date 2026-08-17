package proxies

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
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
	if references := proxyDeletionReferences(cfg, wanted); len(references) > 0 {
		return &proxyDeletionConflictError{References: references}
	}
	return nil
}

func writeProxyDeletionConflict(w http.ResponseWriter, err error) bool {
	var conflict *proxyDeletionConflictError
	if !errors.As(err, &conflict) {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"detail":     conflict.Error(),
		"references": conflict.References,
	})
	return true
}
