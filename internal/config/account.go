package config

import (
	"strings"
	"time"
)

func (a Account) Identifier() string {
	a = NormalizeAccountIdentity(a)
	if strings.TrimSpace(a.Email) != "" {
		return strings.TrimSpace(a.Email)
	}
	if strings.TrimSpace(a.Mobile) != "" {
		return strings.TrimSpace(a.Mobile)
	}
	return ""
}

func NormalizeAccountIdentity(a Account) Account {
	a.Email = strings.TrimSpace(a.Email)
	a.Mobile = strings.TrimSpace(a.Mobile)
	if a.Email == "" && looksLikeEmailIdentifier(a.Mobile) {
		a.Email = a.Mobile
		a.Mobile = ""
		return a
	}
	a.Mobile = NormalizeMobileForStorage(a.Mobile)
	return a
}

// normalizeAccountCooldown keeps only known, unexpired account-wide cooldowns.
// The reason is intentionally runtime-only so upstream response text is never
// persisted beside account credentials.
func normalizeAccountCooldown(state string, untilUnix int64) (string, int64) {
	state = strings.ToLower(strings.TrimSpace(state))
	if state != AccountCooldownRateLimited && state != AccountCooldownTemporarilyMuted {
		return "", 0
	}
	if untilUnix <= time.Now().Unix() {
		return "", 0
	}
	return state, untilUnix
}

func looksLikeEmailIdentifier(raw string) bool {
	return strings.Contains(strings.TrimSpace(raw), "@")
}
