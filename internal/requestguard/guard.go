package requestguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/requestmeta"
	"DeepSeek_Web_To_API/internal/responsecache"
	"DeepSeek_Web_To_API/internal/safetystore"
)

const (
	defaultBlockMessage = "request blocked by safety policy"
	maxScanBodyBytes    = 64 << 20
	maxCollectedText    = 512 << 10
)

type contextKey string

const metadataContextKey contextKey = "request_guard_metadata"

type Options struct {
	Store       *config.Store
	ChatHistory *chathistory.Store
	// SafetyWords / SafetyIPs are the dedicated SQLite stores for the
	// safety-policy lists. When wired they are the source of truth; the
	// legacy config.SafetyConfig list fields still take effect as a
	// fallback for setups that have not yet migrated.
	SafetyWords *safetystore.WordsStore
	SafetyIPs   *safetystore.IPsStore
}

type decision struct {
	blocked bool
	code    string
	detail  string
}

type policy struct {
	enabled                bool
	blockMessage           string
	blockedIPs             []ipMatcher
	blockedConversationIDs map[string]struct{}
}

type ipMatcher struct {
	raw  string
	ip   net.IP
	cidr *net.IPNet
}

type policyCache struct {
	store       *config.Store
	safetyWords *safetystore.WordsStore
	safetyIPs   *safetystore.IPsStore
	mu          sync.Mutex
	signature   string
	cached      policy
}

// autoBanTracker counts safety/banned-word violations per client IP and
// trips a permanent block when an IP repeats violations within the
// sliding window. Tripped IPs are appended to the safety_ips blocked_ips
// table via SafetyIPs.AddBlockedIP, so the next request from that IP is
// stopped at the IP-blocked check before any content scan runs.
type autoBanTracker struct {
	mu        sync.Mutex
	offenders map[string]*offenderRecord
	safetyIPs *safetystore.IPsStore
	policies  *policyCache
}

type offenderRecord struct {
	count     int
	firstSeen time.Time
	banned    bool
}

func newAutoBanTracker(safetyIPs *safetystore.IPsStore, policies *policyCache) *autoBanTracker {
	return &autoBanTracker{
		offenders: map[string]*offenderRecord{},
		safetyIPs: safetyIPs,
		policies:  policies,
	}
}

// maxOffenderRecords caps the in-memory offender map. When the map grows
// beyond this many entries we sweep expired records inline; this protects
// against scanner-farm-style attacks that flood unique source IPs to
// exhaust memory. The cap is generous — typical workloads sit well below
// 1000 simultaneous offenders.
const maxOffenderRecords = 50000

// note records a violation for the given IP. Returns true when this
// violation crosses the threshold and the IP has just been banned. The
// (threshold, window) come from the active SafetyConfig — defaults are
// applied when zero. An empty IP is a no-op (we cannot ban "unknown").
//
// Lock discipline: the offender map is mutated under t.mu only. The
// allowlist check and the SQLite write run WITHOUT holding t.mu so the
// content-scan hot path is not gated on disk I/O. The banned flag is
// flipped only after a successful AddBlockedIP write, so a transient
// SQLite error does not silently exempt the offender forever.
func (t *autoBanTracker) note(ip string, cfg config.SafetyAutoBanConfig, now time.Time) (justBanned bool) {
	if t == nil {
		return false
	}
	enabled := true
	if cfg.Enabled != nil {
		enabled = *cfg.Enabled
	}
	if !enabled {
		return false
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return false
	}
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = 3
	}
	windowSeconds := cfg.WindowSeconds
	if windowSeconds <= 0 {
		windowSeconds = 600
	}
	window := time.Duration(windowSeconds) * time.Second

	t.mu.Lock()
	if len(t.offenders) > maxOffenderRecords {
		t.sweepLocked(window, now)
	}
	rec, ok := t.offenders[ip]
	if !ok || now.Sub(rec.firstSeen) > window {
		t.offenders[ip] = &offenderRecord{count: 1, firstSeen: now}
		t.mu.Unlock()
		return false
	}
	if rec.banned {
		t.mu.Unlock()
		return false
	}
	rec.count++
	crossed := rec.count >= threshold
	currentCount := rec.count
	t.mu.Unlock()

	if !crossed {
		return false
	}

	// Allowlist check + SQLite write happen OUTSIDE t.mu so the content
	// scan hot path does not block on disk I/O.
	if t.isAllowlisted(ip) {
		return false
	}
	if t.safetyIPs != nil {
		if err := t.safetyIPs.AddBlockedIP(ip); err != nil {
			config.Logger.Warn("[request_guard] auto-ban write failed", "ip", ip, "error", err)
			return false
		}
	}

	// Re-acquire mutex to flip rec.banned only AFTER a successful write.
	// If the record was evicted while we were running the disk write, the
	// next violation will simply re-trip the threshold — no harm done.
	t.mu.Lock()
	if rec, ok := t.offenders[ip]; ok {
		rec.banned = true
	}
	t.mu.Unlock()

	// Bust the policy cache so the next request rebuilds the blocked-ip
	// matcher set with the freshly-added entry.
	if t.policies != nil {
		t.policies.mu.Lock()
		t.policies.signature = ""
		t.policies.mu.Unlock()
	}
	config.Logger.Warn("[request_guard] auto-banned IP after repeated violations",
		"ip", ip, "count", currentCount, "threshold", threshold, "window_seconds", windowSeconds)
	return true
}

