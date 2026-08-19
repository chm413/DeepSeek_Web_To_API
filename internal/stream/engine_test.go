package stream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/sse"
)

func TestConsumeSSEPrefersContextCancellationOverReadyParsedLines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var finalized bool
	var contextDone bool
	var parsedCalled bool

	ConsumeSSE(ConsumeConfig{
		Context:           ctx,
		Body:              strings.NewReader("data: {\"p\":\"response/content\",\"v\":\"hello\"}\n\ndata: [DONE]\n"),
		ThinkingEnabled:   false,
		InitialType:       "text",
		KeepAliveInterval: 0,
	}, ConsumeHooks{
		OnParsed: func(_ sse.LineResult) ParsedDecision {
			parsedCalled = true
			return ParsedDecision{}
		},
		OnFinalize: func(_ StopReason, _ error) {
			finalized = true
		},
		OnContextDone: func() {
			contextDone = true
		},
	})

	if !contextDone {
		t.Fatal("expected OnContextDone to run for an already-cancelled context")
	}
	if finalized {
		t.Fatal("expected OnFinalize not to run after context cancellation wins")
	}
	if parsedCalled {
		t.Fatal("expected parsed lines not to be processed after context cancellation wins")
	}
}

func TestConsumeSSEReportsUnexpectedEOFAfterPartialOutput(t *testing.T) {
	var gotReason StopReason
	var gotErr error

	ConsumeSSE(ConsumeConfig{
		Context:           context.Background(),
		Body:              strings.NewReader("data: {\"p\":\"response/content\",\"v\":\"partial\"}\n"),
		ThinkingEnabled:   false,
		InitialType:       "text",
		KeepAliveInterval: 0,
	}, ConsumeHooks{
		OnParsed: func(parsed sse.LineResult) ParsedDecision {
			return ParsedDecision{ContentSeen: len(parsed.Parts) > 0}
		},
		OnFinalize: func(reason StopReason, scannerErr error) {
			gotReason = reason
			gotErr = scannerErr
		},
	})

	if gotReason != StopReasonUpstreamCompleted {
		t.Fatalf("expected upstream_completed reason, got %q", gotReason)
	}
	if !errors.Is(gotErr, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", gotErr)
	}
}
