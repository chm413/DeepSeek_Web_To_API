package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func envWritebackEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (s *Store) IsEnvWritebackEnabled() bool {
	return envWritebackEnabled()
}

func (s *Store) HasEnvConfigSource() bool {
	rawCfg := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON"))
	return rawCfg != ""
}

func (s *Store) ConfigPath() string {
	return s.path
}

func writeConfigFile(path string, cfg Config) error {
	persistCfg := cfg.Clone()
	persistCfg.ClearAccountTokens()
	b, err := json.MarshalIndent(persistCfg, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigBytes(path, b)
}

func writeConfigBytes(path string, b []byte) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	// Write and fsync a sibling temporary file before publishing it. A direct
	// os.WriteFile(path, ...) can leave a truncated config after a process or
	// machine failure while the file is being replaced.
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		if runtime.GOOS != "windows" {
			// Never fall back to direct os.WriteFile or remove the existing target:
			// either operation can leave a truncated/missing config after a
			// process stop. Preserve the last known-good file and let the caller
			// retry on the next update cycle.
			return fmt.Errorf("publish config atomically: %w", err)
		}
		// Windows may reject replacing an existing file with Rename. ReplaceFile
		// performs the replacement in one system call and writes the old target
		// to the explicit sibling backup. This avoids direct truncating writes.
		backup, backupErr := os.CreateTemp(dir, ".config-backup-*.tmp")
		if backupErr != nil {
			return fmt.Errorf("publish config atomically: %w", err)
		}
		backupName := backup.Name()
		if closeErr := backup.Close(); closeErr != nil {
			_ = os.Remove(backupName)
			return fmt.Errorf("publish config atomically: %w", err)
		}
		if removeErr := os.Remove(backupName); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("publish config atomically: %w", err)
		}
		if replaceErr := replaceConfigTarget(tmpName, path, backupName); replaceErr != nil {
			return fmt.Errorf("publish config atomically: %w", replaceErr)
		}
		_ = os.Remove(backupName)
	}
	keepTemp = true
	// Best-effort directory metadata flush. Some platforms (notably Windows)
	// do not allow opening a directory for Sync, so failure is intentionally
	// ignored after the file itself has been synced.
	if directory, openErr := os.Open(dir); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
