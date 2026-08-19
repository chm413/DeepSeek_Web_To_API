package sse

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStartParsedLinePumpEmptyBody(t *testing.T) {
	body := strings.NewReader("")
	results, done := StartParsedLinePump(context.Background(), body, false, "text")

	collected := make([]LineResult, 0)
	for r := range results {
		collected = append(collected, r)
	}
	if err := <-done; err != nil {
		t.Fatalf("unexpected error for an empty stream: %v", err)
	}
	if len(collected) != 0 {
		t.Fatalf("expected no results for empty body, got %d", len(collected))
	}
}

func TestStartParsedLinePumpReportsUnexpectedEOFAfterContent(t *testing.T) {
	body := strings.NewReader("data: {\"p\":\"response/content\",\"v\":\"partial\"}\n")
	results, done := StartParsedLinePump(context.Background(), body, false, "text")

	var content string
	for result := range results {
		for _, part := range result.Parts {
			content += part.Text
		}
	}
	if content != "partial" {
		t.Fatalf("expected partial content to be delivered before EOF, got %q", content)
	}
	if err := <-done; !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF after partial content, got %v", err)
	}
}

func TestStartParsedLinePumpFinishedStatusCountsAsTerminal(t *testing.T) {
	body := strings.NewReader("data: {\"p\":\"response/status\",\"v\":\"FINISHED\"}\n")
	results, done := StartParsedLinePump(context.Background(), body, false, "text")
	for range results {
	}
	if err := <-done; err != nil {
		t.Fatalf("expected FINISHED status to terminate cleanly, got %v", err)
	}
}

func TestStartParsedLinePumpMultipleLines(t *testing.T) {
	body := strings.NewReader(
		"data: {\"p\":\"response/thinking_content\",\"v\":\"think\"}\n" +
			"data: {\"p\":\"response/content\",\"v\":\"text\"}\n" +
			"data: [DONE]\n",
	)
	results, done := StartParsedLinePump(context.Background(), body, true, "thinking")

	collected := make([]LineResult, 0)
	for r := range results {
		collected = append(collected, r)
	}
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collected) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(collected))
	}
	// First should be thinking
	if collected[0].Parts[0].Type != "thinking" {
		t.Fatalf("expected first part thinking, got %q", collected[0].Parts[0].Type)
	}
	// Last should be stop
	last := collected[len(collected)-1]
	if !last.Stop {
		t.Fatal("expected last result to be stop")
	}
}

func TestStartParsedLinePumpTypeTracking(t *testing.T) {
	body := strings.NewReader(
		"data: {\"p\":\"response/fragments\",\"o\":\"APPEND\",\"v\":[{\"type\":\"THINK\",\"content\":\"思\"}]}\n" +
			"data: {\"p\":\"response/fragments/-1/content\",\"v\":\"考\"}\n" +
			"data: {\"p\":\"response/fragments\",\"o\":\"APPEND\",\"v\":[{\"type\":\"RESPONSE\",\"content\":\"答\"}]}\n" +
			"data: {\"p\":\"response/fragments/-1/content\",\"v\":\"案\"}\n" +
			"data: [DONE]\n",
	)
	results, done := StartParsedLinePump(context.Background(), body, true, "text")

	types := make([]string, 0)
	for r := range results {
		for _, p := range r.Parts {
			types = append(types, p.Type)
		}
	}
	<-done

	// Should have: thinking, thinking, text, text
	expected := []string{"thinking", "thinking", "text", "text"}
	if len(types) != len(expected) {
		t.Fatalf("expected types %v, got %v", expected, types)
	}
	for i, want := range expected {
		if types[i] != want {
			t.Fatalf("type[%d] mismatch: want %q got %q (all=%v)", i, want, types[i], types)
		}
	}
}

