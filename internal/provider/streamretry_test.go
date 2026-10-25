package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/openai/openai-go/option"
)

// Tests for the cut-stream retry (streamretry.go). The provider under test is a
// scripted one, so each attempt plays a known sequence of events: that is what
// makes "was anything shown?" observable independently of any wire.

// scriptedProvider plays one canned script per attempt and counts them.
type scriptedProvider struct {
	mu      sync.Mutex
	scripts [][]AssistantMessageEvent
	// connectErrs, when set, makes an attempt fail before any stream exists:
	// attempt i returns connectErrs[i] instead of playing its script. It is how
	// the reported `Post "…": EOF` is reproduced.
	connectErrs []error
	attempts    int
	// requests keeps every request the wrapper sent, so a test can look at what
	// a retry actually replayed: the continuation context, or the original one.
	requests []CompletionRequest
}

func (p *scriptedProvider) Name() string    { return "scripted" }
func (p *scriptedProvider) Models() []Model { return nil }

func (p *scriptedProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	p.mu.Lock()
	i := p.attempts
	p.attempts++
	p.requests = append(p.requests, req)
	var connectErr error
	if i < len(p.connectErrs) {
		connectErr = p.connectErrs[i]
	}
	p.mu.Unlock()

	if connectErr != nil {
		return nil, connectErr
	}
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

// request returns the request attempt i was sent (0-based).
func (p *scriptedProvider) request(i int) CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.requests) {
		return CompletionRequest{}
	}
	return p.requests[i]
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

// thinkingEvent streams a reasoning partial: the model working, not an answer.
// It is what a cut lands on when the connection dies before the answer starts.
func thinkingEvent(thinking string) AssistantMessageEvent {
	return StreamThinkingEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content:   agentcore.ContentList{agentcore.NewThinkingContent(thinking)},
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
	stream, err := withStreamRetry(inner, "").StreamCompletion(ctx, CompletionRequest{Model: "m"})
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

func noticeEvents(events []AssistantMessageEvent) []StreamNoticeEvent {
	var out []StreamNoticeEvent
	for _, ev := range events {
		if n, ok := ev.(StreamNoticeEvent); ok {
			out = append(out, n)
		}
	}
	return out
}

// toolCallIDs lists every tool-call id the caller was shown, in order. The
// wrapper holds tool-call deltas, so this is what the caller actually saw: the
// calls of the terminal message, and never a fragment of a dropped attempt.
func toolCallIDs(events []AssistantMessageEvent) []string {
	var ids []string
	for _, ev := range events {
		if e, ok := ev.(StreamToolCallEvent); ok {
			for _, c := range e.Partial.ToolCalls() {
				ids = append(ids, c.ID)
			}
		}
	}
	return ids
}

// terminalMessage is the message the stream ended with.
func terminalMessage(events []AssistantMessageEvent) agentcore.AssistantMessage {
	for i := len(events) - 1; i >= 0; i-- {
		switch e := events[i].(type) {
		case StreamDoneEvent:
			return e.Message
		case StreamErrorEvent:
			return e.Message
		}
	}
	return agentcore.AssistantMessage{}
}

// fastRetryBackoff removes the production delay so a retry test runs instantly;
// the backoff itself is a separate, deterministic calculation.
func fastRetryBackoff(t *testing.T) {
	t.Helper()
	old := streamRetryBackoff
	streamRetryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { streamRetryBackoff = old })
}

// A stream cut before anything was shown is re-issued: the cut attempt's start
// is dropped, the retry's content is what the caller sees, and a notice explains
// the wait. That is the reported failure — "error: unexpected EOF" on a turn
// that had produced nothing — turned into the answer the user asked for, with
// the retry visible instead of silent.
func TestStreamRetryReissuesACutAttempt(t *testing.T) {
	fastRetryBackoff(t)
	for _, cut := range []struct {
		name   string
		err    error
		reason string
	}{
		{"dropped connection", io.ErrUnexpectedEOF, StreamCutTruncated},
		{"idle connection", fmt.Errorf("no data for 5m0s: %w", errStreamIdle), StreamCutIdle},
		{"quiet model", fmt.Errorf("no output for 6m0s: %w", errStreamStall), StreamCutStall},
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
			notices := noticeEvents(events)
			if len(notices) != 1 {
				t.Fatalf("notices = %d (%+v), want exactly 1 before the retry", len(notices), notices)
			}
			if notices[0].Kind != StreamNoticeRetry || notices[0].Provider != "scripted" {
				t.Errorf("notice = %+v, want a retry notice naming the provider", notices[0])
			}
			if notices[0].Reason != cut.reason {
				t.Errorf("notice reason = %q, want %q", notices[0].Reason, cut.reason)
			}
			if notices[0].Attempt != 1 || notices[0].MaxAttempts != defaultStreamMaxRetries {
				t.Errorf("notice attempt = %d/%d, want 1/%d", notices[0].Attempt, notices[0].MaxAttempts, defaultStreamMaxRetries)
			}
		})
	}
}

