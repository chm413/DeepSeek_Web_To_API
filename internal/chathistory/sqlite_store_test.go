package chathistory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteStoreImportsLegacyJSONAndUsesRetentionLimit(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "chat_history.json")
	detailDir := legacyPath + ".d"
	if err := os.MkdirAll(detailDir, 0o755); err != nil {
		t.Fatalf("create detail dir: %v", err)
	}
	state := File{
		Version:  FileVersion,
		Limit:    1_800_000,
		Revision: 10,
		Items: []SummaryEntry{
			{ID: "old", Revision: 1, DetailRevision: 1, CreatedAt: 1, UpdatedAt: 1, Status: "success", UserInput: "old"},
			{ID: "new", Revision: 2, DetailRevision: 2, CreatedAt: 2, UpdatedAt: 2, Status: "success", UserInput: "new"},
		},
	}
	writeJSONFile(t, legacyPath, state)
	writeJSONFile(t, filepath.Join(detailDir, "old.json"), detailEnvelope{Version: FileVersion, Item: Entry{ID: "old", Revision: 1, CreatedAt: 1, UpdatedAt: 1, Status: "success", Content: "old answer"}})
	writeJSONFile(t, filepath.Join(detailDir, "new.json"), detailEnvelope{Version: FileVersion, Item: Entry{ID: "new", Revision: 2, CreatedAt: 2, UpdatedAt: 2, Status: "success", Content: "new answer"}})

	store := NewSQLite(filepath.Join(dir, "chat_history.sqlite"), legacyPath)
	defer func() { _ = store.Close() }()
	if err := store.Err(); err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Limit != DefaultLimit {
		t.Fatalf("expected default limit after unsupported legacy limit, got %d", snapshot.Limit)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[0].ID != "new" {
		t.Fatalf("unexpected imported summaries: %#v", snapshot.Items)
	}
	item, err := store.Get("old")
	if err != nil {
		t.Fatalf("get imported detail: %v", err)
	}
	if item.Content != "old answer" {
		t.Fatalf("unexpected imported detail: %#v", item)
	}
}

func TestSQLiteStoreMergesNewEntriesFromChangedLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "chat_history.json")
	first := legacyFile{Version: FileVersion, Limit: MaxLimit, Items: []Entry{{ID: "first", Revision: 1, CreatedAt: 1, UpdatedAt: 1, Status: "success", Content: "first"}}}
	writeJSONFile(t, legacyPath, first)
	store := NewSQLite(filepath.Join(dir, "chat_history.sqlite"), legacyPath)
	if err := store.Err(); err != nil {
		t.Fatalf("initial legacy import: %v", err)
	}
	_ = store.Close()

	second := legacyFile{Version: FileVersion, Limit: MaxLimit, Items: []Entry{
		{ID: "first", Revision: 1, CreatedAt: 1, UpdatedAt: 1, Status: "success", Content: "stale first"},
		{ID: "second", Revision: 2, CreatedAt: 2, UpdatedAt: 2, Status: "success", Content: "second"},
	}}
	writeJSONFile(t, legacyPath, second)
	reloaded := NewSQLite(filepath.Join(dir, "chat_history.sqlite"), legacyPath)
	defer func() { _ = reloaded.Close() }()
	if err := reloaded.Err(); err != nil {
		t.Fatalf("changed legacy import: %v", err)
	}
	if _, err := reloaded.Get("second"); err != nil {
		t.Fatalf("expected new legacy entry to be imported: %v", err)
	}
	got, err := reloaded.Get("first")
	if err != nil {
		t.Fatalf("get existing entry: %v", err)
	}
	if got.Content != "first" {
		t.Fatalf("legacy re-import overwrote authoritative sqlite row: %q", got.Content)
	}
}

