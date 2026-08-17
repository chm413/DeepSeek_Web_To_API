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
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultStartupTimeout = 10 * time.Second

type Settings struct {
	BinaryPath      string
	RuntimeDir      string
	StartupTimeout  time.Duration
	AutoDownload    bool
	DownloadDir     string
	DownloadVersion string
	// PersistInstallation is set by store-aware callers. It receives only
	// local core metadata after an automatic download or managed-core reuse.
	PersistInstallation func(Installation) error
}

// Installation describes a managed Xray installation without exposing proxy
// credentials or node configuration.
type Installation struct {
	BinaryPath  string
	DownloadDir string
	Version     string
}

type Status struct {
	Available        bool     `json:"available"`
	BinaryPath       string   `json:"binary_path,omitempty"`
	Version          string   `json:"version,omitempty"`
	Error            string   `json:"error,omitempty"`
	RunningInstances int      `json:"running_instances"`
	ActiveRoutes     int      `json:"active_routes"`
	SupportedTypes   []string `json:"supported_types"`
}

type routeState struct {
	spec    Spec
	port    int
	address string
}

type instance struct {
	key        string
	cmd        *exec.Cmd
	configPath string
	logPath    string
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
	mu       sync.Mutex
	routes   map[string]routeState
	current  *instance
	settings Settings
}

func NewManager() *Manager {
	return &Manager{routes: map[string]routeState{}}
}

var defaultManager = NewManager()

func Default() *Manager { return defaultManager }

func (m *Manager) Ensure(ctx context.Context, spec Spec, settings Settings) (string, error) {
	addresses, err := m.EnsureMany(ctx, []Spec{spec}, settings)
	if err != nil {
		return "", err
	}
	return addresses[NormalizeSpec(spec).ID], nil
}

func (m *Manager) EnsureMany(ctx context.Context, specs []Spec, settings Settings) (map[string]string, error) {
	if m == nil {
		return nil, errors.New("xray manager is nil")
	}
	normalized, err := normalizeSpecs(specs)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.routes == nil {
		m.routes = map[string]routeState{}
	}
	for _, spec := range normalized {
		current, exists := m.routes[spec.ID]
		if exists && sameSpec(current.spec, spec) {
			continue
		}
		port := current.port
		if port == 0 {
			port, err = availableRoutePort(m.routes)
			if err != nil {
				return nil, err
			}
		}
		m.routes[spec.ID] = routeState{
			spec:    spec,
			port:    port,
			address: net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)),
		}
	}
	if err := m.restartLocked(ctx, settings); err != nil {
		return nil, err
	}
	return m.addressesLocked(normalized), nil
}

func (m *Manager) Sync(ctx context.Context, specs []Spec, settings Settings) (map[string]string, error) {
	if m == nil {
		return nil, errors.New("xray manager is nil")
	}
	normalized, err := normalizeSpecs(specs)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]routeState, len(normalized))
	for _, spec := range normalized {
		port := 0
		if current, exists := m.routes[spec.ID]; exists {
			port = current.port
		}
		if port == 0 {
			port, err = availableRoutePort(next)
			if err != nil {
				return nil, err
			}
		}
		next[spec.ID] = routeState{
			spec:    spec,
			port:    port,
			address: net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)),
		}
	}
	m.routes = next
	if err := m.restartLocked(ctx, settings); err != nil {
		return nil, err
	}
	return m.addressesLocked(normalized), nil
}

func (m *Manager) Stop(proxyID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.routes, strings.TrimSpace(proxyID))
	ctx, cancel := context.WithTimeout(context.Background(), effectiveStartupTimeout(m.settings.StartupTimeout))
	defer cancel()
	if err := m.restartLocked(ctx, m.settings); err != nil {
		slog.Error("[xray_proxy] rebuild after route removal failed", "proxy_id", proxyID, "error", err)
	}
}

func (m *Manager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = map[string]routeState{}
	m.stopCurrentLocked()
}

func (m *Manager) Count() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return 0
	}
	if exited, _ := m.current.exited(); exited {
		m.current = nil
		return 0
	}
	return 1
}

func (m *Manager) RouteCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.routes)
}

