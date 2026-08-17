package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

const (
	batchAccountsMaxBodyBytes = 16 << 20
	batchAccountsMaxItems     = 5000
)

type batchAccountsRequest struct {
	Accounts    []batchAccountInput  `json:"accounts"`
	OnDuplicate string               `json:"on_duplicate,omitempty"`
	DryRun      bool                 `json:"dry_run,omitempty"`
	Defaults    batchAccountDefaults `json:"defaults,omitempty"`
}

type batchAccountDefaults struct {
	ProxyID        string `json:"proxy_id,omitempty"`
	ProxyAutoRoute *bool  `json:"proxy_auto_route,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
}

type batchAccountInput struct {
	Name           *string `json:"name,omitempty"`
	Remark         *string `json:"remark,omitempty"`
	Email          string  `json:"email,omitempty"`
	Mobile         string  `json:"mobile,omitempty"`
	Password       string  `json:"password,omitempty"`
	Token          string  `json:"token,omitempty"`
	ProxyID        *string `json:"proxy_id,omitempty"`
	ProxyAutoRoute *bool   `json:"proxy_auto_route,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"`
}

type batchAccountResult struct {
	Index      int    `json:"index"`
	Identifier string `json:"identifier,omitempty"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type batchAccountsSummary struct {
	Success   bool                 `json:"success"`
	DryRun    bool                 `json:"dry_run"`
	Submitted int                  `json:"submitted"`
	Created   int                  `json:"created"`
	Updated   int                  `json:"updated"`
	Skipped   int                  `json:"skipped"`
	Invalid   int                  `json:"invalid"`
	Results   []batchAccountResult `json:"results"`
}

func (h *Handler) batchAccounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"detail": "Content-Type must be application/json"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, batchAccountsMaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req batchAccountsRequest
	if err := decoder.Decode(&req); err != nil {
		status := http.StatusBadRequest
		detail := "invalid JSON request"
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
			detail = "request body exceeds 16 MiB"
		}
		writeJSON(w, status, map[string]any{"detail": detail})
		return
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if len(req.Accounts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "accounts must contain at least one item"})
		return
	}
	if len(req.Accounts) > batchAccountsMaxItems {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "accounts exceeds the 5000 item limit"})
		return
	}
	policy := strings.ToLower(strings.TrimSpace(req.OnDuplicate))
	if policy == "" {
		policy = "skip"
	}
	if policy != "skip" && policy != "update" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "on_duplicate must be skip or update"})
		return
	}
	req.OnDuplicate = policy

	var summary batchAccountsSummary
	if req.DryRun {
		snapshot := h.Store.Snapshot()
		summary = applyBatchAccounts(&snapshot, req)
	} else {
		if err := h.Store.Update(func(c *config.Config) error {
			summary = applyBatchAccounts(c, req)
			return nil
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist accounts"})
			return
		}
		if summary.Created > 0 || summary.Updated > 0 {
			h.Pool.Reset()
		}
	}

	summary.Success = summary.Invalid == 0
	summary.DryRun = req.DryRun
	summary.Submitted = len(req.Accounts)
	config.Logger.Info("[admin_accounts_batch] completed",
		"dry_run", req.DryRun,
		"submitted", summary.Submitted,
		"created", summary.Created,
		"updated", summary.Updated,
		"skipped", summary.Skipped,
		"invalid", summary.Invalid,
	)
	response := map[string]any{
		"success":        summary.Success,
		"dry_run":        summary.DryRun,
		"submitted":      summary.Submitted,
		"created":        summary.Created,
		"updated":        summary.Updated,
		"skipped":        summary.Skipped,
		"invalid":        summary.Invalid,
		"results":        summary.Results,
		"total_accounts": len(h.Store.Snapshot().Accounts),
	}
	writeJSON(w, http.StatusOK, response)
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("invalid trailing JSON data")
	}
	return fmt.Errorf("request must contain one JSON object")
}

func applyBatchAccounts(c *config.Config, req batchAccountsRequest) batchAccountsSummary {
	summary := batchAccountsSummary{Results: make([]batchAccountResult, 0, len(req.Accounts))}
	index := make(map[string]int, len(c.Accounts)+len(req.Accounts))
	for i, acc := range c.Accounts {
		if key := batchAccountKey(acc); key != "" {
			index[key] = i
		}
	}

	for itemIndex, input := range req.Accounts {
		acc, enabledSpecified, autoRouteSpecified, err := normalizeBatchAccount(input, req.Defaults)
		identifier := acc.Identifier()
		if err != nil {
			summary.Invalid++
			summary.Results = append(summary.Results, batchAccountResult{Index: itemIndex, Identifier: identifier, Status: "invalid", Code: "validation_error", Detail: err.Error()})
			continue
		}
		if acc.ProxyID != "" {
			if _, ok := findProxyByID(*c, acc.ProxyID); !ok {
				summary.Invalid++
				summary.Results = append(summary.Results, batchAccountResult{Index: itemIndex, Identifier: identifier, Status: "invalid", Code: "proxy_not_found", Detail: "proxy_id does not exist"})
				continue
			}
		}

		key := batchAccountKey(acc)
		if existingIndex, exists := index[key]; exists {
			if req.OnDuplicate == "skip" {
				summary.Skipped++
				summary.Results = append(summary.Results, batchAccountResult{Index: itemIndex, Identifier: identifier, Status: "skipped", Code: "duplicate"})
				continue
			}
			merged := mergeBatchAccount(c.Accounts[existingIndex], acc, input, enabledSpecified, autoRouteSpecified)
			c.Accounts[existingIndex] = merged
			summary.Updated++
			summary.Results = append(summary.Results, batchAccountResult{Index: itemIndex, Identifier: merged.Identifier(), Status: "updated"})
			continue
		}

		if strings.TrimSpace(acc.Password) == "" && strings.TrimSpace(acc.Token) == "" {
			summary.Invalid++
			summary.Results = append(summary.Results, batchAccountResult{Index: itemIndex, Identifier: identifier, Status: "invalid", Code: "credentials_required", Detail: "password or token is required for a new account"})
			continue
		}
		if acc.Disabled && acc.DisabledAtUnix == 0 {
			acc.DisabledReason = config.AccountDisabledManual
			acc.DisabledAtUnix = time.Now().Unix()
		}
		c.Accounts = append(c.Accounts, acc)
		index[key] = len(c.Accounts) - 1
		summary.Created++
		summary.Results = append(summary.Results, batchAccountResult{Index: itemIndex, Identifier: identifier, Status: "created"})
	}
	return summary
}

