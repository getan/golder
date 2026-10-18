package provider

// Stream failures: why this exists.
//
// A stream can end early in four ways, and every wire golder speaks reports them
// as raw machinery:
//
//   - the connection dies mid-response. net/http reports io.ErrUnexpectedEOF,
//     whose text is exactly "unexpected EOF", for a chunked body that never sees
//     its last chunk or one short of its Content-Length (net/http/internal/
//     chunked.go, transfer.go). On the Responses wire the OpenAI SDK hands its
//     SSE scanner's reader error back unchanged, so the transcript showed a bare
//     "error: unexpected EOF".
//   - the connection goes quiet: no bytes at all (the transport's idle
//     watchdog), which the Responses wire had no guard against at all.
//   - the connection stays alive but stops producing: SSE keep-alives keep the
//     idle window satisfied while the model says nothing (the content-stall
//     watchdog), the failure that a keep-alive-happy gateway hides longest.
//   - the stream ends without a terminal event at all: a start event and then a
//     clean close, which the wire fallback used to turn into a generic error
//     ("stream ended without a terminal event") — a sentence that names the
//     symptom but is not recognized as a cut, so it was surfaced without the
//     retry every other cut gets.
//
// None of the four is a configuration mistake, and all of them turn a turn that
// had produced nothing into a dead end: they ended the stream instead of being
// retried. This file is the shared vocabulary — one place that decides "this
// stream was cut" and one sentence to say so — while streamretry.go owns the
// retry policy built on it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// The cut kinds carried by StreamNoticeEvent/ProviderNoticeEvent. They are the
// vocabulary a front-end localizes, so they stay stable strings.
const (
	StreamCutIdle      = "idle"
	StreamCutStall     = "stall"
	StreamCutTruncated = "truncated"
	StreamCutSilent    = "silent"
)

// Watchdog phases: the two windows are different questions, and a person reading
// the failure needs to know which one expired. "No answer started" means the
// model never began (a long prefill, a queued request, a silent think); "output
// stopped" means it began and then went quiet (a mid-stream hang).
const (
	StreamPhaseFirstToken = "first-token"
	StreamPhaseStream     = "stream"
)

// StreamEvidence is what a watchdog saw while it waited. It is the difference
// between "the gateway keeps the socket warm and says nothing" and "nothing
// arrived at all": with keep-alives counted, the first is visible as bytes and
// zero content frames.
type StreamEvidence struct {
	// Bytes is everything read from the body, keep-alives included.
	Bytes int64
	// Frames counts data payloads that reached the decoder.
	Frames int
	// KeepAlives counts SSE comment lines (": ping" and friends).
	KeepAlives int
	// LastFrame names the last line kind seen: "comment", "event", "blank",
	// "data", or "none" when nothing arrived.
	LastFrame string
}

// String renders the evidence as the clause a failure sentence appends.
func (e StreamEvidence) String() string {
	last := e.LastFrame
	if last == "" {
		last = "none"
	}
	if e.Bytes == 0 {
		return "no bytes arrived"
	}
	return fmt.Sprintf("%d bytes, %d content frames, %d keep-alives, last line: %s",
		e.Bytes, e.Frames, e.KeepAlives, last)
}

// StreamWatchdogError is a watchdog failure that carries its phase, window and
// evidence. It wraps the idle/stall sentinel, so every existing classification
// (isStreamCutErr, streamCutReason, the retry policy) keeps working unchanged.
type StreamWatchdogError struct {
	// Sink is errStreamIdle or errStreamStall.
	Sink error
	// Phase is StreamPhaseFirstToken or StreamPhaseStream.
	Phase string
	// Window is the budget that expired.
	Window time.Duration
	// Seen is what the guard observed.
	Seen StreamEvidence
}

func (e *StreamWatchdogError) Error() string {
	return fmt.Sprintf("%s (%s, %s)", e.Sink.Error(), e.Phase, e.Seen.String())
}

func (e *StreamWatchdogError) Unwrap() error { return e.Sink }