// The reported OpenAI failure was not a dead socket: the stream was healthy and
// the server ended it with response.failed / server_error. Nothing had been
// shown, so it is as safe to re-issue as any other cut — and the notice says the
// server failed, not the connection.
func TestStreamRetryReissuesAnUpstreamServerError(t *testing.T) {
	fastRetryBackoff(t)
	serverErr := newUpstreamStreamError("server_error",
		"response failed [resp-1]: server_error: An error occurred while processing your request.")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(serverErr)},
		{startEvent(), textEvent("recovered"), doneEvent("recovered")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2 (the failed response and one retry)", got)
	}
	if got := countEvents[StreamErrorEvent](events); got != 0 {
		t.Errorf("error events = %d (%q), want none: the retry answered", got, errorMessage(events))
	}
	if got := lastText(events); got != "recovered" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 1 {
		t.Fatalf("notices = %d (%+v), want exactly 1 before the retry", len(notices), notices)
	}
	if notices[0].Reason != StreamCutUpstream {
		t.Errorf("notice reason = %q, want %q", notices[0].Reason, StreamCutUpstream)
	}
	if !strings.Contains(notices[0].Detail, "server_error") {
		t.Errorf("notice detail = %q, want the server's code", notices[0].Detail)
	}
}

// A provider-declared rejection is not transient: an invalid prompt or a
// rejected image must be surfaced unchanged, without a second request.
func TestStreamRetryLeavesAnUpstreamRejectionAlone(t *testing.T) {
	rejection := newUpstreamStreamError("invalid_prompt",
		"response failed: invalid_prompt: the prompt was rejected")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(rejection)},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 1 {
		t.Errorf("attempts = %d, want 1 (a rejection is not retried)", got)
	}
	if got := errorMessage(events); !strings.Contains(got, "prompt was rejected") {
		t.Errorf("failure = %q, want the wire's own answer untouched", got)
	}
	if got := len(noticeEvents(events)); got != 0 {
		t.Errorf("notices = %d, want none for a rejection", got)
	}
}

// A cut that lands after content cannot be re-issued: resending would duplicate
// what the user already read. With continuations switched off it must at least say
// what happened — the reader's bare "unexpected EOF" names nothing — and how many
// attempts were made, so nobody waits for a retry the turn never made.
func TestStreamRetryKeepsContentAndNamesTheCut(t *testing.T) {
	// The default-on resume is covered by streamresume_test.go; this test pins
	// the terminal behavior with the opt-out.
	t.Setenv("GOLDER_STREAM_RESUME", "0")
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
	if got := len(noticeEvents(events)); got != 0 {
		t.Errorf("notices = %d, want none when the cut cannot be retried", got)
	}
	got := errorMessage(events)
	if !strings.Contains(got, "scripted: stream cut") {
		t.Errorf("failure = %q, want it named", got)
	}
	if !strings.Contains(got, "closed before the answer finished") {
		t.Errorf("failure = %q, want the cut described", got)
	}
	if !strings.Contains(got, "the turn stopped after 1 attempt (no retries)") {
		t.Errorf("failure = %q, want the attempt count stated", got)
	}
	if !strings.Contains(got, "continuing a cut answer is disabled") {
		t.Errorf("failure = %q, want it to say why the answer was not continued", got)
	}
	if txt := agentcore.ContentToText(terminalMessage(events).Content); txt != "half an answer" {
		t.Errorf("terminal text = %q, want the shown answer kept in the transcript", txt)
	}
}

// One retry, not a loop: a connection that drops twice is not a problem another
// request fixes, and the caller still needs a terminal event.
func TestStreamRetryGivesUpAfterASecondCut(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "1")
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
	if got := len(noticeEvents(events)); got != 1 {
		t.Errorf("notices = %d, want exactly 1 (one retry was attempted)", got)
	}
	if got := errorMessage(events); !strings.Contains(got, "stream cut") {
		t.Errorf("failure = %q, want the cut named even when the retry failed", got)
	}
	if got := errorMessage(events); !strings.Contains(got, "the turn stopped after 2 attempts (1 retry)") {
		t.Errorf("failure = %q, want it to say the retry was already made", got)
	}
}

// The budget is a budget, not a single retry: a long-running turn gets several
// reconnects before it is declared lost, with the notice counting them.
func TestStreamRetryUsesTheConfiguredBudget(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "3")
	cut := []AssistantMessageEvent{startEvent(), cutEvent(io.ErrUnexpectedEOF)}
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{cut, cut, cut, cut}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 4 {
		t.Errorf("attempts = %d, want 4 (the first and three retries)", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 3 {
		t.Fatalf("notices = %d, want 3", len(notices))
	}
	for i, n := range notices {
		if n.Attempt != i+1 || n.MaxAttempts != 3 {
			t.Errorf("notice %d = %d/%d, want %d/3", i, n.Attempt, n.MaxAttempts, i+1)
		}
	}
	if got := errorMessage(events); !strings.Contains(got, "the turn stopped after 4 attempts (3 retries)") {
		t.Errorf("failure = %q, want the spent budget named", got)
	}
}

// The default budget is several reconnects; a setting only changes it when it is
// a valid count.
func TestStreamMaxRetriesConfiguration(t *testing.T) {
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "")
	if got := streamMaxRetries(); got != defaultStreamMaxRetries {
		t.Errorf("default = %d, want %d", got, defaultStreamMaxRetries)
	}
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "7")
	if got := streamMaxRetries(); got != 7 {
		t.Errorf("configured = %d, want 7", got)
	}
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "not-a-number")
	if got := streamMaxRetries(); got != defaultStreamMaxRetries {
		t.Errorf("malformed = %d, want the default %d", got, defaultStreamMaxRetries)
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
	if got := len(noticeEvents(events)); got != 0 {
		t.Errorf("notices = %d, want none for a real failure", got)
	}
}

