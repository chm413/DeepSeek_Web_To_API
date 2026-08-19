package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var warnOnce sync.Once

type AdminConfigReader interface {
	AdminKey() string
	AdminPasswordHash() string
	AdminJWTSecret() string
	AdminJWTExpireHours() int
	AdminJWTValidAfterUnix() int64
}

func AdminKey() string {
	return effectiveAdminKey(nil)
}

func effectiveAdminKey(store AdminConfigReader) string {
	if !hasConfiguredAdminCredential(store) {
		adminCredentialWarning()
		return ""
	}
	if store != nil {
		if hash := strings.TrimSpace(store.AdminPasswordHash()); hash != "" {
			return ""
		}
	}
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY")); v != "" {
		return v
	}
	if store != nil {
		return strings.TrimSpace(store.AdminKey())
	}
	return ""
}

func hasConfiguredAdminCredential(store AdminConfigReader) bool {
	if store != nil {
		if hash := strings.TrimSpace(store.AdminPasswordHash()); hash != "" {
			return true
		}
	}
	if strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY")) != "" {
		return true
	}
	if store != nil {
		if key := strings.TrimSpace(store.AdminKey()); key != "" {
			return true
		}
	}
	return false
}

func adminCredentialWarning() {
	warnOnce.Do(func() {
		slog.Warn("⚠️  Admin credential is not configured. Set config admin.key/admin.password_hash, or DEEPSEEK_WEB_TO_API_ADMIN_KEY as a deployment override.")
	})
}

func adminCredentialMissingError() error {
	return errors.New("admin credential is missing: set admin.key or admin.password_hash in config.json")
}

func adminJWTSecretMissingError() error {
	return errors.New("admin.jwt_secret is required for admin token signing and verification")
}

func adminCredentialPlaceholderError() error {
	return errors.New("admin credential uses a known placeholder; set a unique admin.key or admin.password_hash")
}

func adminJWTSecretPlaceholderError() error {
	return errors.New("admin.jwt_secret uses a known placeholder; set a unique random secret")
}

// ValidateAdminCredentialConfigured checks whether admin authentication can be established.
// This is used by command startup to fail fast when both env + config credential sources are absent.
func ValidateAdminCredentialConfigured(store AdminConfigReader) error {
	if hasConfiguredAdminCredential(store) {
		return nil
	}
	return adminCredentialMissingError()
}

// ValidateAdminRuntimeSecurity checks whether required admin auth security materials are available.
// This is used on startup before accepting requests.
func ValidateAdminRuntimeSecurity(store AdminConfigReader) error {
	if err := ValidateAdminCredentialConfigured(store); err != nil {
		return err
	}
	secret := strings.TrimSpace(jwtSecret(store))
	if secret == "" {
		return adminJWTSecretMissingError()
	}
	// A copied example configuration must never become a usable production
	// admin boundary.  Check the effective credential source only: a stale
	// admin.key next to a configured password hash is not used for login.
	if strings.TrimSpace(adminPasswordHash(store)) == "" && isKnownAdminPlaceholder(effectiveAdminKey(store)) {
		return adminCredentialPlaceholderError()
	}
	if isKnownAdminPlaceholder(secret) {
		return adminJWTSecretPlaceholderError()
	}
	return nil
}

func adminPasswordHash(store AdminConfigReader) string {
	if store == nil {
		return ""
	}
	return strings.TrimSpace(store.AdminPasswordHash())
}

// isKnownAdminPlaceholder intentionally matches only conventional example
// values and marker forms.  It avoids rejecting arbitrary operator secrets
// merely because they contain words such as "default".
func isKnownAdminPlaceholder(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return false
	}
	switch v {
	case "change-me-admin-key", "change-me-jwt-secret", "change-me", "replace-me", "replace-me-admin-key", "replace-me-jwt-secret":
		return true
	}
	return strings.HasPrefix(v, "change-me-") || strings.HasPrefix(v, "replace-me-")
}

func jwtSecret(store AdminConfigReader) string {
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_JWT_SECRET")); v != "" {
		return v
	}
	if store != nil {
		return strings.TrimSpace(store.AdminJWTSecret())
	}
	return ""
}

func jwtExpireHours(store AdminConfigReader) int {
	if store != nil {
		if n := store.AdminJWTExpireHours(); n > 0 {
			return n
		}
	}
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_JWT_EXPIRE_HOURS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 24
}

func CreateJWT(expireHours int) (string, error) {
	return CreateJWTWithStore(expireHours, nil)
}

