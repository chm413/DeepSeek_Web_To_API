package proxies

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyservice"
)

const batchAccountActionMaxItems = 5000

type batchAccountActionRequest struct {
	Identifiers []string `json:"identifiers"`
	Action      string   `json:"action"`
	ProxyID     *string  `json:"proxy_id,omitempty"`
	AutoRoute   bool     `json:"auto_route,omitempty"`
}

// batchAccountActions applies a single, explicit operation to selected accounts.
// Route changes remain all-or-nothing because a partially changed egress set can
// leave accounts with tokens bound to a different IP.
func (h *Handler) batchAccountActions(w http.ResponseWriter, r *http.Request) {
	var req batchAccountActionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid JSON body"})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "request must contain one JSON object"})
		return
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != "set_proxy" && action != "enable" && action != "disable" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "action must be set_proxy, enable, or disable"})
		return
	}
	if action == "set_proxy" && req.ProxyID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "proxy_id is required for set_proxy"})
		return
	}

	identifiers := uniqueAccountIdentifiers(req.Identifiers)
	if len(identifiers) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "identifiers is required"})
		return
	}
	if len(identifiers) > batchAccountActionMaxItems {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "identifiers exceeds the 5000 item limit"})
		return
	}
	if action == "set_proxy" && req.AutoRoute && !h.Store.Snapshot().ProxyPolicy.AutoRouteEnabled() {
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "automatic proxy routing is disabled in proxy policy"})
		return
	}

	targetProxyID := ""
	if req.ProxyID != nil {
		targetProxyID = strings.TrimSpace(*req.ProxyID)
	}
	manualChanges := make([]proxyservice.AutoRouteChange, 0, len(identifiers))
	affected := 0
	routeChanged := 0

	err := h.Store.Update(func(c *config.Config) error {
		if action == "set_proxy" && targetProxyID != "" {
			if _, ok := findProxyByID(*c, targetProxyID); !ok {
				return newRequestError("代理不存在")
			}
		}

		selected := make(map[string]int, len(identifiers))
		for index, account := range c.Accounts {
			for _, identifier := range identifiers {
				if accountMatchesIdentifier(account, identifier) {
					selected[identifier] = index
					break
				}
			}
		}
		for _, identifier := range identifiers {
			if _, found := selected[identifier]; !found {
				return newRequestError(fmt.Sprintf("账号不存在: %s", identifier))
			}
		}

		if action == "set_proxy" {
			for _, identifier := range identifiers {
				account := &c.Accounts[selected[identifier]]
				fromProxyID := strings.TrimSpace(account.ProxyID)
				nextProxyID := targetProxyID
				if req.AutoRoute && nextProxyID == "" {
					// Keep an already healthy assignment sticky; reconciliation only
					// moves it when that node is no longer in the route pool.
					nextProxyID = fromProxyID
				}
				if req.AutoRoute && strings.TrimSpace(account.Password) == "" {
					return newRequestError(fmt.Sprintf("账号 %s 缺少密码，无法启用自动代理路由", identifier))
				}
				if fromProxyID != nextProxyID && strings.TrimSpace(account.Password) == "" {
					return newRequestError(fmt.Sprintf("账号 %s 缺少密码，无法切换出口代理", identifier))
				}
				account.ProxyID = nextProxyID
				account.ProxyAutoRoute = req.AutoRoute
				if fromProxyID != nextProxyID {
					account.Token = ""
					routeChanged++
					if !req.AutoRoute {
						manualChanges = append(manualChanges, proxyservice.AutoRouteChange{
							AccountID:   account.Identifier(),
							FromProxyID: fromProxyID,
							ToProxyID:   nextProxyID,
							Reason:      "manual_batch_update",
						})
					}
				}
				affected++
			}
		} else {
			now := time.Now().Unix()
			for _, identifier := range identifiers {
				account := &c.Accounts[selected[identifier]]
				account.Disabled = action == "disable"
				if account.Disabled {
					account.DisabledReason = config.AccountDisabledManual
					account.DisabledAtUnix = now
				} else {
					account.DisabledReason = ""
					account.DisabledAtUnix = 0
				}
				affected++
			}
		}
		return validateProxyMutation(c)
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	h.Pool.Reset()
	autoRelogins, err := h.reconcileAndSyncProxyRoutes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	routeChanged += len(autoRelogins)
	manualRelogins := h.reloginManualRouteChanges(r.Context(), manualChanges)
	relogin := mergeRouteReloginResults(autoRelogins, manualRelogins)
	config.Logger.Info("[proxy_router] batch account action completed",
		"action", action,
		"accounts", affected,
		"route_changed", routeChanged,
		"auto_route", req.AutoRoute,
		"proxy_id", targetProxyID,
		"relogin_attempted", relogin["attempted"],
		"relogin_failed", relogin["failed"],
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"action":        action,
		"affected":      affected,
		"route_changed": routeChanged,
		"auto_route":    action == "set_proxy" && req.AutoRoute,
		"proxy_id":      targetProxyID,
		"relogin":       relogin,
	})
}

func uniqueAccountIdentifiers(raw []string) []string {
	identifiers := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		identifier := strings.TrimSpace(value)
		if identifier == "" {
			continue
		}
		key := strings.ToLower(identifier)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		identifiers = append(identifiers, identifier)
	}
	return identifiers
}

func mergeRouteReloginResults(groups ...map[string]map[string]any) map[string]any {
	results := make(map[string]map[string]any)
	for _, group := range groups {
		for identifier, result := range group {
			results[identifier] = result
		}
	}
	failed := 0
	for _, result := range results {
		if success, _ := result["success"].(bool); !success {
			failed++
		}
	}
	return map[string]any{
		"attempted": len(results),
		"succeeded": len(results) - failed,
		"failed":    failed,
		"results":   results,
	}
}