func TestSQLiteStoreCRUDAndTokenStats(t *testing.T) {
	dir := t.TempDir()
	store := NewSQLite(filepath.Join(dir, "chat_history.sqlite"), filepath.Join(dir, "missing.json"))
	defer func() { _ = store.Close() }()
	if err := store.Err(); err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	entry, err := store.Start(StartParams{Model: "deepseek-v4-flash", UserInput: "hello"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := store.Update(entry.ID, UpdateParams{
		Status:       "success",
		Content:      "world",
		StatusCode:   200,
		FinishReason: "stop",
		Usage:        map[string]any{"input_tokens": 3, "output_tokens": 4},
		Completed:    true,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	stats, err := store.TokenUsageStats(0)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Total.TotalTokens != 7 || stats.Total.Requests != 1 {
		t.Fatalf("unexpected stats: %#v", stats.Total)
	}
	upstreamEntry, err := store.Start(StartParams{Model: "deepseek-v4-flash", UserInput: "upstream"})
	if err != nil {
		t.Fatalf("start upstream entry: %v", err)
	}
	if _, err := store.Update(upstreamEntry.ID, UpdateParams{
		Status:     "error",
		StatusCode: 504,
		Completed:  true,
	}); err != nil {
		t.Fatalf("update upstream entry: %v", err)
	}
	outcomes, err := store.OutcomeStats()
	if err != nil {
		t.Fatalf("outcome stats: %v", err)
	}
	if outcomes.Success != 1 || outcomes.Failed != 0 || outcomes.ExcludedFromFailureRate != 1 || outcomes.SuccessRate != 100 {
		t.Fatalf("unexpected outcome stats: %#v", outcomes)
	}
	if _, err := store.SetLimit(DisabledLimit); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := store.Start(StartParams{UserInput: "blocked"}); err != ErrDisabled {
		t.Fatalf("expected disabled error, got %v", err)
	}
	if err := store.Delete(entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(upstreamEntry.ID); err != nil {
		t.Fatalf("delete upstream: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot after delete: %v", err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("expected empty store, got %#v", snapshot.Items)
	}
}

func TestSQLiteStoreWritesCompressedDetails(t *testing.T) {
	dir := t.TempDir()
	store := NewSQLite(filepath.Join(dir, "chat_history.sqlite"), filepath.Join(dir, "missing.json"))
	defer func() { _ = store.Close() }()
	if err := store.Err(); err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	entry, err := store.Start(StartParams{
		Model:       "deepseek-v4-flash",
		UserInput:   "compress me",
		HistoryText: repeatedText("history-", 256),
		FinalPrompt: repeatedText("prompt-", 256),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	updated, err := store.Update(entry.ID, UpdateParams{
		Status:  "success",
		Content: repeatedText("answer-", 256),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.Get(entry.ID)
	if err != nil {
		t.Fatalf("get compressed detail: %v", err)
	}
	if got.Content != updated.Content || got.FinalPrompt != updated.FinalPrompt {
		t.Fatalf("unexpected decoded detail: %#v", got)
	}
	assertSQLiteDetailCompressed(t, store, entry.ID)
}

func TestSQLiteStoreMigratesLegacyUncompressedDetails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chat_history.sqlite")
	legacyEntry := Entry{
		ID:          "legacy",
		Revision:    10,
		CreatedAt:   1,
		UpdatedAt:   2,
		Status:      "success",
		Model:       "deepseek-v4-flash",
		UserInput:   "old raw row",
		HistoryText: repeatedText("legacy-history-", 128),
		Content:     repeatedText("legacy-answer-", 128),
	}
	createLegacySQLiteHistory(t, dbPath, legacyEntry)

	store := NewSQLite(dbPath, filepath.Join(dir, "missing.json"))
	defer func() { _ = store.Close() }()
	if err := store.Err(); err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	got, err := store.Get(legacyEntry.ID)
	if err != nil {
		t.Fatalf("get migrated detail: %v", err)
	}
	if got.Content != legacyEntry.Content || got.HistoryText != legacyEntry.HistoryText {
		t.Fatalf("unexpected migrated detail: %#v", got)
	}
	assertSQLiteDetailCompressed(t, store, legacyEntry.ID)
}

func TestSQLiteStoreBurstPrunesAtMaxLimitAndKeepsCumulativeStats(t *testing.T) {
	dir := t.TempDir()
	store := NewSQLite(filepath.Join(dir, "chat_history.sqlite"), filepath.Join(dir, "missing.json"))
	defer func() { _ = store.Close() }()
	if err := store.Err(); err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	seedSQLiteHistoryRows(t, store.sqlite.db, MaxLimit-1)

	entry, err := store.Start(StartParams{Model: "deepseek-v4-pro", UserInput: "threshold"})
	if err != nil {
		t.Fatalf("start threshold row: %v", err)
	}
	_, retained, err := store.SnapshotPage(0, 1000)
	if err != nil {
		t.Fatalf("snapshot page: %v", err)
	}
	if retained != BurstPruneRetainedLimit {
		t.Fatalf("expected retained rows %d, got %d", BurstPruneRetainedLimit, retained)
	}
	if _, err := store.Get(entry.ID); err != nil {
		t.Fatalf("expected newest entry to be retained: %v", err)
	}
	if _, err := store.Get("seed_000001"); err == nil {
		t.Fatal("expected oldest entry to be pruned")
	}

	outcome, err := store.OutcomeStats()
	if err != nil {
		t.Fatalf("outcome stats: %v", err)
	}
	if outcome.Total != MaxLimit || outcome.RetainedTotal != BurstPruneRetainedLimit || outcome.PrunedTotal != MaxLimit-BurstPruneRetainedLimit {
		t.Fatalf("unexpected cumulative outcome totals: %#v", outcome)
	}
	if outcome.Success != MaxLimit-1 || outcome.Active != 1 {
		t.Fatalf("unexpected cumulative status counts: %#v", outcome)
	}
	tokens, err := store.TokenUsageStats(0)
	if err != nil {
		t.Fatalf("token stats: %v", err)
	}
	if tokens.Total.Requests != MaxLimit-1 || tokens.Total.TotalTokens != MaxLimit-1 {
		t.Fatalf("unexpected cumulative token totals: %#v", tokens.Total)
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	outcome, err = store.OutcomeStats()
	if err != nil {
		t.Fatalf("outcome stats after clear: %v", err)
	}
	if outcome.Total != 0 || outcome.PrunedTotal != 0 || outcome.RetainedTotal != 0 {
		t.Fatalf("expected clear to reset cumulative outcome stats, got %#v", outcome)
	}
}

func TestSQLiteDetailRejectsOversizedInflatedBlob(t *testing.T) {
	previous := sqliteDetailMaxInflatedBytes
	sqliteDetailMaxInflatedBytes = 64
	defer func() { sqliteDetailMaxInflatedBytes = previous }()

	entry := Entry{ID: "oversized", Revision: 1, Content: strings.Repeat("x", int(sqliteDetailMaxInflatedBytes)+1)}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal oversized detail: %v", err)
	}
	blob, err := gzipSQLiteDetail(raw)
	if err != nil {
		t.Fatalf("compress oversized detail: %v", err)
	}
	_, err = decodeSQLiteDetail("", sqliteDetailEncodingGzip, blob)
	if err == nil {
		t.Fatal("expected oversized inflated detail to be rejected")
	}
	if !strings.Contains(err.Error(), "inflated detail exceeds") {
		t.Fatalf("unexpected oversized detail error: %v", err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func repeatedText(prefix string, n int) string {
	out := make([]byte, 0, len(prefix)*n)
	for i := 0; i < n; i++ {
		out = append(out, prefix...)
	}
	return string(out)
}

func assertSQLiteDetailCompressed(t *testing.T, store *Store, id string) {
	t.Helper()
	var detailJSON, detailEncoding string
	var blobSize int
	if err := store.sqlite.db.QueryRow(
		`SELECT detail_json, detail_encoding, length(detail_blob) FROM chat_history WHERE id = ?`,
		id,
	).Scan(&detailJSON, &detailEncoding, &blobSize); err != nil {
		t.Fatalf("read sqlite detail storage: %v", err)
	}
	if detailJSON != "" {
		t.Fatalf("expected raw detail_json to be cleared, got %d bytes", len(detailJSON))
	}
	if detailEncoding != sqliteDetailEncodingGzip {
		t.Fatalf("detail_encoding=%q, want %q", detailEncoding, sqliteDetailEncodingGzip)
	}
	if blobSize <= 0 {
		t.Fatalf("expected compressed detail blob, got size %d", blobSize)
	}
}

func createLegacySQLiteHistory(t *testing.T, dbPath string, entry Entry) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`CREATE TABLE chat_history_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE chat_history (
			id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			completed_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			caller_id TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			stream INTEGER NOT NULL DEFAULT 0,
			user_input TEXT NOT NULL DEFAULT '',
			preview TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL DEFAULT 0,
			elapsed_ms INTEGER NOT NULL DEFAULT 0,
			finish_reason TEXT NOT NULL DEFAULT '',
			detail_revision INTEGER NOT NULL,
			usage_json TEXT NOT NULL DEFAULT '',
			detail_json TEXT NOT NULL
		)`,
		`INSERT INTO chat_history_meta(key, value) VALUES('version', '2'), ('limit', '20000'), ('revision', '10')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec legacy sqlite stmt: %v", err)
		}
	}
	detailJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal legacy detail: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO chat_history (
			id, revision, created_at, updated_at, completed_at, status, caller_id,
			account_id, model, stream, user_input, preview, status_code, elapsed_ms,
			finish_reason, detail_revision, usage_json, detail_json
		) VALUES (?, ?, ?, ?, ?, ?, '', '', ?, 0, ?, ?, 200, 0, 'stop', ?, '', ?)`,
		entry.ID,
		entry.Revision,
		entry.CreatedAt,
		entry.UpdatedAt,
		entry.CompletedAt,
		entry.Status,
		entry.Model,
		entry.UserInput,
		entry.Content,
		entry.Revision,
		string(detailJSON),
	); err != nil {
		t.Fatalf("insert legacy history row: %v", err)
	}
}

func seedSQLiteHistoryRows(t *testing.T, db *sql.DB, count int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin seed sqlite history: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(
		`INSERT INTO chat_history (
			id, revision, created_at, updated_at, completed_at, status, model,
			user_input, preview, status_code, detail_revision, usage_json
		) VALUES (?, ?, ?, ?, ?, 'success', 'deepseek-v4-pro', ?, 'seed', 200, ?, ?)`,
	)
	if err != nil {
		t.Fatalf("prepare seed sqlite history: %v", err)
	}
	defer func() { _ = stmt.Close() }()
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("seed_%06d", i)
		usageJSON := `{"input_tokens":1,"total_tokens":1}`
		if _, err := stmt.Exec(id, i, i, i, i, id, i, usageJSON); err != nil {
			t.Fatalf("insert seed sqlite history %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed sqlite history: %v", err)
	}
}
