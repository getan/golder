package provider

// Retries for a stream cut before anything was shown.
//
// This is where the policy for streamfail.go's vocabulary lives, and it lives
// here — above the drivers, below the loop — because "the stream was cut" is
// wire-independent. The Responses wire detects it in the SDK's reader, the Chat
// and Messages wires in the transport's read loop, and both reach this wrapper
// with the same question: was anything shown, and would re-issuing the request
// be safe?
//
// The answer is exactly the one the drivers already use for an empty completion:
// a retry is safe while nothing has been displayed, because another attempt can
// neither duplicate nor contradict what the user has read. Once any text,
// thinking or tool call has been forwarded the attempt is final, and the wrapper
// only makes sure the failure is described in words a person can act on.
//
// It wraps every resolved provider (see resolve.go), so the policy cannot vary
// by wire, by provider entry, or by how the provider was selected.
//
// The budget is deliberately larger than one: a long-running turn is expensive to
// lose, and a reconnect usually succeeds within a few attempts. Each attempt is
// separated by an exponential backoff (250ms, 500ms, 1s, … capped at 5s), so a
// gateway that is briefly down is not hammered, and the user sees a notice with
// the attempt number while it happens. GOLDER_STREAM_MAX_RETRIES overrides the
// budget (0 disables retries).

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

const (
	// defaultStreamMaxRetries is four retries (five attempts) for a cut stream.
	// Codex reconnects several times before giving up; a turn that has produced
	// nothing is exactly as safe to re-issue as it was on the first attempt.
	defaultStreamMaxRetries = 4
	streamRetryBaseDelay    = 250 * time.Millisecond
	streamRetryMaxDelay     = 5 * time.Second
)

// streamMaxRetries resolves the retry budget. A malformed or out-of-range setting
// falls back to the default rather than to no retries.
func streamMaxRetries() int {
	if v := os.Getenv("GOLDER_STREAM_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 20 {
			return n
		}
	}
	return defaultStreamMaxRetries
}

// streamRetryBackoff is the delay before retry N (1-based). It is a variable so
// tests can run the retry loop without sleeping; production never changes it.
var streamRetryBackoff = func(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	shift := attempt - 1
	if shift > 8 {
		shift = 8
	}
	d := streamRetryBaseDelay << uint(shift)
	if d > streamRetryMaxDelay {
		d = streamRetryMaxDelay
	}
	return d
}

// withStreamRetry wraps inner with the cut-stream retry. initial is the wire the
// resolver picked, used when inner cannot report its own (a direct driver rather
// than the adaptive fallback). A nil provider stays nil.
func withStreamRetry(inner Provider, initial Wire) Provider {
	if inner == nil {
		return nil
	}
	return &streamRetryProvider{inner: inner, wire: initial}
}

type streamRetryProvider struct {
	inner Provider
	wire  Wire
}

func (p *streamRetryProvider) Name() string    { return p.inner.Name() }
func (p *streamRetryProvider) Models() []Model { return p.inner.Models() }

// CurrentWire delegates to the inner provider when it can report a live wire and
// otherwise returns the resolver's initial choice. The status bar reads this per
// frame, so the protocol segment follows the adaptive fallback without an event.
func (p *streamRetryProvider) CurrentWire() Wire {
	if wr, ok := p.inner.(WireReporter); ok {
		if w := wr.CurrentWire(); w != "" {
			return w
		}
	}
	return p.wire
}

// StreamCompletion starts the relay pump and returns the single stream the
// caller consumes. An attempt that was cut before showing anything never reaches
// the caller: its events are held, then dropped.
//
// The first call is retried here, before any stream exists, when it fails at the
// connection level: a POST that dies with EOF has produced nothing, and the
// caller has no stream to report it on. The retry is silent because there is no
// stream to announce it on yet; the pump's later retries do announce theirs.
func (p *streamRetryProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	max := streamMaxRetries()
	in, spent, err := p.connect(ctx, req, 0, max, nil)
	if err != nil {
		return nil, streamConnectError(p.Name(), err, spent+1)
	}
	out := NewAssistantMessageEventStream(0)
	go p.pumpFrom(ctx, req, in, out, spent)
	return out, nil
}