func normalizeBatchAccount(input batchAccountInput, defaults batchAccountDefaults) (config.Account, bool, bool, error) {
	proxyID := ""
	if input.ProxyID != nil {
		proxyID = strings.TrimSpace(*input.ProxyID)
	} else {
		proxyID = strings.TrimSpace(defaults.ProxyID)
	}
	enabled := input.Enabled
	if enabled == nil {
		enabled = defaults.Enabled
	}
	acc := config.NormalizeAccountIdentity(config.Account{
		Name:     optionalTrimmedString(input.Name),
		Remark:   optionalTrimmedString(input.Remark),
		Email:    strings.TrimSpace(input.Email),
		Mobile:   strings.TrimSpace(input.Mobile),
		Password: input.Password,
		Token:    strings.TrimSpace(input.Token),
		ProxyID:  proxyID,
	})
	if input.ProxyAutoRoute != nil {
		acc.ProxyAutoRoute = *input.ProxyAutoRoute
	} else if defaults.ProxyAutoRoute != nil {
		acc.ProxyAutoRoute = *defaults.ProxyAutoRoute
	}
	if acc.ProxyAutoRoute && strings.TrimSpace(input.Password) == "" && strings.TrimSpace(input.Token) != "" {
		return acc, enabled != nil, input.ProxyAutoRoute != nil || defaults.ProxyAutoRoute != nil, fmt.Errorf("password is required for automatic proxy routing")
	}
	if acc.Identifier() == "" {
		return acc, enabled != nil, input.ProxyAutoRoute != nil || defaults.ProxyAutoRoute != nil, fmt.Errorf("email or mobile is required")
	}
	if len(input.Password) > 4096 {
		return acc, enabled != nil, input.ProxyAutoRoute != nil || defaults.ProxyAutoRoute != nil, fmt.Errorf("password exceeds 4096 characters")
	}
	if len(input.Token) > 65536 {
		return acc, enabled != nil, input.ProxyAutoRoute != nil || defaults.ProxyAutoRoute != nil, fmt.Errorf("token exceeds 65536 characters")
	}
	if enabled != nil {
		acc.Disabled = !*enabled
	}
	return acc, enabled != nil, input.ProxyAutoRoute != nil || defaults.ProxyAutoRoute != nil, nil
}

func mergeBatchAccount(existing, incoming config.Account, input batchAccountInput, enabledSpecified, autoRouteSpecified bool) config.Account {
	if input.Name != nil {
		existing.Name = incoming.Name
	}
	if input.Remark != nil {
		existing.Remark = incoming.Remark
	}
	if strings.TrimSpace(input.Password) != "" {
		existing.Password = input.Password
	}
	if strings.TrimSpace(input.Token) != "" {
		existing.Token = strings.TrimSpace(input.Token)
	}
	if input.ProxyID != nil || incoming.ProxyID != "" {
		existing.ProxyID = incoming.ProxyID
	}
	if autoRouteSpecified {
		existing.ProxyAutoRoute = incoming.ProxyAutoRoute
		if existing.ProxyAutoRoute && strings.TrimSpace(input.Password) == "" && strings.TrimSpace(existing.Password) == "" {
			existing.ProxyAutoRoute = false
		}
	}
	if enabledSpecified {
		existing.Disabled = incoming.Disabled
		if incoming.Disabled {
			existing.DisabledReason = config.AccountDisabledManual
			existing.DisabledAtUnix = time.Now().Unix()
		} else {
			existing.DisabledReason = ""
			existing.DisabledAtUnix = 0
		}
		existing.CooldownState = ""
		existing.CooldownUntilUnix = 0
	}
	return existing
}

func optionalTrimmedString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func batchAccountKey(acc config.Account) string {
	if email := strings.ToLower(strings.TrimSpace(acc.Email)); email != "" {
		return "email:" + email
	}
	if mobile := config.CanonicalMobileKey(acc.Mobile); mobile != "" {
		return "mobile:" + mobile
	}
	return ""
}
