package xrayproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const defaultStartupTimeout = 10 * time.Second

type Settings struct {
	BinaryPath     string
	RuntimeDir     string
	StartupTimeout time.Duration
}

type Status struct {
	Available        bool     `json:"available"`
	BinaryPath       string   `json:"binary_path,omitempty"`
	Version          string   `json:"version,omitempty"`
	Error            string   `json:"error,omitempty"`
	RunningInstances int      `json:"running_instances"`
	SupportedTypes   []string `json:"supported_types"`
}

type instance struct {
	proxyID    string
	key        string
	address    string
	cmd        *exec.Cmd
	configPath string
	done       chan struct{}
	mu         sync.Mutex
	exitErr    error
}

func (i *instance) setExit(err error) {
	i.mu.Lock()
	i.exitErr = err
	i.mu.Unlock()
	close(i.done)
}

func (i *instance) exited() (bool, error) {
	select {
	case <-i.done:
		i.mu.Lock()
		defer i.mu.Unlock()
		return true, i.exitErr
	default:
		return false, nil
	}
}

type Manager struct {
	mu        sync.Mutex
	instances map[string]*instance
}

func NewManager() *Manager {
	return &Manager{instances: map[string]*instance{}}
}

var defaultManager = NewManager()

func Default() *Manager { return defaultManager }

func (m *Manager) Ensure(ctx context.Context, spec Spec, settings Settings) (string, error) {
	if m == nil {
		return "", errors.New("xray manager is nil")
	}
	spec = NormalizeSpec(spec)
	if spec.ID == "" {
		return "", errors.New("xray proxy id is required")
	}
	if _, err := BuildConfig(spec, 1); err != nil {
		return "", err
	}
	binaryPath, err := ResolveBinary(settings.BinaryPath)
	if err != nil {
		return "", err
	}
	settings.BinaryPath = binaryPath
	if settings.StartupTimeout <= 0 {
		settings.StartupTimeout = defaultStartupTimeout
	}
	key := instanceKey(spec, settings)

	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.instances[spec.ID]; current != nil {
		if current.key == key {
			if exited, exitErr := current.exited(); !exited {
				return current.address, nil
			} else {
				slog.Warn("[xray_proxy] process exited; restarting", "proxy_id", spec.ID, "error", exitErr)
			}
		}
		m.stopLocked(current)
	}

	localPort, err := availablePort()
	if err != nil {
		return "", err
	}
	configBytes, err := BuildConfig(spec, localPort)
	if err != nil {
		return "", err
	}
	runtimeDir, err := processRuntimeDir(settings.RuntimeDir)
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(runtimeDir, safeFileName(spec.ID)+"-"+key[:12]+".json")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		return "", fmt.Errorf("write xray runtime config: %w", err)
	}
	logPath := filepath.Join(runtimeDir, safeFileName(spec.ID)+"-"+key[:12]+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = os.Remove(configPath)
		return "", fmt.Errorf("open xray runtime log: %w", err)
	}

	cmd := exec.Command(binaryPath, "run", "-c", configPath)
	cmd.Dir = filepath.Dir(binaryPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(binaryPath))
	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = os.Remove(configPath)
		return "", fmt.Errorf("start xray core: %w", err)
	}
	current := &instance{
		proxyID:    spec.ID,
		key:        key,
		address:    net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", localPort)),
		cmd:        cmd,
		configPath: configPath,
		done:       make(chan struct{}),
	}
	m.instances[spec.ID] = current
	go func() {
		waitErr := cmd.Wait()
		if closeErr := logFile.Close(); closeErr != nil {
			slog.Warn("[xray_proxy] close runtime log failed", "proxy_id", spec.ID, "error", closeErr)
		}
		current.setExit(waitErr)
		_ = os.Remove(configPath)
	}()

	startupCtx, cancel := context.WithTimeout(ctx, settings.StartupTimeout)
	defer cancel()
	if err := waitForSOCKS(startupCtx, current); err != nil {
		slog.Error("[xray_proxy] core startup failed", "proxy_id", spec.ID, "proxy_type", spec.Type, "runtime_log", logPath, "error", err)
		m.stopLocked(current)
		return "", fmt.Errorf("%w; see xray runtime log %s", err, logPath)
	}
	slog.Info("[xray_proxy] core started", "proxy_id", spec.ID, "proxy_type", spec.Type, "local_socks", current.address, "pid", cmd.Process.Pid)
	return current.address, nil
}

