package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CurrentConfigSchemaVersion is independent from the application release
// version. Release versions can change frequently while a config migration
// must remain monotonic and safe to retry.
const CurrentConfigSchemaVersion = 2

const (
	defaultMigrationBackupKeep          = 1
	defaultMigrationBackupRetentionDays = 7
	maxMigrationBackupKeep              = 32
	maxMigrationBackupRetentionDays     = 3650
)

type ConfigMigrationReport struct {
	FromVersion int
	ToVersion   int
	Changed     bool
	Changes     []string
}

// ApplyConfigMigrations upgrades an in-memory config without dropping unknown
// top-level fields. It is deliberately idempotent so a failed writeback can be
// retried on the next startup.
func ApplyConfigMigrations(cfg *Config) (ConfigMigrationReport, error) {
	if cfg == nil {
		return ConfigMigrationReport{}, errors.New("config migration received nil config")
	}
	from := cfg.ConfigSchemaVersion
	if from < 0 {
		return ConfigMigrationReport{}, fmt.Errorf("config_schema_version must not be negative: %d", from)
	}
	if from > CurrentConfigSchemaVersion {
		return ConfigMigrationReport{}, fmt.Errorf("config_schema_version %d is newer than this binary supports (%d)", from, CurrentConfigSchemaVersion)
	}
	report := ConfigMigrationReport{FromVersion: from, ToVersion: from}
	mark := func(change string) {
		report.Changed = true
		report.Changes = append(report.Changes, change)
	}

	if cfg.ConfigSchemaVersion < 1 {
		cfg.ConfigSchemaVersion = 1
		report.ToVersion = 1
		mark("recorded the initial config schema version")
	}

	// v2 introduced the server-side update checker. Missing fields retain the
	// documented conservative policy: checking on, automatic download/apply
	// off, and a six-hour poll interval.
	if cfg.AppUpdate.Enabled == nil {
		cfg.AppUpdate.Enabled = migrationBoolPtr(true)
		mark("enabled the server-side update checker by default")
	}
	if cfg.AppUpdate.AutoDownload == nil {
		cfg.AppUpdate.AutoDownload = migrationBoolPtr(false)
		mark("disabled automatic release downloads by default")
	}
	if cfg.AppUpdate.AutoApply == nil {
		cfg.AppUpdate.AutoApply = migrationBoolPtr(false)
		mark("disabled automatic release application by default")
	}
	if cfg.AppUpdate.CheckIntervalMinutes <= 0 {
		cfg.AppUpdate.CheckIntervalMinutes = 360
		mark("set the default update check interval to 360 minutes")
	}
	if cfg.ConfigSchemaVersion < CurrentConfigSchemaVersion {
		cfg.ConfigSchemaVersion = CurrentConfigSchemaVersion
		report.ToVersion = CurrentConfigSchemaVersion
		mark("migrated app_update settings to schema version 2")
	}
	if report.ToVersion == 0 {
		report.ToVersion = cfg.ConfigSchemaVersion
	}
	return report, nil
}

func migrationBoolPtr(value bool) *bool {
	return &value
}

// persistConfigMigration writes the migrated config only after a private
// backup has been created. The source can differ from destination when an old
// container used /app/config.json and the new layout uses /data/config.json.
func persistConfigMigration(destination, source string, cfg Config, accountsDB bool, report ConfigMigrationReport) error {
	destination = strings.TrimSpace(destination)
	source = strings.TrimSpace(source)
	if destination == "" || !report.Changed {
		return nil
	}
	if source == "" {
		source = destination
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read config before migration: %w", err)
	}
	backupPath, err := backupMigratedConfig(destination, raw, report.FromVersion)
	if err != nil {
		return err
	}

	// Patch the raw document instead of serializing Config back wholesale.
	// This preserves unknown nested settings written by older/newer releases;
	// Config.UnmarshalJSON can intentionally only retain unknown top-level
	// fields in its typed runtime representation.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode config for migration patch: %w", err)
	}
	if document == nil {
		document = map[string]json.RawMessage{}
	}
	if err := patchConfigMigrationDocument(document, cfg, accountsDB); err != nil {
		return err
	}
	b, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal migrated config: %w", err)
	}
	if err := writeConfigBytes(destination, b); err != nil {
		return fmt.Errorf("write migrated config (backup %s): %w", backupPath, err)
	}
	if err := pruneMigrationBackups(filepath.Dir(destination), time.Now()); err != nil {
		// The migrated config is already valid and durable. A cleanup failure
		// must not roll it back, but it is important to surface the retained
		// sensitive backup so the operator can remove it manually.
		Logger.Warn("[config] migration backup cleanup deferred", "error", err, "directory", filepath.Dir(destination))
	}
	Logger.Info("[config] migrated legacy config", "from_version", report.FromVersion, "to_version", report.ToVersion, "changes", report.Changes, "source", source, "destination", destination, "backup", backupPath)
	return nil
}

