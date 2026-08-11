package upstreamsession

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultTTL         = 6 * time.Hour
	defaultMaxBranches = 8
)

type Scope struct {
	CallerID   string
	SessionKey string
	AccountID  string
	Surface    string
	Variant    string
}

type Lease struct {
	store           *Store
	key             string
	entryID         string
	SessionID       string
	ParentMessageID int
	DeltaMessages   []any
	TurnCount       int
	Rotate          bool
}

type entry struct {
	id               string
	scope            Scope
	sessionID        string
	parentMessageID  int
	requestMessages  []json.RawMessage
	responseMessages []json.RawMessage
	turns            int
	updatedAt        time.Time
	busy             bool
}

type Store struct {
	mu          sync.Mutex
	ttl         time.Duration
	maxBranches int
	nextID      uint64
	entries     map[string][]*entry
}

// MatchDiagnostics describes why a strict incremental branch lookup missed
// without exposing any prompt or account contents in logs.
type MatchDiagnostics struct {
	InvalidInput           bool
	Branches               int
	Busy                   int
	NotExtendable          int
	RequestPrefixMismatch  int
	ResponsePrefixMismatch int
	Extendable             int
	ExpectedResponseShape  string
	CurrentResponseShape   string
	ExpectedResponseHash   string
	CurrentResponseHash    string
}

func NewStore(ttl time.Duration, maxBranches int) *Store {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if maxBranches <= 0 {
		maxBranches = defaultMaxBranches
	}
	return &Store{ttl: ttl, maxBranches: maxBranches, entries: map[string][]*entry{}}
}

func (s *Store) Prepare(scope Scope, messages []any) (*Lease, bool) {
	return s.PrepareWithMaxTurns(scope, messages, 0)
}