func (m *Manager) Stop(proxyID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.instances[strings.TrimSpace(proxyID)]; current != nil {
		m.stopLocked(current)
	}
}

func (m *Manager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, current := range m.instances {
		m.stopLocked(current)
	}
}

func (m *Manager) Count() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for proxyID, current := range m.instances {
		if exited, _ := current.exited(); exited {
			delete(m.instances, proxyID)
			continue
		}
		count++
	}
	return count
}

func (m *Manager) stopLocked(current *instance) {
	if current == nil {
		return
	}
	delete(m.instances, current.proxyID)
	if current.cmd != nil && current.cmd.Process != nil {
		if err := current.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			slog.Warn("[xray_proxy] stop core failed", "proxy_id", current.proxyID, "error", err)
		}
	}
	_ = os.Remove(current.configPath)
	slog.Info("[xray_proxy] core stopped", "proxy_id", current.proxyID)
}

func ResolveBinary(explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		candidate := os.ExpandEnv(value)
		if resolved, ok := executableFile(candidate); ok {
			return resolved, nil
		}
		return "", fmt.Errorf("configured xray core executable was not found: %s", candidate)
	}
	if value := strings.TrimSpace(os.Getenv("XRAY_BINARY_PATH")); value != "" {
		candidate := os.ExpandEnv(value)
		if resolved, ok := executableFile(candidate); ok {
			return resolved, nil
		}
		return "", fmt.Errorf("xray executable from XRAY_BINARY_PATH was not found: %s", candidate)
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		name := "xray"
		if runtime.GOOS == "windows" {
			name = "xray.exe"
		}
		base := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(base, name), filepath.Join(base, "bin", name))
	}
	for _, candidate := range candidates {
		if resolved, ok := executableFile(candidate); ok {
			return resolved, nil
		}
	}
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	if resolved, err := exec.LookPath(name); err == nil {
		return filepath.Abs(resolved)
	}
	return "", errors.New("xray core executable was not found; set proxy_core.xray_binary_path or XRAY_BINARY_PATH")
}

func Probe(ctx context.Context, settings Settings) Status {
	status := Status{
		RunningInstances: Default().Count(),
		SupportedTypes:   []string{"vless", "vmess", "hysteria2"},
	}
	binaryPath, err := ResolveBinary(settings.BinaryPath)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.BinaryPath = binaryPath
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, binaryPath, "version")
	configureCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		status.Error = fmt.Sprintf("run Xray version: %v", err)
		return status
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 0 {
		status.Version = strings.TrimSpace(lines[0])
	}
	status.Available = true
	return status
}

func waitForSOCKS(ctx context.Context, current *instance) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := net.DialTimeout("tcp", current.address, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if exited, exitErr := current.exited(); exited {
			if exitErr == nil {
				return errors.New("xray core exited before SOCKS listener became ready")
			}
			return fmt.Errorf("xray core exited before SOCKS listener became ready: %w", exitErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for xray SOCKS listener: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve xray SOCKS port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func processRuntimeDir(base string) (string, error) {
	base = strings.TrimSpace(os.ExpandEnv(base))
	if base == "" {
		base = filepath.Join(os.TempDir(), "DeepSeek_Web_To_API-xray")
	}
	dir := filepath.Join(base, fmt.Sprintf("process-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create xray runtime directory: %w", err)
	}
	return dir, nil
}

func instanceKey(spec Spec, settings Settings) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		spec.ID,
		spec.Type,
		spec.URI,
		settings.BinaryPath,
		settings.RuntimeDir,
		settings.StartupTimeout.String(),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func safeFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "proxy"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "proxy"
	}
	return b.String()
}

func executableFile(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return "", false
	}
	return abs, true
}
