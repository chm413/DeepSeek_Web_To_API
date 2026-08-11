package upstreamsession

import "testing"

func TestStorePrepareReturnsOnlyStrictExtensionDelta(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "chat", Variant: "v1"}
	request := []any{map[string]any{"role": "user", "content": "first"}}
	response := []any{map[string]any{"role": "assistant", "content": "answer"}}
	store.Record(scope, request, response, "session-1", 101)

	current := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "answer"},
		map[string]any{"role": "user", "content": "second"},
	}
	lease, ok := store.Prepare(scope, current)
	if !ok {
		t.Fatal("expected strict extension to reuse the stored session")
	}
	if lease.SessionID != "session-1" || lease.ParentMessageID != 101 {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	if len(lease.DeltaMessages) != 1 {
		t.Fatalf("expected one delta message, got %#v", lease.DeltaMessages)
	}

	if _, busyHit := store.Prepare(scope, current); busyHit {
		t.Fatal("busy branch must not be leased concurrently")
	}
	lease.Invalidate()
}

func TestStoreRejectsEditedAssistantBranch(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "chat", Variant: "v1"}
	store.Record(
		scope,
		[]any{map[string]any{"role": "user", "content": "first"}},
		[]any{map[string]any{"role": "assistant", "content": "original"}},
		"session-1",
		101,
	)
	current := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "edited"},
		map[string]any{"role": "user", "content": "branch"},
	}
	if _, ok := store.Prepare(scope, current); ok {
		t.Fatal("edited assistant history must start a new upstream branch")
	}
}

func TestStoreIgnoresTransientAssistantReasoningFields(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "chat", Variant: "v1"}
	store.Record(
		scope,
		[]any{map[string]any{"role": "user", "content": "first"}},
		[]any{map[string]any{
			"role":              "assistant",
			"content":           "answer",
			"reasoning_content": "private reasoning",
		}},
		"session-1",
		101,
	)
	current := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "answer"},
		map[string]any{"role": "user", "content": "second"},
	}
	if _, ok := store.Prepare(scope, current); !ok {
		t.Fatal("omitted transient reasoning must still reuse the visible assistant branch")
	}
}

func TestStoreIgnoresTransientAssistantReasoningBlocks(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "responses", Variant: "v1"}
	store.Record(
		scope,
		[]any{map[string]any{"role": "user", "content": "first"}},
		[]any{map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "reasoning", "text": "private reasoning"},
				map[string]any{"type": "output_text", "text": "answer"},
			},
		}},
		"session-1",
		101,
	)
	current := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}},
		map[string]any{"role": "user", "content": "second"},
	}
	if _, ok := store.Prepare(scope, current); !ok {
		t.Fatal("omitted reasoning content block must still reuse the visible assistant branch")
	}
}

func TestStoreDiagnoseReportsResponsePrefixMismatch(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "chat", Variant: "v1"}
	store.Record(
		scope,
		[]any{map[string]any{"role": "user", "content": "first"}},
		[]any{map[string]any{"role": "assistant", "content": "answer"}},
		"session-1",
		101,
	)
	diagnostics := store.Diagnose(scope, []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "edited"},
		map[string]any{"role": "user", "content": "second"},
	})
	if diagnostics.Branches != 1 || diagnostics.ResponsePrefixMismatch != 1 || diagnostics.Extendable != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}

func TestLeaseReleaseMakesBranchReusable(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "chat", Variant: "v1"}
	store.Record(
		scope,
		[]any{map[string]any{"role": "user", "content": "first"}},
		[]any{map[string]any{"role": "assistant", "content": "answer"}},
		"session-1",
		101,
	)
	current := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "answer"},
		map[string]any{"role": "user", "content": "second"},
	}
	lease, ok := store.Prepare(scope, current)
	if !ok {
		t.Fatal("expected initial lease")
	}
	lease.Release()
	if _, ok := store.Prepare(scope, current); !ok {
		t.Fatal("released branch should be reusable")
	}
}

func TestStoreMarksStrictExtensionForConfiguredTurnRotation(t *testing.T) {
	store := NewStore(0, 0)
	scope := Scope{CallerID: "caller", SessionKey: "conversation", AccountID: "account", Surface: "chat", Variant: "v1"}
	store.Record(
		scope,
		[]any{map[string]any{"role": "user", "content": "first"}},
		[]any{map[string]any{"role": "assistant", "content": "answer"}},
		"session-1",
		101,
	)
	current := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "answer"},
		map[string]any{"role": "user", "content": "second"},
	}
	lease, ok := store.PrepareWithMaxTurns(scope, current, 1)
	if !ok {
		t.Fatal("expected strict extension lease")
	}
	if !lease.Rotate || lease.TurnCount != 1 {
		t.Fatalf("expected one-turn branch to rotate: %#v", lease)
	}
	if len(lease.DeltaMessages) != 1 {
		t.Fatalf("rotation lease lost current delta: %#v", lease.DeltaMessages)
	}
	lease.Invalidate()
}