// PrepareWithMaxTurns leases the strict extension of a recorded branch. When
// maxTurns is positive and the branch has reached that locally configured
// threshold, the lease is marked Rotate so the caller can compact history and
// create a fresh upstream session instead of sending another pinned request.
func (s *Store) PrepareWithMaxTurns(scope Scope, messages []any, maxTurns int) (*Lease, bool) {
	if s == nil || !validScope(scope) || len(messages) == 0 {
		return nil, false
	}
	current, ok := canonicalMessages(messages)
	if !ok {
		return nil, false
	}

	now := time.Now()
	key := scopeKey(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	var selected *entry
	selectedConsumed := -1
	for _, candidate := range s.entries[key] {
		if candidate.busy || candidate.scope != scope || candidate.parentMessageID <= 0 || strings.TrimSpace(candidate.sessionID) == "" {
			continue
		}
		consumed := len(candidate.requestMessages) + len(candidate.responseMessages)
		if consumed <= selectedConsumed || len(current) <= consumed {
			continue
		}
		if !messagePrefixEqual(current, candidate.requestMessages, 0) || !messagePrefixEqual(current, candidate.responseMessages, len(candidate.requestMessages)) {
			continue
		}
		selected = candidate
		selectedConsumed = consumed
	}
	if selected == nil {
		return nil, false
	}
	selected.busy = true
	delta := cloneAnyMessages(messages[selectedConsumed:])
	return &Lease{
		store:           s,
		key:             key,
		entryID:         selected.id,
		SessionID:       selected.sessionID,
		ParentMessageID: selected.parentMessageID,
		DeltaMessages:   delta,
		TurnCount:       selected.turns,
		Rotate:          maxTurns > 0 && selected.turns >= maxTurns,
	}, true
}

// Diagnose reports the shape of an incremental cache miss. It intentionally
// returns counters only; callers can log these without leaking conversation
// text or persisted account identifiers.
func (s *Store) Diagnose(scope Scope, messages []any) MatchDiagnostics {
	var diagnostics MatchDiagnostics
	if s == nil || !validScope(scope) || len(messages) == 0 {
		diagnostics.InvalidInput = true
		return diagnostics
	}
	current, ok := canonicalMessages(messages)
	if !ok {
		diagnostics.InvalidInput = true
		return diagnostics
	}

	now := time.Now()
	key := scopeKey(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	for _, candidate := range s.entries[key] {
		if candidate.scope != scope || candidate.parentMessageID <= 0 || strings.TrimSpace(candidate.sessionID) == "" {
			continue
		}
		diagnostics.Branches++
		if candidate.busy {
			diagnostics.Busy++
			continue
		}
		consumed := len(candidate.requestMessages) + len(candidate.responseMessages)
		if len(current) <= consumed {
			diagnostics.NotExtendable++
			continue
		}
		if !messagePrefixEqual(current, candidate.requestMessages, 0) {
			diagnostics.RequestPrefixMismatch++
			continue
		}
		if !messagePrefixEqual(current, candidate.responseMessages, len(candidate.requestMessages)) {
			diagnostics.ResponsePrefixMismatch++
			if diagnostics.ExpectedResponseShape == "" {
				mismatchOffset := len(candidate.requestMessages)
				for i := range candidate.responseMessages {
					if string(current[mismatchOffset+i]) == string(candidate.responseMessages[i]) {
						continue
					}
					diagnostics.ExpectedResponseShape = canonicalMessageShape(candidate.responseMessages[i])
					diagnostics.CurrentResponseShape = canonicalMessageShape(current[mismatchOffset+i])
					diagnostics.ExpectedResponseHash = canonicalMessageHash(candidate.responseMessages[i])
					diagnostics.CurrentResponseHash = canonicalMessageHash(current[mismatchOffset+i])
					break
				}
			}
			continue
		}
		diagnostics.Extendable++
	}
	return diagnostics
}

func (s *Store) Record(scope Scope, requestMessages, responseMessages []any, sessionID string, parentMessageID int) {
	if s == nil || !validScope(scope) || strings.TrimSpace(sessionID) == "" || parentMessageID <= 0 {
		return
	}
	requestCanonical, requestOK := canonicalMessages(requestMessages)
	responseCanonical, responseOK := canonicalMessages(responseMessages)
	if !requestOK || !responseOK || len(requestCanonical) == 0 || len(responseCanonical) == 0 {
		return
	}

	now := time.Now()
	key := scopeKey(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.nextID++
	created := &entry{
		id:               fmt.Sprintf("inc-%d", s.nextID),
		scope:            scope,
		sessionID:        strings.TrimSpace(sessionID),
		parentMessageID:  parentMessageID,
		requestMessages:  requestCanonical,
		responseMessages: responseCanonical,
		turns:            countTurns(requestCanonical),
		updatedAt:        now,
	}
	items := append(s.entries[key], created)
	if len(items) > s.maxBranches {
		oldest := 0
		for i := 1; i < len(items); i++ {
			if items[i].updatedAt.Before(items[oldest].updatedAt) {
				oldest = i
			}
		}
		items = append(items[:oldest], items[oldest+1:]...)
	}
	s.entries[key] = items
}

func (l *Lease) Complete(scope Scope, requestMessages, responseMessages []any, sessionID string, parentMessageID int) {
	if l == nil || l.store == nil {
		return
	}
	requestCanonical, requestOK := canonicalMessages(requestMessages)
	responseCanonical, responseOK := canonicalMessages(responseMessages)
	if !requestOK || !responseOK || len(requestCanonical) == 0 || len(responseCanonical) == 0 || !validScope(scope) || parentMessageID <= 0 || strings.TrimSpace(sessionID) == "" {
		l.Invalidate()
		return
	}
	s := l.store
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.entries[l.key] {
		if candidate.id != l.entryID {
			continue
		}
		candidate.scope = scope
		candidate.sessionID = strings.TrimSpace(sessionID)
		candidate.parentMessageID = parentMessageID
		candidate.requestMessages = requestCanonical
		candidate.responseMessages = responseCanonical
		candidate.turns = countTurns(requestCanonical)
		candidate.updatedAt = now
		candidate.busy = false
		return
	}
}

func countTurns(messages []json.RawMessage) int {
	turns := 0
	for _, raw := range messages {
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		role, _ := item["role"].(string)
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "user":
			turns++
		}
	}
	if turns == 0 && len(messages) > 0 {
		return 1
	}
	return turns
}

func (l *Lease) Invalidate() {
	if l == nil || l.store == nil {
		return
	}
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.entries[l.key]
	for i, candidate := range items {
		if candidate.id == l.entryID {
			items = append(items[:i], items[i+1:]...)
			break
		}
	}
	if len(items) == 0 {
		delete(s.entries, l.key)
	} else {
		s.entries[l.key] = items
	}
}

func (l *Lease) Release() {
	if l == nil || l.store == nil {
		return
	}
	s := l.store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.entries[l.key] {
		if candidate.id == l.entryID {
			candidate.busy = false
			return
		}
	}
}

func validScope(scope Scope) bool {
	return strings.TrimSpace(scope.CallerID) != "" &&
		strings.TrimSpace(scope.SessionKey) != "" &&
		strings.TrimSpace(scope.AccountID) != "" &&
		strings.TrimSpace(scope.Surface) != "" &&
		strings.TrimSpace(scope.Variant) != ""
}

func scopeKey(scope Scope) string {
	return strings.Join([]string{scope.CallerID, scope.SessionKey, scope.AccountID, scope.Surface, scope.Variant}, "\x00")
}

func canonicalMessages(messages []any) ([]json.RawMessage, bool) {
	out := make([]json.RawMessage, 0, len(messages))
	for _, message := range messages {
		b, err := canonicalMessage(message)
		if err != nil {
			return nil, false
		}
		out = append(out, json.RawMessage(b))
	}
	return out, true
}

func canonicalMessage(message any) ([]byte, error) {
	b, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return nil, err
	}
	item, ok := normalized.(map[string]any)
	if ok && strings.EqualFold(strings.TrimSpace(stringField(item["role"])), "assistant") {
		// Reasoning is transient provider state. Most compatible clients replay
		// only role/content/tool_calls, and Gemini strips thought parts during
		// its OpenAI round trip. The visible answer and tool calls remain strict.
		for _, key := range []string{"reasoning_content", "reasoning", "thinking", "thought_signature"} {
			delete(item, key)
		}
		if blocks, ok := item["content"].([]any); ok {
			visible := blocks[:0]
			for _, block := range blocks {
				blockMap, _ := block.(map[string]any)
				blockType := strings.ToLower(strings.TrimSpace(stringField(blockMap["type"])))
				switch blockType {
				case "reasoning", "thinking", "redacted_thinking":
					continue
				default:
					visible = append(visible, block)
				}
			}
			item["content"] = visible
		}
		normalized = item
	}
	return json.Marshal(normalized)
}

func stringField(value any) string {
	text, _ := value.(string)
	return text
}

func canonicalMessageHash(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func canonicalMessageShape(raw json.RawMessage) string {
	var item map[string]any
	if json.Unmarshal(raw, &item) != nil {
		return "invalid"
	}
	keys := make([]string, 0, len(item))
	for key := range item {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{"keys=" + strings.Join(keys, ","), "role=" + stringField(item["role"])}
	switch content := item["content"].(type) {
	case string:
		parts = append(parts, fmt.Sprintf("content=string:%d", len(content)))
	case []any:
		blockTypes := make([]string, 0, len(content))
		textBytes := 0
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			blockType := strings.TrimSpace(stringField(block["type"]))
			if blockType == "" {
				blockType = "unknown"
			}
			blockTypes = append(blockTypes, blockType)
			textBytes += len(stringField(block["text"]))
		}
		parts = append(parts, fmt.Sprintf("content=blocks:%d:%s:text_bytes=%d", len(content), strings.Join(blockTypes, ","), textBytes))
	case nil:
		parts = append(parts, "content=nil")
	default:
		parts = append(parts, fmt.Sprintf("content=%T", content))
	}
	if calls, ok := item["tool_calls"].([]any); ok {
		parts = append(parts, fmt.Sprintf("tool_calls=%d", len(calls)))
	}
	return strings.Join(parts, ";")
}

func messagePrefixEqual(current, expected []json.RawMessage, offset int) bool {
	if offset < 0 || len(current) < offset+len(expected) {
		return false
	}
	for i := range expected {
		if string(current[offset+i]) != string(expected[i]) {
			return false
		}
	}
	return true
}

func cloneAnyMessages(messages []any) []any {
	if len(messages) == 0 {
		return nil
	}
	b, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	var out []any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func (s *Store) pruneLocked(now time.Time) {
	for key, items := range s.entries {
		kept := items[:0]
		for _, candidate := range items {
			if candidate.busy || now.Sub(candidate.updatedAt) <= s.ttl {
				kept = append(kept, candidate)
			}
		}
		if len(kept) == 0 {
			delete(s.entries, key)
			continue
		}
		s.entries[key] = kept
	}
}
