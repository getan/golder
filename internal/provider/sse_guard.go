package provider

// Empty SSE frames: why this exists.
//
// The Responses driver streams through openai-go's ssestream decoder. That
// decoder dispatches an event on EVERY blank line — including a blank line that
// terminates nothing: a leading blank, a keep-alive, an empty `data:` field, or
// one extra newline after the last event. The dispatch then carries empty Data,
// and the SDK runs json.Unmarshal on it, which fails with Go's error for empty
// input: "unexpected end of JSON input".
//
// Seen live against a third-party Responses relay (OPENAI_BASE_URL): runs died
// with an empty assistant message and exactly that text, several times in one
// session, while other attempts on the same model succeeded — the signature of
// noise at stream start, not of a bad request.
//
// golder's own SSE transport is already immune (transport.go's flush returns
// early when the payload buffer is empty), so only the SDK path needs this.
//
// The fix is a RoundTripper that drops frames carrying no content. A frame WITH
// content is left byte-for-byte alone, because a blank line is what separates
// two events: removing the wrong one would merge them and corrupt the stream.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sseContentType is the media type whose bodies are filtered.
const sseContentType = "text/event-stream"

// sseGuardTransport filters contentless frames out of SSE response bodies.
type sseGuardTransport struct{ base http.RoundTripper }

func (t *sseGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), sseContentType) {
		return resp, nil
	}
	resp.Body = newEmptySSEFrameFilter(resp.Body)
	return resp, nil
}

// guardSSEFrames returns a copy of c whose transport drops contentless SSE
// frames. The client is copied so the caller's (shared, cached) client keeps
// its own transport: only the SDK's Responses streaming sees the filter.
func guardSSEFrames(c *http.Client) *http.Client {
	if c == nil {
		return nil
	}
	out := *c
	out.Transport = &sseGuardTransport{base: c.Transport}
	return &out
}

// emptySSEFrameFilter removes SSE frames that carry no content at all.
//
// A frame is kept when a `data:` field with a non-whitespace value arrived
// since the last dispatch. Everything else — a leading blank line, a blank line
// after only `event:`/comment lines, an empty `data:` field, a repeated
// separator — is swallowed, because the alternative is an empty event that the
// SDK cannot decode.
type emptySSEFrameFilter struct {
	r  io.ReadCloser
	br *bufio.Reader
	// out holds bytes classified as surviving, pending delivery to Read.
	out []byte
	// have records a content-carrying data field since the last dispatch.
	have bool
	// err is stashed once the underlying reader is done, so buffered bytes are
	// delivered before the error that ends them.
	err error
}

func newEmptySSEFrameFilter(r io.ReadCloser) *emptySSEFrameFilter {
	return &emptySSEFrameFilter{r: r, br: bufio.NewReader(r)}
}

func (f *emptySSEFrameFilter) Read(p []byte) (int, error) {
	for len(f.out) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		line, err := f.br.ReadBytes('\n')
		if len(line) > 0 {
			f.classify(line)
		}
		if err != nil {
			f.err = err
		}
		if len(f.out) == 0 && f.err != nil {
			return 0, f.err
		}
	}
	n := copy(p, f.out)
	f.out = f.out[n:]
	return n, nil
}

// classify appends one raw line to the output buffer when it survives.
func (f *emptySSEFrameFilter) classify(line []byte) {
	body := bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(body)) == 0 {
		// A dispatch. Keep it only when it closes a frame that carried content;
		// a blank line with nothing behind it becomes an empty event.
		if f.have {
			f.out = append(f.out, line...)
			f.have = false
		}
		return
	}
	f.out = append(f.out, line...)
	name, value, ok := bytes.Cut(bytes.TrimLeft(body, " \t"), []byte(":"))
	if ok && string(name) == "data" && len(bytes.TrimSpace(value)) > 0 {
		f.have = true
	}
}

func (f *emptySSEFrameFilter) Close() error { return f.r.Close() }

// sseStreamError names a failed SSE read. A JSON syntax failure means the
// upstream stream was malformed (or truncated mid-frame); saying so, and
// naming the provider, beats leaving Go's bare parser error as the whole
// message — the shape that made "unexpected end of JSON input" unexplained.
func sseStreamError(name string, err error) error {
	if err == nil {
		return nil
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) || strings.Contains(err.Error(), "unexpected end of JSON input") {
		return fmt.Errorf("%s: upstream sent a malformed event frame (%s): %w",
			name, err.Error(), err)
	}
	return fmt.Errorf("%s: %w", name, err)
}