// connect issues one attempt and retries a connection-level failure until the
// shared budget is spent. start is the retry number the call carries (the
// watchdog budgets widen with it); onRetry, when set, is told about each retry
// before it is made — the pump emits a notice there, while the very first call
// has no stream to notify on yet.
//
// It returns the stream, the attempt number the next call should use, and the
// error when the attempt could not be started at all.
func (p *streamRetryProvider) connect(ctx context.Context, req CompletionRequest, start, max int, onRetry func(attempt int, err error)) (*AssistantMessageEventStream, int, error) {
	attempt := start
	for {
		in, err := p.inner.StreamCompletion(withStreamAttempt(ctx, attempt), req)
		if err == nil {
			return in, attempt, nil
		}
		if !isConnectFailure(err) || attempt >= max || ctx.Err() != nil {
			return nil, attempt, err
		}
		attempt++
		if onRetry != nil {
			onRetry(attempt, err)
		}
		if !waitRetry(ctx, streamRetryBackoff(attempt)) {
			return nil, attempt, err
		}
	}
}

func (p *streamRetryProvider) pump(ctx context.Context, req CompletionRequest, in *AssistantMessageEventStream, out *AssistantMessageEventStream) {
	p.pumpFrom(ctx, req, in, out, 0)
}

// pumpFrom is pump with the number of connection retries already spent, so the
// budget covers the whole turn rather than resetting after the first call.
func (p *streamRetryProvider) pumpFrom(ctx context.Context, req CompletionRequest, in *AssistantMessageEventStream, out *AssistantMessageEventStream, spent int) {
	defer out.Close()

	max := streamMaxRetries()
	retries := spent
	// firstTokenCuts counts consecutive attempts that produced nothing at all. Two
	// of those mean the upstream is not answering — widening the budget a third
	// time only makes the user wait longer for the same silence.
	firstTokenCuts := 0
	for {
		cut := p.forward(ctx, out, in)
		if streamCutIsFirstToken(cut) {
			firstTokenCuts++
		} else if cut != nil {
			firstTokenCuts = 0
		}
		switch {
		case cut == nil:
			return
		case ctx.Err() != nil:
			// The turn was cancelled while the cut was being handled: this is the
			// user's own interrupt, and every front-end prints its own notice for
			// an abort. Filing it as a provider failure is the wording bug that
			// once made Ctrl+C read as a malfunction.
			p.emitAborted(ctx, out)
			return
		case retries >= max:
			// The budget is spent: a connection that drops this often is not a
			// problem another request fixes, so say how many retries were made.
			p.emitFailure(ctx, out, cut, retries)
			return
		case firstTokenCuts >= 2:
			// Two attempts in a row never started an answer. Stop here and say so:
			// the remaining budget would buy two more identical waits.
			p.emitSilent(ctx, out, cut, firstTokenCuts)
			return
		}

		retries++
		// Tell the front-end before the next attempt starts. The first
		// attempt's start event was held, so without this the interface shows
		// only the spinner and the user cannot tell a retry from a hang.
		_ = out.Emit(ctx, StreamNoticeEvent{
			Kind:        StreamNoticeRetry,
			Provider:    p.Name(),
			Reason:      streamCutReason(cut),
			Window:      streamCutWindow(cut),
			Attempt:     retries,
			MaxAttempts: max,
			Detail:      streamCutEvidence(cut),
		})
		if !waitRetry(ctx, streamRetryBackoff(retries)) {
			p.emitAborted(ctx, out)
			return
		}
		// The retry may re-resolve a wire (the wrapper sits above the wire
		// fallback) and always builds a fresh request and decoder, so no state
		// from the cut attempt is carried over.
		next, used, err := p.connect(ctx, req, retries, max, func(attempt int, cerr error) {
			// A retry that cannot connect is the same failure the loop exists
			// for, so it is announced the same way instead of ending the turn
			// with a raw transport error.
			_ = out.Emit(ctx, StreamNoticeEvent{
				Kind:        StreamNoticeRetry,
				Provider:    p.Name(),
				Reason:      StreamCutConnect,
				Attempt:     attempt,
				MaxAttempts: max,
				Detail:      connectDetail(cerr),
			})
		})
		retries = used
		if err != nil {
			if ctx.Err() != nil {
				p.emitAborted(ctx, out)
				return
			}
			// The retry could not start. A connection failure is the failure
			// this loop is for, so it is reported with the connect sentence; a
			// configuration error (a missing key, a bad flag) keeps its own
			// wording, which is what the caller needs to fix it.
			p.emitFailure(ctx, out, connectOrCut(p.Name(), err, cut), retries)
			return
		}
		in = next
	}
}

