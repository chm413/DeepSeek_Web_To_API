package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var Logger = newLogger()

const logFileEnv = "DEEPSEEK_WEB_TO_API_LOG_FILE"

var (
	loggerOutputMu   sync.Mutex
	loggerOutputFile *os.File
)

func newLogger() *slog.Logger {
	return newLoggerWithLevel(os.Getenv("LOG_LEVEL"))
}

func newLoggerWithLevel(raw string) *slog.Logger {
	level := new(slog.LevelVar)
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DEBUG":
		level.Set(slog.LevelDebug)
	case "WARN":
		level.Set(slog.LevelWarn)
	case "ERROR":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	h := slog.NewTextHandler(loggerOutput(), &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

// loggerOutput writes directly to a configured file when present. This keeps
// diagnostics available for detached Windows services where stdout cannot be
// safely redirected by the process launcher.
func loggerOutput() io.Writer {
	rawPath := strings.TrimSpace(os.Getenv(logFileEnv))
	if rawPath == "" {
		closeLoggerOutputFile()
		return os.Stdout
	}
	if !filepath.IsAbs(rawPath) {
		rawPath = filepath.Join(BaseDir(), rawPath)
	}
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o750); err != nil {
		return os.Stdout
	}
	file, err := os.OpenFile(rawPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return os.Stdout
	}

	loggerOutputMu.Lock()
	previous := loggerOutputFile
	loggerOutputFile = file
	loggerOutputMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return file
}

func closeLoggerOutputFile() {
	loggerOutputMu.Lock()
	previous := loggerOutputFile
	loggerOutputFile = nil
	loggerOutputMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
}

func RefreshLogger() {
	Logger = newLogger()
}

func RefreshLoggerWithLevel(raw string) {
	Logger = newLoggerWithLevel(raw)
}
