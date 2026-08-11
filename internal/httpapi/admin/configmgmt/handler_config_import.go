package configmgmt

import (
	"encoding/json"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func (h *Handler) configImport(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid json"})
		return
	}

	mode := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = strings.TrimSpace(strings.ToLower(fieldString(req, "mode")))
	}
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "replace" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "mode must be merge or replace"})
		return
	}

	payload := req
	if raw, ok := req["config"].(map[string]any); ok && len(raw) > 0 {
		payload = raw
	}
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid config payload"})
		return
	}
	var incoming config.Config
	if err := json.Unmarshal(rawJSON, &incoming); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	incoming.ClearAccountTokens()

	hasPayloadKey := func(key string) bool {
		_, ok := payload[key]
		return ok
	}

	importedKeys, importedAccounts, importedProxies := 0, 0, 0
	err = h.Store.Update(func(c *config.Config) error {
		next := c.Clone()
		if mode == "replace" {
			next = incoming.Clone()
			next.Accounts = normalizeAndDedupeAccounts(next.Accounts)
			importedKeys = len(next.APIKeys)
			importedAccounts = len(next.Accounts)
			importedProxies = len(next.Proxies)
		} else {
			var changed int
			next.APIKeys, changed = mergeAPIKeysPreferStructured(next.APIKeys, incoming.APIKeys)
			importedKeys += changed

			existingAccounts := map[string]struct{}{}
			for _, acc := range next.Accounts {
				acc = normalizeAccountForStorage(acc)
				key := accountDedupeKey(acc)
				if key != "" {
					existingAccounts[key] = struct{}{}
				}
			}
			for _, acc := range incoming.Accounts {
				acc = normalizeAccountForStorage(acc)
				key := accountDedupeKey(acc)
				if key == "" {
					continue
				}
				if _, ok := existingAccounts[key]; ok {
					continue
				}
				existingAccounts[key] = struct{}{}
				next.Accounts = append(next.Accounts, acc)
				importedAccounts++
			}

			// Merge proxies by stable id; existing entries are preserved (do not
			// overwrite user-edited credentials), new ids are appended.
			existingProxies := map[string]struct{}{}
			for _, p := range next.Proxies {
				p = config.NormalizeProxy(p)
				if p.ID != "" {
					existingProxies[p.ID] = struct{}{}
				}
			}
			for _, p := range incoming.Proxies {
				p = config.NormalizeProxy(p)
				if p.ID == "" {
					continue
				}
				if _, ok := existingProxies[p.ID]; ok {
					continue
				}
				existingProxies[p.ID] = struct{}{}
				next.Proxies = append(next.Proxies, p)
				importedProxies++
			}

			if len(incoming.ModelAliases) > 0 {
				if next.ModelAliases == nil {
					next.ModelAliases = map[string]string{}
				}
				for k, v := range incoming.ModelAliases {
					next.ModelAliases[k] = v
				}
			}
			if incoming.Responses.StoreTTLSeconds > 0 {
				next.Responses.StoreTTLSeconds = incoming.Responses.StoreTTLSeconds
			}
			if strings.TrimSpace(incoming.Embeddings.Provider) != "" {
				next.Embeddings.Provider = incoming.Embeddings.Provider
			}
			if strings.TrimSpace(incoming.Admin.PasswordHash) != "" {
				next.Admin.PasswordHash = incoming.Admin.PasswordHash
			}
			if incoming.Admin.JWTExpireHours > 0 {
				next.Admin.JWTExpireHours = incoming.Admin.JWTExpireHours
			}
			if incoming.Admin.JWTValidAfterUnix > 0 {
				next.Admin.JWTValidAfterUnix = incoming.Admin.JWTValidAfterUnix
			}
			if incoming.Runtime.AccountMaxInflight > 0 {
				next.Runtime.AccountMaxInflight = incoming.Runtime.AccountMaxInflight
			}
			if incoming.Runtime.AccountMaxQueue > 0 {
				next.Runtime.AccountMaxQueue = incoming.Runtime.AccountMaxQueue
			}
			if incoming.Runtime.GlobalMaxInflight > 0 {
				next.Runtime.GlobalMaxInflight = incoming.Runtime.GlobalMaxInflight
			}
			if incoming.Runtime.TokenRefreshIntervalHours > 0 {
				next.Runtime.TokenRefreshIntervalHours = incoming.Runtime.TokenRefreshIntervalHours
			}
			// Whole-block sections: when payload provides the key, replace
			// the corresponding subtree wholesale. Use payload presence
			// (not Go zero-value) so that explicit empty lists/disables work.
			if hasPayloadKey("safety") {
				next.Safety = incoming.Safety
			}
			if hasPayloadKey("cache") {
				next.Cache = incoming.Cache
			}
			if hasPayloadKey("compat") {
				next.Compat = incoming.Compat
			}
			if hasPayloadKey("auto_delete") {
				next.AutoDelete = incoming.AutoDelete
			}
			if hasPayloadKey("history_split") {
				next.HistorySplit = incoming.HistorySplit
			}
			if hasPayloadKey("current_input_file") {
				next.CurrentInputFile = incoming.CurrentInputFile
			}
			if hasPayloadKey("thinking_injection") {
				next.ThinkingInjection = incoming.ThinkingInjection
			}
			if hasPayloadKey("proxy_core") {
				next.ProxyCore = incoming.ProxyCore
			}
			// Storage paths: when payload provides "storage" we replace the
			// whole subtree so newly added fields (e.g. token_usage_sqlite_path)
			// survive a round-trip through export → import. Operators who do
			// not want to overwrite local paths simply omit the key from the
			// payload they upload.
			if hasPayloadKey("storage") {
				next.Storage = incoming.Storage
			}
			// Server block: be selective. Port and BindAddr are deployment-
			// specific (e.g. one host listens on 127.0.0.1, another on
			// 0.0.0.0) and should never be cloned from another machine via
			// "merge". Logical knobs (log level, total timeout, auto-build
			// WebUI, static admin dir, remote file upload toggle) are safe
			// to apply when explicitly provided.
			if rawServer, ok := payload["server"].(map[string]any); ok {
				if _, present := rawServer["log_level"]; present {
					next.Server.LogLevel = incoming.Server.LogLevel
				}
				if _, present := rawServer["http_total_timeout_seconds"]; present {
					next.Server.HTTPTotalTimeoutSeconds = incoming.Server.HTTPTotalTimeoutSeconds
				}
				if _, present := rawServer["auto_build_webui"]; present {
					next.Server.AutoBuildWebUI = incoming.Server.AutoBuildWebUI
				}
				if _, present := rawServer["static_admin_dir"]; present {
					next.Server.StaticAdminDir = incoming.Server.StaticAdminDir
				}
				if _, present := rawServer["remote_file_upload_enabled"]; present {
					next.Server.RemoteFileUploadEnabled = incoming.Server.RemoteFileUploadEnabled
				}
			}
		}

		normalizeSettingsConfig(&next)
		if err := validateSettingsConfig(next); err != nil {
			return newRequestError(err.Error())
		}

		*c = next
		return nil
	})
	if err != nil {
		if detail, ok := requestErrorDetail(err); ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": detail})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}

	xrayproxy.Default().StopAll()
	h.Pool.Reset()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"mode":              mode,
		"imported_keys":     importedKeys,
		"imported_accounts": importedAccounts,
		"imported_proxies":  importedProxies,
		"message":           "config imported",
	})
}
