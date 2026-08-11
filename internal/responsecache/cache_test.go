package responsecache

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
)

type stubResolver struct {
	caller string
	err    error
}

func (s stubResolver) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &auth.RequestAuth{CallerID: s.caller}, nil
}

// fakeClock is a manually-advanced clock for the lease tests below. It is
// anchored at the real time.Now() on construction so that code comparing the
// injected clock against real filesystem mtimes (enforceDiskLimit /
// maybeSweepDisk walk the cache dir and diff ModTime against now) still sees
// sane, near-zero ages.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Lease offsets for the sliding-window / session-stickiness tests. Each test
// observes the cache at 65% of stickyTTL (must still be live) and again at
// 130% (only live because the earlier observation slid the lease).
//
// These tests used to sleep for real: a 200ms TTL with 130ms sleeps, i.e. 70ms
// of slack. That passed when the package ran alone but failed reproducibly
// under tests/scripts/run-unit-all.sh. The cause is not merely scheduling
// noise — setWithPolicySession fixes an entry's memory deadline from `now` and
// only *then* does gzip + temp-file write + rename, a full WalkDir in
// enforceDiskLimit, and another full WalkDir in maybeSweepDisk (which always
// runs on the first store, since lastDiskSweep is zero). All of that I/O burns
// wall time against a deadline that is already set, so the usable slack is
// unbounded from the test's point of view and no sleep constant is safe. The
// session test primes two entries and pays it twice, which is why it failed
// even after the margin was widened 5x.
//
// Driving the cache from an injected clock removes the race entirely: the
// assertions below are exact rather than probabilistic, and the tests no
// longer sleep at all.
const (
	stickyTTL  = 100 * time.Second
	stickyStep = 65 * time.Second
)

// TestSessionWideStickyTTLRefreshesSiblingsOnHit pins the session-level
// stickiness contract: when ANY entry in a logical conversation is hit,
// every sibling entry of that session also gets its lease refreshed. This
// is what keeps a multi-turn coding-agent conversation's full prefix hot
// as long as the conversation is alive — turn 5's hit reaches back and
// extends the lease on turns 1..4 too. Without this, an early-turn entry
// stored 25 minutes ago would expire even while the conversation is
// actively progressing.
func TestSessionWideStickyTTLRefreshesSiblingsOnHit(t *testing.T) {
	t.Parallel()

	// Injected clock: the test advances it explicitly, so no assertion can
	// race the store's own disk I/O. 1h diskTTL keeps the extension cap far.
	clk := newFakeClock()
	cache := New(Options{Dir: t.TempDir(), MemoryTTL: stickyTTL, DiskTTL: time.Hour, Now: clk.Now})
	resolver := stubResolver{caller: "caller-a"}

	// Turn 1: distinct body, but same first user message ("hi") — keeps
	// the SessionKey stable across turns. Turn 2 appends an assistant +
	// user message and arrives stickyStep later.
	turn1 := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	turn2 := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"continue"}]}`

	var upstream int32
	handler := cache.Wrap(resolver, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstream, 1)
		w.Header().Set("Content-Type", "application/json")
		// Echo something distinguishable per body so we can confirm the
		// right entry is served.
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"echo_len":` + fmt.Sprintf("%d", len(body)) + `}`))
	}))

	mkReq := func(body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer key-a")
		return req
	}

	// Prime turn 1.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, mkReq(turn1))
	if rec1.Code != http.StatusOK {
		t.Fatalf("turn1 prime status=%d", rec1.Code)
	}

	// One step later, prime turn 2 (different body, but same session).
	// Storing it is session activity, so turn1's lease is bumped to
	// t0+stickyStep+stickyTTL as a side effect.
	clk.advance(stickyStep)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, mkReq(turn2))
	if rec2.Code != http.StatusOK {
		t.Fatalf("turn2 prime status=%d", rec2.Code)
	}

	// Another step later: now at t0+2*stickyStep, which is strictly past
	// turn1's original t0+stickyTTL deadline. Hitting turn2 must serve from
	// memory and refresh turn1's lease again via session linkage.
	clk.advance(stickyStep)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, mkReq(turn2))
	if got := rec3.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("turn2 second hit must serve from memory, got %q", got)
	}

	// Retry turn 1 at the same instant — 2*stickyStep past its original
	// prime, i.e. well beyond a plain stickyTTL. Without the session-wide
	// bump it would already have been swept, so a memory hit here is what
	// actually proves the sibling refresh happened.
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, mkReq(turn1))
	if got := rec4.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("turn1 retry after session bump must serve from memory, got %q", got)
	}

	// Two upstream calls (one per prime), zero on hits.
	if got := atomic.LoadInt32(&upstream); got != 2 {
		t.Fatalf("expected exactly 2 upstream calls (one per turn prime), got %d", got)
	}

	stats := cache.Stats()
	if got, _ := stats["session_hits"].(int64); got < 1 {
		t.Fatalf("session_hits = %v, want >= 1 (rec3/rec4 should each fire)", stats["session_hits"])
	}
	if got, _ := stats["active_sessions"].(int); got != 1 {
		t.Fatalf("active_sessions = %v, want 1", stats["active_sessions"])
	}
}

