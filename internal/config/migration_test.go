package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyConfigMigrationsIsIdempotent(t *testing.T) {
	cfg := Config{}
	first, err := ApplyConfigMigrations(&cfg)
	if err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	if !first.Changed || cfg.ConfigSchemaVersion != CurrentConfigSchemaVersion {
		t.Fatalf("initial migration did not update schema: report=%#v config=%#v", first, cfg)
	}
	if cfg.AppUpdate.Enabled == nil || !*cfg.AppUpdate.Enabled ||
		cfg.AppUpdate.AutoDownload == nil || *cfg.AppUpdate.AutoDownload ||
		cfg.AppUpdate.AutoApply == nil || *cfg.AppUpdate.AutoApply ||
		cfg.AppUpdate.CheckIntervalMinutes != 360 {
		t.Fatalf("unexpected migrated app_update defaults: %#v", cfg.AppUpdate)
	}

	second, err := ApplyConfigMigrations(&cfg)
	if err != nil {
		t.Fatalf("apply idempotent migration: %v", err)
	}
	if second.Changed {
		t.Fatalf("migrating an already-current config changed it: %#v", second)
	}
}

func TestApplyConfigMigrationsRejectsFutureSchema(t *testing.T) {
	cfg := Config{ConfigSchemaVersion: CurrentConfigSchemaVersion + 1}
	if _, err := ApplyConfigMigrations(&cfg); err == nil {
		t.Fatal("expected a newer config schema to be rejected")
	}
}

func TestPruneMigrationBackupsRemovesStaleAndExcessCopies(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_MIGRATION_BACKUP_KEEP", "2")
	t.Setenv("DEEPSEEK_WEB_TO_API_MIGRATION_BACKUP_RETENTION_DAYS", "7")
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "migrations", "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatalf("create backup directory: %v", err)
	}
	now := time.Now()
	for i := 0; i < 4; i++ {
		path := filepath.Join(backupDir, "config-v2-20260101T00000000"+string(rune('0'+i))+"Z.json")
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write backup %d: %v", i, err)
		}
		modTime := now.Add(-time.Duration(i) * time.Hour)
		if i == 3 {
			modTime = now.Add(-8 * 24 * time.Hour)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("set backup time %d: %v", i, err)
		}
	}
	if err := pruneMigrationBackups(dir, now); err != nil {
		t.Fatalf("prune backups: %v", err)
	}
	remaining, err := filepath.Glob(filepath.Join(backupDir, "config-v*.json"))
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining backups=%d, want 2: %v", len(remaining), remaining)
	}
}
