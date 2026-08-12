package config

import (
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestPromptLimitSnapshotAppliesDefaults pins that an absent prompt_limit
// block resolves to the documented defaults rather than zero values. A zeroed
// MaxChars would mean "limit 0", which LimitForModel treats as disabled — so a
// regression here silently turns the whole feature off.
func TestPromptLimitSnapshotAppliesDefaults(t *testing.T) {
	t.Parallel()

	s := &Store{}
	got := s.PromptLimitSnapshot()
	want := DefaultPromptLimitSettings()
	if got != want {
		t.Fatalf("empty config snapshot = %+v, want %+v", got, want)
	}
	if got.MaxCharsExpert >= got.MaxCharsDefault {
		t.Fatalf("expert ceiling (%d) must be stricter than default (%d)",
			got.MaxCharsExpert, got.MaxCharsDefault)
	}
	if got.AutoCompressEnable {
		t.Fatal("automatic prompt compression must default to disabled")
	}
	if got.SummaryCompactionEnable || got.SummaryCompactionThreshold != 0.8 {
		t.Fatalf("summary compaction defaults = enabled:%v threshold:%v", got.SummaryCompactionEnable, got.SummaryCompactionThreshold)
	}
}

func TestPromptLimitConfigRoundTripIncludesIncrementalRotation(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"prompt_limit":{"enabled":true,"auto_compress_enabled":false,"incremental_max_turns":64,"incremental_rotation_keep_recent":8}}`), &cfg); err != nil {
		t.Fatalf("decode prompt_limit: %v", err)
	}
	if cfg.PromptLimit.IncrementalMaxTurns == nil || *cfg.PromptLimit.IncrementalMaxTurns != 64 || cfg.PromptLimit.IncrementalRotationKeepRecent != 8 {
		t.Fatalf("rotation config was not decoded: %+v", cfg.PromptLimit)
	}
	store := &Store{cfg: cfg}
	snapshot := store.PromptLimitSnapshot()
	if snapshot.AutoCompressEnable || snapshot.IncrementalMaxTurns != 64 || snapshot.IncrementalRotationKeepRecent != 8 {
		t.Fatalf("unexpected resolved prompt limit: %+v", snapshot)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode prompt_limit: %v", err)
	}
	if !strings.Contains(string(b), `"incremental_max_turns":64`) || !strings.Contains(string(b), `"incremental_rotation_keep_recent":8`) {
		t.Fatalf("rotation config was lost during encode: %s", b)
	}
}

func TestPromptLimitConfigRoundTripIncludesSummaryCompaction(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"prompt_limit":{"summary_compaction_enabled":true,"summary_compaction_threshold":0.75}}`), &cfg); err != nil {
		t.Fatalf("decode prompt_limit: %v", err)
	}
	store := &Store{cfg: cfg}
	snapshot := store.PromptLimitSnapshot()
	if !snapshot.SummaryCompactionEnable || snapshot.SummaryCompactionThreshold != 0.75 {
		t.Fatalf("unexpected summary compaction snapshot: %+v", snapshot)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode prompt_limit: %v", err)
	}
	if !strings.Contains(string(b), `"summary_compaction_enabled":true`) || !strings.Contains(string(b), `"summary_compaction_threshold":0.75`) {
		t.Fatalf("summary compaction config was lost during encode: %s", b)
	}
}

// TestPromptLimitSnapshotOverlaysPartialConfig pins that setting one field
// leaves the others at their defaults. The resolver uses `> 0` / `!= nil`
// guards, so a partial operator config must not zero out neighbouring knobs.
func TestPromptLimitSnapshotOverlaysPartialConfig(t *testing.T) {
	t.Parallel()

	s := &Store{}
	s.cfg.PromptLimit.MaxCharsExpert = 42000
	s.cfg.PromptLimit.AutoCompressEnabled = boolPtr(false)

	got := s.PromptLimitSnapshot()
	def := DefaultPromptLimitSettings()

	if got.MaxCharsExpert != 42000 {
		t.Fatalf("MaxCharsExpert = %d, want 42000", got.MaxCharsExpert)
	}
	if got.AutoCompressEnable {
		t.Fatal("AutoCompressEnable = true, want false (explicitly disabled)")
	}
	if got.MaxCharsDefault != def.MaxCharsDefault {
		t.Fatalf("MaxCharsDefault = %d, want default %d (untouched)",
			got.MaxCharsDefault, def.MaxCharsDefault)
	}
	if got.KeepRecentTurns != def.KeepRecentTurns {
		t.Fatalf("KeepRecentTurns = %d, want default %d (untouched)",
			got.KeepRecentTurns, def.KeepRecentTurns)
	}
	if !got.Enabled || !got.KeepSystemMessage {
		t.Fatalf("nil bool pointers must resolve true, got Enabled=%v KeepSystem=%v",
			got.Enabled, got.KeepSystemMessage)
	}
}

