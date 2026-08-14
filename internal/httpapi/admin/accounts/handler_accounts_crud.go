package accounts

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/usagepricing"
)

const accountUsageWindow = 24 * time.Hour

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	page := intFromQuery(r, "page", 1)
	pageSize := intFromQuery(r, "page_size", 10)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 5000 {
		pageSize = 5000
	}
	accounts := h.Store.Snapshot().Accounts
	reverseAccounts(accounts)
	q := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("q")))
	if q != "" {
		filtered := make([]config.Account, 0, len(accounts))
		for _, acc := range accounts {
			id := strings.ToLower(acc.Identifier())
			if strings.Contains(id, q) ||
				strings.Contains(strings.ToLower(acc.Name), q) ||
				strings.Contains(strings.ToLower(acc.Remark), q) ||
				strings.Contains(strings.ToLower(acc.Email), q) ||
				strings.Contains(strings.ToLower(acc.Mobile), q) {
				filtered = append(filtered, acc)
			}
		}
		accounts = filtered
	}
	total := len(accounts)
	totalPages := 1
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]map[string]any, 0, end-start)
	runtimeAccounts := map[string]map[string]any{}
	if h.Pool != nil {
		runtimeAccounts = accountRuntimeFromPoolStatus(h.Pool.Status())
	}
	accountUsage := map[string]chathistory.AccountTokenUsage{}
	if h.ChatHistory != nil {
		var err error
		accountUsage, err = h.ChatHistory.AccountTokenUsageByAccount(accountUsageWindow)
		if err != nil {
			log.Printf("admin accounts: aggregate 24h token usage failed: %v", err)
			accountUsage = map[string]chathistory.AccountTokenUsage{}
		}
	}
	now := time.Now()
	for _, acc := range accounts[start:end] {
		testStatus, _ := h.Store.AccountTestStatus(acc.Identifier())
		var testResult config.AccountTestResult
		var hasTestResult bool
		if resultStore, ok := h.Store.(interface {
			AccountTestResult(string) (config.AccountTestResult, bool)
		}); ok {
			testResult, hasTestResult = resultStore.AccountTestResult(acc.Identifier())
		}
		sessionCount, hasSessionCount := h.Store.AccountSessionCount(acc.Identifier())
		token := strings.TrimSpace(acc.Token)
		item := map[string]any{
			"identifier":       acc.Identifier(),
			"name":             acc.Name,
			"remark":           acc.Remark,
			"email":            acc.Email,
			"mobile":           acc.Mobile,
			"proxy_id":         acc.ProxyID,
			"proxy_auto_route": acc.ProxyAutoRoute,
			"has_password":     acc.Password != "",
			"has_token":        token != "",
			"token_preview":    maskSecretPreview(token),
			"test_status":      testStatus,
			"enabled":          !acc.Disabled,
			"disabled":         acc.Disabled,
			"disabled_reason":  acc.DisabledReason,
		}
		usage := accountUsage[strings.ToLower(strings.TrimSpace(acc.Identifier()))]
		item["token_usage_24h"] = map[string]any{
			"window_seconds":          int64(accountUsageWindow.Seconds()),
			"requests":                usage.Requests,
			"input_tokens":            usage.InputTokens,
			"output_tokens":           usage.OutputTokens,
			"cache_hit_input_tokens":  usage.CacheHitInputTokens,
			"cache_miss_input_tokens": usage.CacheMissInputTokens,
			"total_tokens":            usage.TotalTokens,
			"estimated_cost_usd":      usagepricing.CalculateUSD(usage.ByModel, now),
			"currency":                usagepricing.Currency,
		}
		if acc.DisabledAtUnix > 0 {
			item["disabled_at"] = time.Unix(acc.DisabledAtUnix, 0)
		}
		healthState := account.HealthHealthy
		if acc.Disabled {
			switch acc.DisabledReason {
			case config.AccountDisabledUpstreamBanned:
				healthState = account.HealthPermanentlyBanned
			case config.AccountDisabledInvalidCredentials:
				healthState = account.HealthInvalidCredentials
			default:
				healthState = account.HealthDisabled
			}
		}
		if healthProvider, ok := h.Pool.(interface {
			AccountHealth(string) (account.Health, bool)
		}); ok {
			if health, found := healthProvider.AccountHealth(acc.Identifier()); found {
				healthState = health.State
				item["health_reason"] = health.Reason
				item["health_updated_at"] = health.UpdatedAt
				if !health.Until.IsZero() {
					item["health_until"] = health.Until
				}
			}
		}
		item["health_state"] = healthState
		item["account_state"] = healthState
		if runtime := runtimeAccounts[acc.Identifier()]; runtime != nil {
			for _, key := range []string{"in_use", "max_inflight", "available_slots", "utilization_percent"} {
				if value, ok := runtime[key]; ok {
					item[key] = value
				}
			}
			if activity, ok := runtime["activity_state"].(string); ok {
				item["runtime_state"] = activity
			}
			if healthState == account.HealthHealthy {
				if state, ok := runtime["state"].(string); ok {
					item["account_state"] = state
				}
			}
		}
		if hasSessionCount {
			item["session_count"] = sessionCount
		}
		if hasTestResult {
			item["test_result"] = testResult
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "page_size": pageSize, "total_pages": totalPages})
}

