package auth

import (
	"fmt"
	"time"

	"DeepSeek_Web_To_API/internal/account"
)

// AccountHealthError carries an explicit upstream account state discovered
// during password login or an authenticated account request.
type AccountHealthError struct {
	State   account.HealthState
	Until   time.Time
	Code    int
	Message string
}

func (e *AccountHealthError) Error() string {
	if e == nil {
		return "account health error"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.State == account.HealthTemporarilyMuted {
		if !e.Until.IsZero() {
			return fmt.Sprintf("account temporarily muted until %s", e.Until.Format(time.RFC3339))
		}
		return "account temporarily muted"
	}
	if e.State == account.HealthRateLimited {
		if !e.Until.IsZero() {
			return fmt.Sprintf("account rate limited until %s", e.Until.Format(time.RFC3339))
		}
		return "account rate limited"
	}
	if e.State == account.HealthPermanentlyBanned {
		return "account permanently banned"
	}
	if e.State == account.HealthInvalidCredentials {
		return "account credentials rejected"
	}
	return "account health rejected"
}