// A stream that ends without any terminal event and without content is as cut as
// a dropped connection, and worth the same single retry.
// The reported failure: a POST that never reached a response, which the Responses
// driver hands back as a stream error event. Nothing was produced, so it takes
// the retry instead of ending the turn with net/http's wrapper.
func TestStreamRetryRetriesAConnectFailureBeforeContent(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")
	eof := &url.Error{Op: "Post", URL: "https://api.commandcode.ai/provider/v1/responses", Err: io.EOF}
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(eof)},
		{startEvent(), textEvent("hello"), doneEvent("hello")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2 (the connect failure is retried)", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 1 || notices[0].Reason != StreamCutConnect {
		t.Fatalf("notices = %+v, want one connect retry", notices)
	}
	if !strings.Contains(notices[0].Detail, "EOF") {
		t.Errorf("notice detail = %q, want the transport's own text", notices[0].Detail)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the retry to have succeeded", got)
	}
	if got := lastText(events); !strings.Contains(got, "hello") {
		t.Errorf("text = %q, want the retry's answer", got)
	}
}

// The same failure on the very first call, before any stream exists: nothing can
// be announced on a stream that does not exist yet, so that retry is silent.
func TestStreamCompletionRetriesAConnectFailure(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")
	eof := &url.Error{Op: "Post", URL: "https://api.commandcode.ai/provider/v1/responses", Err: io.EOF}
	inner := &scriptedProvider{
		connectErrs: []error{eof},
		scripts: [][]AssistantMessageEvent{
			nil,
			{startEvent(), textEvent("hello"), doneEvent("hello")},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the retry to have succeeded", got)
	}
	if got := lastText(events); !strings.Contains(got, "hello") {
		t.Errorf("text = %q, want the retry's answer", got)
	}
}

// With the budget spent the turn ends with a sentence naming the provider, the
// attempt count and the transport's text — not the bare wrapper.
func TestStreamCompletionConnectFailureSentenceWhenBudgetSpent(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "0")
	eof := &url.Error{Op: "Post", URL: "https://api.commandcode.ai/provider/v1/responses", Err: io.EOF}
	inner := &scriptedProvider{connectErrs: []error{eof}}
	_, err := withStreamRetry(inner, "").StreamCompletion(context.Background(), CompletionRequest{Model: "m"})
	if err == nil {
		t.Fatal("want the connect failure to surface")
	}
	for _, want := range []string{"scripted", "api.commandcode.ai", "retry the turn"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	}
}

// A budget is a budget whatever the cut looks like. Attempts that never start an
// answer are retried like every other cut, up to the configured count: the turn
// that keeps dying on a bad network is exactly the one worth continuing, and the
// evidence the guard counted travels in every notice so the wait is not a mystery.
// (Stopping after two silent attempts — the old rule — is what left a turn with
// three of its five attempts unspent.)
func TestStreamRetrySpendsTheWholeBudgetOnSilentAttempts(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")
	firstTokenCut := func() AssistantMessageEvent {
		return cutEvent(&StreamWatchdogError{
			Sink:   errStreamStall,
			Phase:  StreamPhaseFirstToken,
			Window: 2 * time.Minute,
			Seen:   StreamEvidence{Bytes: 412, KeepAlives: 12, LastFrame: "comment"},
		})
	}
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), firstTokenCut()},
		{startEvent(), firstTokenCut()},
		{startEvent(), firstTokenCut()},
		{startEvent(), firstTokenCut()},
		{startEvent(), firstTokenCut()},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 5 {
		t.Errorf("attempts = %d, want 5 (the first attempt and four retries)", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 4 {
		t.Fatalf("notices = %d, want 4 (one before each retry)", len(notices))
	}
	for i, n := range notices {
		if n.Attempt != i+1 || n.MaxAttempts != 4 {
			t.Errorf("notice %d = %d/%d, want %d/4", i, n.Attempt, n.MaxAttempts, i+1)
		}
		if n.Reason != StreamCutStall {
			t.Errorf("notice %d reason = %q, want %q", i, n.Reason, StreamCutStall)
		}
		if !strings.Contains(n.Detail, "12 keep-alives") {
			t.Errorf("notice %d detail = %q, want the guard's evidence", i, n.Detail)
		}
	}
	got := errorMessage(events)
	for _, want := range []string{
		"no answer started within 2m0s",
		"12 keep-alives",
		"the turn stopped after 5 attempts (4 retries)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("failure = %q, want %q", got, want)
		}
	}
}

