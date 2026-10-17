package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// Tests for the cut-stream retry (streamretry.go). The provider under test is a
// scripted one, so each attempt plays a known sequence of events: that is what
// makes "was anything shown?" observable independently of any wire.

// scriptedProvider plays one canned script per attempt and counts them.
type scriptedProvider struct {
	mu       sync.Mutex
	scripts  [][]AssistantMessageEvent
	attempts int
}

func (p *scriptedProvider) Name() string    { return "scripted" }
func (p *scriptedProvider) Models() []Model { return nil }

func (p *scriptedProvider) StreamCompletion(ctx context.Context, _ CompletionRequest) (*AssistantMessageEventStream, error) {
	p.mu.Lock()
	i := p.attempts
	p.attempts++
	p.mu.Unlock()

	s := NewAssistantMessageEventStream(0)
	go func() {
		defer s.Close()
		if i >= len(p.scripts) {
			return
		}
		for _, ev := range p.scripts[i] {
			if err := s.Emit(ctx, ev); err != nil {
				return
			}
		}
	}()
	return s, nil
}

func (p *scriptedProvider) attemptsRun() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts
}

// cutEvent ends an attempt the way a dropped connection does, with the reader's
// own error.
func cutEvent(err error) AssistantMessageEvent {
	return StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			Provider:     "scripted",
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: err.Error(),
		},
		Err: err,
	}
}

func startEvent() AssistantMessageEvent {
	return StreamStartEvent{Partial: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}}
}

func textEvent(text string) AssistantMessageEvent {
	return StreamTextEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}}
}

func doneEvent(text string) AssistantMessageEvent {
	return StreamDoneEvent{Message: agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
		StopReason: agentcore.StopReasonEndTurn,
	}}
}

// drainEvents runs the wrapper and reports everything the caller saw.
func drainEvents(t *testing.T, inner Provider, ctx context.Context) []AssistantMessageEvent {
	t.Helper()
	stream, err := withStreamRetry(inner).StreamCompletion(ctx, CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var events []AssistantMessageEvent
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	return events
}

func countEvents[T AssistantMessageEvent](events []AssistantMessageEvent) int {
	n := 0
	for _, ev := range events {
		if _, ok := ev.(T); ok {
			n++
		}
	}
	return n
}

func lastText(events []AssistantMessageEvent) string {
	var text string
	for _, ev := range events {
		switch e := ev.(type) {
		case StreamTextEvent:
			text = agentcore.ContentToText(e.Partial.Content)
		case StreamDoneEvent:
			text = agentcore.ContentToText(e.Message.Content)
		}
	}
	return text
}

func errorMessage(events []AssistantMessageEvent) string {
	for _, ev := range events {
		if e, ok := ev.(StreamErrorEvent); ok {
			return e.Message.ErrorMessage
		}
	}
	return ""
}

// A stream cut before anything was shown is re-issued, and the caller never
// learns the first attempt happened: one start, the retry's content, no error.
// That is the reported failure — "error: unexpected EOF" on a turn that had
// produced nothing — turned into the answer the user asked for.
func TestStreamRetryReissuesACutAttemptInvisibly(t *testing.T) {
	for _, cut := range []struct {
		name string
		err  error
	}{
		{"dropped connection", io.ErrUnexpectedEOF},
		{"idle connection", fmt.Errorf("no data for 5m0s: %w", errStreamIdle)},
		{"quiet model", fmt.Errorf("no output for 6m0s: %w", errStreamStall)},
	} {
		t.Run(cut.name, func(t *testing.T) {
			inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
				{startEvent(), cutEvent(cut.err)},
				{startEvent(), textEvent("recovered"), doneEvent("recovered")},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			events := drainEvents(t, inner, ctx)

			if got := inner.attemptsRun(); got != 2 {
				t.Errorf("attempts = %d, want 2 (the cut attempt and one retry)", got)
			}
			if got := countEvents[StreamStartEvent](events); got != 1 {
				t.Errorf("start events = %d, want exactly 1 (the cut attempt's start is dropped)", got)
			}
			if got := countEvents[StreamErrorEvent](events); got != 0 {
				t.Errorf("error events = %d (%q), want none: the retry answered", got, errorMessage(events))
			}
			if got := lastText(events); got != "recovered" {
				t.Errorf("final text = %q, want the retry's answer", got)
			}
		})
	}
}

// A cut that lands after content cannot be retried: resending would duplicate
// what the user already read. It must at least say what happened — the reader's
// bare "unexpected EOF" names nothing.
func TestStreamRetryKeepsContentAndNamesTheCut(t *testing.T) {
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("half an answer"), cutEvent(io.ErrUnexpectedEOF)},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 1 {
		t.Errorf("attempts = %d, want 1 (content was shown, so no retry)", got)
	}
	if got := lastText(events); got != "half an answer" {
		t.Errorf("text = %q, want the streamed content kept", got)
	}
	got := errorMessage(events)
	if !strings.Contains(got, "scripted: stream cut") {
		t.Errorf("failure = %q, want it named", got)
	}
	if !strings.Contains(got, "closed before the answer finished") {
		t.Errorf("failure = %q, want the cut described", got)
	}
}

// One retry, not a loop: a connection that drops twice is not a problem another
// request fixes, and the caller still needs a terminal event.
func TestStreamRetryGivesUpAfterASecondCut(t *testing.T) {
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), cutEvent(io.ErrUnexpectedEOF)},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2 (one retry, bounded)", got)
	}
	if got := countEvents[StreamErrorEvent](events); got != 1 {
		t.Fatalf("error events = %d, want exactly 1 terminal error", got)
	}
	if got := errorMessage(events); !strings.Contains(got, "stream cut") {
		t.Errorf("failure = %q, want the cut named even when the retry failed", got)
	}
}

// Anything that is not a cut is the wire's real answer: it must be surfaced
// unchanged, without a second request. "model_coming_soon" is the reported case
// that must never be masked.
func TestStreamRetryLeavesRealFailuresAlone(t *testing.T) {
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(&fallbackTestErr{"400 invalid_request_error: [model_coming_soon] This model is coming soon."})},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 1 {
		t.Errorf("attempts = %d, want 1 (a real failure is not retried)", got)
	}
	if got := errorMessage(events); !strings.Contains(got, "model_coming_soon") {
		t.Errorf("failure = %q, want the wire's own answer untouched", got)
	}
}

// A stream that ends without any terminal event and without content is as cut as
// a dropped connection, and worth the same single retry.
func TestStreamRetryRetriesASilentlyEndedAttempt(t *testing.T) {
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent()}, // the producer ends without a terminal event
		{startEvent(), textEvent("recovered"), doneEvent("recovered")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if got := lastText(events); got != "recovered" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
}

// A resolved provider carries the wrapper, whichever way it was selected, and
// UnwrapProvider still reaches the concrete driver underneath.
func TestResolvedProvidersCarryTheStreamRetry(t *testing.T) {
	p, name, err := ResolveNamedProvider("opencode-go", "glm-5.3", "", "", func(string) string { return "k" })
	if err != nil {
		t.Fatalf("ResolveNamedProvider: %v", err)
	}
	if name != "opencode-go" {
		t.Errorf("provider name = %q, want opencode-go", name)
	}
	if _, ok := p.(*streamRetryProvider); !ok {
		t.Fatalf("resolved provider = %T, want the cut-stream retry wrapper", p)
	}
	if UnwrapProvider(p) == nil {
		t.Fatal("UnwrapProvider returned nil")
	}
}