// TestSlidingWindowKeepsActiveEntryHot pins the session-stickiness contract:
// every memory hit must extend the entry's lease by memoryTTL (capped by
// disk expiration). Active conversations that keep hitting the same key
// stay hot beyond the initial TTL, while inactive entries still age out at
// the original ceiling.
func TestSlidingWindowKeepsActiveEntryHot(t *testing.T) {
	t.Parallel()

	// Controllable clock (see stickyTTL). Disk TTL = 1h so the extension
	// cap is far away and never engages.
	clk := newFakeClock()
	cache := New(Options{Dir: t.TempDir(), MemoryTTL: stickyTTL, DiskTTL: time.Hour, Now: clk.Now})
	var upstream int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstream, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sticky":true}`))
	}))

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer key-a")
		return req
	}

	// Prime: store one entry.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("prime status=%d", rec.Code)
	}

	// Advance to 65% of the TTL — before the original deadline. The hit must
	// succeed AND must slide the deadline out to now+stickyTTL (= 1.65 TTL).
	clk.advance(stickyStep)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, mkReq())
	if got := rec2.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("expected memory hit before original deadline, got %q", got)
	}

	// Advance to 130% of the TTL. Without the sliding window the entry would
	// have expired at 1.0 TTL; because the previous hit slid the deadline to
	// 1.65 TTL, this must still be a memory hit.
	clk.advance(stickyStep)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, mkReq())
	if got := rec3.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("expected sliding-window memory hit past original deadline, got %q", got)
	}

	// Three requests, one upstream call — the prime.
	if got := atomic.LoadInt32(&upstream); got != 1 {
		t.Fatalf("expected exactly one upstream call, got %d", got)
	}
}