// isAllowlisted returns true when the IP appears (verbatim or via a CIDR
// match) in safety_ips.allowed_ips. Runs WITHOUT holding t.mu — querying
// SQLite under the tracker mutex would inject DB latency into every
// content-block decision and create a fragile lock-ordering dependency.
// Freshness wins over caching here: admin-side allowlist edits take
// effect immediately, and auto-ban escalation is rare enough that the
// per-trip read cost is negligible.
func (t *autoBanTracker) isAllowlisted(ip string) bool {
	if t == nil || t.safetyIPs == nil {
		return false
	}
	allowed, err := t.safetyIPs.AllowedIPs()
	if err != nil || len(allowed) == 0 {
		return false
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, raw := range allowed {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if literal := net.ParseIP(raw); literal != nil && literal.Equal(parsed) {
			return true
		}
		if _, cidr, err := net.ParseCIDR(raw); err == nil && cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// sweepLocked drops offender records older than the window so the map
// does not grow unbounded. Caller MUST hold t.mu. Banned records are
// preserved (their persistent ban already lives in safety_ips.blocked_ips
// — keeping them here is just bookkeeping for log dedup).
func (t *autoBanTracker) sweepLocked(window time.Duration, now time.Time) {
	if t == nil {
		return
	}
	for ip, rec := range t.offenders {
		if rec.banned {
			continue
		}
		if now.Sub(rec.firstSeen) > window {
			delete(t.offenders, ip)
		}
	}
}

func Middleware(opts Options) func(http.Handler) http.Handler {
	policies := &policyCache{store: opts.Store, safetyWords: opts.SafetyWords, safetyIPs: opts.SafetyIPs}
	tracker := newAutoBanTracker(opts.SafetyIPs, policies)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawBody := []byte(nil)
			bodyAvailable := requestBodyShouldBeRead(r)
			if bodyAvailable {
				body, ok := readAndRestoreBody(w, r)
				if !ok {
					return
				}
				rawBody = body
			}

			meta := requestmeta.Metadata{
				ClientIP:       requestmeta.ClientIP(r),
				ConversationID: requestmeta.ConversationID(r, rawBody),
			}
			r = r.WithContext(context.WithValue(r.Context(), metadataContextKey, meta))

			p := policies.load()
			if d := p.evaluate(r, rawBody, meta); d.blocked {
				if shouldCountAutoBan(d.code) {
					autoBanCfg := config.SafetyAutoBanConfig{}
					if opts.Store != nil {
						autoBanCfg = opts.Store.SafetyConfig().AutoBan
					}
					// note() returns true when this violation tipped
					// the IP over the threshold and the ban committed
					// to SQLite. The tracker already emits a WARN log
					// on commit and the new row appears in
					// safety_ips.blocked_ips, so the audit-trail event
					// is fully captured without additional plumbing
					// here. Discarding the return is intentional.
					_ = tracker.note(meta.ClientIP, autoBanCfg, time.Now())
				}
				recordBlockedHistory(opts.ChatHistory, rawBody, meta, d)
				writeBlocked(w, p.blockMessage, d)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// shouldCountAutoBan returns true for decision codes that correspond to a
// content-side violation an attacker can deliberately trigger (LLM
// safety verdict in v1.0.14+, plus the legacy substring/regex/jailbreak
// codes for already-recorded historical events). IP-blocked /
// conversation-blocked outcomes are not counted: those are already
// terminal and do not need an auto-ban escalation.
func shouldCountAutoBan(code string) bool {
	switch code {
	case "llm_safety_blocked",
		"content_blocked", "content_regex_blocked", "jailbreak_blocked":
		return true
	}
	return false
}

func FromContext(ctx context.Context) (requestmeta.Metadata, bool) {
	if ctx == nil {
		return requestmeta.Metadata{}, false
	}
	meta, ok := ctx.Value(metadataContextKey).(requestmeta.Metadata)
	return meta, ok
}

func requestBodyShouldBeRead(r *http.Request) bool {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return false
	}
	// Administrative payloads are not part of the public content-safety scan.
	// Skipping the eager 64 MiB copy here also ensures route-specific limits in
	// requestbody/auth are applied before an attacker can duplicate a large
	// login or config-import body in this middleware.
	if r.URL != nil && strings.HasPrefix(strings.TrimSpace(r.URL.Path), "/admin/") {
		return false
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/") || strings.HasPrefix(contentType, "application/octet-stream") {
		return false
	}
	return responsecache.CacheableRequest(r) || strings.Contains(contentType, "json") || strings.Contains(contentType, "+json")
}

func readAndRestoreBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxScanBodyBytes+1))
	if closeErr := r.Body.Close(); closeErr != nil {
		config.Logger.Warn("[request_guard] close request body failed", "error", closeErr)
	}
	if err != nil {
		http.Error(w, "read request body failed", http.StatusBadRequest)
		return nil, false
	}
	if len(rawBody) > maxScanBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(rawBody))
	return rawBody, true
}

