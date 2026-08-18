package responses

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"

	"github.com/google/uuid"
)

const localCompactionHandlePrefix = "ds2api_compact_"

const minimumCompactionIdleTTL = 24 * time.Hour

type storedResponse struct {
	Owner     string
	Value     map[string]any
	ExpiresAt time.Time
}

type responseStore struct {
	mu            sync.Mutex
	ttl           time.Duration
	compactionTTL time.Duration
	now           func() time.Time
	items         map[string]storedResponse
	inputs        map[string]storedInput
	sessions      map[string]storedSession
	compactions   map[string]storedCompaction
}

type storedInput struct {
	Owner         string
	Messages      []any
	Tools         any
	HasTools      bool
	ToolChoice    any
	HasToolChoice bool
	ExpiresAt     time.Time
}

type storedSession struct {
	Owner      string
	SessionKey string
	ExpiresAt  time.Time
}

// storedCompaction holds the locally compacted, canonical message history
// behind an opaque handle returned through the Responses compaction protocol.
// It is deliberately process-local: this proxy cannot mint provider-owned
// encrypted state that another Responses implementation could validate.
type storedCompaction struct {
	Owner         string
	Messages      []any
	Tools         any
	HasTools      bool
	ToolChoice    any
	HasToolChoice bool
	ExpiresAt     time.Time
}

func newResponseStore(ttl time.Duration) *responseStore {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &responseStore{
		ttl:           ttl,
		compactionTTL: maxDuration(ttl, minimumCompactionIdleTTL),
		now:           time.Now,
		items:         make(map[string]storedResponse),
		inputs:        make(map[string]storedInput),
		sessions:      make(map[string]storedSession),
		compactions:   make(map[string]storedCompaction),
	}
}

func (s *responseStore) putCompaction(owner string, messages []any) string {
	return s.putCompactionState(owner, messages, nil, false, nil, false)
}

func (s *responseStore) putCompactionState(owner string, messages []any, tools any, hasTools bool, toolChoice any, hasToolChoice bool) string {
	if s == nil || owner == "" || len(messages) == 0 {
		return ""
	}
	handle := localCompactionHandlePrefix + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if s.compactions == nil {
		s.compactions = make(map[string]storedCompaction)
	}
	s.compactions[responseStoreKey(owner, handle)] = storedCompaction{
		Owner:         owner,
		Messages:      cloneAnySlice(messages),
		Tools:         cloneAnyValue(tools),
		HasTools:      hasTools,
		ToolChoice:    cloneAnyValue(toolChoice),
		HasToolChoice: hasToolChoice,
		ExpiresAt:     now.Add(s.compactionTTL),
	}
	config.Logger.Info("[responses_compaction_store] stored",
		"owner_fingerprint", responseStateFingerprint(owner),
		"handle_fingerprint", responseStateFingerprint(handle),
		"messages", len(messages),
		"context_bytes", responseStateSize(messages),
		"tools_present", hasTools,
		"tool_count", responseToolCount(tools),
		"tool_choice_present", hasToolChoice,
		"tool_contract_fingerprint", responseToolContractFingerprint(tools, hasTools, toolChoice, hasToolChoice),
		"idle_ttl_seconds", int64(s.compactionTTL/time.Second),
	)
	return handle
}

func (s *responseStore) getCompaction(owner, handle string) ([]any, bool) {
	item, ok := s.getCompactionState(owner, handle)
	if !ok {
		return nil, false
	}
	return item.Messages, true
}