// TestSingleFlightDedupesConcurrentIdenticalRequests pins the in-flight
// dedup contract: when N requests with the same cache key arrive while an
// upstream call is in progress, only one upstream call runs and the
// remaining N-1 wait on the in-flight slot, then serve from cache. This is
// what protects upstream from flaky-client retries during streaming.
func TestSingleFlightDedupesConcurrentIdenticalRequests(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var upstream int32
	releaseUpstream := make(chan struct{})
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstream, 1)
		// Block long enough for waiters to enter the in-flight slot.
		<-releaseUpstream
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"first":true}`))
	}))

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer key-a")
		return req
	}

	const concurrency = 5
	results := make(chan *httptest.ResponseRecorder, concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, mkReq())
			results <- rec
		}()
	}

	// Give all goroutines time to register: owner runs upstream, others
	// land in the inflight wait.
	time.Sleep(50 * time.Millisecond)
	close(releaseUpstream)

	for i := 0; i < concurrency; i++ {
		select {
		case rec := <-results:
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != `{"first":true}` {
				t.Fatalf("unexpected body: %s", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent request to complete")
		}
	}

	if got := atomic.LoadInt32(&upstream); got != 1 {
		t.Fatalf("expected single-flight to dedupe to 1 upstream call, got %d", got)
	}
	stats := cache.Stats()
	inflightHits, _ := stats["inflight_hits"].(int64)
	if inflightHits < int64(concurrency-1) {
		t.Fatalf("inflight_hits = %d, want >= %d", inflightHits, concurrency-1)
	}
}

// TestEmbeddingsCacheSharesAcrossCallers verifies the policy from
// docs/cache-research.md §4: embeddings are deterministic functions of input
// text so cross-caller sharing is safe and prevents redundant upstream
// calls. Two different API keys posting the same body must hit the same
// cached response on the second request.
func TestEmbeddingsCacheSharesAcrossCallers(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var upstream int32
	makeHandler := func(callerID string) http.Handler {
		return cache.Wrap(stubResolver{caller: callerID}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&upstream, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
		}))
	}

	body := `{"model":"text-embedding-3-small","input":"hello world"}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer key-alice")
	rec1 := httptest.NewRecorder()
	makeHandler("caller-alice").ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rec1.Code, rec1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer key-bob")
	rec2 := httptest.NewRecorder()
	makeHandler("caller-bob").ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", rec2.Code, rec2.Body.String())
	}

	if got := atomic.LoadInt32(&upstream); got != 1 {
		t.Fatalf("expected upstream called once across two callers, got %d", got)
	}
	if got := rec2.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("expected cross-caller memory hit, got %q", got)
	}
	stats := cache.Stats()
	paths, ok := stats["paths"].(map[string]any)
	if !ok {
		t.Fatalf("expected paths breakdown, got %T", stats["paths"])
	}
	emb, ok := paths["/v1/embeddings"].(map[string]any)
	if !ok {
		t.Fatalf("expected /v1/embeddings entry, paths=%v", paths)
	}
	if got := emb["hits"]; got != int64(1) {
		t.Fatalf("/v1/embeddings hits = %v, want 1", got)
	}
	if got := emb["stores"]; got != int64(1) {
		t.Fatalf("/v1/embeddings stores = %v, want 1", got)
	}
	if got := emb["shared"]; got != true {
		t.Fatalf("/v1/embeddings shared flag = %v, want true", got)
	}
}

// TestChatCompletionsCacheStaysPerCaller verifies the inverse policy:
// LLM completions are sampling-based and a hit returns a previously sampled
// response. Crossing the caller boundary would expose one tenant's reply to
// another, so the cache must remain partitioned. Same body, different
// CallerID → independent cache entries.
func TestChatCompletionsCacheStaysPerCaller(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var upstream int32
	makeHandler := func(callerID string) http.Handler {
		return cache.Wrap(stubResolver{caller: callerID}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&upstream, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		}))
	}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer key-alice")
	rec1 := httptest.NewRecorder()
	makeHandler("caller-alice").ServeHTTP(rec1, req1)

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer key-bob")
	rec2 := httptest.NewRecorder()
	makeHandler("caller-bob").ServeHTTP(rec2, req2)

	if got := atomic.LoadInt32(&upstream); got != 2 {
		t.Fatalf("expected upstream called twice (one per caller), got %d", got)
	}
	if got := rec2.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "" {
		t.Fatalf("expected no cross-caller hit on chat completions, got source=%q", got)
	}
	stats := cache.Stats()
	paths, ok := stats["paths"].(map[string]any)
	if !ok {
		t.Fatalf("expected paths breakdown, got %T", stats["paths"])
	}
	chat, ok := paths["/v1/chat/completions"].(map[string]any)
	if !ok {
		t.Fatalf("expected /v1/chat/completions entry, paths=%v", paths)
	}
	if got := chat["hits"]; got != int64(0) {
		t.Fatalf("/v1/chat/completions hits = %v, want 0 (per-caller boundary)", got)
	}
	if got := chat["stores"]; got != int64(2) {
		t.Fatalf("/v1/chat/completions stores = %v, want 2", got)
	}
	if got := chat["shared"]; got != false {
		t.Fatalf("/v1/chat/completions shared flag = %v, want false", got)
	}
}

