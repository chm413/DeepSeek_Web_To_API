package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func loadStore() (*Store, error) {
	cfg, fromEnv, sourcePath, err := loadConfigWithSource()
	cfg.NormalizeCredentials()
	accountsDB, accounts, accountsErr := newAccountSQLiteStore(accountSQLitePathForConfig(cfg, fromEnv), cfg.Accounts)
	if accountsErr != nil {
		err = errors.Join(err, accountsErr)
	} else if accountsDB != nil {
		cfg.Accounts = accounts
	}
	migrationReport, migrationErr := ApplyConfigMigrations(&cfg)
	if migrationErr != nil {
		err = errors.Join(err, migrationErr)
	}
	validateErr := ValidateConfig(cfg)
	if validateErr != nil {
		err = errors.Join(err, validateErr)
	}
	path := ConfigPath()
	if migrationErr == nil && !fromEnv && sourcePath != "" && configHasLegacyRuntimeFields(sourcePath) {
		markConfigMigrationChange(&migrationReport, "removed deprecated runtime-only account fields")
	}
	if migrationErr == nil && !fromEnv && sourcePath != "" && accountsDB != nil && configHasInlineAccounts(sourcePath) {
		// A config can already carry the current schema while still containing
		// account credentials from before accounts.sqlite was introduced. Once
		// the seed/import has succeeded, schedule a writeback so that schema
		// equality never leaves those credentials duplicated on disk.
		markConfigMigrationChange(&migrationReport, "moved inline accounts into accounts SQLite")
	}
	if migrationErr == nil && !fromEnv && configMigrationNeedsRelocation(sourcePath, path) {
		markConfigMigrationRelocation(&migrationReport, sourcePath, path)
	}
	if migrationErr == nil && validateErr == nil && migrationReport.Changed && !fromEnv && sourcePath != "" {
		if persistErr := persistConfigMigration(path, sourcePath, cfg, accountsDB != nil, migrationReport); persistErr != nil {
			// A migration writeback must never make an otherwise valid runtime
			// unusable. Keep the upgraded snapshot in memory and retry safely on
			// the next startup after logging the backup/write failure.
			Logger.Warn("[config] migration writeback deferred", "error", persistErr, "path", path)
		}
	}
	return &Store{cfg: cfg, path: path, fromEnv: fromEnv, accountsDB: accountsDB}, err
}

func loadConfig() (Config, bool, error) {
	cfg, fromEnv, _, err := loadConfigWithSource()
	return cfg, fromEnv, err
}

// loadConfigWithSource keeps the original file path alongside the decoded
// config. A startup migration can therefore back up an immutable legacy
// /app/config.json before writing the upgraded copy into /app/data/config.json.
func loadConfigWithSource() (Config, bool, string, error) {
	rawCfg := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON"))
	if rawCfg != "" {
		cfg, fromEnv, err := loadConfigFromEnv(rawCfg)
		if fromEnv {
			return cfg, true, "", err
		}
		return cfg, false, ConfigPath(), err
	}
	return loadConfigFromPrimaryFileWithSource()
}

func loadConfigFromEnv(rawCfg string) (Config, bool, error) {
	cfg, err := parseConfigString(rawCfg)
	if err != nil {
		if envWritebackEnabled() {
			if fileCfg, fileErr := loadConfigFromFile(ConfigPath()); fileErr == nil {
				return fileCfg, false, nil
			}
		}
		return cfg, true, err
	}
	cfg.ClearAccountTokens()
	cfg.DropInvalidAccounts()
	if !envWritebackEnabled() {
		return cfg, true, err
	}
	return loadOrBootstrapEnvWritebackConfig(cfg, err)
}

func loadOrBootstrapEnvWritebackConfig(cfg Config, parseErr error) (Config, bool, error) {
	// #nosec G304 -- ConfigPath is an operator-controlled local config path.
	content, fileErr := os.ReadFile(ConfigPath())
	if fileErr == nil {
		var fileCfg Config
		if unmarshalErr := json.Unmarshal(content, &fileCfg); unmarshalErr == nil {
			fileCfg.DropInvalidAccounts()
			return fileCfg, false, parseErr
		}
	}
	if errors.Is(fileErr, os.ErrNotExist) {
		if _, migrationErr := ApplyConfigMigrations(&cfg); migrationErr != nil {
			return cfg, true, migrationErr
		}
		if validateErr := ValidateConfig(cfg); validateErr != nil {
			return cfg, true, validateErr
		}
		if writeErr := writeConfigFile(ConfigPath(), cfg.Clone()); writeErr == nil {
			return cfg, false, parseErr
		} else {
			Logger.Warn("[config] env writeback bootstrap failed", "error", writeErr)
		}
	}
	return cfg, true, parseErr
}

func loadConfigFromPrimaryFileWithSource() (Config, bool, string, error) {
	path := ConfigPath()
	cfg, err := loadConfigFromFile(path)
	if err != nil {
		if legacyPath := legacyContainerConfigFallbackPath(path); legacyPath != "" {
			if legacyCfg, legacyErr := loadConfigFromFile(legacyPath); legacyErr == nil {
				Logger.Info("[config] loaded legacy container config path", "path", legacyPath)
				return legacyCfg, false, legacyPath, nil
			}
		}
		if legacyCfg, ok := loadLegacyContainerConfigIfNeeded(); ok {
			return legacyCfg, false, legacyContainerConfigPath(), nil
		}
		return Config{}, false, "", err
	}
	return cfg, false, path, nil
}

func loadLegacyContainerConfigIfNeeded() (Config, bool) {
	if !shouldTryLegacyContainerConfigPath() {
		return Config{}, false
	}
	legacyPath := legacyContainerConfigPath()
	legacyCfg, legacyErr := loadConfigFromFile(legacyPath)
	if legacyErr != nil {
		return Config{}, false
	}
	Logger.Info("[config] loaded legacy container config path", "path", legacyPath)
	return legacyCfg, true
}

func loadConfigFromFile(path string) (Config, error) {
	// #nosec G304 -- config loading reads an operator-controlled local config path.
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(content, &cfg); err != nil {
		return Config{}, err
	}
	cfg.NormalizeCredentials()
	cfg.DropInvalidAccounts()
	return cfg, nil
}

func configHasLegacyRuntimeFields(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), `"test_status"`)
}

func configHasInlineAccounts(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(content, &document); err != nil {
		return false
	}
	raw, ok := document["accounts"]
	if !ok || rawJSONMissingOrNull(raw) {
		return false
	}
	var accounts []json.RawMessage
	return json.Unmarshal(raw, &accounts) == nil && len(accounts) > 0
}

func configMigrationNeedsRelocation(source, destination string) bool {
	source = strings.TrimSpace(source)
	destination = strings.TrimSpace(destination)
	if source == "" || destination == "" {
		return false
	}
	return filepath.Clean(source) != filepath.Clean(destination)
}