func (s *responseStore) getCompactionState(owner, handle string) (storedCompaction, bool) {
	if s == nil || owner == "" || !strings.HasPrefix(handle, localCompactionHandlePrefix) {
		return storedCompaction{}, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	key := responseStoreKey(owner, handle)
	item, ok := s.compactions[key]
	if !ok || item.Owner != owner {
		config.Logger.Warn("[responses_compaction_store] miss",
			"owner_fingerprint", responseStateFingerprint(owner),
			"handle_fingerprint", responseStateFingerprint(handle),
		)
		return storedCompaction{}, false
	}
	item.ExpiresAt = now.Add(s.compactionTTL)
	s.compactions[key] = item
	config.Logger.Info("[responses_compaction_store] hit",
		"owner_fingerprint", responseStateFingerprint(owner),
		"handle_fingerprint", responseStateFingerprint(handle),
		"messages", len(item.Messages),
		"context_bytes", responseStateSize(item.Messages),
		"tools_present", item.HasTools,
		"tool_count", responseToolCount(item.Tools),
		"tool_choice_present", item.HasToolChoice,
		"tool_contract_fingerprint", responseToolContractFingerprint(item.Tools, item.HasTools, item.ToolChoice, item.HasToolChoice),
		"idle_ttl_seconds", int64(s.compactionTTL/time.Second),
	)
	item.Messages = cloneAnySlice(item.Messages)
	item.Tools = cloneAnyValue(item.Tools)
	item.ToolChoice = cloneAnyValue(item.ToolChoice)
	return item, true
}

// putInput stores the canonical input messages used for a response. Keeping
// this separate from the wire response object allows previous_response_id to
// reconstruct a new request without pretending DeepSeek can persist opaque
// provider-owned encrypted reasoning state.
func (s *responseStore) putInput(owner, id string, messages []any) {
	s.putInputState(owner, id, messages, nil, false, nil, false)
}

func (s *responseStore) putInputState(owner, id string, messages []any, tools any, hasTools bool, toolChoice any, hasToolChoice bool) {
	if s == nil || owner == "" || id == "" || len(messages) == 0 {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	s.inputs[responseStoreKey(owner, id)] = storedInput{
		Owner:         owner,
		Messages:      cloneAnySlice(messages),
		Tools:         cloneAnyValue(tools),
		HasTools:      hasTools,
		ToolChoice:    cloneAnyValue(toolChoice),
		HasToolChoice: hasToolChoice,
		ExpiresAt:     now.Add(s.ttl),
	}
	config.Logger.Info("[responses_state] stored input snapshot",
		"owner_fingerprint", responseStateFingerprint(owner),
		"response_id_fingerprint", responseStateFingerprint(id),
		"messages", len(messages),
		"context_bytes", responseStateSize(messages),
		"tools_present", hasTools,
		"tool_count", responseToolCount(tools),
		"tool_choice_present", hasToolChoice,
		"tool_contract_fingerprint", responseToolContractFingerprint(tools, hasTools, toolChoice, hasToolChoice),
		"ttl_seconds", int64(s.ttl/time.Second),
	)
}

func (s *responseStore) getInputState(owner, id string) (storedInput, bool) {
	if s == nil || owner == "" || id == "" {
		return storedInput{}, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	item, ok := s.inputs[responseStoreKey(owner, id)]
	if !ok || item.Owner != owner {
		config.Logger.Warn("[responses_state] input snapshot miss",
			"owner_fingerprint", responseStateFingerprint(owner),
			"response_id_fingerprint", responseStateFingerprint(id),
		)
		return storedInput{}, false
	}
	config.Logger.Info("[responses_state] input snapshot hit",
		"owner_fingerprint", responseStateFingerprint(owner),
		"response_id_fingerprint", responseStateFingerprint(id),
		"messages", len(item.Messages),
		"context_bytes", responseStateSize(item.Messages),
		"tools_present", item.HasTools,
		"tool_count", responseToolCount(item.Tools),
		"tool_choice_present", item.HasToolChoice,
		"tool_contract_fingerprint", responseToolContractFingerprint(item.Tools, item.HasTools, item.ToolChoice, item.HasToolChoice),
		"ttl_remaining_seconds", int64(item.ExpiresAt.Sub(now).Seconds()),
	)
	item.Messages = cloneAnySlice(item.Messages)
	item.Tools = cloneAnyValue(item.Tools)
	item.ToolChoice = cloneAnyValue(item.ToolChoice)
	return item, true
}

func (s *responseStore) putSessionKey(owner, id, sessionKey string) {
	if s == nil || owner == "" || id == "" || strings.TrimSpace(sessionKey) == "" {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if s.sessions == nil {
		s.sessions = make(map[string]storedSession)
	}
	s.sessions[responseStoreKey(owner, id)] = storedSession{
		Owner:      owner,
		SessionKey: strings.TrimSpace(sessionKey),
		ExpiresAt:  now.Add(s.ttl),
	}
}

func (s *responseStore) getSessionKey(owner, id string) (string, bool) {
	if s == nil || owner == "" || id == "" {
		return "", false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	item, ok := s.sessions[responseStoreKey(owner, id)]
	if !ok || item.Owner != owner || strings.TrimSpace(item.SessionKey) == "" {
		return "", false
	}
	return item.SessionKey, true
}

func responseStoreKey(owner, id string) string {
	return owner + "\x00" + id
}

func responseStoreOwner(a *auth.RequestAuth) string {
	if a == nil {
		return ""
	}
	return a.CallerID
}

func (s *responseStore) put(owner, id string, value map[string]any) {
	if s == nil || owner == "" || id == "" || value == nil {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	s.items[responseStoreKey(owner, id)] = storedResponse{
		Owner:     owner,
		Value:     cloneAnyMap(value),
		ExpiresAt: now.Add(s.ttl),
	}
}

func (s *responseStore) get(owner, id string) (map[string]any, bool) {
	if s == nil || owner == "" || id == "" {
		return nil, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	item, ok := s.items[responseStoreKey(owner, id)]
	if !ok {
		return nil, false
	}
	if item.Owner != owner {
		return nil, false
	}
	return cloneAnyMap(item.Value), true
}

func (s *responseStore) sweepLocked(now time.Time) {
	for k, v := range s.items {
		if now.After(v.ExpiresAt) {
			delete(s.items, k)
		}
	}
	for k, v := range s.inputs {
		if now.After(v.ExpiresAt) {
			delete(s.inputs, k)
		}
	}
	for k, v := range s.sessions {
		if now.After(v.ExpiresAt) {
			delete(s.sessions, k)
		}
	}
	for k, v := range s.compactions {
		if now.After(v.ExpiresAt) {
			delete(s.compactions, k)
		}
	}
}

func (s *responseStore) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func responseStateFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func responseStateSize(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(raw)
}

func responseToolCount(value any) int {
	switch tools := value.(type) {
	case []any:
		return len(tools)
	case []map[string]any:
		return len(tools)
	default:
		return 0
	}
}

func responseStateItemCount(value any) int {
	switch items := value.(type) {
	case []any:
		return len(items)
	case []map[string]any:
		return len(items)
	default:
		return 0
	}
}

// responseToolContractFingerprint lets logs correlate a tool schema across
// state transfers without writing the schema, descriptions, or arguments to
// the log file.
func responseToolContractFingerprint(tools any, hasTools bool, toolChoice any, hasToolChoice bool) string {
	payload := struct {
		Tools         any  `json:"tools"`
		HasTools      bool `json:"has_tools"`
		ToolChoice    any  `json:"tool_choice"`
		HasToolChoice bool `json:"has_tool_choice"`
	}{
		Tools:         tools,
		HasTools:      hasTools,
		ToolChoice:    toolChoice,
		HasToolChoice: hasToolChoice,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "unavailable"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAnySlice(in []any) []any {
	if in == nil {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}

// cloneAnyValue round-trips JSON-compatible request fragments so tool schemas
// cannot be mutated through a caller-owned map after they enter the store.
func cloneAnyValue(in any) any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return in
	}
	return out
}

func (h *Handler) getResponseStore() *responseStore {
	if h == nil {
		return nil
	}
	h.responsesMu.Lock()
	defer h.responsesMu.Unlock()
	if h.responses == nil {
		ttl := 15 * time.Minute
		if h.Store != nil {
			ttl = time.Duration(h.Store.ResponsesStoreTTLSeconds()) * time.Second
		}
		h.responses = newResponseStore(ttl)
	}
	return h.responses
}