func TestStreamRetryRetriesASilentlyEndedAttempt(t *testing.T) {
	fastRetryBackoff(t)
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
	notices := noticeEvents(events)
	if len(notices) != 1 || notices[0].Reason != StreamCutSilent {
		t.Errorf("notices = %+v, want one silent-cut notice", notices)
	}
}

// The wire fallback sits inside the retry wrapper and turns a start-only stream
// into an error event ("stream ended without a terminal event"). That error is
// the same failure as a dropped connection, so the wrapper must recognize it
// through the fallback's wrapping and retry — otherwise the fallback's generic
// sentence is what the user sees, with no second attempt.
func TestStreamRetryRetriesAWireFallbacksSilentEnd(t *testing.T) {
	fastRetryBackoff(t)
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(fmt.Errorf("commandcode: %w", errStreamNoTerminal))},
		{startEvent(), textEvent("recovered"), doneEvent("recovered")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2 (a silent end is a cut)", got)
	}
	if got := lastText(events); got != "recovered" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
	if got := countEvents[StreamErrorEvent](events); got != 0 {
		t.Errorf("error events = %d (%q), want none", got, errorMessage(events))
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

// The reported failure: a stream cut after only thinking, before any answer text
// had started. Reasoning is transient — replaced wholesale when the answer
// begins — so the attempt has produced nothing the user can keep and is re-issued
// rather than ended with "stream cut … retry the turn". Before the fix, any
// thinking counted as "shown", so this exact cut terminated the turn.
func TestStreamRetryReissuesAThinkingOnlyCut(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), thinkingEvent("weighing the options"), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), thinkingEvent("weighing the options"), textEvent("the answer"), doneEvent("the answer")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2 (a thinking-only cut is re-issued)", got)
	}
	if got := countEvents[StreamErrorEvent](events); got != 0 {
		t.Fatalf("error events = %d (%q), want none: the retry answered", got, errorMessage(events))
	}
	if got := lastText(events); got != "the answer" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 1 || notices[0].Kind != StreamNoticeRetry {
		t.Errorf("notices = %+v, want exactly one retry notice", notices)
	}
}

// The same thinking-only cut without an explicit error event — the producer just
// stops after reasoning — must be re-issued too. It is the no-terminal-event path
// of the same bug: before the fix, a stream that produced only thinking was
// "content produced", so the turn ended with the cut sentence instead of retrying.
func TestStreamRetryReissuesAThinkingOnlySilentEnd(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), thinkingEvent("half a thought")}, // the producer ends with no terminal event
		{startEvent(), thinkingEvent("half a thought"), textEvent("the answer"), doneEvent("the answer")},
	}}
	events := drainEvents(t, inner, context.Background())

	if got := inner.attemptsRun(); got != 2 {
		t.Errorf("attempts = %d, want 2 (a thinking-only silent end is re-issued)", got)
	}
	if got := lastText(events); got != "the answer" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the retry to have answered", got)
	}
}

// The reported root cause: a cut that lands while the model is asking for a tool.
// The old relay counted the first tool-call delta as "the tool committed the
// attempt" and made every later cut terminal, spending none of the five attempts
// — although the runtime executes calls only from a successful terminal message
// (loop.go returns on StopReasonError before ExecuteToolCalls), so the call had
// not run and could still be regenerated. Here the call is dropped with the cut
// attempt and the request is re-issued; the caller sees only the retry's answer.
func TestStreamRetryReplaysACutInALocalToolCall(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), toolCallEvent(), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("recovered"), doneEvent("recovered")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (a cut in a local tool call is re-issued)", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the retry to have answered", got)
	}
	if got := lastText(events); got != "recovered" {
		t.Errorf("text = %q, want the retry's answer", got)
	}
	if ids := toolCallIDs(events); len(ids) != 0 {
		t.Errorf("tool calls shown = %v, want none: the cut attempt's call was dropped", ids)
	}
	if calls := terminalMessage(events).ToolCalls(); len(calls) != 0 {
		t.Errorf("terminal message carries %d calls, want none", len(calls))
	}
	notices := noticeEvents(events)
	if len(notices) != 1 || notices[0].Reason != StreamCutTruncated {
		t.Fatalf("notices = %+v, want one truncated-cut retry", notices)
	}
}

