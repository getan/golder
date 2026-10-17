package provider

// One retry for a stream cut before anything was shown.
//
// This is where the policy for streamfail.go's vocabulary lives, and it lives
// here — above the drivers, below the loop — because "the stream was cut" is
// wire-independent. The Responses wire detects it in the SDK's reader, the Chat
// and Messages wires in the transport's read loop, and both reach this wrapper
// with the same question: was anything shown, and would re-issuing the request
// be safe?
//
// The answer is exactly the one the drivers already use for an empty completion:
// a retry is safe while nothing has been displayed, because a second attempt can
// neither duplicate nor contradict what the user has read. Once any text,
// thinking or tool call has been forwarded the attempt is final, and the wrapper
// only makes sure the failure is described in words a person can act on.
//
// It wraps every resolved provider (see resolve.go), so the policy cannot vary
// by wire, by provider entry, or by how the provider was selected.

import (
	"context"
	"fmt"

	"github.com/getan/golder/internal/agentcore"
)

// withStreamRetry wraps inner with the cut-stream retry. A nil provider stays
// nil so callers cannot turn a resolution failure into a wrapped nil.
func withStreamRetry(inner Provider) Provider {
	if inner == nil {
		return nil
	}
	return &streamRetryProvider{inner: inner}
}

type streamRetryProvider struct{ inner Provider }

func (p *streamRetryProvider) Name() string    { return p.inner.Name() }
func (p *streamRetryProvider) Models() []Model { return p.inner.Models() }

// StreamCompletion starts the relay pump and returns the single stream the
// caller consumes. An attempt that was cut before showing anything never reaches
// the caller: its events are held, then dropped.
func (p *streamRetryProvider) StreamCompletion(ctx context.Context, req CompletionRequest) (*AssistantMessageEventStream, error) {
	in, err := p.inner.StreamCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	out := NewAssistantMessageEventStream(0)
	go p.pump(ctx, req, in, out)
	return out, nil
}

func (p *streamRetryProvider) pump(ctx context.Context, req CompletionRequest, in *AssistantMessageEventStream, out *AssistantMessageEventStream) {
	defer out.Close()

	retried := false
	for {
		if cut := p.forward(ctx, out, in); cut == nil {
			return
		} else if retried || ctx.Err() != nil {
			// A second cut, or a cancelled turn: report it. A connection that
			// drops twice is not a problem another request fixes.
			p.emitFailure(ctx, out, cut)
			return
		} else {
			retried = true
			// Tell the front-end before the second attempt starts. The first
			// attempt's start event was held, so without this the interface shows
			// only the spinner and the user cannot tell a retry from a hang.
			_ = out.Emit(ctx, StreamNoticeEvent{
				Kind:     StreamNoticeRetry,
				Provider: p.Name(),
				Reason:   streamCutReason(cut),
				Window:   streamCutWindow(cut),
			})
			// The retry may re-resolve a wire (the wrapper sits above the wire
			// fallback) and always builds a fresh request and decoder, so no
			// state from the cut attempt is carried over.
			next, err := p.inner.StreamCompletion(ctx, req)
			if err != nil {
				// The retry could not even start; the cut is still the failure
				// the stream ended with, and the early error (a missing key, a
				// bad flag) has its own reporting path.
				p.emitFailure(ctx, out, cut)
				return
			}
			in = next
		}
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
// sees a normal stream rather than one that closed on it.
func (p *streamRetryProvider) emitFailure(ctx context.Context, out *AssistantMessageEventStream, err error) {
	_ = out.Emit(ctx, StreamStartEvent{Partial: agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Provider:  p.Name(),
	}})
	_ = out.Emit(ctx, StreamErrorEvent{
		Message: agentcore.AssistantMessage{
			RoleField:    agentcore.RoleAssistant,
			Provider:     p.Name(),
			StopReason:   agentcore.StopReasonError,
			ErrorMessage: streamCutError(p.Name(), err).Error(),
		},
		Err: err,
	})
}
