package config

// rebuildIndexes must be called with the lock already held (or during init).
func (s *Store) rebuildIndexes() {
	prevStatus := s.accTest
	prevResult := s.accTestResult
	prevSession := s.accSess
	s.keyMap = make(map[string]struct{}, len(s.cfg.Keys))
	for _, k := range s.cfg.Keys {
		s.keyMap[k] = struct{}{}
	}
	s.accMap = make(map[string]int, len(s.cfg.Accounts))
	s.accTest = make(map[string]string, len(s.cfg.Accounts))
	s.accTestResult = make(map[string]AccountTestResult, len(s.cfg.Accounts))
	s.accSess = make(map[string]int, len(s.cfg.Accounts))
	for i, acc := range s.cfg.Accounts {
		id := acc.Identifier()
		if id != "" {
			s.accMap[id] = i
			if status, ok := prevStatus[id]; ok {
				s.setAccountTestStatusLocked(acc, status, "")
			}
			if result, ok := prevResult[id]; ok {
				s.setAccountTestResultLocked(acc, result, "")
			}
			if count, ok := prevSession[id]; ok {
				s.setAccountSessionCountLocked(acc, count, "")
			}
		}
	}
}

// findAccountIndexLocked expects the store lock to already be held.
func (s *Store) findAccountIndexLocked(identifier string) (int, bool) {
	candidates := []string{identifier}
	if mobile := CanonicalMobileKey(identifier); mobile != "" && mobile != identifier {
		candidates = append(candidates, mobile)
	}
	for _, key := range candidates {
		if idx, ok := s.accMap[key]; ok && idx >= 0 && idx < len(s.cfg.Accounts) {
			return idx, true
		}
	}
	// Fallback for token-only accounts whose derived identifier changed after
	// a token refresh; this preserves correctness on map misses.
	for i, acc := range s.cfg.Accounts {
		id := acc.Identifier()
		for _, key := range candidates {
			if id == key {
				return i, true
			}
		}
	}
	return -1, false
}

func (s *Store) setAccountTestStatusLocked(acc Account, status, hintedIdentifier string) {
	status = lower(status)
	if status == "" {
		return
	}
	if id := acc.Identifier(); id != "" {
		s.accTest[id] = status
	}
	if email := acc.Email; email != "" {
		s.accTest[email] = status
	}
	if mobile := CanonicalMobileKey(acc.Mobile); mobile != "" {
		s.accTest[mobile] = status
	}
	if hintedIdentifier = lower(hintedIdentifier); hintedIdentifier != "" {
		s.accTest[hintedIdentifier] = status
	}
}

func (s *Store) setAccountTestResultLocked(acc Account, result AccountTestResult, hintedIdentifier string) {
	if id := acc.Identifier(); id != "" {
		s.accTestResult[id] = result
	}
	if email := acc.Email; email != "" {
		s.accTestResult[email] = result
	}
	if mobile := CanonicalMobileKey(acc.Mobile); mobile != "" {
		s.accTestResult[mobile] = result
	}
	if hintedIdentifier = lower(hintedIdentifier); hintedIdentifier != "" {
		s.accTestResult[hintedIdentifier] = result
	}
}

func (s *Store) setAccountSessionCountLocked(acc Account, count int, hintedIdentifier string) {
	if count < 0 {
		count = 0
	}
	if id := acc.Identifier(); id != "" {
		s.accSess[id] = count
	}
	if email := acc.Email; email != "" {
		s.accSess[email] = count
	}
	if mobile := CanonicalMobileKey(acc.Mobile); mobile != "" {
		s.accSess[mobile] = count
	}
	if hintedIdentifier = lower(hintedIdentifier); hintedIdentifier != "" {
		s.accSess[hintedIdentifier] = count
	}
}
