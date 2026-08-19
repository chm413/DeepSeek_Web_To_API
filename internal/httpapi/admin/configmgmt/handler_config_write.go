package configmgmt

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/config"
)

func (h *Handler) updateConfig(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	old := h.Store.Snapshot()
	err := h.Store.Update(func(c *config.Config) error {
		if apiKeys, ok := toAPIKeys(req["api_keys"]); ok {
			c.APIKeys = apiKeys
		} else if keys, ok := toStringSlice(req["keys"]); ok {
			c.Keys = keys
		}
		if accountsRaw, ok := req["accounts"].([]any); ok {
			existing := map[string]config.Account{}
			for _, a := range old.Accounts {
				a = normalizeAccountForStorage(a)
				key := accountDedupeKey(a)
				if key != "" {
					existing[key] = a
				}
			}
			seen := map[string]struct{}{}
			accounts := make([]config.Account, 0, len(accountsRaw))
			for _, item := range accountsRaw {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				acc := normalizeAccountForStorage(toAccount(m))
				key := accountDedupeKey(acc)
				if key == "" {
					continue
				}
				if _, ok := seen[key]; ok {
					continue
				}
				if prev, ok := existing[key]; ok {
					if strings.TrimSpace(acc.Password) == "" {
						acc.Password = prev.Password
					}
				}
				seen[key] = struct{}{}
				accounts = append(accounts, acc)
			}
			c.Accounts = accounts
		}
		if m, ok := req["model_aliases"].(map[string]any); ok {
			aliases := make(map[string]string, len(m))
			for k, v := range m {
				aliases[k] = fmt.Sprintf("%v", v)
			}
			c.ModelAliases = aliases
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "配置已更新"})
}

func (h *Handler) addKey(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	key, _ := req["key"].(string)
	key = strings.TrimSpace(key)
	name := fieldString(req, "name")
	remark := fieldString(req, "remark")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "Key 不能为空"})
		return
	}
	err := h.Store.Update(func(c *config.Config) error {
		for _, item := range c.APIKeys {
			if item.Key == key {
				return fmt.Errorf("key 已存在")
			}
		}
		c.APIKeys = append(c.APIKeys, config.APIKey{Key: key, Name: name, Remark: remark})
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_keys": len(h.Store.Snapshot().Keys)})
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request) {
	key := keySelectorFromRequest(r)
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "key 不能为空"})
		return
	}

	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}
	name, nameOK := fieldStringOptional(req, "name")
	remark, remarkOK := fieldStringOptional(req, "remark")
	nextKey, nextKeyOK := fieldStringOptional(req, "key")

	err := h.Store.Update(func(c *config.Config) error {
		idx := findAPIKeyIndex(c.APIKeys, key)
		if idx < 0 {
			return fmt.Errorf("key 不存在")
		}
		if nextKeyOK {
			nextKey = strings.TrimSpace(nextKey)
			if nextKey == "" {
				return fmt.Errorf("key 不能为空")
			}
			if nextKey != c.APIKeys[idx].Key {
				for _, item := range c.APIKeys {
					if item.Key == nextKey {
						return fmt.Errorf("key 已存在")
					}
				}
				c.APIKeys[idx].Key = nextKey
			}
		}
		if nameOK {
			c.APIKeys[idx].Name = name
		}
		if remarkOK {
			c.APIKeys[idx].Remark = remark
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_keys": len(h.Store.Snapshot().Keys)})
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	key := keySelectorFromRequest(r)
	err := h.Store.Update(func(c *config.Config) error {
		idx := findAPIKeyIndex(c.APIKeys, key)
		if idx < 0 {
			return fmt.Errorf("key 不存在")
		}
		c.APIKeys = append(c.APIKeys[:idx], c.APIKeys[idx+1:]...)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "total_keys": len(h.Store.Snapshot().Keys)})
}

func findAPIKeyIndex(keys []config.APIKey, selector string) int {
	selector = strings.TrimSpace(selector)
	for i, item := range keys {
		if item.Key == selector || apiKeyID(item.Key) == selector {
			return i
		}
	}
	return -1
}

func keySelectorFromRequest(r *http.Request) string {
	selector := strings.TrimSpace(chi.URLParam(r, "key"))
	if selector == "" {
		selector = strings.TrimSpace(chi.URLParam(r, "id"))
	}
	return selector
}

func (h *Handler) batchImport(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "无效的 JSON 格式"})
		return
	}
	textAccounts, textErr := parsePlainAccountLines(fieldString(req, "accounts_text"))
	if textErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": textErr.Error()})
		return
	}
	importedKeys, importedAccounts := 0, 0
	submittedKeys, submittedAccounts := 0, 0
	duplicateAccounts, invalidAccounts := 0, 0
	err := h.Store.Update(func(c *config.Config) error {
		if apiKeys, ok := toAPIKeys(req["api_keys"]); ok {
			submittedKeys += len(apiKeys)
			var changed int
			c.APIKeys, changed = mergeAPIKeysPreferStructured(c.APIKeys, apiKeys)
			importedKeys += changed
		}
		if keys, ok := req["keys"].([]any); ok {
			legacy := make([]config.APIKey, 0, len(keys))
			for _, k := range keys {
				key := strings.TrimSpace(fmt.Sprintf("%v", k))
				if key == "" {
					continue
				}
				legacy = append(legacy, config.APIKey{Key: key})
			}
			submittedKeys += len(legacy)
			var changed int
			c.APIKeys, changed = mergeAPIKeysPreferStructured(c.APIKeys, legacy)
			importedKeys += changed
		}
		existing := map[string]bool{}
		for _, a := range c.Accounts {
			a = normalizeAccountForStorage(a)
			key := accountDedupeKey(a)
			if key != "" {
				existing[key] = true
			}
		}
		appendAccount := func(acc config.Account) {
			submittedAccounts++
			key := accountDedupeKey(acc)
			if key == "" {
				invalidAccounts++
				return
			}
			if existing[key] {
				duplicateAccounts++
				return
			}
			c.Accounts = append(c.Accounts, acc)
			existing[key] = true
			importedAccounts++
		}
		if accounts, ok := req["accounts"].([]any); ok {
			for _, item := range accounts {
				m, ok := item.(map[string]any)
				if !ok {
					invalidAccounts++
					submittedAccounts++
					continue
				}
				appendAccount(normalizeAccountForStorage(toAccount(m)))
			}
		}
		for _, acc := range textAccounts {
			appendAccount(acc)
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	h.Pool.Reset()

	skippedKeys := submittedKeys - importedKeys
	if skippedKeys < 0 {
		skippedKeys = 0
	}
	skippedAccounts := duplicateAccounts + invalidAccounts

	resp := map[string]any{
		"success":            true,
		"imported_keys":      importedKeys,
		"imported_accounts":  importedAccounts,
		"submitted_keys":     submittedKeys,
		"submitted_accounts": submittedAccounts,
		"skipped_keys":       skippedKeys,
		"skipped_accounts":   skippedAccounts,
		"duplicate_accounts": duplicateAccounts,
		"invalid_accounts":   invalidAccounts,
	}
	if message := buildBatchImportMessage(submittedKeys, importedKeys, skippedKeys, submittedAccounts, importedAccounts, duplicateAccounts, invalidAccounts); message != "" {
		resp["message"] = message
	}
	writeJSON(w, http.StatusOK, resp)
}

func buildBatchImportMessage(submittedKeys, importedKeys, skippedKeys, submittedAccounts, importedAccounts, duplicateAccounts, invalidAccounts int) string {
	parts := make([]string, 0, 2)
	if submittedAccounts > 0 && importedAccounts == 0 {
		switch {
		case duplicateAccounts > 0 && invalidAccounts > 0:
			parts = append(parts, fmt.Sprintf("未导入任何账号：%d 个已存在，%d 个格式无效", duplicateAccounts, invalidAccounts))
		case duplicateAccounts > 0:
			parts = append(parts, fmt.Sprintf("未导入任何账号：%d 个账号均已存在", duplicateAccounts))
		case invalidAccounts > 0:
			parts = append(parts, fmt.Sprintf("未导入任何账号：%d 个账号缺少邮箱或手机号", invalidAccounts))
		default:
			parts = append(parts, "未导入任何账号")
		}
	}
	if submittedKeys > 0 && importedKeys == 0 {
		parts = append(parts, fmt.Sprintf("未导入任何 API Key：%d 个已存在", skippedKeys))
	}
	return strings.Join(parts, "；")
}

func parsePlainAccountLines(raw string) ([]config.Account, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	accounts := make([]config.Account, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		identifier, password, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("第 %d 行格式错误，应为 账号:密码", i+1)
		}
		identifier = strings.TrimSpace(identifier)
		password = strings.TrimSpace(password)
		if identifier == "" || password == "" {
			return nil, fmt.Errorf("第 %d 行账号或密码为空", i+1)
		}
		acc := config.Account{Password: password}
		if strings.Contains(identifier, "@") {
			acc.Email = identifier
		} else {
			acc.Mobile = config.NormalizeMobileForStorage(identifier)
			if acc.Mobile == "" {
				return nil, fmt.Errorf("第 %d 行手机号无效", i+1)
			}
		}
		accounts = append(accounts, normalizeAccountForStorage(acc))
	}
	return normalizeAndDedupeAccounts(accounts), nil
}
