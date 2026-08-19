package auth

import (
	"net/http"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

type adminRuntimeTestConfig struct {
	key           string
	passwordHash  string
	jwtSecret     string
	expireHours   int
	jwtValidAfter int64
}

func (c adminRuntimeTestConfig) AdminKey() string              { return c.key }
func (c adminRuntimeTestConfig) AdminPasswordHash() string     { return c.passwordHash }
func (c adminRuntimeTestConfig) AdminJWTSecret() string        { return c.jwtSecret }
func (c adminRuntimeTestConfig) AdminJWTExpireHours() int      { return c.expireHours }
func (c adminRuntimeTestConfig) AdminJWTValidAfterUnix() int64 { return c.jwtValidAfter }

func TestJWTCreateVerify(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "test-admin-key")
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "test-jwt-secret")
	token, err := CreateJWT(1)
	if err != nil {
		t.Fatalf("create jwt failed: %v", err)
	}
	payload, err := VerifyJWT(token)
	if err != nil {
		t.Fatalf("verify jwt failed: %v", err)
	}
	if payload["role"] != "admin" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestVerifyAdminRequest(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "test-admin-key")
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "test-jwt-secret")
	token, _ := CreateJWT(1)
	req, _ := http.NewRequest(http.MethodGet, "/admin/config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if err := VerifyAdminRequest(req); err != nil {
		t.Fatalf("expected token accepted: %v", err)
	}
}

func TestVerifyJWTWithStoreValidAfter(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"admin":{"password_hash":"`+HashAdminPassword("oldpass")+`"}}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "test-jwt-secret")
	store := config.LoadStore()
	token, err := CreateJWTWithStore(1, store)
	if err != nil {
		t.Fatalf("create jwt failed: %v", err)
	}
	if _, err := VerifyJWTWithStore(token, store); err != nil {
		t.Fatalf("verify before invalidation failed: %v", err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Admin.JWTValidAfterUnix = 1<<62 - 1
		return nil
	}); err != nil {
		t.Fatalf("set valid-after failed: %v", err)
	}
	if _, err := VerifyJWTWithStore(token, store); err == nil {
		t.Fatal("expected token invalid after valid-after update")
	}
}

func TestVerifyJWTWithStoreSameSecondInvalidationAndRelogin(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"admin":{"password_hash":"`+HashAdminPassword("oldpass")+`"}}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "test-jwt-secret")
	store := config.LoadStore()

	oldToken, err := CreateJWTWithStore(1, store)
	if err != nil {
		t.Fatalf("create old jwt failed: %v", err)
	}
	oldPayload, err := VerifyJWTWithStore(oldToken, store)
	if err != nil {
		t.Fatalf("verify old jwt before invalidation failed: %v", err)
	}
	oldIAT, _ := oldPayload["iat"].(float64)

	if err := store.Update(func(c *config.Config) error {
		c.Admin.JWTValidAfterUnix = int64(oldIAT)
		return nil
	}); err != nil {
		t.Fatalf("set valid-after failed: %v", err)
	}

	if _, err := VerifyJWTWithStore(oldToken, store); err == nil {
		t.Fatal("expected old token invalid when iat == valid-after")
	}

	newToken, err := CreateJWTWithStore(1, store)
	if err != nil {
		t.Fatalf("create new jwt failed: %v", err)
	}
	if _, err := VerifyJWTWithStore(newToken, store); err != nil {
		t.Fatalf("expected new token valid after invalidation cutoff: %v", err)
	}
}

func TestValidateAdminRuntimeSecurityRequiresJWTSecret(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "test-admin-key")
	if err := ValidateAdminRuntimeSecurity(nil); err == nil {
		t.Fatal("expected missing JWT secret error")
	}
}

func TestValidateAdminRuntimeSecurityPassesWithHashAndJWTSecret(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"admin":{"password_hash":"`+HashAdminPassword("oldpass")+`"}}`)
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "test-jwt-secret")
	store := config.LoadStore()
	if err := ValidateAdminRuntimeSecurity(store); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestAdminRuntimeSecurityUsesUnifiedConfigSecrets(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"admin":{"key":"config-admin-key","jwt_secret":"config-jwt-secret"}}`)
	store := config.LoadStore()
	if err := ValidateAdminRuntimeSecurity(store); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if !VerifyAdminCredential("config-admin-key", store) {
		t.Fatal("expected admin key from config to be accepted")
	}
	token, err := CreateJWTWithStore(1, store)
	if err != nil {
		t.Fatalf("create jwt with config secret failed: %v", err)
	}
	if _, err := VerifyJWTWithStore(token, store); err != nil {
		t.Fatalf("verify jwt with config secret failed: %v", err)
	}
}

func TestValidateAdminRuntimeSecurityRejectsKnownAdminKeyPlaceholder(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "")
	store := adminRuntimeTestConfig{key: "change-me-admin-key", jwtSecret: "a-unique-jwt-secret"}
	err := ValidateAdminRuntimeSecurity(store)
	if err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("expected admin key placeholder rejection, got %v", err)
	}
}

func TestValidateAdminRuntimeSecurityRejectsKnownJWTPlaceholder(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY", "")
	t.Setenv("DEEPSEEK_WEB_TO_API_JWT_SECRET", "")
	store := adminRuntimeTestConfig{key: "a-unique-admin-key", jwtSecret: "change-me-jwt-secret"}
	err := ValidateAdminRuntimeSecurity(store)
	if err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("expected JWT placeholder rejection, got %v", err)
	}
}