// TestPromptLimitSnapshotIsAtomicUnderConcurrentUpdate pins the reason
// PromptLimitSnapshot exists at all.
//
// Reading the knobs through the six per-field accessors takes six separate
// read locks, so a concurrent config write can land between them and yield a
// torn mix of old and new values. The consequence is not cosmetic: the compress
// phase could observe a high ceiling (decide no trimming is needed) while the
// enforce phase observes a low one and rejects the request with 413 — a
// spurious failure caused purely by a config write racing a request.
//
// Both loops are bounded by a fixed round count rather than a stop channel.
// An earlier version had the writer spin until a channel closed *after*
// wg.Wait(), which self-deadlocked; and because Go's RWMutex hands the lock to
// waiting writers ahead of readers, a tight unbounded write loop starves the
// readers outright. runtime.Gosched() on each side keeps the interleaving dense
// enough to actually catch a tear.
//
// This test mutates s.cfg directly under the lock instead of going through
// Store.Update: Update also persists to disk, and thousands of file writes are
// pure noise for a guarantee that is purely about in-memory lock coverage.
func TestPromptLimitSnapshotIsAtomicUnderConcurrentUpdate(t *testing.T) {
	t.Parallel()

	// Two internally-consistent configurations. Every field differs, so any
	// torn read is guaranteed to produce a combination matching neither.
	const (
		loDefault, loExpert, loKeep = 10000, 5000, 2
		hiDefault, hiExpert, hiKeep = 200000, 90000, 12
		rounds                      = 20000
		readers                     = 4
	)

	s := &Store{}
	s.cfg.PromptLimit.MaxCharsDefault = loDefault
	s.cfg.PromptLimit.MaxCharsExpert = loExpert
	s.cfg.PromptLimit.CompressKeepRecent = loKeep

	var wg sync.WaitGroup

	// Writer: flip the whole block back and forth under the write lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		high := true
		for n := 0; n < rounds; n++ {
			s.mu.Lock()
			if high {
				s.cfg.PromptLimit.MaxCharsDefault = hiDefault
				s.cfg.PromptLimit.MaxCharsExpert = hiExpert
				s.cfg.PromptLimit.CompressKeepRecent = hiKeep
			} else {
				s.cfg.PromptLimit.MaxCharsDefault = loDefault
				s.cfg.PromptLimit.MaxCharsExpert = loExpert
				s.cfg.PromptLimit.CompressKeepRecent = loKeep
			}
			s.mu.Unlock()
			high = !high
			runtime.Gosched()
		}
	}()

	// Readers: every snapshot must be wholly "low" or wholly "high".
	var torn int64
	var tornMu sync.Mutex
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < rounds; n++ {
				got := s.PromptLimitSnapshot()
				isLow := got.MaxCharsDefault == loDefault &&
					got.MaxCharsExpert == loExpert &&
					got.KeepRecentTurns == loKeep
				isHigh := got.MaxCharsDefault == hiDefault &&
					got.MaxCharsExpert == hiExpert &&
					got.KeepRecentTurns == hiKeep
				if !isLow && !isHigh {
					tornMu.Lock()
					torn++
					tornMu.Unlock()
				}
				runtime.Gosched()
			}
		}()
	}

	wg.Wait()

	if torn != 0 {
		t.Fatalf("observed %d torn snapshots; PromptLimitSnapshot must read every "+
			"field under one lock", torn)
	}
}
