package provider

// Retries for a stream that was cut.
//
// This is where the policy for streamfail.go's vocabulary lives, and it lives
// here — above the drivers, below the loop — because "the stream was cut" is
// wire-independent. The Responses wire detects it in the SDK's reader, the Chat
// and Messages wires in the transport's read loop, and both reach this wrapper
// with the same question: what did this attempt produce, and what may be done
// about it?
//
// One budget, shared by every recovery. A turn attempts the request once and may
// re-issue or continue it defaultStreamMaxRetries times (four retries, five
// attempts in all), whatever the shape of the cut: a connection that never
// opened, an idle watchdog ("no data arrived for 1m30s"), a content stall, a body
// that ended early ("unexpected EOF"), a stream that closed without a terminal
// event, a cut before the answer started, and a cut after part of it was already
// on screen. A cut in the middle of the answer and a cut on the continuation of
// that answer spend the same counter, so no phase of a turn gets a private
// allowance and no recovery can jump the queue.
// GOLDER_STREAM_MAX_RETRIES overrides the budget, and 0 disables retries
// entirely: the first cut then ends the turn. A cancelled turn stops immediately
// in every case — that is the user's own interrupt, not the upstream's failure.
//
// What may be recovered is decided by what the attempt produced, not by which
// wire produced it:
//
//   - Nothing visible, or reasoning only: the request is re-issued unchanged.
//     Reasoning streams as transient scaffolding, is replaced wholesale when the
//     answer starts, and is worth nothing if the stream dies before that, so a
//     cut that left only thinking behind left nothing to keep.
//   - Visible answer text: the answer is continued from where it stopped — the
//     partial text is replayed as the assistant's previous turn plus one short
//     instruction. A re-issued request would have to re-render text the user has
//     already read (or take it back off the screen); a continuation appends.
//   - A local tool call: the call does NOT commit the attempt. The runtime
//     executes calls only from a successful terminal message (loop.go returns on
//     StopReasonError before it reaches ExecuteToolCalls), so a cut anywhere in a
//     local call leaves it unexecuted and re-issuing costs nothing but a request.
//     The call's deltas are therefore held back and dropped when the attempt is
//     cut (see forward), and the complete call arrives exactly once, with the
//     terminal message the runtime executes. Nothing incomplete reaches the
//     caller, and no half-built call is spliced into a continuation's prefix,
//     where it would be an unpaired tool call the next request is rejected for.
//   - A provider-executed (server) tool call: never replayed, never continued.
//     The provider ran that tool itself — hosted search, code interpreter — and
//     may have billed for it, so a second request runs it a second time. This is
//     the one cut that stays terminal, and the sentence says exactly that.
//
// It wraps every resolved provider (see resolve.go), so the policy cannot vary
// by wire, by provider entry, or by how the provider was selected.
//
// Each attempt is separated by an exponential backoff (250ms, 500ms, 1s, …
// capped at 5s), so a gateway that is briefly down is not hammered, and the user
// sees a notice carrying the original cause, the watchdog window that expired
// and the attempt number while it happens. When the loop stops it names how many
// attempts were made and why it stopped, instead of inviting a retry that may
// already have run five times.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

const (
	// defaultStreamMaxRetries is the retry budget for one turn: four retries,
	// five attempts in all, shared by every kind of cut — the connection, the
	// watchdogs, a body that ended early, a stream with no terminal event, a cut
	// before the answer started, and a cut after part of it was on screen. Codex
	// reconnects several times before giving up, and losing a long turn to a bad
	// network is the most expensive failure there is: a turn that keeps trying is
	// what the user asked for.
	defaultStreamMaxRetries = 4
	streamRetryBaseDelay    = 250 * time.Millisecond
	streamRetryMaxDelay     = 5 * time.Second
)

// Why a cut the loop could recover in principle was left terminal anyway. The
// sentence names it: "retry the turn" alone would invite the very thing the loop
// just refused to do, and say nothing about what it declined to do and why.
const (
	stopServerTool = "the response had already run a provider-side tool call, which is never replayed automatically"
	stopResumeOff  = "continuing a cut answer is disabled (GOLDER_STREAM_RESUME=0)"
)

