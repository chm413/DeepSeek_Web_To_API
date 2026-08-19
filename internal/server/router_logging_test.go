package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"DeepSeek_Web_To_API/internal/requestmeta"
)

func TestSanitizeLoggedRequestRedactsCredentialsWithoutMutatingRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models?API_KEY=secret&apiKey=camel&client-secret=client&refresh_token=refresh&token=abc&limit=2", nil)
	clone := sanitizeLoggedRequest(req)
	if clone == req {
		t.Fatal("expected a request copy")
	}
	if got := clone.URL.Query().Get("API_KEY"); got != "[REDACTED]" {
		t.Fatalf("api key was not redacted: %q", got)
	}
	if got := clone.URL.Query().Get("token"); got != "[REDACTED]" {
		t.Fatalf("token was not redacted: %q", got)
	}
	for _, name := range []string{"apiKey", "client-secret", "refresh_token"} {
		if got := clone.URL.Query().Get(name); got != "[REDACTED]" {
			t.Fatalf("%s was not redacted: %q", name, got)
		}
	}
	if got := clone.URL.Query().Get("limit"); got != "2" {
		t.Fatalf("non-secret query changed: %q", got)
	}
	if got := req.URL.Query().Get("API_KEY"); got != "secret" {
		t.Fatalf("live request was mutated: %q", got)
	}

	pathReq := httptest.NewRequest(http.MethodDelete, "/admin/keys/my-secret-key", nil)
	pathClone := sanitizeLoggedRequest(pathReq)
	if got := pathClone.URL.Path; got != "/admin/keys/[REDACTED]" {
		t.Fatalf("admin key path was not redacted: %q", got)
	}
	if pathReq.URL.Path != "/admin/keys/my-secret-key" {
		t.Fatalf("live admin key path was mutated: %q", pathReq.URL.Path)
	}

	idReq := httptest.NewRequest(http.MethodDelete, "/admin/keys/id/key_deadbeef", nil)
	if got := sanitizeLoggedRequest(idReq).URL.Path; got != "/admin/keys/[REDACTED]" {
		t.Fatalf("opaque admin key path was not redacted: %q", got)
	}
}

func TestConfigureTrustedProxyCIDRsUsesExplicitCIDRsAndRejectsCatchAll(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_TRUSTED_PROXY_CIDRS", "10.42.0.0/16,0.0.0.0/0,not-a-cidr")
	configureTrustedProxyCIDRs()
	t.Cleanup(func() { requestmeta.SetTrustedProxyCIDRs(nil) })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "10.42.0.9:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.11, 10.42.0.1")
	if got := requestmeta.ClientIP(req); got != "198.51.100.11" {
		t.Fatalf("explicit proxy CIDR was not applied: %q", got)
	}

	untrusted := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	untrusted.RemoteAddr = "192.168.1.9:4567"
	untrusted.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := requestmeta.ClientIP(untrusted); got != "192.168.1.9" {
		t.Fatalf("private peer outside configured CIDR must not be trusted: %q", got)
	}
}
