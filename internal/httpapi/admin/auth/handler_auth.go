package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	authn "DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/requestmeta"
)

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := authn.VerifyAdminRequestWithStore(r, h.Store); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": err.Error()})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	limiter := h.getLoginLimiter()
	clientIP := requestmeta.ClientIP(r)
	if wait := limiter.check(clientIP, ""); wait > 0 {
		writeLoginRateLimited(w, wait)
		return
	}
	if r.Body == nil {
		wait := limiter.failure(clientIP, "")
		if wait > 0 {
			writeLoginRateLimited(w, wait)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminLoginBodyBytes)
	var req map[string]any
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		wait := limiter.failure(clientIP, "")
		if wait > 0 {
			writeLoginRateLimited(w, wait)
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "login request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		wait := limiter.failure(clientIP, "")
		if wait > 0 {
			writeLoginRateLimited(w, wait)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid request body"})
		return
	}
	adminKey, _ := req["admin_key"].(string)
	if wait := limiter.check(clientIP, adminKey); wait > 0 {
		writeLoginRateLimited(w, wait)
		return
	}
	expireHours := intFrom(req["expire_hours"])
	if !authn.VerifyAdminCredential(adminKey, h.Store) {
		wait := limiter.failure(clientIP, adminKey)
		if wait > 0 {
			writeLoginRateLimited(w, wait)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Invalid admin key"})
		return
	}
	limiter.success(clientIP, adminKey)
	token, err := authn.CreateJWTWithStore(expireHours, h.Store)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if expireHours <= 0 {
		expireHours = h.Store.AdminJWTExpireHours()
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "token": token, "expires_in": expireHours * 3600})
}

func writeLoginRateLimited(w http.ResponseWriter, wait time.Duration) {
	seconds := int64((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"detail": "too many login attempts; retry later"})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "No credentials provided"})
		return
	}
	token := strings.TrimSpace(header[7:])
	payload, err := authn.VerifyJWTWithStore(token, h.Store)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": err.Error()})
		return
	}
	exp, _ := payload["exp"].(float64)
	remaining := int64(exp) - time.Now().Unix()
	if remaining < 0 {
		remaining = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "expires_at": int64(exp), "remaining_seconds": remaining})
}
