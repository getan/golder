package provider

// Truncated streams: why this exists.
//
// net/http reports io.ErrUnexpectedEOF — the string is exactly "unexpected EOF"
// — when a response body ends before the response does: the connection died
// mid-chunk, or short of the advertised Content-Length (see
// net/http/internal/chunked.go and transfer.go). On a streaming request that is
// the whole symptom of a cut connection, and every wire golder speaks surfaces
// it the same way, from the reader up.
//
// Two things are wrong with that when it reaches a person. It is unreadable:
// "error: unexpected EOF" names neither the provider, nor the wire, nor whether
// the answer was lost — and on the Responses wire it arrives bare, because the
// OpenAI SDK's SSE scanner hands its reader's error back unchanged while
// golder's own transport at least prefixes "read error". And it is not final: a
// dropped connection is the one failure that retrying almost always fixes, yet
// the turn simply ended.
//
// The fix is deliberately narrow. A truncated stream is retried once when
// nothing has been shown yet (the same safety argument as the empty-completion
// retry: no content, so a retry cannot duplicate anything); a truncation that
// arrived after content is surfaced with a message that says what happened and
// what to do about it, rather than a bare reader error.

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// isTruncatedStreamErr reports a stream whose body ended before the response
// finished. The sentinel is the reliable signal; the string match covers the
// SDK paths that wrap the reader's error without preserving it.
func isTruncatedStreamErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "unexpected eof")
}

// streamTruncatedError renders a truncation for the transcript: what happened,
// whose stream it was, and the one thing worth trying. Any other error is
// returned unchanged, so this is safe to call on every failure path.
func streamTruncatedError(provider string, err error) error {
	if !isTruncatedStreamErr(err) {
		return err
	}
	where := strings.TrimSpace(provider)
	if where == "" {
		where = "the provider"
	} else {
		where += ":"
	}
	return fmt.Errorf("%s stream truncated — the connection closed before the answer finished (%v); retry the turn", where, err)
}