func (c *policyCache) load() policy {
	cfg := config.SafetyConfig{}
	if c != nil && c.store != nil {
		cfg = c.store.SafetyConfig()
	}
	cfg = mergeSafetySources(cfg, c)
	signature := safetyConfigSignature(cfg)
	if c != nil {
		c.mu.Lock()
		if signature == c.signature {
			p := c.cached
			c.mu.Unlock()
			return p
		}
		p := buildPolicy(cfg)
		c.signature = signature
		c.cached = p
		c.mu.Unlock()
		return p
	}
	return buildPolicy(cfg)
}

// mergeSafetySources combines the legacy config.SafetyConfig lists with the
// dedicated SQLite stores. SQLite values are appended to the config-supplied
// values so an upgrade is non-destructive: data that lives in either source
// (or both) reaches the policy. Duplicates are removed in buildPolicy via
// the existing dedupe-by-string normalization.
func mergeSafetySources(cfg config.SafetyConfig, c *policyCache) config.SafetyConfig {
	if c == nil {
		return cfg
	}
	if c.safetyWords != nil {
		if content, regex, jail, err := c.safetyWords.Snapshot(); err == nil {
			cfg.BannedContent = appendUnique(cfg.BannedContent, content)
			cfg.BannedRegex = appendUnique(cfg.BannedRegex, regex)
			cfg.Jailbreak.Patterns = appendUnique(cfg.Jailbreak.Patterns, jail)
		}
	}
	if c.safetyIPs != nil {
		if blocked, _, blockedConv, err := c.safetyIPs.Snapshot(); err == nil {
			// allowed_ips is reserved for future opt-in allowlist semantics
			// and is intentionally not yet consumed by buildPolicy.
			cfg.BlockedIPs = appendUnique(cfg.BlockedIPs, blocked)
			cfg.BlockedConversationIDs = appendUnique(cfg.BlockedConversationIDs, blockedConv)
		}
	}
	// v1.0.14: builtin safety catalogue (R14/furry/R18/crack/CTF/jailbreak
	// substring + regex lists from v1.0.13) was removed. Operators reported
	// unacceptably high false-positive rate on natural prose. Content-level
	// review now flows through internal/safetyllm with a binary verdict.
	// IP / conversation-id blocking + auto-ban remain on this fast path.
	return cfg
}

func appendUnique(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, v := range base {
		seen[v] = struct{}{}
	}
	out := append([]string(nil), base...)
	for _, v := range extra {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func safetyConfigSignature(cfg config.SafetyConfig) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(b)
}

func buildPolicy(cfg config.SafetyConfig) policy {
	enabled := false
	if cfg.Enabled != nil {
		enabled = *cfg.Enabled
	}
	p := policy{
		enabled:                enabled,
		blockMessage:           strings.TrimSpace(cfg.BlockMessage),
		blockedConversationIDs: map[string]struct{}{},
	}
	if p.blockMessage == "" {
		p.blockMessage = defaultBlockMessage
	}
	if !p.enabled {
		return p
	}
	for _, raw := range cfg.BlockedIPs {
		if matcher, ok := parseIPMatcher(raw); ok {
			p.blockedIPs = append(p.blockedIPs, matcher)
		}
	}
	for _, id := range cfg.BlockedConversationIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			p.blockedConversationIDs[strings.ToLower(id)] = struct{}{}
		}
	}
	// v1.0.14: substring banned_content / banned_regex / jailbreak.patterns
	// were removed in favor of LLM-based binary safety review. The fields
	// remain on SafetyConfig for backward-compatible JSON config files
	// and WebUI display, but they are no longer compiled into a policy
	// nor consulted by evaluate(). Operators wanting content-level review
	// must enable safety.llm_check.enabled.
	return p
}

