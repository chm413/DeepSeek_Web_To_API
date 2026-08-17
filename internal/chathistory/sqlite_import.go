package chathistory

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

func (s *sqliteStore) importLegacyIfEmptyLocked(tx *sql.Tx) error {
	legacyPath := strings.TrimSpace(s.legacyPath)
	if legacyPath == "" {
		return nil
	}
	raw, err := os.ReadFile(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy chat history index: %w", err)
	}
	hash := sha256.Sum256(raw)
	hashText := hex.EncodeToString(hash[:])
	if previous, metaErr := s.metaLocked(tx, legacyJSONImportHashMeta); metaErr == nil && strings.TrimSpace(previous) == hashText {
		return nil
	}
	entries, legacyLimit, err := s.legacyEntries(raw)
	if err != nil {
		return err
	}
	if !isAllowedLimit(legacyLimit) {
		legacyLimit = DefaultLimit
	}
	if err := s.setMetaLocked(tx, "limit", fmt.Sprintf("%d", legacyLimit)); err != nil {
		return err
	}
	sortEntriesNewestFirst(entries)
	if legacyLimit != DisabledLimit && len(entries) > legacyLimit {
		entries = entries[:legacyLimit]
	}
	maxRevision := int64(0)
	imported := 0
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		if entry.Revision <= 0 {
			entry.Revision = fallbackRevision(entry)
		}
		if entry.Revision > maxRevision {
			maxRevision = entry.Revision
		}
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM chat_history WHERE id = ? LIMIT 1`, entry.ID).Scan(&exists); err == nil {
			// The SQLite copy is authoritative once it contains an entry. This
			// keeps a stale legacy JSON file from overwriting newer responses.
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check legacy chat history entry %q: %w", entry.ID, err)
		}
		if err := s.upsertEntryLocked(tx, entry); err != nil {
			return err
		}
		imported++
	}
	if err := s.bumpRevisionAtLeastLocked(tx, maxRevision); err != nil {
		return err
	}
	if err := s.setMetaLocked(tx, legacyJSONImportHashMeta, hashText); err != nil {
		return err
	}
	if imported > 0 {
		config.Logger.Info("[chat_history] imported legacy JSON into SQLite", "sqlite", s.path, "legacy", legacyPath, "count", imported, "scanned", len(entries), "limit", legacyLimit)
	}
	return nil
}

func (s *sqliteStore) legacyEntries(raw []byte) ([]Entry, int, error) {
	if legacy, ok, err := parseLegacy(raw); err != nil {
		return nil, 0, err
	} else if ok {
		entries := make([]Entry, 0, len(legacy.Items))
		for _, item := range legacy.Items {
			if strings.TrimSpace(item.ID) == "" {
				continue
			}
			item.Messages = cloneMessages(item.Messages)
			entries = append(entries, cloneEntry(item))
		}
		return entries, legacy.Limit, nil
	}
	var state File
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, 0, fmt.Errorf("decode legacy chat history index: %w", err)
	}
	entries := make([]Entry, 0, len(state.Items))
	for _, summary := range state.Items {
		if strings.TrimSpace(summary.ID) == "" {
			continue
		}
		entry, err := readDetailFile(s.legacyDetailDir, summary.ID)
		if err != nil {
			entry = entryFromSummary(summary)
		}
		entries = append(entries, cloneEntry(entry))
	}
	return entries, state.Limit, nil
}

func (s *sqliteStore) stopUnfinishedLocked(tx *sql.Tx, reason string) error {
	rows, err := tx.Query(`SELECT detail_json, detail_encoding, detail_blob FROM chat_history WHERE status IN ('queued', 'streaming')`)
	if err != nil {
		return fmt.Errorf("read unfinished chat history sqlite rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []Entry{}
	for rows.Next() {
		var detailJSON, detailEncoding string
		var detailBlob []byte
		if err := rows.Scan(&detailJSON, &detailEncoding, &detailBlob); err != nil {
			return fmt.Errorf("scan unfinished chat history sqlite row: %w", err)
		}
		item, err := decodeSQLiteDetail(detailJSON, detailEncoding, detailBlob)
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan unfinished chat history sqlite rows: %w", err)
	}
	now := time.Now().UnixMilli()
	for _, item := range items {
		revision, err := s.nextRevisionLocked(tx)
		if err != nil {
			return err
		}
		item.Revision = revision
		item.UpdatedAt = now
		if item.CompletedAt == 0 {
			item.CompletedAt = now
		}
		if item.ElapsedMs <= 0 && item.CreatedAt > 0 && now >= item.CreatedAt {
			item.ElapsedMs = now - item.CreatedAt
		}
		item.Status = "stopped"
		item.FinishReason = "server_restart"
		if strings.TrimSpace(item.Error) == "" {
			item.Error = reason
		}
		if err := s.upsertEntryLocked(tx, item); err != nil {
			return err
		}
	}
	return nil
}

func sortEntriesNewestFirst(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].UpdatedAt == entries[j].UpdatedAt {
			return entries[i].CreatedAt > entries[j].CreatedAt
		}
		return entries[i].UpdatedAt > entries[j].UpdatedAt
	})
}

func fallbackRevision(item Entry) int64 {
	if item.UpdatedAt > 0 {
		return item.UpdatedAt
	}
	return time.Now().UnixNano()
}
