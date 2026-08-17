package config

import "testing"

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