// The same cut mid-argument: the JSON is not even complete, so there is no call
// to keep. The attempt is re-issued from the original request, and the caller is
// shown no fragment of it.
func TestStreamRetryReplaysACutInAnIncompleteToolCall(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "2")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), toolCallDeltaEvent(), cutEvent(errStreamIdle)},
		{startEvent(), textEvent("recovered"), doneEvent("recovered")},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if ids := toolCallIDs(events); len(ids) != 0 {
		t.Errorf("tool calls shown = %v, want none (an unfinished call is not a call)", ids)
	}
	if got := lastText(events); got != "recovered" {
		t.Errorf("text = %q, want the retry's answer", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 1 || !strings.Contains(notices[0].Detail, "inside a tool call") {
		t.Errorf("notices = %+v, want one naming the cut inside the call", notices)
	}
	// A re-issue, not a continuation: the retry replays the original context, so
	// no half-built call and no "continue" instruction reach the wire.
	if got := len(inner.request(1).Context.Messages); got != 0 {
		t.Errorf("retry context = %d messages, want the original empty context", got)
	}
}

// A provider-executed (hosted) tool call is the one thing that must never be
// replayed: the provider already ran the tool — and billed for it — so a second
// request runs it again. The cut stays terminal, with less text than an answer and
// a sentence that says exactly why.
func TestStreamRetryNeverReplaysAServerToolCallCut(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	for _, tc := range []struct {
		name    string
		script  []AssistantMessageEvent
		wantTxt string
	}{
		{"no text", []AssistantMessageEvent{startEvent(), serverToolCallEvent(), cutEvent(io.ErrUnexpectedEOF)}, ""},
		{"after text", []AssistantMessageEvent{startEvent(), textEvent("searching"), serverToolCallEvent(), cutEvent(io.ErrUnexpectedEOF)}, "searching"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
				tc.script,
				{startEvent(), textEvent("must not run"), doneEvent("must not run")},
			}}
			events := drainEvents(t, inner, context.Background())

			if got := inner.attemptsRun(); got != 1 {
				t.Errorf("attempts = %d, want 1 (a provider-run tool call is never replayed)", got)
			}
			if got := len(noticeEvents(events)); got != 0 {
				t.Errorf("notices = %d, want none: nothing was re-sent", got)
			}
			got := errorMessage(events)
			for _, want := range []string{
				"stream cut",
				"the turn stopped after 1 attempt (no retries)",
				"provider-side tool call",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("failure = %q, want %q", got, want)
				}
			}
			if txt := agentcore.ContentToText(terminalMessage(events).Content); txt != tc.wantTxt {
				t.Errorf("terminal text = %q, want %q", txt, tc.wantTxt)
			}
			if calls := terminalMessage(events).ToolCalls(); len(calls) != 0 {
				t.Errorf("terminal message carries %d calls, want none", len(calls))
			}
		})
	}
}

// The caller is shown completed calls once, with the terminal message, and never
// as a delta it would have to take back: that is what makes a cut attempt
// disappear without leaving a card for a call that will not run.
func TestStreamRetryForwardsCompletedToolCallsOnce(t *testing.T) {
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("working"), toolCallDeltaEvent(), toolCallEvent(), doneWithCall("working", "call-9", "bash", `{"cmd":"ls"}`)},
	}}
	events := drainEvents(t, inner, context.Background())

	if got := inner.attemptsRun(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
	ids := toolCallIDs(events)
	if len(ids) != 1 || ids[0] != "call-9" {
		t.Fatalf("tool calls shown = %v, want exactly the final call-9", ids)
	}
	var callAt, doneAt = -1, -1
	for i, ev := range events {
		switch ev.(type) {
		case StreamToolCallEvent:
			callAt = i
		case StreamDoneEvent:
			doneAt = i
		}
	}
	if callAt < 0 || doneAt < 0 || callAt > doneAt {
		t.Errorf("tool-call event at %d, done at %d: the call must precede the terminal message", callAt, doneAt)
	}
	if calls := terminalMessage(events).ToolCalls(); len(calls) != 1 || calls[0].ID != "call-9" {
		t.Errorf("terminal calls = %+v, want the model's call", calls)
	}
}