func accountRuntimeFromPoolStatus(status map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	if status == nil {
		return out
	}
	switch values := status["account_runtime"].(type) {
	case map[string]map[string]any:
		return values
	case map[string]any:
		for id, raw := range values {
			if item, ok := raw.(map[string]any); ok {
				out[id] = item
			}
		}
	}
	return out
}

func (h *Handler) addAccount(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	acc := toAccount(req)
	if acc.Disabled && acc.DisabledAtUnix == 0 {
		acc.DisabledAtUnix = time.Now().Unix()
	}
	if acc.Identifier() == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "需要 email 或 mobile"})
		return
	}
	err := h.Store.Update(func(c *config.Config) error {
		if acc.ProxyID != "" {
			if _, ok := findProxyByID(*c, acc.ProxyID); !ok {
				return fmt.Errorf("代理不存在")
			}
		}
		mobileKey := config.CanonicalMobileKey(acc.Mobile)
		for _, a := range c.Accounts {
			if acc.Email != "" && a.Email == acc.Email {
				return fmt.Errorf("邮箱已存在")
			}
			if mobileKey != "" && config.CanonicalMobileKey(a.Mobile) == mobileKey {
				return fmt.Errorf("手机号已存在")
			}
		}
		c.Accounts = append(c.Accounts, acc)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_accounts": len(h.Store.Snapshot().Accounts)})
}

func (h *Handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}

	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	name, nameOK := fieldStringOptional(req, "name")
	remark, remarkOK := fieldStringOptional(req, "remark")
	enabled, enabledOK := fieldBoolOptional(req, "enabled")
	disabled, disabledOK := fieldBoolOptional(req, "disabled")
	if enabledOK {
		disabled = !enabled
		disabledOK = true
	}

	err := h.Store.Update(func(c *config.Config) error {
		for i, acc := range c.Accounts {
			if !accountMatchesIdentifier(acc, identifier) {
				continue
			}
			if nameOK {
				c.Accounts[i].Name = name
			}
			if remarkOK {
				c.Accounts[i].Remark = remark
			}
			if disabledOK {
				c.Accounts[i].Disabled = disabled
				if disabled {
					c.Accounts[i].DisabledReason = config.AccountDisabledManual
					c.Accounts[i].DisabledAtUnix = time.Now().Unix()
				} else {
					c.Accounts[i].DisabledReason = ""
					c.Accounts[i].DisabledAtUnix = 0
				}
			}
			return nil
		}
		return newRequestError("账号不存在")
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if disabledOK {
		if !disabled {
			h.clearAccountHealth(identifier)
		}
		h.Pool.Reset()
		config.Logger.Info("[admin_account] account enabled state updated", "account", identifier, "enabled", !disabled)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_accounts": len(h.Store.Snapshot().Accounts)})
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}
	err := h.Store.Update(func(c *config.Config) error {
		idx := -1
		for i, a := range c.Accounts {
			if accountMatchesIdentifier(a, identifier) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("账号不存在")
		}
		c.Accounts = append(c.Accounts[:idx], c.Accounts[idx+1:]...)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_accounts": len(h.Store.Snapshot().Accounts)})
}