// streamResumeEnabled lets a text answer that was cut after it had been shown
// continue from the partial text instead of ending the turn. It is on by default
// because a lost turn is the more common cost; set it to 0/false/off to keep a
// cut after visible text terminal. It governs continuations only: a cut with
// nothing (or only thinking) on screen is re-issued whatever this says, because
// re-issuing repeats nothing the caller has seen.
func streamResumeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_STREAM_RESUME"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// forwardState is what the relay saw in one attempt. It carries exactly the three
// things that decide how a cut may be recovered: the partial the caller has
// already rendered, whether the model asked for a call golder would run itself,
// and whether the provider ran one of its own.
type forwardState struct {
	// partial is the last partial forwarded to the caller: the answer text and
	// reasoning already on screen. Held tool-call deltas are deliberately not
	// folded in (see forward).
	partial agentcore.AssistantMessage
	// localCall is set when the model asked for a golder tool. It does not commit
	// the attempt: the runtime runs calls only from a successful terminal message
	// (loop.go returns on StopReasonError before ExecuteToolCalls), so a cut
	// leaves the call unexecuted and the request safe to re-issue.
	localCall bool
	// serverCall is set when the provider executed a hosted tool itself. That
	// call may already have run and been billed, so the attempt must never be
	// replayed or continued — the one cut that stays terminal.
	serverCall bool
}

func (s *forwardState) observeCalls(msg agentcore.AssistantMessage) {
	for _, call := range msg.ToolCalls() {
		if call.IsServer() {
			s.serverCall = true
		} else {
			s.localCall = true
		}
	}
}

func withoutToolCalls(msg agentcore.AssistantMessage) agentcore.AssistantMessage {
	content := make(agentcore.ContentList, 0, len(msg.Content))
	for _, block := range msg.Content {
		if _, tool := block.(agentcore.ToolCallContent); !tool {
			content = append(content, block)
		}
	}
	msg.Content = content
	return msg
}

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

// StreamCompletion starts the relay pump and returns the single stream the caller
// consumes. From then on the wrapper is the only writer: an attempt that was cut
// is either dropped (its start, and any tool-call delta it held) or continued, so
// the caller sees one assistant message however many requests it took.
//
// The first call is retried here, before any stream exists, when it fails at the
// connection level: a POST that dies with EOF has produced nothing, and the caller
// has no stream to report it on. That retry is silent because there is no stream to
// announce it on yet — and it spends the same budget as every later one, so the
// turn cannot get a second allowance by failing before the stream starts.
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
// shared budget is spent. start is the retry number the attempt carries (the
// watchdogs read it, though the first-token window deliberately does not widen with
// it); onRetry, when set, is told about each retry before it is made — the pump
// emits a notice there, while the very first call has no stream to notify on yet.
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