func (m *Manager) restartLocked(ctx context.Context, settings Settings) error {
	if len(m.routes) == 0 {
		m.settings = settings
		m.stopCurrentLocked()
		return nil
	}
	settings.StartupTimeout = effectiveStartupTimeout(settings.StartupTimeout)
	binaryPath, err := ResolveOrDownload(ctx, settings)
	if err != nil {
		return err
	}
	settings.BinaryPath = binaryPath
	key := sharedInstanceKey(m.routes, settings)
	if m.current != nil && m.current.key == key {
		if exited, exitErr := m.current.exited(); !exited {
			m.settings = settings
			return nil
		} else {
			slog.Warn("[xray_proxy] shared process exited; restarting", "error", exitErr)
		}
	}
	m.stopCurrentLocked()

	routes := make([]Route, 0, len(m.routes))
	for _, route := range m.routes {
		routes = append(routes, Route{Spec: route.spec, SocksPort: route.port})
	}
	configBytes, err := BuildConfigMany(routes)
	if err != nil {
		return err
	}
	runtimeDir, err := processRuntimeDir(settings.RuntimeDir)
	if err != nil {
		return err
	}
	configPath := filepath.Join(runtimeDir, "shared-"+key[:12]+".json")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		return fmt.Errorf("write xray runtime config: %w", err)
	}
	logPath := filepath.Join(runtimeDir, "shared-"+key[:12]+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = os.Remove(configPath)
		return fmt.Errorf("open xray runtime log: %w", err)
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
		return fmt.Errorf("start xray core: %w", err)
	}
	current := &instance{
		key:        key,
		cmd:        cmd,
		configPath: configPath,
		logPath:    logPath,
		done:       make(chan struct{}),
	}
	m.current = current
	m.settings = settings
	go func() {
		waitErr := cmd.Wait()
		if closeErr := logFile.Close(); closeErr != nil {
			slog.Warn("[xray_proxy] close runtime log failed", "error", closeErr)
		}
		current.setExit(waitErr)
		_ = os.Remove(configPath)
	}()

	startupCtx, cancel := context.WithTimeout(ctx, settings.StartupTimeout)
	defer cancel()
	if err := waitForRoutes(startupCtx, current, m.routes); err != nil {
		slog.Error("[xray_proxy] shared core startup failed", "routes", len(m.routes), "runtime_log", logPath, "error", err)
		m.stopCurrentLocked()
		return fmt.Errorf("%w; see xray runtime log %s", err, logPath)
	}
	slog.Info("[xray_proxy] shared core started", "routes", len(m.routes), "pid", cmd.Process.Pid, "runtime_log", logPath)
	return nil
}

func (m *Manager) stopCurrentLocked() {
	current := m.current
	if current == nil {
		return
	}
	m.current = nil
	if current.cmd != nil && current.cmd.Process != nil {
		if err := current.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			slog.Warn("[xray_proxy] stop shared core failed", "error", err)
		}
	}
	_ = os.Remove(current.configPath)
	slog.Info("[xray_proxy] shared core stopped")
}

func (m *Manager) addressesLocked(specs []Spec) map[string]string {
	out := make(map[string]string, len(specs))
	for _, spec := range specs {
		if route, exists := m.routes[spec.ID]; exists {
			out[spec.ID] = route.address
		}
	}
	return out
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
		name := xrayExecutableName()
		base := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(base, name), filepath.Join(base, "bin", name))
	}
	for _, candidate := range candidates {
		if resolved, ok := executableFile(candidate); ok {
			return resolved, nil
		}
	}
	if resolved, err := exec.LookPath(xrayExecutableName()); err == nil {
		return filepath.Abs(resolved)
	}
	return "", errors.New("xray core executable was not found; set proxy_core.xray_binary_path or XRAY_BINARY_PATH")
}

func Probe(ctx context.Context, settings Settings) Status {
	status := Status{
		RunningInstances: Default().Count(),
		ActiveRoutes:     Default().RouteCount(),
		SupportedTypes:   []string{"vless", "vmess", "hysteria2", "shadowsocks"},
	}
	binaryPath, err := ResolveOrDownload(ctx, settings)
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

func waitForRoutes(ctx context.Context, current *instance, routes map[string]routeState) error {
	pending := make(map[string]string, len(routes))
	for id, route := range routes {
		pending[id] = route.address
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for len(pending) > 0 {
		for id, address := range pending {
			conn, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				delete(pending, id)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if exited, exitErr := current.exited(); exited {
			if exitErr == nil {
				return errors.New("xray core exited before SOCKS listeners became ready")
			}
			return fmt.Errorf("xray core exited before SOCKS listeners became ready: %w", exitErr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for xray SOCKS listeners: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve xray SOCKS port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func availableRoutePort(routes map[string]routeState) (int, error) {
	for attempt := 0; attempt < 32; attempt++ {
		port, err := availablePort()
		if err != nil {
			return 0, err
		}
		used := false
		for _, route := range routes {
			if route.port == port {
				used = true
				break
			}
		}
		if !used {
			return port, nil
		}
	}
	return 0, errors.New("could not reserve a unique local SOCKS port")
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

func normalizeSpecs(specs []Spec) ([]Spec, error) {
	out := make([]Spec, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		spec = NormalizeSpec(spec)
		if spec.ID == "" {
			return nil, errors.New("xray proxy id is required")
		}
		if _, exists := seen[spec.ID]; exists {
			return nil, fmt.Errorf("duplicate xray proxy id: %s", spec.ID)
		}
		if _, err := BuildConfig(spec, 1); err != nil {
			return nil, err
		}
		seen[spec.ID] = struct{}{}
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func sameSpec(a, b Spec) bool {
	a = NormalizeSpec(a)
	b = NormalizeSpec(b)
	return a.ID == b.ID && a.Type == b.Type && a.URI == b.URI
}

func sharedInstanceKey(routes map[string]routeState, settings Settings) string {
	ids := make([]string, 0, len(routes))
	for id := range routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := []string{
		settings.BinaryPath,
		settings.RuntimeDir,
		settings.StartupTimeout.String(),
	}
	for _, id := range ids {
		route := routes[id]
		parts = append(parts, route.spec.ID, route.spec.Type, route.spec.URI, fmt.Sprintf("%d", route.port))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func effectiveStartupTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultStartupTimeout
	}
	return value
}

func xrayExecutableName() string {
	if runtime.GOOS == "windows" {
		return "xray.exe"
	}
	return "xray"
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
