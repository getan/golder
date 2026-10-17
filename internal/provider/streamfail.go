package provider

// Stream failures: why this exists.
//
// A stream can end early in three ways, and every wire golder speaks reports all
// three as raw machinery:
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
//
// None of the three is a configuration mistake, and all three turn a turn that
// had produced nothing into a dead end: they ended the stream instead of being
// retried. This file is the shared vocabulary — one place that decides "this
// stream was cut" and one sentence to say so — while streamretry.go owns the
// retry policy built on it.

import (
	"errors"
	"fmt"
	"io"
	"strings"
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
		errors.Is(err, errStreamStall)
}

// streamCutError renders a cut stream for the transcript: what happened, whose
// stream it was, and the one thing worth trying. Any other error is returned
// unchanged, so this is safe to call on every failure path.
func streamCutError(provider string, err error) error {
	if !isStreamCutErr(err) {
		return err
	}
	where := strings.TrimSpace(provider)
	if where == "" {
		where = "the provider"
	} else {
		where += ":"
	}
	return fmt.Errorf("%s stream cut — %s; retry the turn", where, streamCutDetail(err))
}

// streamCutDetail names which of the three happened, with the window that
// expired where one did — the numbers the watchdogs used, so the sentence
// matches the setting (GOLDER_STREAM_IDLE_TIMEOUT) the user can change.
func streamCutDetail(err error) string {
	switch {
	case errors.Is(err, errStreamIdle):
		return fmt.Sprintf("no data arrived for %s (the connection went quiet)", idleTimeout())
	case errors.Is(err, errStreamStall):
		return fmt.Sprintf("no output arrived for %s (the model stopped producing)", stallTimeout())
	default:
		return fmt.Sprintf("the connection closed before the answer finished (%v)", err)
	}
}