func parseIPMatcher(raw string) (ipMatcher, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ipMatcher{}, false
	}
	if ip := net.ParseIP(raw); ip != nil {
		return ipMatcher{raw: raw, ip: ip}, true
	}
	if _, cidr, err := net.ParseCIDR(raw); err == nil {
		return ipMatcher{raw: raw, cidr: cidr}, true
	}
	return ipMatcher{}, false
}

// evaluate runs the FAST request-guard checks: IP block / conversation-id
// block. Content / jailbreak / banned-regex matching was removed in v1.0.14
// in favor of LLM-based binary review (internal/safetyllm). Operators
// reported high false-positive rate from substring + regex matching on
// natural prose; the LLM checker runs at the chat / responses / messages
// handler entry instead, after Auth.Determine, so it has a real
// RequestAuth + can budget upstream calls per-caller.
//
// IP / conv blocking stays here because (a) it's cheap, (b) it doesn't
// need the LLM to make the decision, and (c) it must run before any
// upstream call (LLM check itself) to short-circuit known-bad sources.
func (p policy) evaluate(r *http.Request, body []byte, meta requestmeta.Metadata) decision {
	if !p.enabled {
		return decision{}
	}
	if p.ipBlocked(meta.ClientIP) {
		return decision{blocked: true, code: "ip_blocked", detail: "request ip is blocked"}
	}
	if meta.ConversationID != "" {
		if _, ok := p.blockedConversationIDs[strings.ToLower(meta.ConversationID)]; ok {
			return decision{blocked: true, code: "conversation_blocked", detail: "conversation id is blocked"}
		}
	}
	_ = body // body unused after content-scan removal; kept in signature for handler hook compatibility
	_ = r
	return decision{}
}

func (p policy) ipBlocked(rawIP string) bool {
	ip := net.ParseIP(strings.TrimSpace(rawIP))
	if ip == nil {
		return false
	}
	for _, matcher := range p.blockedIPs {
		if matcher.ip != nil && matcher.ip.Equal(ip) {
			return true
		}
		if matcher.cidr != nil && matcher.cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func extractRequestText(body []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return string(body)
	}
	var out strings.Builder
	collectText(&out, value, 0)
	return out.String()
}

func collectText(out *strings.Builder, value any, depth int) {
	if out.Len() >= maxCollectedText || depth > 12 {
		return
	}
	switch v := value.(type) {
	case string:
		appendText(out, v)
	case map[string]any:
		// Recurse into all values so that tool_result / functionResponse /
		// nested tool blocks are scanned even when their container key is not
		// in the legacy whitelist. depth + maxCollectedText still bound the
		// traversal so this is bounded.
		for _, item := range v {
			collectText(out, item, depth+1)
		}
	case []any:
		for _, item := range v {
			collectText(out, item, depth+1)
		}
	}
}

func appendText(out *strings.Builder, text string) {
	text = strings.TrimSpace(text)
	if text == "" || out.Len() >= maxCollectedText {
		return
	}
	if out.Len() > 0 {
		out.WriteByte('\n')
	}
	remaining := maxCollectedText - out.Len()
	if len(text) > remaining {
		text = text[:remaining]
	}
	out.WriteString(text)
}

func recordBlockedHistory(store *chathistory.Store, body []byte, meta requestmeta.Metadata, d decision) {
	if store == nil || !store.Enabled() {
		return
	}
	now := time.Now()
	userInput := extractRequestText(body)
	if len(userInput) > 2048 {
		userInput = userInput[:2048]
	}
	model := modelFromBody(body)
	entry, err := store.Start(chathistory.StartParams{
		Status:         "error",
		Model:          model,
		UserInput:      userInput,
		RequestIP:      meta.ClientIP,
		ConversationID: meta.ConversationID,
	})
	if err != nil || entry.ID == "" {
		return
	}
	_, err = store.Update(entry.ID, chathistory.UpdateParams{
		Status:       "error",
		Error:        d.detail,
		StatusCode:   http.StatusForbidden,
		ElapsedMs:    time.Since(now).Milliseconds(),
		FinishReason: "policy_blocked",
		Completed:    true,
	})
	if err != nil {
		config.Logger.Warn("[request_guard] record blocked history failed", "error", err)
	}
}

func modelFromBody(body []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	if model, ok := obj["model"].(string); ok {
		return strings.TrimSpace(model)
	}
	return ""
}

func writeBlocked(w http.ResponseWriter, message string, d decision) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"detail": message,
		"code":   d.code,
		"reason": d.detail,
	}); err != nil {
		config.Logger.Warn("[request_guard] write blocked response failed", "error", err)
	}
}