// pumpFrom relays attempts until one of them ends the turn. spent is the number
// of retries the connection phase already used, so the budget covers the whole
// turn instead of resetting after the first call.
//
// One loop, one counter. A cut before the answer, a cut in the middle of it and a
// cut on the continuation of it all spend the same budget: the previous shape ran
// text continuations on a separate allowance of two and abandoned the turn after
// two silent attempts even with budget left, which is why a turn cut mid-answer
// could see one retry where the setting promised four.
func (p *streamRetryProvider) pumpFrom(ctx context.Context, req CompletionRequest, in *AssistantMessageEventStream, out *AssistantMessageEventStream, spent int) {
	defer out.Close()

	max := streamMaxRetries()
	retries := spent
	// prefix is the answer text the caller has already rendered. When it is set
	// the next attempt continues that answer instead of re-issuing the request,
	// so nothing shown is repeated or lost: every continuation event is merged
	// with it (prefixContent) and its start event is suppressed, because the
	// caller's stream already started and a second start would reset the partial
	// it is rendering.
	var prefix agentcore.ContentList

	for {
		state := forwardState{partial: agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Provider:  p.Name(),
		}}
		cut := p.forward(ctx, out, in, prefix, len(prefix) > 0, &state)
		if cut == nil {
			return // a terminal event of the attempt's own reached the caller
		}
		if ctx.Err() != nil {
			// The turn was cancelled while the cut was being handled: this is the
			// user's own interrupt, and every front-end prints its own notice for
			// an abort. Filing it as a provider failure is the wording bug that
			// once made Ctrl+C read as a malfunction. Cancellation is also the one
			// thing that stops a turn with budget left.
			p.emitAborted(ctx, out)
			return
		}

		// What the attempt produced decides how the cut may be recovered, and an
		// empty why means "recoverable". Once answer text is on the caller's screen
		// the turn keeps continuing: a continuation that arrived at nothing may not
		// fall back to re-issuing, because a start event would take back text the
		// user has already read.
		continuing := len(prefix) > 0 || hasText(state.partial)
		why := ""
		switch {
		case state.serverCall:
			why = stopServerTool
		case continuing && !streamResumeEnabled():
			why = stopResumeOff
		}
		if why != "" || retries >= max {
			// Terminal: either it must not be recovered, or the budget is spent —
			// and with GOLDER_STREAM_MAX_RETRIES=0 that is the first cut, so the
			// turn stops before it makes a second request.
			p.emitStopped(ctx, out, cut, shownContent(&state, prefix), retries, why)
			return
		}

		retries++
		// Tell the front-end before the next attempt starts. The attempt's start
		// event was held, so without this the interface shows only the spinner and
		// the user cannot tell a retry from a hang. The notice keeps the original
		// cause and the watchdog window that expired, whatever the recovery is:
		// a continuation is still the same cut.
		detail := streamCutEvidence(cut)
		switch {
		case continuing:
			detail = joinDetail(detail, "continuing the cut answer from where it stopped")
		case state.localCall:
			// The cut landed inside a tool call, and the call was withheld: the
			// notice says so, because the card the model was about to raise will
			// not appear until the re-issued attempt asks for it again.
			detail = joinDetail(detail, "the cut landed inside a tool call that had not run yet")
		}
		_ = out.Emit(ctx, StreamNoticeEvent{
			Kind:        StreamNoticeRetry,
			Provider:    p.Name(),
			Reason:      streamCutReason(cut),
			Window:      streamCutWindow(cut),
			Attempt:     retries,
			MaxAttempts: max,
			Detail:      detail,
		})
		if !waitRetry(ctx, streamRetryBackoff(retries)) {
			p.emitAborted(ctx, out)
			return
		}

		nextReq := req
		if continuing {
			// Continue the answer: the partial text becomes the assistant's
			// previous turn, and the caller's rendering keeps it as a prefix. The
			// events of this attempt already carry the earlier prefix (prefixContent),
			// so when the attempt produced no text at all — it was cut before its
			// first token — the prefix is what continues.
			partial := textOnly(state.partial)
			if len(partial.Content) == 0 {
				partial.Content = prefix
			}
			prefix = partial.Content
			nextReq = continuationRequest(req, partial)
		} else {
			// Re-issue: nothing visible is kept. Anything the cut attempt rendered
			// (reasoning, or the start of a call whose deltas were held) is
			// replaced by the retry's own start event, so no partial from a
			// dropped attempt survives into the caller's message or its history.
			prefix = nil
		}
		// The retry may re-resolve a wire (the wrapper sits above the wire
		// fallback) and always builds a fresh request and decoder, so no state
		// from the cut attempt is carried over.
		next, used, err := p.connect(ctx, nextReq, retries, max, func(attempt int, cerr error) {
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
			p.emitStopped(ctx, out, connectOrCut(p.Name(), err, cut), shownContent(&state, prefix), retries, "")
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
// attempt ended the turn itself (a done message, or the wire's own failure, both
// already emitted). A non-nil error is the reason the attempt was cut; whether
// that may be re-issued, continued or must end the turn is one policy and it
// lives in pumpFrom.
func (p *streamRetryProvider) forward(ctx context.Context, out *AssistantMessageEventStream, in *AssistantMessageEventStream, prefix agentcore.ContentList, suppressStart bool, state *forwardState) error {
	// The attempt's start event is held rather than forwarded. It is not
	// something a person can see, and holding it is what lets a cut attempt
	// disappear without a trace — a forwarded start would have to be taken back.
	// A continuation suppresses it: the caller's stream already started, and a
	// second start would reset the partial it is rendering.
	var start *StreamStartEvent
	if state == nil {
		state = &forwardState{}
	}
	emitStart := func() {
		if start == nil {
			return
		}
		if suppressStart {
			start = nil
			return
		}
		if len(prefix) > 0 {
			start.Partial = prefixContent(prefix, start.Partial)
		}
		_ = out.Emit(ctx, *start)
		start = nil
	}

	for ev := range in.Events() {
		switch e := ev.(type) {
		case StreamStartEvent:
			state.observeCalls(e.Partial)
			e.Partial = withoutToolCalls(e.Partial)
			if start == nil {
				start = &e
			}
		case StreamTextEvent:
			state.observeCalls(e.Partial)
			e.Partial = withoutToolCalls(e.Partial)
			e.Partial = prefixContent(prefix, e.Partial)
			state.partial = e.Partial
			emitStart()
			_ = out.Emit(ctx, e)
		case StreamThinkingEvent:
			state.observeCalls(e.Partial)
			e.Partial = withoutToolCalls(e.Partial)
			// Thinking is transient scaffolding, not an answer: it is forwarded so
			// the user watches the model work, but it does NOT commit the attempt.
			// A stream cut after only thinking is therefore re-issued rather than
			// ending the turn.
			e.Partial = prefixContent(prefix, e.Partial)
			state.partial = e.Partial
			emitStart()
			_ = out.Emit(ctx, e)
		case StreamToolCallEvent:
			// A tool-call delta is HELD, not forwarded, and this is the fix that
			// makes a cut in a tool call recoverable. The call the model is
			// building is worth nothing on its own: a local call has not run
			// (the runtime executes calls only from a successful terminal
			// message), and a half-written arguments JSON is not even a call. If
			// it were forwarded, a cut would leave a call on screen and in the
			// caller's partial that will never execute — and, once spliced into a
			// continuation's prefix, an assistant tool call the next request
			// cannot pair with a result, which every wire rejects. So the delta is
			// recorded (which kind it is decides whether the attempt may be
			// replayed) and dropped; the complete calls arrive once, with the
			// terminal message the runtime executes.
			state.observeCalls(e.Partial)
		case StreamErrorEvent:
			state.observeCalls(e.Message)
			if isStreamCutErr(e.Err) {
				// Hand the raw cut back: the caller decides, and it has the state
				// that says what this attempt produced. Describing the cut here
				// is what used to make a cut after a tool call terminal with the
				// whole retry budget unspent.
				return e.Err
			}
			e.Message = prefixContent(prefix, e.Message)
			emitStart()
			_ = out.Emit(ctx, e)
			return nil
		case StreamDoneEvent:
			e.Message = prefixContent(prefix, e.Message)
			state.partial = e.Message
			emitStart()
			if len(e.Message.ToolCalls()) > 0 {
				// The held call deltas reach the caller here, complete and exactly
				// once: the terminal message is authoritative, so a front-end that
				// opens a card per partial opens exactly the cards the runtime is
				// about to execute.
				_ = out.Emit(ctx, StreamToolCallEvent{Partial: e.Message})
			}
			_ = out.Emit(ctx, e)
			return nil
		default:
			emitStart()
			_ = out.Emit(ctx, e)
		}
	}

	// The attempt ended without a terminal event: the stream simply closed on the
	// caller. That is a cut like any other — the wire fallback calls it "stream
	// ended without a terminal event" — so it takes the same path as the rest.
	return fmt.Errorf("%s: %w", p.Name(), errStreamNoTerminal)
}

// continuationRequest builds the request that continues a cut answer: the shaped
// context of the original turn, the partial answer text as the assistant's
// previous turn, and one short instruction. That context already holds the
// earlier turns and their completed tool results, and the caller has not yet
// written the cut attempt into it, so nothing from the dropped attempt is replayed
// except the text the user can already see. partial must be text-only (textOnly).
func continuationRequest(req CompletionRequest, partial agentcore.AssistantMessage) CompletionRequest {
	out := req
	msgs := make(agentcore.MessageList, 0, len(req.Context.Messages)+2)
	msgs = append(msgs, req.Context.Messages...)
	msgs = append(msgs, partial)
	msgs = append(msgs, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content: agentcore.ContentList{agentcore.NewTextContent(
			"Continue exactly where you stopped. Do not repeat text you already wrote.")},
	})
	out.Context.Messages = msgs
	return out
}

// textOnly keeps the answer text of a partial and drops everything else, because
// only text may be replayed into a continuation. A local tool call there would be
// an assistant call with no result to pair it — OpenAI and Anthropic both reject
// that history outright — and a partial reasoning block carries a signature taken
// from the middle of the response it belonged to: replaying that either fails the
// request or poisons the provider's cache for the conversation.
func textOnly(msg agentcore.AssistantMessage) agentcore.AssistantMessage {
	out := msg
	out.Content = nil
	for _, c := range msg.Content {
		if t, ok := c.(agentcore.TextContent); ok && strings.TrimSpace(t.Text) != "" {
			out.Content = append(out.Content, t)
		}
	}
	return out
}

// prefixContent prepends the already-shown content to a continuation event.
// The provider's partial messages describe only the new call, so without this a
// resumed text event would replace the caller's rendered answer with its tail.
func prefixContent(prefix agentcore.ContentList, msg agentcore.AssistantMessage) agentcore.AssistantMessage {
	if len(prefix) == 0 {
		return msg
	}
	msg.Content = append(append(agentcore.ContentList{}, prefix...), msg.Content...)
	return msg
}

// hasText reports whether the partial holds visible answer text (not only
// thinking). It is what separates "continue this answer" from "re-issue the
// request": text on screen must not be repeated, and nothing on screen is free to
// redo.
func hasText(msg agentcore.AssistantMessage) bool {
	for _, c := range msg.Content {
		if t, ok := c.(agentcore.TextContent); ok && strings.TrimSpace(t.Text) != "" {
			return true
		}
	}
	return false
}

// shownContent is what the caller has rendered, for the terminal message: the
// attempt's own partial when it says anything, and otherwise the answer prefix the
// caller is still displaying (an attempt cut before its first token produces no
// partial of its own, and the text already on screen is still part of the
// transcript).
func shownContent(state *forwardState, prefix agentcore.ContentList) agentcore.AssistantMessage {
	if hasText(state.partial) || len(prefix) == 0 {
		return state.partial
	}
	out := state.partial
	out.Content = prefix
	return out
}

// joinDetail appends one clause to a notice's detail, keeping the evidence first:
// what the guard saw is why the user is waiting, and what the retry is doing comes
// after it.
func joinDetail(detail, clause string) string {
	switch detail {
	case "":
		return clause
	default:
		return detail + "; " + clause
	}
}

// emitStopped ends the turn with the cut and the number of attempts named, so the
// caller always gets a terminal message — and a message that says what was tried,
// not the bare "retry the turn" that reads as if nothing had been. shown is what
// the caller has already rendered; its answer text is kept in the terminal
// message, because the reply the user read is part of the transcript even when the
// stream that carried it died. Everything else in shown is dropped: a terminal
// error message must not carry a tool call (no result pairs with it, and the
// runtime would either run it twice or be rejected by the next request) or a
// partial reasoning signature.
func (p *streamRetryProvider) emitStopped(ctx context.Context, out *AssistantMessageEventStream, cut error, shown agentcore.AssistantMessage, retries int, why string) {
	sentence := cut
	switch {
	case isConnectFailure(cut):
		// A connection failure gets the "never reached a response" sentence, which
		// counts the attempts and keeps the transport's own text.
		sentence = streamConnectError(p.Name(), cut, retries+1)
	case isStreamCutErr(cut):
		sentence = streamCutStoppedError(p.Name(), cut, retries+1, retries, why)
	}
	final := textOnly(shown)
	if len(shown.Content) == 0 {
		// Nothing was rendered, so the message has to be opened first: a caller
		// that receives only an error would otherwise have to synthesize the start
		// of the assistant message itself.
		_ = out.Emit(ctx, StreamStartEvent{Partial: agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant,
			Provider:  p.Name(),
		}})
	}
	final.RoleField = agentcore.RoleAssistant
	final.Provider = p.Name()
	final.StopReason = agentcore.StopReasonError
	final.ErrorMessage = sentence.Error()
	_ = out.Emit(ctx, StreamErrorEvent{Message: final, Err: cut})
}

// streamCutStoppedError is the sentence for a cut the loop could not recover: what
// happened (with the watchdog's window and the evidence it counted), how many
// attempts were made, and — when the loop stopped with budget left — why it did.
// It names the attempt count because "retry the turn" is exactly wrong here: the
// turn was retried, and the user needs to know that before deciding to wait.
// Anything that is not a cut is returned unchanged, so a configuration error keeps
// its own wording.
func streamCutStoppedError(provider string, err error, attempts, retries int, why string) error {
	if !isStreamCutErr(err) {
		return err
	}
	where := strings.TrimSpace(provider)
	if where == "" {
		where = "the provider"
	} else {
		where += ":"
	}
	sentence := fmt.Sprintf("%s stream cut — %s; the turn stopped after %s (%s)",
		where, streamCutDetail(provider, err), attemptsPhrase(attempts), retriesPhrase(retries))
	if why != "" {
		sentence += ": " + why
	}
	return errors.New(sentence)
}

// attemptsPhrase renders how many requests the turn made in all.
func attemptsPhrase(attempts int) string {
	if attempts <= 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", attempts)
}

// retriesPhrase renders how many of those attempts were retries, including the
// zero that GOLDER_STREAM_MAX_RETRIES=0 allows: "no retries" is the fact the user
// needs to see when nothing was re-sent.
func retriesPhrase(retries int) string {
	switch {
	case retries <= 0:
		return "no retries"
	case retries == 1:
		return "1 retry"
	default:
		return fmt.Sprintf("%d retries", retries)
	}
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