func TestMiddlewareCachesProtocolResponseInMemory(t *testing.T) {
	t.Parallel()

	var hits int32
	cache := New(Options{
		Dir:       t.TempDir(),
		MemoryTTL: time.Minute,
		DiskTTL:   time.Hour,
		OnHit: func(_ *http.Request, entry Entry, source string) {
			if source == "memory" && string(entry.Body) == `{"ok":true}` {
				atomic.AddInt32(&hits, 1)
			}
		},
	})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req1.Header.Set("Authorization", "Bearer key-a")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rec1.Code, rec1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req2.Header.Set("Authorization", "Bearer key-a")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected handler once, got %d", got)
	}
	if got := rec2.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("expected memory cache hit, got %q", got)
	}
	if got := rec2.Body.String(); got != `{"ok":true}` {
		t.Fatalf("unexpected cached body: %s", got)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected one hit callback, got %d", got)
	}
	stats := cache.Stats()
	if got := stats["lookups"]; got != int64(2) {
		t.Fatalf("expected two cache lookups, got %v", got)
	}
	if got := stats["hits"]; got != int64(1) {
		t.Fatalf("expected one cache hit, got %v", got)
	}
	if got := stats["misses"]; got != int64(1) {
		t.Fatalf("expected one cache miss, got %v", got)
	}
	if got := stats["stores"]; got != int64(1) {
		t.Fatalf("expected one cache store, got %v", got)
	}
	if got := stats["cacheable_lookups"]; got != int64(2) {
		t.Fatalf("expected two cacheable lookups, got %v", got)
	}
	if got := stats["cacheable_misses"]; got != int64(1) {
		t.Fatalf("expected one cacheable miss, got %v", got)
	}
	if got := stats["memory_hits"]; got != int64(1) {
		t.Fatalf("expected one memory hit, got %v", got)
	}
}

func TestCacheFallsBackToCompressedDiskAfterMemoryExpiry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := New(Options{Dir: dir, MemoryTTL: time.Millisecond, DiskTTL: time.Hour})
	key := strings.Repeat("a", 64)
	cache.Set(key, Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: []byte(`{"disk":true}`),
	})
	path, ok := cache.diskPath(key)
	if !ok {
		t.Fatal("expected cache disk path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatalf("expected gzip-compressed cache file, got prefix %x", raw[:min(len(raw), 2)])
	}

	time.Sleep(5 * time.Millisecond)
	entry, source, ok := cache.Get(key)
	if !ok {
		t.Fatal("expected disk cache hit")
	}
	if source != "disk" {
		t.Fatalf("expected disk source, got %q", source)
	}
	if got := string(entry.Body); got != `{"disk":true}` {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestRequestBypassSkipsCache(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, call)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.Header.Set("Cache-Control", "no-cache")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
			t.Fatalf("unexpected cache hit on bypass request")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected bypass to call handler twice, got %d", got)
	}
}

func TestOversizedBodySkipsCacheWithoutConsumingBody(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour, MaxBody: 4})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		body := make([]byte, 16)
		n, err := r.Body.Read(body)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("read body: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"call":%d,"body":%q}`, call, string(body[:n]))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`ok`))
	req.Header.Set("Authorization", "Bearer key-a")
	req.ContentLength = 100
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
		t.Fatalf("unexpected cache hit on oversized request")
	}
	if !strings.Contains(rec.Body.String(), `"body":"ok"`) {
		t.Fatalf("handler did not receive original body: %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected oversized body to call handler once, got %d", got)
	}
}