// One counter for the whole turn. A connection that dies before any response, an
// idle stream mid-answer (the reported "no data arrived for 1m30s"), a stream that
// closes with no terminal event, and a cut after visible text all draw on the same
// budget — so a turn cannot collect a second allowance by being cut at the other
// end of the request, and the four retries the user configured are the four they
// get. The connect retry is silent (there is no stream to announce it on yet), so
// it takes slot 1 and the notices start at 2/4.
func TestStreamRetrySharesOneBudgetAcrossCutKinds(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")
	idle := &StreamWatchdogError{
		Sink:   errStreamIdle,
		Phase:  StreamPhaseStream,
		Window: 90 * time.Second,
		Seen:   StreamEvidence{Bytes: 392835, Frames: 78, KeepAlives: 46, LastFrame: "data"},
	}
	inner := &scriptedProvider{
		connectErrs: []error{&url.Error{Op: "Post", URL: "https://api.openai.test/v1/responses", Err: io.EOF}},
		scripts: [][]AssistantMessageEvent{
			nil, // the connect failure: no stream at all
			{startEvent(), thinkingEvent("reasoning"), cutEvent(idle)},
			{startEvent()}, // closed with no terminal event
			{startEvent(), textEvent("half "), cutEvent(io.ErrUnexpectedEOF)},
			{startEvent(), textEvent("an answer"), doneEvent("an answer")},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 5 {
		t.Fatalf("attempts = %d, want 5 (one attempt and the four shared retries)", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the last retry to have answered", got)
	}
	if got := lastText(events); got != "half an answer" {
		t.Errorf("text = %q, want the shown prefix plus the continuation", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 3 {
		t.Fatalf("notices = %d (%+v), want 3 (the silent connect retry announces nothing)", len(notices), notices)
	}
	wantReasons := []string{StreamCutIdle, StreamCutSilent, StreamCutTruncated}
	for i, n := range notices {
		if n.Reason != wantReasons[i] {
			t.Errorf("notice %d reason = %q, want %q", i, n.Reason, wantReasons[i])
		}
		if n.Attempt != i+2 || n.MaxAttempts != 4 {
			t.Errorf("notice %d = %d/%d, want %d/4", i, n.Attempt, n.MaxAttempts, i+2)
		}
	}
	// The reported failure, verbatim in the notice: the window that expired and
	// what the guard counted while it waited.
	if notices[0].Window != 90*time.Second {
		t.Errorf("idle notice window = %s, want 1m30s", notices[0].Window)
	}
	for _, want := range []string{"392835 bytes", "78 content frames", "46 keep-alives"} {
		if !strings.Contains(notices[0].Detail, want) {
			t.Errorf("idle notice detail = %q, want %q", notices[0].Detail, want)
		}
	}
	// The last attempt continued the answer rather than re-issuing it, so the text
	// the user already read is in its context — and the text of the first three
	// attempts is not.
	req := inner.request(4)
	if got := len(req.Context.Messages); got != 2 {
		t.Fatalf("continuation context = %d messages, want the partial text and the instruction", got)
	}
	if am, ok := req.Context.Messages[0].(agentcore.AssistantMessage); !ok || agentcore.ContentToText(am.Content) != "half " {
		t.Errorf("continuation replays %v, want the visible text only", req.Context.Messages[0])
	}
}

// GOLDER_STREAM_MAX_RETRIES=0 means no retries: the first cut ends the turn, and
// the sentence says "no retries" so the user is not left wondering whether
// anything was re-sent.
func TestStreamRetryStopsImmediatelyWithNoBudget(t *testing.T) {
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "0")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("must not run"), doneEvent("must not run")},
	}}
	events := drainEvents(t, inner, context.Background())

	if got := inner.attemptsRun(); got != 1 {
		t.Errorf("attempts = %d, want 1 (a zero budget allows no retry)", got)
	}
	if got := len(noticeEvents(events)); got != 0 {
		t.Errorf("notices = %d, want none", got)
	}
	got := errorMessage(events)
	for _, want := range []string{"stream cut", "the turn stopped after 1 attempt (no retries)"} {
		if !strings.Contains(got, want) {
			t.Errorf("failure = %q, want %q", got, want)
		}
	}
}

// A long answer that keeps getting cut continues as often as the budget allows.
// The old limit of two continuations — and the separate allowance that came with
// it — is gone: the setting the user chose is the number of attempts they get, and
// every continuation keeps the text already on screen without repeating it.
func TestStreamRetryContinuesTextUntilTheBudgetIsSpent(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	cut := func(text string) []AssistantMessageEvent {
		return []AssistantMessageEvent{startEvent(), textEvent(text), cutEvent(io.ErrUnexpectedEOF)}
	}
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		cut("a "), cut("b "), cut("c "), cut("d "), cut("e"),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := drainEvents(t, inner, ctx)

	if got := inner.attemptsRun(); got != 5 {
		t.Errorf("attempts = %d, want 5 (the first attempt and four continuations)", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 4 {
		t.Fatalf("notices = %d, want 4", len(notices))
	}
	for i, n := range notices {
		if !strings.Contains(n.Detail, "continuing the cut answer") {
			t.Errorf("notice %d detail = %q, want the continuation named", i, n.Detail)
		}
	}
	if got := lastText(events); got != "a b c d e" {
		t.Errorf("text = %q, want each piece appended once", got)
	}
	got := errorMessage(events)
	if !strings.Contains(got, "the turn stopped after 5 attempts (4 retries)") {
		t.Errorf("failure = %q, want the spent budget named", got)
	}
	if !strings.Contains(got, "closed before the answer finished") {
		t.Errorf("failure = %q, want the cut described", got)
	}
	if txt := agentcore.ContentToText(terminalMessage(events).Content); txt != "a b c d e" {
		t.Errorf("terminal text = %q, want the answer the user read kept in the transcript", txt)
	}
}

// A continuation that is itself cut before its first token keeps continuing from
// the same prefix rather than re-issuing the request: the text on the caller's
// screen is the user's, and a re-issue would replace it with a fresh start. The
// attempt still spends the shared budget.
func TestStreamRetryKeepsContinuingAfterACutBeforeTheFirstToken(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "3")
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), textEvent("half "), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), cutEvent(errStreamIdle)}, // the continuation never produced a token
		{startEvent(), textEvent("an answer"), doneEvent("an answer")},
	}}
	events := drainEvents(t, inner, context.Background())

	if got := inner.attemptsRun(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if got := countEvents[StreamStartEvent](events); got != 1 {
		t.Errorf("start events = %d, want 1 (the answer is never restarted)", got)
	}
	for _, i := range []int{1, 2} {
		req := inner.request(i)
		if got := len(req.Context.Messages); got != 2 {
			t.Fatalf("attempt %d context = %d messages, want the partial text and the instruction", i, got)
		}
		am, ok := req.Context.Messages[0].(agentcore.AssistantMessage)
		if !ok || agentcore.ContentToText(am.Content) != "half " {
			t.Errorf("attempt %d replays %v, want the text the user already read", i, req.Context.Messages[0])
		}
	}
	if got := lastText(events); got != "half an answer" {
		t.Errorf("text = %q, want the prefix plus the continuation", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the last continuation to have answered", got)
	}
}

// A cancelled turn stops at once, with budget left: that interrupt is the user's
// own, so it makes no further request and is reported as an abort rather than as a
// provider failure with a retry count.
func TestStreamRetryStopsOnCancellation(t *testing.T) {
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")
	old := streamRetryBackoff
	// A long wait is what the interrupt has to break out of; the test cancels
	// instead of waiting for it.
	streamRetryBackoff = func(int) time.Duration { return time.Hour }
	t.Cleanup(func() { streamRetryBackoff = old })

	inner := &scriptedProvider{scripts: [][]AssistantMessageEvent{
		{startEvent(), cutEvent(io.ErrUnexpectedEOF)},
		{startEvent(), textEvent("must not run"), doneEvent("must not run")},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := withStreamRetry(inner, "").StreamCompletion(ctx, CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	notices := 0
	for ev := range stream.Events() {
		if _, ok := ev.(StreamNoticeEvent); ok {
			notices++
			cancel() // the user interrupts while the retry is backing off
		}
	}
	if notices != 1 {
		t.Errorf("notices = %d, want 1 (the retry that was announced)", notices)
	}
	if got := inner.attemptsRun(); got != 1 {
		t.Errorf("attempts = %d, want 1: a cancelled turn makes no further request", got)
	}
}

// The same policy over a real wire. The next three drive the Responses driver over
// a stubbed transport — SSE frames in, net/http's own reader errors out — so the
// exact shapes the user reported are exercised end to end: the wrapper, the
// driver's decoder, and the body's failure.

// drainWith runs a wrapped provider with a full request and reports everything the
// caller saw.
func drainWith(t *testing.T, p Provider, req CompletionRequest) []AssistantMessageEvent {
	t.Helper()
	stream, err := p.StreamCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var events []AssistantMessageEvent
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	return events
}

// responsesRetryRequest is the request shape every wire test here sends.
func responsesRetryRequest() CompletionRequest {
	return CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	}
}

// guardedResponsesDriver is newResponsesTestDriver with the SSE guard in front of
// the transport — the client production builds (see guardSSEFrames) — so a body
// that goes silent produces the watchdog error the user reported instead of
// hanging on a read.
func guardedResponsesDriver(baseURL string, rt roundTripFunc) *responsesDriver {
	d := NewOpenAIResponsesProvider("openai", baseURL, nil)
	d.clientOpts = []option.RequestOption{
		option.WithHTTPClient(guardSSEFrames(&http.Client{Transport: rt})),
	}
	return d
}

// sseThenStall is a response body that delivers the given bytes and then goes
// silent without closing: the reported "no data arrived for 1m30s (392835 bytes,
// 78 content frames, 46 keep-alives)" — bytes and keep-alives were counted, then
// nothing arrived, and the read never returned.
type sseThenStall struct {
	data []byte
	done chan struct{}
	once sync.Once
}

func (b *sseThenStall) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	<-b.done // the guard closes the body when its window expires
	return 0, io.EOF
}

func (b *sseThenStall) Close() error {
	b.once.Do(func() { close(b.done) })
	return nil
}

// The reported first failure over a real SSE body: the socket stays open, some
// keep-alives arrive, and then nothing — for longer than the idle window. The
// guard turns that into a cut, and the turn is retried instead of ending after one
// attempt with "retry the turn".
func TestResponsesIdleStreamIsRetriedOverRealSSE(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "40ms")
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "2")
	var calls int32
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: &sseThenStall{
					data: []byte(": keep-alive\n\n: keep-alive\n\n"),
					done: make(chan struct{}),
				},
			}, nil
		}
		return sseResponse(
			deltaFrame("recovered"),
			completedFrame("recovered", "resp_2", "gpt-4o", 3, 4),
		), nil
	})
	d := guardedResponsesDriver("https://api.openai.test/v1", rt)
	events := drainWith(t, withStreamRetry(d, WireResponses), responsesRetryRequest())

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("attempts = %d, want 2 (the idle stream and one retry)", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the retry to have answered", got)
	}
	if got := lastText(events); got != "recovered" {
		t.Errorf("text = %q, want the retry's answer", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 1 {
		t.Fatalf("notices = %d (%+v), want 1", len(notices), notices)
	}
	if notices[0].Reason != StreamCutIdle {
		t.Errorf("notice reason = %q, want %q", notices[0].Reason, StreamCutIdle)
	}
	if notices[0].Window != 40*time.Millisecond {
		t.Errorf("notice window = %s, want the configured 40ms", notices[0].Window)
	}
	if !strings.Contains(notices[0].Detail, "keep-alives") {
		t.Errorf("notice detail = %q, want the guard's evidence", notices[0].Detail)
	}
}

// The reported second failure over a real SSE body: the response ends before the
// answer does (net/http's io.ErrUnexpectedEOF). With text already on screen the
// answer is continued, and the continuation's request carries that text — and no
// tool call, so the history stays replayable.
func TestResponsesTruncatedStreamIsContinuedOverRealSSE(t *testing.T) {
	fastRetryBackoff(t)
	t.Setenv("GOLDER_STREAM_RESUME", "1")
	var calls int32
	var mu sync.Mutex
	var bodies []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			if b, err := io.ReadAll(r.Body); err == nil {
				mu.Lock()
				bodies = append(bodies, string(b))
				mu.Unlock()
			}
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &truncatedBody{data: []byte("data: " + deltaFrame("half an answer") + "\n\n")},
			}, nil
		}
		return sseResponse(
			deltaFrame(" and the rest"),
			completedFrame(" and the rest", "resp_2", "gpt-4o", 3, 4),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)
	events := drainWith(t, withStreamRetry(d, WireResponses), responsesRetryRequest())

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("attempts = %d, want 2 (the cut answer and its continuation)", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the continuation to have answered", got)
	}
	if got := lastText(events); got != "half an answer and the rest" {
		t.Errorf("text = %q, want the shown prefix plus the continuation", got)
	}
	notices := noticeEvents(events)
	if len(notices) != 1 || notices[0].Reason != StreamCutTruncated {
		t.Fatalf("notices = %+v, want one truncated-cut retry", notices)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("request bodies = %d, want 2", len(bodies))
	}
	for _, want := range []string{"half an answer", "Continue exactly where you stopped"} {
		if !strings.Contains(bodies[1], want) {
			t.Errorf("continuation body = %s, want %q replayed", bodies[1], want)
		}
	}
	if strings.Contains(bodies[1], "function_call") {
		t.Errorf("continuation body replayed a tool call: %s", bodies[1])
	}
}

