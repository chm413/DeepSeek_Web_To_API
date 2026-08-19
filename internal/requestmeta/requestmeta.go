package requestmeta

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
)

var ConversationIDHeaders = []string{
	"X-DeepSeek-Web-To-API-Conversation-ID",
	"X-Ds2-Conversation-ID",
	"X-Conversation-ID",
	"Conversation-ID",
	"X-Codex-Conversation-ID",
	"X-Codex-Session-ID",
	"X-OpenCode-Conversation-ID",
	"X-OpenCode-Session-ID",
	"OpenAI-Conversation-ID",
	"Anthropic-Conversation-ID",
	// OpenClaw client family (adopted from cnb openclaw-tunning d8e209c).
	// Without these the client's per-turn request body still hashes
	// differently each turn (volatile metadata fields), so prefix-reuse
	// cannot fire — header pinning is the cheap stable path.
	"X-OpenClaw-Session-Id",
	"X-Client-Request-Id",
	"X-Session-Affinity",
	"Session-Id",
}

var conversationIDBodyKeys = map[string]struct{}{
	"conversation_id": {},
	"conversationid":  {},
	"chat_id":         {},
	"chatid":          {},
	"thread_id":       {},
	"threadid":        {},
	"session_id":      {},
	"sessionid":       {},
	"parent_id":       {},
	"parentid":        {},
	// OpenClaw body metadata (under `metadata.*`); our recursive walker
	// already descends into `metadata`, only the key names need adding.
	"openclaw_session_id": {},
	"openclawsessionid":   {},
}

type Metadata struct {
	ClientIP       string
	ConversationID string
}

// defaultTrustedProxyCIDRs accepts only loopback peers. A private address is
// not proof that the immediate peer is a reverse proxy: on a LAN, VPN, or
// Docker bridge an untrusted client can connect from RFC1918 space and forge
// forwarding headers. Same-host Caddy / Nginx deployments continue to work
// because their connection appears as 127.0.0.1 or ::1. Operators using a
// reverse proxy elsewhere must opt in with SetTrustedProxyCIDRs at startup;
// the server wires DEEPSEEK_WEB_TO_API_TRUSTED_PROXY_CIDRS to that setter.
//
// Forwarding headers are ignored for any other peer, and the connection
// address remains the source of truth.
// — that's the deployment shape documented in docs/deployment.md.
var defaultTrustedProxyCIDRs = []string{
	"127.0.0.0/8",
	"::1/128",
}

var trustedProxyNets = parseTrustedNets(defaultTrustedProxyCIDRs)

// SetTrustedProxyCIDRs replaces the trusted-proxy set used by ClientIP.
// Empty input restores the defaults. Invalid CIDRs are skipped silently.
// Intended for one-shot wiring at server startup, not concurrent calls.
func SetTrustedProxyCIDRs(cidrs []string) {
	if len(cidrs) == 0 {
		trustedProxyNets = parseTrustedNets(defaultTrustedProxyCIDRs)
		return
	}
	trustedProxyNets = parseTrustedNets(cidrs)
}

func parseTrustedNets(raw []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(raw))
	for _, c := range raw {
		ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil || ipnet == nil || ip == nil {
			continue
		}
		ones, bits := ipnet.Mask.Size()
		// A catch-all proxy range would re-enable header spoofing for every
		// direct client. Require a non-zero, well-formed prefix even when this
		// setter is called outside the server bootstrap path.
		if ones <= 0 || bits <= 0 || ones > bits {
			continue
		}
		nets = append(nets, ipnet)
	}
	return nets
}

func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range trustedProxyNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP resolves the originating client IP using a "trusted-proxy
// chain" model: client-supplied headers (X-Forwarded-For, X-Real-IP,
// CF-Connecting-IP) are honored ONLY when the immediate TCP peer is in
// the explicitly trusted-proxy set (loopback by default). When the
// peer is internet-facing, those headers are ignored and the peer
// address is returned verbatim. This blocks the trivial XFF spoof in
// which an attacker sends `X-Forwarded-For: <admin-allowlisted-ip>` to
// bypass IP blocklists or auto-ban escalation.
//
// For X-Forwarded-For specifically the RIGHTMOST untrusted entry is
// returned (the hop closest to the trusted proxy chain), not the
// leftmost (which is attacker-controlled when XFF is appended hop-by-hop).
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	peerIP := parsePeerIP(r.RemoteAddr)
	if !isTrustedProxy(peerIP) {
		if peerIP != nil {
			return peerIP.String()
		}
		return cleanIP(r.RemoteAddr)
	}
	// Trusted peer: prefer well-known single-value headers first, then
	// XFF rightmost-untrusted.
	for _, header := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		if v := strings.TrimSpace(r.Header.Get(header)); v != "" {
			if ip := cleanIP(v); ip != "" {
				return ip
			}
		}
	}
	for _, raw := range r.Header.Values("X-Forwarded-For") {
		parts := strings.Split(raw, ",")
		// Walk right-to-left for the closest hop NOT itself a trusted
		// proxy. That is the IP that delivered the request to the
		// trusted edge — i.e., the actual public client.
		for i := len(parts) - 1; i >= 0; i-- {
			ip := cleanIP(parts[i])
			if ip == "" {
				continue
			}
			parsed := net.ParseIP(ip)
			if parsed == nil {
				continue
			}
			if !isTrustedProxy(parsed) {
				return ip
			}
		}
		// All hops were trusted; treat the leftmost as the originating
		// public client (operator presumably configured trust to
		// extend that far).
		for _, p := range parts {
			if ip := cleanIP(p); ip != "" {
				return ip
			}
		}
	}
	if peerIP != nil {
		return peerIP.String()
	}
	return cleanIP(r.RemoteAddr)
}

func parsePeerIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	host = strings.Trim(host, "[]")
	return net.ParseIP(host)
}

func ConversationID(r *http.Request, body []byte) string {
	if r != nil {
		for _, header := range ConversationIDHeaders {
			if id := normalizeID(r.Header.Get(header)); id != "" {
				return id
			}
		}
	}
	if len(body) == 0 {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ""
	}
	return conversationIDFromJSON(value, 0)
}

func cleanIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	ip := net.ParseIP(raw)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func conversationIDFromJSON(value any, depth int) string {
	if depth > 3 {
		return ""
	}
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if _, ok := conversationIDBodyKeys[canonicalIDKey(key)]; ok {
				if id := normalizeID(item); id != "" {
					return id
				}
			}
		}
		for _, key := range []string{"metadata", "conversation", "session", "thread"} {
			if nested, ok := v[key]; ok {
				if id := conversationIDFromJSON(nested, depth+1); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func canonicalIDKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	return strings.ToLower(key)
}

func normalizeID(value any) string {
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case json.Number:
		raw = v.String()
	default:
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 256 {
		return ""
	}
	for _, r := range raw {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return raw
}
