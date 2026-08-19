package proxysubscription

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

type staticSubscriptionResolver struct {
	addresses []netip.Addr
	err       error
}

func (r staticSubscriptionResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, r.err
}

func TestFetchRejectsPrivateAndLoopbackTargetsBeforeRequest(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	for _, rawURL := range []string{
		server.URL,
		"http://127.0.0.1:8080/sub?token=secret-token",
		"http://[::1]:8080/sub",
		"http://localhost:8080/sub",
	} {
		_, err := Fetch(context.Background(), rawURL)
		if err == nil {
			t.Fatalf("Fetch(%q) unexpectedly succeeded", rawURL)
		}
		if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), rawURL) {
			t.Fatalf("Fetch(%q) leaked URL in error: %v", rawURL, err)
		}
	}
}

func TestFetchRejectsUnsupportedAndMalformedURLs(t *testing.T) {
	for _, rawURL := range []string{"ftp://example.com/sub", "://bad", "http:///missing-host"} {
		_, err := Fetch(context.Background(), rawURL)
		if err == nil {
			t.Fatalf("Fetch(%q) unexpectedly succeeded", rawURL)
		}
	}
}

func TestAllowedSubscriptionAddressRejectsReservedRanges(t *testing.T) {
	tests := map[string]bool{
		"8.8.8.8":              true,
		"127.0.0.1":            false,
		"10.0.0.1":             false,
		"100.64.0.1":           false,
		"192.0.2.1":            false,
		"198.18.0.1":           false,
		"203.0.113.10":         false,
		"2001:db8::1":          false,
		"2001:4860:4860::8888": true,
	}
	for raw, want := range tests {
		address := netip.MustParseAddr(raw)
		if got := allowedSubscriptionAddress(address); got != want {
			t.Errorf("allowedSubscriptionAddress(%s)=%v, want %v", raw, got, want)
		}
	}
}

func TestSanitizeErrorRedactsSubscriptionURLAndCredentials(t *testing.T) {
	rawURL := "https://user:secret@example.com/list?token=private-token#fragment"
	for _, err := range []error{
		errors.New("fetch subscription: " + rawURL),
		&url.Error{Op: "Get", URL: rawURL, Err: errors.New("connection refused")},
		errors.New("invalid node vless://uuid:password@example.com:443?security=tls&auth=node-secret"),
		errors.New("request failed?password=another-secret"),
	} {
		got := SanitizeError(err)
		if strings.Contains(got, "secret") || strings.Contains(got, "private-token") || strings.Contains(got, "example.com") {
			t.Fatalf("sanitized error leaked URL data: %q", got)
		}
	}
}

func TestResolveAllowedAddressesRejectsPrivateLiteral(t *testing.T) {
	if _, err := resolveAllowedAddresses(context.Background(), "127.0.0.1", nil); err == nil {
		t.Fatal("expected private literal to be rejected")
	}
}

func TestResolveAllowedAddressesRejectsMixedPrivateDNSAnswer(t *testing.T) {
	resolver := staticSubscriptionResolver{addresses: []netip.Addr{
		netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("10.0.0.1"),
	}}
	if _, err := resolveAllowedAddresses(context.Background(), "mixed.example", resolver); err == nil {
		t.Fatal("expected mixed public/private DNS answer to be rejected")
	}
}

func TestValidateSubscriptionTargetRejectsPrivateRedirectTarget(t *testing.T) {
	redirectTarget, err := url.Parse("http://169.254.169.254/latest/meta-data")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSubscriptionTarget(context.Background(), redirectTarget, nil); err == nil {
		t.Fatal("expected private redirect target to be rejected")
	}
}