// waitRetry sleeps for d, returning false when the turn is cancelled instead.
func waitRetry(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// forward relays one attempt and reports how it ended. A nil return means the
// caller has seen a terminal event and the stream is over. A non-nil error means
// the attempt was cut before anything was shown, and nothing was forwarded: the
// caller may re-issue the request.
func (p *streamRetryProvider) forward(ctx context.Context, out *AssistantMessageEventStream, in *AssistantMessageEventStream) error {
	// The attempt's start event is held rather than forwarded. It is not
	// something a person can see, and holding it is what lets a cut attempt
	// disappear without a trace — a forwarded start would have to be taken back.
	var start *StreamStartEvent
	shown := false
	emitStart := func() {
		if start != nil {
			_ = out.Emit(ctx, *start)
			start = nil
		}
	}

	for ev := range in.Events() {
		switch e := ev.(type) {
		case StreamStartEvent:
			if start == nil {
				start = &e
			}
		case StreamErrorEvent:
			if !shown && isStreamCutErr(e.Err) {
				return e.Err
			}
			if isStreamCutErr(e.Err) {
				// A cut that landed after content cannot be retried without
				// duplicating what the user already read; it must at least say
				// what happened instead of printing the reader's error.
				e.Message.ErrorMessage = streamCutError(p.Name(), e.Err).Error()
			}
			emitStart()
			_ = out.Emit(ctx, e)
			return nil
		case StreamDoneEvent:
			emitStart()
			shown = true
			_ = out.Emit(ctx, e)
			return nil
		default:
			emitStart()
			shown = true
			_ = out.Emit(ctx, e)
		}
	}

	// The attempt ended without a terminal event. With nothing produced that is
	// as much a cut stream as a dropped connection; with content produced the
	// caller has to be told the answer stops there.
	if !shown {
		return fmt.Errorf("%s: %w", p.Name(), errStreamNoTerminal)
	}
	emitStart()
	_ = out.Emit(ctx, StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			Provider:   p.Name(),
			StopReason: agentcore.StopReasonError,
		},
		Err: fmt.Errorf("%s: %w", p.Name(), errStreamNoTerminal),
	})
	return nil
}

// emitFailure ends the stream with a start and a terminal error, so the caller
// sees a normal stream rather than one that closed on it. afterRetry selects the
// sentence: a cut that happened before any retry invites one, and a cut that
// survived one says so.
func (p *streamRetryProvider) emitFailure(ctx context.Context, out *AssistantMessageEventStream, err error, retries int) {
	// A connection failure gets the "never reached a response" sentence, which
	// counts the attempts; a stream cut keeps the cut sentence, which names
	// which window expired.
	sentence := streamConnectError(p.Name(), err, retries+1)
	if !isConnectFailure(err) {
		sentence = streamCutError(p.Name(), err)
		if retries > 0 {
			sentence = streamCutRetriedError(p.Name(), err, retries)
		}
	}
	_ = out.Emit(ctx, StreamStartEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Provider:  p.Name(),
	}})
	_ = out.Emit(ctx, StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			Provider:     p.Name(),
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: sentence.Error(),
		},
		Err: err,
	})
}

// emitSilent ends the stream after consecutive attempts that never started an
// answer. The sentence says exactly that, so nobody waits for a retry that the
// upstream has already refused twice.
func (p *streamRetryProvider) emitSilent(ctx context.Context, out *AssistantMessageEventStream, err error, attempts int) {
	sentence := streamCutUpstreamSilent(p.Name(), err, attempts)
	_ = out.Emit(ctx, StreamStartEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Provider:  p.Name(),
	}})
	_ = out.Emit(ctx, StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			Provider:     p.Name(),
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: sentence.Error(),
		},
		Err: err,
	})
}

// emitAborted ends a cancelled turn: the front-end prints its own interrupt
// notice for this stop reason, so the message stays empty.
func (p *streamRetryProvider) emitAborted(ctx context.Context, out *AssistantMessageEventStream) {
	_ = out.Emit(ctx, StreamStartEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Provider:  p.Name(),
	}})
	_ = out.Emit(ctx, StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:  agentcore.RoleAssistant,
			Provider:   p.Name(),
			StopReason: agentcore.StopReasonAborted,
		},
		Err: context.Canceled,
	})
}