func TestUnknownLengthBodyCanBeCached(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour, MaxBody: 1024})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"body":%q}`, string(body))
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if i == 1 && rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "memory" {
			t.Fatalf("expected second unknown-length request to hit memory cache, got %q", rec.Header().Get("X-DeepSeek-Web-To-API-Cache"))
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected unknown-length body to cache after first call, got %d handler calls", got)
	}
}

func TestUnknownLengthOversizedBodyBypassesCacheWithoutDroppingBody(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour, MaxBody: 4})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"call":%d,"body":%q}`, call, string(body))
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`abcdef`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
			t.Fatalf("unexpected cache hit on oversized unknown-length request")
		}
		if !strings.Contains(rec.Body.String(), `"body":"abcdef"`) {
			t.Fatalf("handler did not receive original body: %s", rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected oversized unknown-length body to call handler twice, got %d", got)
	}
}

func TestCacheableRequestCoversSupportedProtocols(t *testing.T) {
	t.Parallel()

	paths := []string{
		"/v1/chat/completions",
		"/v1/v1/chat/completions",
		"/chat/completions",
		"/v1/responses",
		"/v1/v1/responses",
		"/responses",
		"/v1/embeddings",
		"/v1/v1/embeddings",
		"/embeddings",
		"/anthropic/v1/messages",
		"/v1/messages",
		"/v1/v1/messages",
		"/messages",
		"/anthropic/v1/messages/count_tokens",
		"/v1/v1/messages/count_tokens",
		"/messages/count_tokens",
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"/v1/models/gemini-2.5-pro:streamGenerateContent",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		if !CacheableRequest(req) {
			t.Fatalf("expected %s to be cacheable", path)
		}
	}

}

func TestRequestKeyVariesByCallerAndProtocolHeaders(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"claude"}`))
	req.Header.Set("Anthropic-Version", "2023-06-01")
	body := []byte(`{"model":"claude"}`)

	base := RequestKey(req, "caller-a", body)
	if base == RequestKey(req, "caller-b", body) {
		t.Fatal("expected caller to affect cache key")
	}

	req.Header.Set("Anthropic-Version", "2024-01-01")
	if base == RequestKey(req, "caller-a", body) {
		t.Fatal("expected protocol version header to affect cache key")
	}
}

func TestRequestKeyCanonicalizesProtocolAliases(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m"}`)
	base := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	root := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(string(body)))
	doubleV1 := httptest.NewRequest(http.MethodPost, "/v1/v1/chat/completions", strings.NewReader(string(body)))
	if RequestKey(base, "caller-a", body) != RequestKey(root, "caller-a", body) {
		t.Fatal("expected root OpenAI alias to share cache key")
	}
	if RequestKey(base, "caller-a", body) != RequestKey(doubleV1, "caller-a", body) {
		t.Fatal("expected double-v1 OpenAI alias to share cache key")
	}

	claude := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	claudeRoot := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(string(body)))
	claudeAnthropic := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(body)))
	if RequestKey(claude, "caller-a", body) != RequestKey(claudeRoot, "caller-a", body) {
		t.Fatal("expected Claude root alias to share cache key")
	}
	if RequestKey(claude, "caller-a", body) != RequestKey(claudeAnthropic, "caller-a", body) {
		t.Fatal("expected Anthropic-prefixed alias to share cache key")
	}
}

