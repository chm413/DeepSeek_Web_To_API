package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu            sync.RWMutex
	cfg           Config
	path          string
	fromEnv       bool
	accountsDB    *accountSQLiteStore
	keyMap        map[string]struct{}          // O(1) API key lookup index
	accMap        map[string]int               // O(1) account lookup: identifier -> slice index
	accTest       map[string]string            // runtime-only account test status cache
	accTestResult map[string]AccountTestResult // runtime-only account test detail cache
	accSess       map[string]int               // runtime-only account session count cache
}

// AccountTestResult is the latest operator-triggered account check. It is
// intentionally runtime-only: failure details remain visible after a Web UI
// refresh but are never persisted with account credentials.
type AccountTestResult struct {
	Status         string `json:"status"`
	Phase          string `json:"phase,omitempty"`
	FailureReason  string `json:"failure_reason,omitempty"`
	ErrorCode      int    `json:"error_code,omitempty"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	AccountState   string `json:"account_state,omitempty"`
	AutoDisabled   bool   `json:"auto_disabled,omitempty"`
	ConfigWarning  string `json:"config_warning,omitempty"`
	ResponseTimeMs int    `json:"response_time_ms,omitempty"`
	UpdatedAtUnix  int64  `json:"updated_at_unix"`
}

func LoadStore() *Store {
	store, err := loadStore()
	if err != nil {
		Logger.Warn("[config] load failed", "error", err)
	}
	if len(store.cfg.Keys) == 0 && len(store.cfg.Accounts) == 0 {
		Logger.Warn("[config] empty config loaded")
	}
	store.rebuildIndexes()
	return store
}

func LoadStoreWithError() (*Store, error) {
	store, err := loadStore()
	if err != nil {
		return nil, err
	}
	store.rebuildIndexes()
	return store, nil
}

func (s *Store) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

func (s *Store) HasAPIKey(k string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.keyMap[k]
	return ok
}

func (s *Store) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.cfg.Keys)
}

func (s *Store) Accounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.cfg.Accounts)
}

func (s *Store) FindAccount(identifier string) (Account, bool) {
	identifier = strings.TrimSpace(identifier)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if idx, ok := s.findAccountIndexLocked(identifier); ok {
		return s.cfg.Accounts[idx], true
	}
	return Account{}, false
}

func (s *Store) UpdateAccountTestStatus(identifier, status string) error {
	identifier = strings.TrimSpace(identifier)
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.findAccountIndexLocked(identifier)
	if !ok {
		return errors.New("account not found")
	}
	s.setAccountTestStatusLocked(s.cfg.Accounts[idx], status, identifier)
	return nil
}

func (s *Store) AccountTestStatus(identifier string) (string, bool) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.accTest[identifier]
	return status, ok
}

// UpdateAccountTestResult records the full, non-secret outcome of an
// operator-triggered account check in the in-memory status cache.
func (s *Store) UpdateAccountTestResult(identifier string, result AccountTestResult) error {
	identifier = strings.TrimSpace(identifier)
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.findAccountIndexLocked(identifier)
	if !ok {
		return errors.New("account not found")
	}
	result.Status = lower(result.Status)
	if result.Status == "" {
		result.Status = "failed"
	}
	result.Phase = strings.TrimSpace(result.Phase)
	result.FailureReason = truncateRuntimeTestDetail(result.FailureReason)
	result.ConfigWarning = truncateRuntimeTestDetail(result.ConfigWarning)
	if result.UpdatedAtUnix <= 0 {
		result.UpdatedAtUnix = time.Now().Unix()
	}
	s.setAccountTestStatusLocked(s.cfg.Accounts[idx], result.Status, identifier)
	s.setAccountTestResultLocked(s.cfg.Accounts[idx], result, identifier)
	return nil
}

// AccountTestResult returns the latest operator-triggered check outcome for
// the supplied account identifier. The result is cleared on process restart.
func (s *Store) AccountTestResult(identifier string) (AccountTestResult, bool) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return AccountTestResult{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.accTestResult[identifier]
	return result, ok
}

func truncateRuntimeTestDetail(value string) string {
	const maxRunes = 1200
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "..."
}

func (s *Store) UpdateAccountSessionCount(identifier string, count int) error {
	identifier = strings.TrimSpace(identifier)
	if count < 0 {
		count = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.findAccountIndexLocked(identifier)
	if !ok {
		return errors.New("account not found")
	}
	s.setAccountSessionCountLocked(s.cfg.Accounts[idx], count, identifier)
	return nil
}

func (s *Store) AccountSessionCount(identifier string) (int, bool) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	count, ok := s.accSess[identifier]
	return count, ok
}

func (s *Store) UpdateAccountToken(identifier, token string) error {
	identifier = strings.TrimSpace(identifier)
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.findAccountIndexLocked(identifier)
	if !ok {
		return errors.New("account not found")
	}
	oldID := s.cfg.Accounts[idx].Identifier()
	s.cfg.Accounts[idx].Token = token
	newID := s.cfg.Accounts[idx].Identifier()
	// Keep historical aliases usable for long-lived queues while also adding
	// the latest identifier after token refresh.
	if identifier != "" {
		s.accMap[identifier] = idx
	}
	if oldID != "" {
		s.accMap[oldID] = idx
	}
	if newID != "" {
		s.accMap[newID] = idx
	}
	if s.accountsDB != nil {
		if err := s.accountsDB.updateToken(identifier, token); err != nil {
			return err
		}
	}
	return s.saveLocked()
}

func (s *Store) SetAccountDisabled(identifier string, disabled bool, reason string) error {
	identifier = strings.TrimSpace(identifier)
	return s.Update(func(cfg *Config) error {
		for i := range cfg.Accounts {
			if !accountIdentifiersMatch(cfg.Accounts[i], identifier) {
				continue
			}
			cfg.Accounts[i].Disabled = disabled
			if disabled {
				cfg.Accounts[i].DisabledReason = strings.TrimSpace(reason)
				cfg.Accounts[i].DisabledAtUnix = time.Now().Unix()
			} else {
				cfg.Accounts[i].DisabledReason = ""
				cfg.Accounts[i].DisabledAtUnix = 0
			}
			return nil
		}
		return errors.New("account not found")
	})
}

func accountIdentifiersMatch(acc Account, identifier string) bool {
	if identifier == "" {
		return false
	}
	if strings.TrimSpace(acc.Email) == identifier || acc.Identifier() == identifier {
		return true
	}
	mobile := CanonicalMobileKey(identifier)
	return mobile != "" && mobile == CanonicalMobileKey(acc.Mobile)
}

func (s *Store) Replace(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg.NormalizeCredentials()
	if err := s.persistAccountsLocked(&cfg); err != nil {
		return err
	}
	s.cfg = cfg.Clone()
	s.rebuildIndexes()
	return s.saveLocked()
}

func (s *Store) Update(mutator func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	base := s.cfg.Clone()
	cfg := base.Clone()
	if err := mutator(&cfg); err != nil {
		return err
	}
	cfg.ReconcileCredentials(base)
	cfg.NormalizeCredentials()
	if err := s.persistAccountsLocked(&cfg); err != nil {
		return err
	}
	s.cfg = cfg
	s.rebuildIndexes()
	return s.saveLocked()
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fromEnv && !envWritebackEnabled() {
		Logger.Info("[save_config] source from env, skip write")
		return nil
	}
	persistCfg := s.cfg.Clone()
	persistCfg.ClearAccountTokens()
	if s.accountsDB != nil {
		persistCfg.Accounts = nil
	}
	b, err := json.MarshalIndent(persistCfg, "", "  ")
	if err != nil {
		return err
	}
	if err := writeConfigBytes(s.path, b); err != nil {
		return err
	}
	s.fromEnv = false
	return nil
}

func (s *Store) saveLocked() error {
	if s.fromEnv && !envWritebackEnabled() {
		Logger.Info("[save_config] source from env, skip write")
		return nil
	}
	persistCfg := s.cfg.Clone()
	persistCfg.ClearAccountTokens()
	if s.accountsDB != nil {
		persistCfg.Accounts = nil
	}
	b, err := json.MarshalIndent(persistCfg, "", "  ")
	if err != nil {
		return err
	}
	if err := writeConfigBytes(s.path, b); err != nil {
		return err
	}
	s.fromEnv = false
	return nil
}

func (s *Store) IsEnvBacked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fromEnv
}

func (s *Store) ExportJSONAndBase64() (string, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exportCfg := s.cfg.Clone()
	exportCfg.ClearAccountTokens()
	b, err := json.Marshal(exportCfg)
	if err != nil {
		return "", "", err
	}
	return string(b), base64.StdEncoding.EncodeToString(b), nil
}

func (s *Store) persistAccountsLocked(cfg *Config) error {
	if s == nil || s.accountsDB == nil || cfg == nil {
		return nil
	}
	accounts, err := s.accountsDB.replace(cfg.Accounts)
	if err != nil {
		return err
	}
	cfg.Accounts = accounts
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.accountsDB == nil {
		return nil
	}
	return s.accountsDB.close()
}
