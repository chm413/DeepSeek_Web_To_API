package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountSQLiteMigratesFileBackedAccountsAndKeepsConfigClean(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	accountsPath := filepath.Join(dir, "accounts.sqlite")
	body := `{
		"keys":["k1"],
		"proxies":[{"id":"node-a","type":"socks5","host":"127.0.0.1","port":1080}],
		"accounts":[
			{"email":"user@example.com","password":"p1","token":"persisted-token","proxy_id":"node-a","proxy_auto_route":true},
			{"mobile":"13800000000","password":"p2","disabled":true,"disabled_reason":"manual","disabled_at_unix":1700000000}
		],
		"storage":{"accounts_sqlite_path":` + quoteJSON(accountsPath) + `}
	}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", configPath)
	t.Setenv("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH", "")

	store, err := LoadStoreWithError()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if got := len(store.Accounts()); got != 2 {
		t.Fatalf("expected 2 migrated accounts, got %d", got)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save store: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(content), `"accounts"`) {
		t.Fatalf("expected accounts to be stored outside config json, got: %s", content)
	}
	if err := store.UpdateAccountToken("user@example.com", "runtime-token"); err != nil {
		t.Fatalf("update token: %v", err)
	}

	reloaded, err := LoadStoreWithError()
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	defer func() { _ = reloaded.Close() }()
	acc, ok := reloaded.FindAccount("user@example.com")
	if !ok {
		t.Fatal("expected account to reload from accounts sqlite")
	}
	if acc.Token != "runtime-token" {
		t.Fatalf("expected token from accounts sqlite, got %q", acc.Token)
	}
	if !acc.ProxyAutoRoute || acc.ProxyID != "node-a" {
		t.Fatalf("expected automatic proxy route to persist in accounts sqlite, got %#v", acc)
	}
	disabled, ok := reloaded.FindAccount("13800000000")
	if !ok || !disabled.Disabled || disabled.DisabledReason != AccountDisabledManual || disabled.DisabledAtUnix != 1700000000 {
		t.Fatalf("expected disabled state from accounts sqlite, got %#v, %v", disabled, ok)
	}
}

func TestAccountSQLiteEnabledForEnvConfigWhenPathExplicit(t *testing.T) {
	dir := t.TempDir()
	accountsPath := filepath.Join(dir, "accounts.sqlite")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[{"email":"seed@example.com","password":"p1"}]
	}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH", filepath.Join(dir, "config.json"))
	t.Setenv("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH", accountsPath)
	t.Setenv("DEEPSEEK_WEB_TO_API_ENV_WRITEBACK", "0")

	store, err := LoadStoreWithError()
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Update(func(c *Config) error {
		c.Accounts = append(c.Accounts, Account{Email: "next@example.com", Password: "p2"})
		return nil
	}); err != nil {
		t.Fatalf("update env-backed accounts: %v", err)
	}

	reloaded, err := LoadStoreWithError()
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	defer func() { _ = reloaded.Close() }()
	if got := len(reloaded.Accounts()); got != 2 {
		t.Fatalf("expected sqlite accounts to override env seed, got %d", got)
	}
	if _, ok := reloaded.FindAccount("next@example.com"); !ok {
		t.Fatal("expected env-backed account update to persist into sqlite")
	}
}
