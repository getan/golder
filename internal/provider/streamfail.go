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
	"errors"
	"fmt"
	"io"
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

// cutCause is the reader's error as the user should read it: the drivers prefix
// their errors with the provider name, which the sentence already carries.
func cutCause(provider string, err error) string {
	text := err.Error()
	if p := strings.TrimSpace(provider); p != "" {
		text = strings.TrimPrefix(text, p+": ")
	}
	return text
}
