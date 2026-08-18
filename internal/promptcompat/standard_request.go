package promptcompat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"DeepSeek_Web_To_API/internal/config"
)

type StandardRequest struct {
	Surface        string
	RequestedModel string
	ResolvedModel  string
	ResponseModel  string
	Messages       []any
	// LatestUserText is the raw text content of the most recent user
	// message in the request, with no system prompt / history / gateway
	// injections / DeepSeek protocol markers attached. v1.0.19 LLM
	// safety review uses this instead of FinalPrompt so the audit LLM
	// only sees what the human typed — passing FinalPrompt was causing
	// the audit to flag legitimate short prompts because it saw the
	// gateway's own system instructions ("integrity guard ... adversarial
	// inputs ...") and treated them as user content.
	LatestUserText                string
	HistoryText                   string
	PromptTokenText               string
	CurrentInputFileApplied       bool
	CurrentInputPrefixHash        string // hex sha256[:16] of the cached prefix transcript when applied
	CurrentInputPrefixReused      bool   // true when the prefix-cache hit reused a prior file_id (no upload)
	CurrentInputPrefixChars       int    // size of the cached/uploaded prefix portion in bytes
	CurrentInputTailChars         int    // size of the freshly-included tail (newest turns since last prefix snap)
	CurrentInputTailEntries       int    // number of role-blocks in the tail (parsed via `\n=== ` markers)
	CurrentInputCheckpointRefresh bool   // first turn or prefix-string mismatch — a fresh prefix was uploaded
	ToolsRaw                      any
	// Preserve JSON field presence when a local response/compaction snapshot
	// is replayed. A nil value can be explicit, so value nil alone is not
	// enough to distinguish it from an omitted field.
	HasTools                  bool
	HasToolChoice             bool
	ToolChoiceRaw             any
	FinalPrompt               string
	IncrementalFormatPrompt   string
	IncrementalSessionRotated bool
	ToolNames                 []string
	ToolChoice                ToolChoicePolicy
	Stream                    bool
	Thinking                  bool
	ExposeReasoning           bool
	Search                    bool
	RefFileIDs                []string
	RefFileTokens             int
	PassThrough               map[string]any
}

func (r StandardRequest) IncrementalVariant() string {
	payload := struct {
		Surface      string
		Model        string
		Thinking     bool
		Search       bool
		FormatPrompt string
		Tools        any
		ToolChoice   ToolChoicePolicy
		PassThrough  map[string]any
	}{
		Surface:      r.Surface,
		Model:        r.ResolvedModel,
		Thinking:     r.Thinking,
		Search:       r.Search,
		FormatPrompt: r.IncrementalFormatPrompt,
		Tools:        r.ToolsRaw,
		ToolChoice:   r.ToolChoice,
		PassThrough:  r.PassThrough,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceForced   ToolChoiceMode = "forced"
)

type ToolChoicePolicy struct {
	Mode       ToolChoiceMode
	ForcedName string
	Allowed    map[string]struct{}
}

func DefaultToolChoicePolicy() ToolChoicePolicy {
	return ToolChoicePolicy{Mode: ToolChoiceAuto}
}

func (p ToolChoicePolicy) IsNone() bool {
	return p.Mode == ToolChoiceNone
}

func (p ToolChoicePolicy) IsRequired() bool {
	return p.Mode == ToolChoiceRequired || p.Mode == ToolChoiceForced
}

func (p ToolChoicePolicy) Allows(name string) bool {
	if len(p.Allowed) == 0 {
		return true
	}
	_, ok := p.Allowed[name]
	return ok
}

func (r StandardRequest) CompletionPayload(sessionID string) map[string]any {
	modelID := r.ResolvedModel
	if modelID == "" {
		modelID = r.RequestedModel
	}
	modelType := "default"
	if resolvedType, ok := config.GetModelType(modelID); ok {
		modelType = resolvedType
	}
	refFileIDs := make([]any, 0, len(r.RefFileIDs))
	for _, fileID := range r.RefFileIDs {
		if fileID == "" {
			continue
		}
		refFileIDs = append(refFileIDs, fileID)
	}
	payload := map[string]any{
		"chat_session_id":   sessionID,
		"model_type":        modelType,
		"parent_message_id": nil,
		"prompt":            r.FinalPrompt,
		"ref_file_ids":      refFileIDs,
		"thinking_enabled":  r.Thinking,
		"search_enabled":    r.Search,
	}
	for k, v := range r.PassThrough {
		payload[k] = v
	}
	return payload
}