// The tool-call twin over a real wire: the model's function_call is complete on the
// wire, then the body dies. The call never ran (the runtime executes calls only
// from a successful terminal message), so the request is re-issued and the caller
// is handed exactly one call — the retry's — which is what the runtime runs once.
func TestResponsesToolCallCutIsReissuedOverRealSSE(t *testing.T) {
	fastRetryBackoff(t)
	var calls int32
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			frame := functionCallDoneFrame("call_1", "bash", `{"cmd":"ls"}`)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &truncatedBody{data: []byte("data: " + frame + "\n\n")},
			}, nil
		}
		return sseResponse(
			completedToolFrame("resp_2", "gpt-4o", "call_2", "bash", `{"cmd":"ls"}`),
		), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)
	events := drainWith(t, withStreamRetry(d, WireResponses), responsesRetryRequest())

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("attempts = %d, want 2 (a cut in a local tool call is re-issued)", got)
	}
	if got := errorMessage(events); got != "" {
		t.Errorf("failure = %q, want the retry to have answered", got)
	}
	if ids := toolCallIDs(events); len(ids) != 1 || ids[0] != "call_2" {
		t.Errorf("tool calls shown = %v, want exactly the retry's call_2", ids)
	}
	msg := terminalMessage(events)
	calls2 := msg.ToolCalls()
	if len(calls2) != 1 || calls2[0].ID != "call_2" || calls2[0].IsServer() {
		t.Errorf("terminal calls = %+v, want the one local call the retry asked for", calls2)
	}
}