func patchConfigMigrationDocument(document map[string]json.RawMessage, cfg Config, accountsDB bool) error {
	version, err := json.Marshal(cfg.ConfigSchemaVersion)
	if err != nil {
		return fmt.Errorf("encode config schema version: %w", err)
	}
	document["config_schema_version"] = version

	update := map[string]json.RawMessage{}
	if raw, ok := document["app_update"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &update); err != nil {
			return fmt.Errorf("decode app_update for migration patch: %w", err)
		}
	}
	setDefault := func(key string, value any) error {
		if raw, exists := update[key]; exists && !rawJSONMissingOrNull(raw) {
			return nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		update[key] = encoded
		return nil
	}
	if err := setDefault("enabled", dereferenceBool(cfg.AppUpdate.Enabled, true)); err != nil {
		return fmt.Errorf("encode app_update.enabled: %w", err)
	}
	if err := setDefault("auto_download", dereferenceBool(cfg.AppUpdate.AutoDownload, false)); err != nil {
		return fmt.Errorf("encode app_update.auto_download: %w", err)
	}
	if err := setDefault("auto_apply", dereferenceBool(cfg.AppUpdate.AutoApply, false)); err != nil {
		return fmt.Errorf("encode app_update.auto_apply: %w", err)
	}
	if err := setPositiveDefault(update, "check_interval_minutes", cfg.AppUpdate.CheckIntervalMinutes); err != nil {
		return fmt.Errorf("encode app_update.check_interval_minutes: %w", err)
	}
	encodedUpdate, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("encode app_update migration patch: %w", err)
	}
	document["app_update"] = encodedUpdate
	if accountsDB {
		// The SQLite seed/import completed before this write. Removing only this
		// field prevents password/token duplication without touching unrelated
		// legacy or future settings in the raw document.
		delete(document, "accounts")
		return nil
	}
	if err := removeLegacyAccountRuntimeFields(document); err != nil {
		return err
	}
	return nil
}

func rawJSONMissingOrNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

func setPositiveDefault(document map[string]json.RawMessage, key string, value int) error {
	if raw, exists := document[key]; exists && !rawJSONMissingOrNull(raw) {
		var existing int
		if err := json.Unmarshal(raw, &existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	document[key] = encoded
	return nil
}

// removeLegacyAccountRuntimeFields clears values that were never part of the
// persisted account contract. The migration patches raw JSON to preserve
// unknown fields, so this cleanup has to happen explicitly instead of relying
// on Config's typed marshal path to omit test_status.
func removeLegacyAccountRuntimeFields(document map[string]json.RawMessage) error {
	rawAccounts, ok := document["accounts"]
	if !ok || len(rawAccounts) == 0 || string(rawAccounts) == "null" {
		return nil
	}
	var accounts []map[string]json.RawMessage
	if err := json.Unmarshal(rawAccounts, &accounts); err != nil {
		return fmt.Errorf("decode accounts for migration patch: %w", err)
	}
	changed := false
	for _, account := range accounts {
		if _, ok := account["test_status"]; ok {
			delete(account, "test_status")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(accounts)
	if err != nil {
		return fmt.Errorf("encode accounts for migration patch: %w", err)
	}
	document["accounts"] = encoded
	return nil
}

func markConfigMigrationChange(report *ConfigMigrationReport, change string) {
	if report == nil {
		return
	}
	report.Changed = true
	for _, existing := range report.Changes {
		if existing == change {
			return
		}
	}
	report.Changes = append(report.Changes, change)
}

func markConfigMigrationRelocation(report *ConfigMigrationReport, source, destination string) {
	if configMigrationNeedsRelocation(source, destination) {
		markConfigMigrationChange(report, "moved legacy config into the persistent config path")
	}
}

func dereferenceBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func backupMigratedConfig(destination string, content []byte, fromVersion int) (string, error) {
	dir := filepath.Join(filepath.Dir(destination), "migrations", "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config migration backup directory: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(dir, "config-v"+strconv.Itoa(fromVersion)+"-"+stamp+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create config migration backup: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				Logger.Warn("[config] failed to remove incomplete migration backup", "path", path, "error", err)
			}
		}
	}()
	if _, err := file.Write(content); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			Logger.Warn("[config] failed to close incomplete migration backup", "path", path, "error", closeErr)
		}
		return "", fmt.Errorf("write config migration backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			Logger.Warn("[config] failed to close unsynced migration backup", "path", path, "error", closeErr)
		}
		return "", fmt.Errorf("sync config migration backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close config migration backup: %w", err)
	}
	ok = true
	return path, nil
}

type migrationBackupEntry struct {
	path    string
	modTime time.Time
}

// pruneMigrationBackups removes stale migration copies that may contain
// passwords, tokens, and API keys. It keeps only the newest configured number
// of fresh backups and never follows symlinks. A failed cleanup is returned so
// callers can log it without making a successful migration unusable.
func pruneMigrationBackups(configDir string, now time.Time) error {
	dir := filepath.Join(configDir, "migrations", "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	backups := make([]migrationBackupEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "config-v") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect migration backup %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		backups = append(backups, migrationBackupEntry{path: filepath.Join(dir, name), modTime: info.ModTime()})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].modTime.Equal(backups[j].modTime) {
			return backups[i].path > backups[j].path
		}
		return backups[i].modTime.After(backups[j].modTime)
	})
	keep := migrationBackupKeep()
	cutoff := now.Add(-migrationBackupRetention())
	kept := 0
	for _, backup := range backups {
		if kept < keep && !backup.modTime.Before(cutoff) {
			kept++
			continue
		}
		if err := os.Remove(backup.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove migration backup %s: %w", filepath.Base(backup.path), err)
		}
	}
	return nil
}

func migrationBackupKeep() int {
	value := defaultMigrationBackupKeep
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_MIGRATION_BACKUP_KEEP")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= maxMigrationBackupKeep {
			value = parsed
		}
	}
	return value
}

func migrationBackupRetention() time.Duration {
	days := defaultMigrationBackupRetentionDays
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_MIGRATION_BACKUP_RETENTION_DAYS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= maxMigrationBackupRetentionDays {
			days = parsed
		}
	}
	return time.Duration(days) * 24 * time.Hour
}