func TestRequestKeyCanonicalizesJSONBodyAndIgnoredMetadata(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyA := []byte(`{"metadata":{"trace":"a"},"model":"m","messages":[{"content":"hello","role":"user"}],"user":"u1"}`)
	bodyB := []byte(`{
		"user":"u2",
		"messages":[{"role":"user","content":"hello"}],
		"model":"m",
		"metadata":{"trace":"b"}
	}`)

	if RequestKey(req, "caller-a", bodyA) != RequestKey(req, "caller-a", bodyB) {
		t.Fatal("expected equivalent JSON payloads to share cache key")
	}

	bodyC := []byte(`{"model":"m","messages":[{"role":"user","content":"different"}]}`)
	if RequestKey(req, "caller-a", bodyA) == RequestKey(req, "caller-a", bodyC) {
		t.Fatal("expected semantic prompt body to affect cache key")
	}
}

func TestRequestKeyPreservesSemanticJSONNull(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyWithNull := []byte(`{"model":"m","messages":[{"role":"user","content":null}]}`)
	bodyWithoutContent := []byte(`{"model":"m","messages":[{"role":"user"}]}`)

	if RequestKey(req, "caller-a", bodyWithNull) == RequestKey(req, "caller-a", bodyWithoutContent) {
		t.Fatal("expected semantic JSON null to remain part of the cache key")
	}
}

func TestRequestKeySemanticModeIgnoresTransientToolIDsAndWhitespace(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyA := []byte(`{
		"model":"m",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"read_file","arguments":{"path":"README.md"}}}]},
			{"role":"tool","tool_call_id":"call_a","content":"line one\nline two"}
		]
	}`)
	bodyB := []byte(`{
		"model":"m",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_b","type":"function","function":{"name":"read_file","arguments":{"path":"README.md"}}}]},
			{"role":"tool","tool_call_id":"call_b","content":" line one   line two "}
		]
	}`)

	if RequestKey(req, "caller-a", bodyA) != RequestKey(req, "caller-a", bodyB) {
		t.Fatal("expected semantic cache key to ignore transient tool ids and whitespace")
	}
}

func TestRequestKeyIgnoresClaudeTransportCacheFields(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyA := []byte(`{
		"model":"claude-sonnet-4-6",
		"betas":["claude-code"],
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}},
				{"type":"cache_edits","edits":[{"type":"delete","cache_reference":"toolu_old"}]}
			]
		}]
	}`)
	bodyB := []byte(`{
		"messages":[{
			"content":[{"text":"hello","type":"text"}],
			"role":"user"
		}],
		"model":"claude-sonnet-4-6"
	}`)

	if RequestKey(req, "caller-a", bodyA) != RequestKey(req, "caller-a", bodyB) {
		t.Fatal("expected Claude transport cache fields to be ignored in cache key")
	}
}

func TestMiddlewareCountsUncacheableMisses(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer key-a")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stats := cache.Stats()
	if got := stats["misses"]; got != int64(1) {
		t.Fatalf("expected one miss, got %v", got)
	}
	if got := stats["stores"]; got != int64(0) {
		t.Fatalf("expected no cache store, got %v", got)
	}
	if got := stats["cacheable_misses"]; got != int64(0) {
		t.Fatalf("expected no cacheable misses, got %v", got)
	}
	if got := stats["uncacheable_misses"]; got != int64(1) {
		t.Fatalf("expected one uncacheable miss, got %v", got)
	}
	if got := stats["uncacheable_status_non_2xx"]; got != int64(1) {
		t.Fatalf("expected status_non_2xx reason, got %v", got)
	}
}

func TestCacheSkipsUncacheableResponses(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	key := strings.Repeat("b", 64)
	cache.Set(key, Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Set-Cookie": []string{"sid=1"},
		},
		Body: []byte(`{"private":true}`),
	})
	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected Set-Cookie response to skip cache")
	}

	cache.Set(key, Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(`{"private":true}`),
	})
	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected no-store response to skip cache")
	}
}