// watchdogError builds a StreamWatchdogError for the phase and window that
// expired.
func watchdogError(sink error, phase string, window time.Duration, seen StreamEvidence) error {
	return &StreamWatchdogError{Sink: sink, Phase: phase, Window: window, Seen: seen}
}

// streamCutEvidence returns the evidence clause for a watchdog failure, or "" for
// any other error. The retry notice appends it so the waiting user can tell a
// silent gateway from a dead one.
func streamCutEvidence(err error) string {
	var wd *StreamWatchdogError
	if errors.As(err, &wd) {
		return wd.Seen.String()
	}
	return ""
}

// streamCutIsFirstToken reports whether the failure happened before the model
// produced anything. Two of those in a row mean the upstream is not answering at
// all, which is a different situation from a stream that keeps breaking mid-answer.
func streamCutIsFirstToken(err error) bool {
	var wd *StreamWatchdogError
	return errors.As(err, &wd) && wd.Phase == StreamPhaseFirstToken
}

// isTruncatedStreamErr reports a stream whose body ended before the response
// finished. The sentinel is the reliable signal; the string match covers the SDK
// paths that wrap the reader's error without preserving it.
func isTruncatedStreamErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "unexpected eof")
}

// isStreamCutErr reports any failure that means "the stream ended early": a
// dropped connection, an idle watchdog, or a content stall. It is deliberately
// narrower than "any error" — a 400 or a decoder failure is the wire's real
// answer and must be surfaced as it is, not retried.
func isStreamCutErr(err error) bool {
	if err == nil {
		return false
	}
	return isTruncatedStreamErr(err) ||
		errors.Is(err, errStreamIdle) ||
		errors.Is(err, errStreamStall) ||
		errors.Is(err, errStreamNoTerminal)
}

// streamCutReason names which cut happened, in the stable vocabulary above.
// Call it only after isStreamCutErr says the error is a cut.
func streamCutReason(err error) string {
	switch {
	case errors.Is(err, errStreamIdle):
		return StreamCutIdle
	case errors.Is(err, errStreamStall):
		return StreamCutStall
	case isTruncatedStreamErr(err):
		return StreamCutTruncated
	default:
		return StreamCutSilent
	}
}

// streamCutWindow is the watchdog window that expired, for the two time-based
// cuts; zero for the others. A front-end prints it so the notice matches the
// setting (GOLDER_STREAM_IDLE_TIMEOUT) the user can change.
func streamCutWindow(err error) time.Duration {
	var wd *StreamWatchdogError
	if errors.As(err, &wd) {
		return wd.Window
	}
	switch {
	case errors.Is(err, errStreamIdle):
		return idleTimeout()
	case errors.Is(err, errStreamStall):
		return stallTimeout()
	default:
		return 0
	}
}

// streamCutError renders a cut stream for the transcript: what happened, whose
// stream it was, and the one thing worth trying. Any other error is returned
// unchanged, so this is safe to call on every failure path.
func streamCutError(provider string, err error) error {
	return cutError(provider, err, "retry the turn")
}

// streamCutRetriedError is the same sentence for a stream that was cut again
// after the retry: saying only "retry the turn" would leave the user wondering
// whether the retry had run at all, and whether waiting would help. It did, and
// it will not.
func streamCutRetriedError(provider string, err error, retries int) error {
	times := "once"
	if retries != 1 {
		times = fmt.Sprintf("%d times", retries)
	}
	return cutError(provider, err, fmt.Sprintf("already retried %s, and the connection dropped again — retry the turn", times))
}

// cutError builds the sentence for a cut stream with the given remedy.
func cutError(provider string, err error, remedy string) error {
	if !isStreamCutErr(err) {
		return err
	}
	where := strings.TrimSpace(provider)
	if where == "" {
		where = "the provider"
	} else {
		where += ":"
	}
	return fmt.Errorf("%s stream cut — %s; %s", where, streamCutDetail(provider, err), remedy)
}

