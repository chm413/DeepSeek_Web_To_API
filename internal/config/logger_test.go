package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerWritesToConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "service.log")
	t.Setenv(logFileEnv, path)
	logger := newLoggerWithLevel("INFO")
	logger.Info("logger file test", "trace_id", "trace-test")
	closeLoggerOutputFile()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read logger output: %v", err)
	}
	if !strings.Contains(string(content), "logger file test") || !strings.Contains(string(content), "trace-test") {
		t.Fatalf("logger output missing expected fields: %s", content)
	}
}
