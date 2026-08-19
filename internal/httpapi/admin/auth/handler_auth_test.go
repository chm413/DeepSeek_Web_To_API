package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

func newLoginTestHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"admin":{"key":"test-admin-key","jwt_secret":"test-jwt-secret"}}`)
	return &Handler{Store: config.LoadStore()}
}

func loginRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"admin_key":"`+key+`"}`))
	req.RemoteAddr = "198.51.100.10:12345"
	return req
}

func TestLoginRejectsOversizedBody(t *testing.T) {
	h := newLoginTestHandler(t)
	body := `{"admin_key":"` + strings.Repeat("a", maxAdminLoginBodyBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString(body))
	req.RemoteAddr = "198.51.100.10:12345"
	rec := httptest.NewRecorder()

	h.login(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginRejectsTrailingJSON(t *testing.T) {
	h := newLoginTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"admin_key":"test-admin-key"}{"admin_key":"ignored"}`))
	req.RemoteAddr = "198.51.100.11:12345"
	rec := httptest.NewRecorder()

	h.login(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginFailureBackoffUsesIPAndCredentialBuckets(t *testing.T) {
	h := newLoginTestHandler(t)
	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	h.loginLimiter = &loginLimiter{
		entries: make(map[string]loginLimitState),
		now:     func() time.Time { return now },
	}

	for i, key := range []string{"bad-one", "bad-two", "bad-three"} {
		rec := httptest.NewRecorder()
		h.login(rec, loginRequest(key))
		if i < 2 && rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
		if i == 2 {
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("third failed attempt should start backoff, got %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Retry-After") != "1" {
				t.Fatalf("retry-after=%q, want 1", rec.Header().Get("Retry-After"))
			}
		}
	}

	blocked := httptest.NewRecorder()
	h.login(blocked, loginRequest("test-admin-key"))
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("expected IP backoff to block another credential, got %d", blocked.Code)
	}

	now = now.Add(loginBackoffBase)
	success := httptest.NewRecorder()
	h.login(success, loginRequest("test-admin-key"))
	if success.Code != http.StatusOK {
		t.Fatalf("expected valid login after backoff expiry, got %d body=%s", success.Code, success.Body.String())
	}
	if wait := h.loginLimiter.check("198.51.100.10", "test-admin-key"); wait != 0 {
		t.Fatalf("successful login should clear limiter state, wait=%s", wait)
	}
}

func TestLoginLimiterBacksOffRepeatedCredentialAcrossIPs(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	l := &loginLimiter{entries: make(map[string]loginLimitState), now: func() time.Time { return now }}
	for i := 0; i < loginFailureThreshold-1; i++ {
		if wait := l.failure("203.0.113.1", "bad-key"); wait != 0 {
			t.Fatalf("failure %d wait=%s, want no backoff yet", i+1, wait)
		}
	}
	if wait := l.failure("203.0.113.1", "bad-key"); wait != loginBackoffBase {
		t.Fatalf("first backoff wait=%s, want %s", wait, loginBackoffBase)
	}
	if wait := l.check("203.0.113.2", "bad-key"); wait != loginBackoffBase {
		t.Fatalf("credential bucket must survive IP rotation, wait=%s", wait)
	}
}

func TestLoginLimiterDoesNotGloballyBlockMalformedEmptyCredentials(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	l := &loginLimiter{entries: make(map[string]loginLimitState), now: func() time.Time { return now }}
	for i := 0; i < loginFailureThreshold; i++ {
		l.failure("203.0.113."+string(rune('1'+i)), "")
	}
	if wait := l.check("203.0.113.99", ""); wait != 0 {
		t.Fatalf("empty credential failures should remain IP scoped, wait=%s", wait)
	}
}