// streamCutDetail names which of the three happened, with the window that
// expired where one did — the numbers the watchdogs used, so the sentence
// matches the setting (GOLDER_STREAM_IDLE_TIMEOUT) the user can change. The
// underlying cause is rendered without the provider prefix the drivers already
// put on it, so the gateway is not named twice in one sentence.
func streamCutDetail(provider string, err error) string {
	var wd *StreamWatchdogError
	if errors.As(err, &wd) {
		return watchdogDetail(wd)
	}
	switch {
	case errors.Is(err, errStreamIdle):
		return fmt.Sprintf("no data arrived for %s (the connection went quiet)", idleTimeout())
	case errors.Is(err, errStreamStall):
		return fmt.Sprintf("no output arrived for %s (the model stopped producing)", stallTimeout())
	case errors.Is(err, errStreamNoTerminal):
		return "the stream ended without a final event (the connection closed before the answer was complete)"
	default:
		return fmt.Sprintf("the connection closed before the answer finished (%s)", cutCause(provider, err))
	}
}

// watchdogDetail renders the phase-aware sentence for a watchdog failure: which
// question expired, with which budget, and what the guard saw while waiting.
func watchdogDetail(wd *StreamWatchdogError) string {
	switch {
	case wd.Phase == StreamPhaseFirstToken && errors.Is(wd.Sink, errStreamStall):
		return fmt.Sprintf("no answer started within %s (the connection stayed open but produced nothing; %s)",
			wd.Window, wd.Seen.String())
	case wd.Phase == StreamPhaseFirstToken:
		return fmt.Sprintf("no data arrived for %s (the connection went quiet; %s)", wd.Window, wd.Seen.String())
	case errors.Is(wd.Sink, errStreamStall):
		return fmt.Sprintf("output stopped for %s (the model stopped producing; %s)", wd.Window, wd.Seen.String())
	default:
		return fmt.Sprintf("no data arrived for %s (the connection went quiet; %s)", wd.Window, wd.Seen.String())
	}
}

// streamCutUpstreamSilent is the sentence for a provider that produced nothing on
// every attempt: the retry budget is not spent, it is pointless.
func streamCutUpstreamSilent(provider string, err error, attempts int) error {
	where := strings.TrimSpace(provider)
	if where == "" {
		where = "the provider"
	} else {
		where += ":"
	}
	return fmt.Errorf("%s no answer started on %d attempts — the connection stayed open and the model produced nothing; "+
		"the gateway or the model is not answering, retry later (%s)",
		where, attempts, streamCutEvidence(err))
}

// firstContentTimeout is the budget for the first content frame: the phase where
// a long prefill or a silent think is legitimate. It widens with each retry, so a
// genuinely slow request is not cut three times at the same mark, while a gateway
// that answers nothing still ends the turn instead of hanging.
func firstContentTimeout(ctx context.Context) time.Duration {
	base := 3 * time.Minute
	if v := os.Getenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			base = d
		}
	}
	switch streamAttempt(ctx) {
	case 0:
		return base
	case 1:
		return base + time.Minute
	default:
		return base + 2*time.Minute
	}
}

// streamAttemptKey carries the retry number into a provider call, so the watchdog
// budgets widen per attempt without any shared mutable state (sub-agents stream
// concurrently).
type streamAttemptKey struct{}

// withStreamAttempt tags ctx with the attempt number (0 = the first call).
func withStreamAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, streamAttemptKey{}, attempt)
}

// streamAttempt returns the attempt number carried by ctx, 0 when absent.
func streamAttempt(ctx context.Context) int {
	n, _ := ctx.Value(streamAttemptKey{}).(int)
	return n
}

// cutCause is the reader's error as the user should read it: the drivers prefix
// their errors with the provider name, which the sentence already carries.
func cutCause(provider string, err error) string {
	text := err.Error()
	if p := strings.TrimSpace(provider); p != "" {
		text = strings.TrimPrefix(text, p+": ")
	}
	return text
}