func CreateJWTWithStore(expireHours int, store AdminConfigReader) (string, error) {
	if expireHours <= 0 {
		expireHours = jwtExpireHours(store)
	}
	issuedAt := time.Now().Unix()
	// If sessions were invalidated in this same second, move iat forward by
	// one second so newly minted tokens remain valid with strict cutoff checks.
	if store != nil {
		if validAfter := store.AdminJWTValidAfterUnix(); validAfter >= issuedAt {
			issuedAt = validAfter + 1
		}
	}
	expireAt := time.Unix(issuedAt, 0).Add(time.Duration(expireHours) * time.Hour).Unix()
	header := map[string]any{"alg": "HS256", "typ": "JWT"}
	payload := map[string]any{"iat": issuedAt, "exp": expireAt, "role": "admin"}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(payload)
	headerB64 := rawB64Encode(h)
	payloadB64 := rawB64Encode(p)
	msg := headerB64 + "." + payloadB64
	sig, err := signHS256(msg, store)
	if err != nil {
		return "", err
	}
	return msg + "." + rawB64Encode(sig), nil
}

func VerifyJWT(token string) (map[string]any, error) {
	return VerifyJWTWithStore(token, nil)
}

func VerifyJWTWithStore(token string, store AdminConfigReader) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format")
	}
	msg := parts[0] + "." + parts[1]
	expected, err := signHS256(msg, store)
	if err != nil {
		return nil, err
	}
	actual, err := rawB64Decode(parts[2])
	if err != nil {
		return nil, errors.New("invalid signature")
	}
	if !hmac.Equal(expected, actual) {
		return nil, errors.New("invalid signature")
	}
	payloadBytes, err := rawB64Decode(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload")
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, errors.New("invalid payload")
	}
	exp, _ := payload["exp"].(float64)
	if int64(exp) < time.Now().Unix() {
		return nil, errors.New("token expired")
	}
	if store != nil {
		validAfter := store.AdminJWTValidAfterUnix()
		if validAfter > 0 {
			iat, _ := payload["iat"].(float64)
			if int64(iat) <= validAfter {
				return nil, errors.New("token expired")
			}
		}
	}
	return payload, nil
}

func VerifyAdminRequest(r *http.Request) error {
	return VerifyAdminRequestWithStore(r, nil)
}

func VerifyAdminRequestWithStore(r *http.Request, store AdminConfigReader) error {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return errors.New("authentication required")
	}
	token := strings.TrimSpace(authHeader[7:])
	if token == "" {
		return errors.New("authentication required")
	}
	if VerifyAdminCredential(token, store) {
		return nil
	}
	if _, err := VerifyJWTWithStore(token, store); err == nil {
		return nil
	}
	return errors.New("invalid credentials")
}

func VerifyAdminCredential(candidate string, store AdminConfigReader) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if store != nil {
		hash := strings.TrimSpace(store.AdminPasswordHash())
		if hash != "" {
			return verifyAdminPasswordHash(candidate, hash)
		}
	}
	key := effectiveAdminKey(store)
	if key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1
}

func UsingDefaultAdminKey(store AdminConfigReader) bool {
	if store != nil && strings.TrimSpace(store.AdminPasswordHash()) != "" {
		return false
	}
	if strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ADMIN_KEY")) != "" {
		return false
	}
	if store != nil && strings.TrimSpace(store.AdminKey()) != "" {
		return false
	}
	return true
}

func HashAdminPassword(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if sum, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost); err == nil {
		return "bcrypt:" + string(sum)
	}
	return ""
}

func verifyAdminPasswordHash(candidate, encoded string) bool {
	encoded = strings.TrimSpace(encoded)
	normalized := strings.ToLower(encoded)
	if encoded == "" {
		return false
	}
	if strings.HasPrefix(normalized, "bcrypt:") {
		sum := strings.TrimSpace(strings.TrimPrefix(encoded, "bcrypt:"))
		err := bcrypt.CompareHashAndPassword([]byte(sum), []byte(candidate))
		return err == nil
	}
	if strings.HasPrefix(normalized, "sha256:") {
		want := strings.TrimPrefix(encoded, "sha256:")
		want = strings.ToLower(want)
		sum := sha256.Sum256([]byte(candidate))
		got := hex.EncodeToString(sum[:])
		return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(encoded)) == 1
}

func signHS256(msg string, store AdminConfigReader) ([]byte, error) {
	if strings.TrimSpace(jwtSecret(store)) == "" {
		return nil, adminJWTSecretMissingError()
	}
	h := hmac.New(sha256.New, []byte(jwtSecret(store)))
	_, _ = h.Write([]byte(msg))
	return h.Sum(nil), nil
}

func rawB64Encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func rawB64Decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