// And the one cut that stays terminal over a real wire: a hosted (provider-run)
// call was in flight when the body died, so re-issuing would run it a second time
// at the provider. The turn stops after one attempt and says why.
func TestResponsesHostedToolCallCutIsNotReplayedOverRealSSE(t *testing.T) {
	fastRetryBackoff(t)
	var calls int32
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			frame := webSearchCallFrame("ws_1", "news")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &truncatedBody{data: []byte("data: " + frame + "\n\n")},
			}, nil
		}
		return sseResponse(completedFrame("must not run", "resp_2", "gpt-4o", 3, 4)), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)
	events := drainWith(t, withStreamRetry(d, WireResponses), responsesRetryRequest())

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("attempts = %d, want 1 (a provider-run tool call is never replayed)", got)
	}
	if got := len(noticeEvents(events)); got != 0 {
		t.Errorf("notices = %d, want none: nothing was re-sent", got)
	}
	got := errorMessage(events)
	for _, want := range []string{
		"stream cut",
		"the turn stopped after 1 attempt (no retries)",
		"provider-side tool call",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("failure = %q, want %q", got, want)
		}
	}
	if calls := terminalMessage(events).ToolCalls(); len(calls) != 0 {
		t.Errorf("terminal message carries %d calls, want none", len(calls))
	}
}
