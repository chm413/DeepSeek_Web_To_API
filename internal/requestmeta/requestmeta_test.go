package requestmeta

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConversationIDUsesToolchainHeaders(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Codex-Session-ID", "codex-session-1")

	if got := ConversationID(req, nil); got != "codex-session-1" {
		t.Fatalf("conversation id=%q", got)
	}
}

func TestConversationIDFallsBackToBodyMetadata(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := []byte(`{"metadata":{"conversation_id":"conv-body-1"},"input":"hello"}`)

	if got := ConversationID(req, body); got != "conv-body-1" {
		t.Fatalf("conversation id=%q", got)
	}
}

func TestClientIPNormalizesRemoteAddr(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "203.0.113.10:4567"

	if got := ClientIP(req); got != "203.0.113.10" {
		t.Fatalf("client ip=%q", got)
	}
}

func TestClientIPPrefersForwardedHeader(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.11, 127.0.0.2")

	if got := ClientIP(req); got != "198.51.100.11" {
		t.Fatalf("client ip=%q", got)
	}
}

// TestClientIPIgnoresForgedHeadersFromUntrustedPeer pins the
// trusted-proxy hardening: when the immediate TCP peer is a public
// internet address (i.e., NOT in the trusted-proxy CIDRs) we MUST
// ignore X-Forwarded-For / X-Real-IP / CF-Connecting-IP and return
// the actual peer. Without this an attacker can send
// `X-Forwarded-For: 8.8.8.8` to bypass IP blocklists or to forge
// allowlist matches that exempt them from auto-ban escalation.
func TestClientIPIgnoresForgedHeadersFromUntrustedPeer(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "203.0.113.10:4567"
	req.Header.Set("X-Forwarded-For", "192.0.2.99, 8.8.8.8")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "5.6.7.8")

	if got := ClientIP(req); got != "203.0.113.10" {
		t.Fatalf("forged headers must be ignored from untrusted peer; got %q want %q", got, "203.0.113.10")
	}
}

// TestClientIPUsesRightmostUntrustedXFFEntry verifies the leftmost-XFF
// spoof fix: when multiple hops appear we must return the closest
// non-trusted entry, NOT the leftmost (which is attacker-controlled
// when XFF is appended hop-by-hop).
func TestClientIPUsesRightmostUntrustedXFFEntry(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	// Attacker prepends a forged "8.8.8.8" to the chain. The real
	// public client is 198.51.100.11; 127.0.0.2 is the trusted reverse
	// proxy that delivered the request to us.
	req.Header.Set("X-Forwarded-For", "8.8.8.8, 198.51.100.11, 127.0.0.2")

	if got := ClientIP(req); got != "198.51.100.11" {
		t.Fatalf("rightmost-untrusted XFF expected, got %q", got)
	}
}

func TestClientIPIgnoresForwardingHeadersFromPrivatePeerByDefault(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "10.42.0.9:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.11")
	req.Header.Set("X-Real-IP", "203.0.113.42")

	if got := ClientIP(req); got != "10.42.0.9" {
		t.Fatalf("private peer forwarding headers must not be trusted by default; got %q", got)
	}
}

func TestClientIPUsesExplicitlyTrustedPrivateProxy(t *testing.T) {
	// trusted proxy configuration is process-global and production configures
	// it once at startup before serving requests.
	SetTrustedProxyCIDRs([]string{"10.42.0.0/16"})
	t.Cleanup(func() { SetTrustedProxyCIDRs(nil) })

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "10.42.0.9:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.11, 10.42.0.1")

	if got := ClientIP(req); got != "198.51.100.11" {
		t.Fatalf("explicitly trusted private proxy was not honored; got %q", got)
	}
}

func TestSetTrustedProxyCIDRsRejectsCatchAll(t *testing.T) {
	SetTrustedProxyCIDRs([]string{"0.0.0.0/0"})
	t.Cleanup(func() { SetTrustedProxyCIDRs(nil) })
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.168.1.9:4567"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := ClientIP(req); got != "192.168.1.9" {
		t.Fatalf("catch-all trusted proxy range must be ignored, got %q", got)
	}
}
