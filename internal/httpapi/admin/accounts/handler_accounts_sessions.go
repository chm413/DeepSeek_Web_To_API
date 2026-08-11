package accounts

import (
	"encoding/json"
	"net/http"
	"strings"

	authn "DeepSeek_Web_To_API/internal/auth"
)

func (h *Handler) deleteAllSessions(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	identifier, _ := req["identifier"].(string)
	if strings.TrimSpace(identifier) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "需要账号标识（identifier / email / mobile）"})
		return
	}
	acc, ok := findAccountByIdentifier(h.Store, identifier)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}

	authCtx := &authn.RequestAuth{UseConfigToken: true, AccountID: acc.Identifier(), Account: acc}
	proxyCtx := authn.WithAuth(r.Context(), authCtx)
	token, err := h.DS.Login(proxyCtx, acc)
	if err != nil {
		result := map[string]any{"success": false, "message": "登录失败: " + err.Error()}
		h.recordAccountHealth(identifier, err, result)
		writeJSON(w, http.StatusOK, result)
		return
	}
	_ = h.Store.UpdateAccountToken(acc.Identifier(), token)
	authCtx.DeepSeekToken = token

	err = h.DS.DeleteAllSessionsForToken(proxyCtx, token)
	if err != nil {
		newToken, loginErr := h.DS.Login(proxyCtx, acc)
		if loginErr != nil {
			result := map[string]any{"success": false, "message": "Token 重试刷新失败: " + loginErr.Error(), "delete_error": err.Error()}
			h.recordAccountHealth(identifier, loginErr, result)
			writeJSON(w, http.StatusOK, result)
			return
		}
		token = newToken
		_ = h.Store.UpdateAccountToken(acc.Identifier(), token)
		authCtx.DeepSeekToken = token
		if retryErr := h.DS.DeleteAllSessionsForToken(proxyCtx, token); retryErr != nil {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "message": "删除失败: " + retryErr.Error()})
			return
		}
	}

	_ = h.Store.UpdateAccountSessionCount(identifier, 0)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "删除成功"})
}
