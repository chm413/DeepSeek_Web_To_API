package proxysubscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxSubscriptionBytes = 8 << 20

const maxSubscriptionRedirects = 5

var subscriptionURLPattern = regexp.MustCompile(`(?i)(?:https?|vless|vmess|hysteria2|hy2|ss)://[^\s"'<>]+`)
var subscriptionSecretPattern = regexp.MustCompile(`(?i)([?&](?:token|access_token|auth|password|passwd|pass|secret|key|api[_-]?key)=)[^&\s]+`)

type subscriptionResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Fetch downloads a subscription only when every URL involved in the request
// resolves to globally routable addresses. Validation is repeated at redirect
// time and immediately before dialing to reduce DNS rebinding/redirect SSRF
// exposure.
func Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := parseSubscriptionURL(rawURL)
	if err != nil {
		return nil, err
	}
	resolver := net.DefaultResolver
	if err := validateSubscriptionTarget(ctx, parsed, resolver); err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// Subscription URLs are administrator supplied. Do not inherit a process
		// HTTP(S)_PROXY value that could silently change the security boundary.
		Proxy:                 nil,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			return dialAllowedAddress(dialCtx, network, address, resolver, dialer)
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxSubscriptionRedirects {
				return errors.New("subscription redirect limit exceeded")
			}
			if err := validateSubscriptionTarget(req.Context(), req.URL, resolver); err != nil {
				return errors.New("subscription redirect target is not allowed")
			}
			return nil
		},
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("create subscription request failed")
	}
	req.Header.Set("User-Agent", "Clash.Meta/1.19 DeepSeek-Web-To-API")
	req.Header.Set("Accept", "text/plain, application/yaml, application/json, */*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("fetch subscription: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch subscription: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBytes+1))
	if err != nil {
		return nil, errors.New("read subscription response failed")
	}
	if len(body) > maxSubscriptionBytes {
		return nil, errors.New("subscription response exceeds 8 MiB")
	}
	return body, nil
}

func parseSubscriptionURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Host == "" {
		return nil, errors.New("subscription URL must use http or https")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("subscription URL must use http or https")
	}
	// Credentials are accepted for compatibility, but never included in errors
	// or logs. An empty host is rejected above.
	parsed.Scheme = scheme
	return parsed, nil
}

func validateSubscriptionTarget(ctx context.Context, parsed *url.URL, resolver subscriptionResolver) error {
	if parsed == nil {
		return errors.New("subscription redirect target is not allowed")
	}
	if _, err := parseSubscriptionURL(parsed.String()); err != nil {
		return err
	}
	if _, err := resolveAllowedAddresses(ctx, parsed.Hostname(), resolver); err != nil {
		return err
	}
	return nil
}

func resolveAllowedAddresses(ctx context.Context, host string, resolver subscriptionResolver) ([]netip.Addr, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" || strings.Contains(host, "%") {
		return nil, errors.New("subscription URL host is not allowed")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if !allowedSubscriptionAddress(literal) {
			return nil, errors.New("subscription URL host resolves to a private or non-global address")
		}
		return []netip.Addr{literal}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("subscription URL host could not be resolved")
	}
	allowed := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !allowedSubscriptionAddress(address) {
			// Reject a mixed public/private answer as a whole. Otherwise an
			// attacker can win the dial race with the private address.
			return nil, errors.New("subscription URL host resolves to a private or non-global address")
		}
		allowed = append(allowed, address)
	}
	return allowed, nil
}

func allowedSubscriptionAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, reserved := range []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),   // shared address space
		netip.MustParsePrefix("192.0.0.0/24"),    // protocol assignments
		netip.MustParsePrefix("192.0.2.0/24"),    // documentation
		netip.MustParsePrefix("198.18.0.0/15"),   // benchmark/test
		netip.MustParsePrefix("198.51.100.0/24"), // documentation
		netip.MustParsePrefix("203.0.113.0/24"),  // documentation
		netip.MustParsePrefix("2001:db8::/32"),   // IPv6 documentation
	} {
		if reserved.Contains(address) {
			return false
		}
	}
	return true
}

func dialAllowedAddress(ctx context.Context, network, address string, resolver subscriptionResolver, dialer *net.Dialer) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("subscription connection target is not allowed")
	}
	addresses, err := resolveAllowedAddresses(ctx, host, resolver)
	if err != nil {
		return nil, err
	}
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 15 * time.Second}
	}
	var lastErr error
	for _, resolved := range addresses {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, errors.New("subscription connection failed")
	}
	return nil, errors.New("subscription connection target is not allowed")
}

// SanitizeError removes administrator-supplied URLs and credentials from
// errors persisted in the configuration or returned by the admin API.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "subscription request failed"
	}
	value := subscriptionURLPattern.ReplaceAllString(err.Error(), "<redacted-url>")
	value = subscriptionSecretPattern.ReplaceAllString(value, `${1}<redacted>`)
	if strings.TrimSpace(value) == "" {
		return "subscription request failed"
	}
	return value
}