func TestStartParsedLinePumpContextCancellation(t *testing.T) {
	pr, pw := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	results, done := StartParsedLinePump(ctx, pr, false, "text")

	// Write one line to allow it to start
	go func() {
		_, _ = io.WriteString(pw, "data: {\"p\":\"response/content\",\"v\":\"hello\"}\n")
		// Don't close yet - wait for context cancel
	}()

	// Read first result
	r := <-results
	if !r.Parsed || len(r.Parts) == 0 {
		t.Fatalf("expected first parsed result, got %#v", r)
	}

	// Cancel context - this will cause the pump to exit on next send
	cancel()
	// Close the pipe to unblock scanner.Scan()
	_ = pw.Close()

	// Drain remaining results
	for range results {
	}

	err := <-done
	// Error may be context.Canceled or nil (if pipe closed first)
	if err != nil && err != context.Canceled {
		t.Fatalf("expected context.Canceled or nil error, got %v", err)
	}
}

func TestStartParsedLinePumpOnlyDONE(t *testing.T) {
	body := strings.NewReader("data: [DONE]\n")
	results, done := StartParsedLinePump(context.Background(), body, false, "text")

	collected := make([]LineResult, 0)
	for r := range results {
		collected = append(collected, r)
	}
	<-done

	if len(collected) != 1 {
		t.Fatalf("expected 1 result, got %d", len(collected))
	}
	if !collected[0].Stop {
		t.Fatal("expected stop on [DONE]")
	}
}

func TestStartParsedLinePumpNonSSELines(t *testing.T) {
	body := strings.NewReader(
		"event: update\n" +
			": comment line\n" +
			"data: {\"p\":\"response/content\",\"v\":\"valid\"}\n" +
			"data: [DONE]\n",
	)
	results, done := StartParsedLinePump(context.Background(), body, false, "text")

	var validCount int
	for r := range results {
		if r.Parsed && len(r.Parts) > 0 {
			validCount++
		}
	}
	<-done

	if validCount != 1 {
		t.Fatalf("expected 1 valid result, got %d", validCount)
	}
}

func TestStartParsedLinePumpThinkingDisabled(t *testing.T) {
	body := strings.NewReader(
		"data: {\"p\":\"response/fragments\",\"o\":\"APPEND\",\"v\":[{\"type\":\"THINK\",\"content\":\"思\"}]}\n" +
			"data: {\"p\":\"response/fragments/-1/content\",\"v\":\"考\"}\n" +
			"data: {\"v\":\"隐藏\"}\n" +
			"data: {\"p\":\"response/fragments\",\"o\":\"APPEND\",\"v\":[{\"type\":\"RESPONSE\",\"content\":\"答\"}]}\n" +
			"data: {\"p\":\"response/content\",\"v\":\"response\"}\n" +
			"data: [DONE]\n",
	)
	results, done := StartParsedLinePump(context.Background(), body, false, "text")

	var parts []ContentPart
	for r := range results {
		parts = append(parts, r.Parts...)
	}
	<-done

	got := strings.Builder{}
	for _, p := range parts {
		if p.Type != "text" {
			t.Fatalf("expected only text parts with thinking disabled, got %#v", parts)
		}
		got.WriteString(p.Text)
	}
	if got.String() != "答response" {
		t.Fatalf("expected hidden thinking to be dropped, got %q from %#v", got.String(), parts)
	}
}

func TestStartParsedLinePumpAccumulatesSmallChunks(t *testing.T) {
	body := strings.NewReader(
		"data: {\"p\":\"response/content\",\"v\":\"h\"}\n" +
			"data: {\"p\":\"response/content\",\"v\":\"i\"}\n" +
			"data: [DONE]\n",
	)

	results, done := StartParsedLinePump(context.Background(), body, false, "text")

	collected := make([]LineResult, 0)
	for r := range results {
		collected = append(collected, r)
	}
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(collected) != 2 {
		t.Fatalf("expected 2 results (accumulated content + done), got %d", len(collected))
	}
	if len(collected[0].Parts) != 2 {
		t.Fatalf("expected 2 accumulated parts, got %d", len(collected[0].Parts))
	}
	if !collected[1].Stop {
		t.Fatal("expected second result to stop")
	}
}