func TestStatsReportsCompressionAndTTLs(t *testing.T) {
	t.Parallel()

	cache := New(Options{
		Dir:            t.TempDir(),
		MemoryTTL:      2 * time.Minute,
		DiskTTL:        3 * time.Hour,
		MemoryMaxBytes: 1234,
		DiskMaxBytes:   5678,
	})
	stats := cache.Stats()
	if got := stats["memory_ttl_seconds"]; got != 120 {
		t.Fatalf("memory_ttl_seconds=%v", got)
	}
	if got := stats["disk_ttl_seconds"]; got != 10800 {
		t.Fatalf("disk_ttl_seconds=%v", got)
	}
	if got := stats["memory_max_bytes"]; got != int64(1234) {
		t.Fatalf("memory_max_bytes=%v", got)
	}
	if got := stats["disk_max_bytes"]; got != int64(5678) {
		t.Fatalf("disk_max_bytes=%v", got)
	}
	if got := stats["max_body_bytes"]; got != int64(defaultMaxBody) {
		t.Fatalf("max_body_bytes=%v", got)
	}
	if got := stats["compression"]; got != "gzip" {
		t.Fatalf("compression=%v", got)
	}
}

func TestApplyOptionsHotReloadsCacheSettings(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Hour, DiskTTL: time.Hour, MemoryMaxBytes: 1024, DiskMaxBytes: 1024, SemanticKey: true})
	cache.Set(strings.Repeat("f", 64), Entry{Status: http.StatusOK, Body: []byte(`{"cached":true}`)})
	if stats := cache.Stats(); stats["memory_items"] != 1 {
		t.Fatalf("expected warm cache before hot reload, got %v", stats["memory_items"])
	}

	newDir := t.TempDir()
	cache.ApplyOptions(Options{
		Dir:            newDir,
		MemoryTTL:      time.Minute,
		DiskTTL:        2 * time.Hour,
		MaxBody:        2048,
		MemoryMaxBytes: 512,
		DiskMaxBytes:   4096,
		SemanticKey:    false,
	})

	stats := cache.Stats()
	if got := stats["disk_dir"]; got != newDir {
		t.Fatalf("disk_dir=%v", got)
	}
	if got := stats["memory_ttl_seconds"]; got != 60 {
		t.Fatalf("memory_ttl_seconds=%v", got)
	}
	if got := stats["disk_ttl_seconds"]; got != 7200 {
		t.Fatalf("disk_ttl_seconds=%v", got)
	}
	if got := stats["max_body_bytes"]; got != int64(2048) {
		t.Fatalf("max_body_bytes=%v", got)
	}
	if got := stats["memory_max_bytes"]; got != int64(512) {
		t.Fatalf("memory_max_bytes=%v", got)
	}
	if got := stats["disk_max_bytes"]; got != int64(4096) {
		t.Fatalf("disk_max_bytes=%v", got)
	}
	if got := stats["semantic_key"]; got != false {
		t.Fatalf("semantic_key=%v", got)
	}
	if got := stats["memory_items"]; got != 0 {
		t.Fatalf("expected memory cache to be reset after hot reload, got %v", got)
	}
}

func TestMemoryLimitEvictsEntries(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Hour, DiskTTL: time.Hour, MemoryMaxBytes: 6})
	cache.Set(strings.Repeat("c", 64), Entry{Status: http.StatusOK, Body: []byte(`aaaa`)})
	cache.Set(strings.Repeat("d", 64), Entry{Status: http.StatusOK, Body: []byte(`bbbb`)})

	stats := cache.Stats()
	if got := stats["memory_bytes"].(int64); got > 6 {
		t.Fatalf("memory_bytes=%d exceeds limit", got)
	}
	if got := stats["memory_items"]; got != 1 {
		t.Fatalf("memory_items=%v, want 1", got)
	}
}

func TestDiskLimitPrunesCompressedFiles(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Hour, DiskTTL: time.Hour, MemoryMaxBytes: 1, DiskMaxBytes: 1})
	key := strings.Repeat("e", 64)
	cache.Set(key, Entry{Status: http.StatusOK, Body: []byte(`{"too":"large for tiny disk limit"}`)})

	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected disk limit to prune cache entry")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
