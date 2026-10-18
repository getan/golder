package provider

// SSE stream guard: the SDK path's frame filter and its watchdogs.
//
// The Responses driver streams through openai-go's ssestream decoder, which
// reads the HTTP body itself. That leaves two jobs to a wrapper around the body:
//
//  1. Frame filtering. The decoder dispatches an event on EVERY blank line —
//     including a blank line that terminates nothing: a leading blank, a
//     keep-alive, an empty `data:` field, or one extra newline after the last
//     event. The dispatch then carries empty Data, and the SDK runs
//     json.Unmarshal on it, which fails with Go's error for empty input:
//     "unexpected end of JSON input". Seen live against a third-party Responses
//     relay: runs died with an empty assistant message and exactly that text,
//     several times in one session, while other attempts on the same model
//     succeeded — the signature of noise at stream start, not of a bad request.
//     golder's own SSE transport is already immune (transport.go's flush returns
//     early when the payload buffer is empty), so only the SDK path needs this.
//
//  2. Watchdogs. golder's own transport watches its reads for an idle stream and
//     for a stream that stops producing while keep-alives keep it alive; the SDK
//     path had no such guard, so a gateway that accepted the request and then
//     went silent left the turn hanging with no timeout, no error, and no way
//     out but Ctrl+C. Because this wrapper is what frames the body into SSE
//     lines, it can apply both windows with exactly the semantics the transport
//     uses (same knob, same numbers — see idleTimeout/stallTimeout).
//
// The frame filter must stay byte-exact for everything it keeps: a frame WITH
// content is left untouched, because a blank line is what separates two events
// and removing the wrong one would merge them into one undecodable payload.
//
// Reading happens on a goroutine because a deadline cannot interrupt a blocking
// read; Read selects over the framed lines and the two windows, and a timeout
// closes the body, which is what unblocks the reader.

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sseContentType is the media type whose bodies are guarded.
const sseContentType = "text/event-stream"

// guardChunkBuf is how many framed lines the reader may run ahead of Read.
const guardChunkBuf = 32

// sseGuardTransport guards SSE response bodies.
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
	resp.Body = newSSEStreamGuard(req.Context(), resp.Body)
	return resp, nil
}

// guardSSEFrames returns a copy of c whose transport guards SSE bodies. The
// client is copied so the caller's (shared, cached) client keeps its own
// transport: only the SDK's Responses streaming sees the guard.
func guardSSEFrames(c *http.Client) *http.Client {
	if c == nil {
		return nil
	}
	out := *c
	out.Transport = &sseGuardTransport{base: c.Transport}
	return &out
}

// guardChunk is one framed line, or the reader's terminal error.
type guardChunk struct {
	data    []byte
	content bool
	err     error
}

// sseStreamGuard frames an SSE body into lines, drops the frames that carry no
// content, and enforces the idle and content-stall windows on the way.
type sseStreamGuard struct {
	r     io.ReadCloser
	idle  time.Duration
	stall time.Duration

	chunks chan guardChunk
	// done stops the reader goroutine when Read gives up, so a body nobody reads
	// anymore cannot leak it.
	done chan struct{}
	once sync.Once

	// out holds classified bytes pending delivery to Read.
	out []byte
	// err is stashed once the reader is done, so buffered bytes are delivered
	// before the error that ends them.
	err error

	timer       *time.Timer
	lastByte    time.Time
	lastContent time.Time

	// started anchors the first-token budget, and gotContent flips the guard from
	// that budget to the inter-chunk one: a cold prefill may legitimately take
	// minutes, a stall after output began may not.
	started    time.Time
	gotContent bool
	first      time.Duration
	phase      string
	seen       StreamEvidence
}

func newSSEStreamGuard(ctx context.Context, r io.ReadCloser) *sseStreamGuard {
	now := time.Now()
	g := &sseStreamGuard{
		r:           r,
		idle:        idleTimeout(),
		stall:       stallTimeout(),
		first:       firstContentTimeout(ctx),
		phase:       StreamPhaseFirstToken,
		chunks:      make(chan guardChunk, guardChunkBuf),
		done:        make(chan struct{}),
		timer:       time.NewTimer(time.Hour),
		lastByte:    now,
		lastContent: now,
		started:     now,
		seen:        StreamEvidence{LastFrame: "none"},
	}
	g.timer.Stop()
	go g.read()
	return g
}

func (g *sseStreamGuard) Read(p []byte) (int, error) {
	for {
		if len(g.out) > 0 {
			n := copy(p, g.out)
			g.out = g.out[n:]
			return n, nil
		}
		if g.err != nil {
			return 0, g.err
		}
		if len(p) == 0 {
			return 0, nil
		}
		if err := g.fill(); err != nil {
			g.err = err
			return 0, err
		}
	}
}

// fill waits for the next surviving chunk within both windows.
func (g *sseStreamGuard) fill() error {
	wait := g.idle - time.Since(g.lastByte)
	content := g.stall - time.Since(g.lastContent)
	if !g.gotContent {
		// Before the first content frame the budget is measured from the request,
		// not from the last content: nothing has produced yet.
		content = g.first - time.Since(g.started)
	}
	if content < wait {
		wait = content
	}
	if wait <= 0 {
		return g.timeout()
	}
	resetTimer(g.timer, wait)
	select {
	case c, ok := <-g.chunks:
		if !ok {
			return io.EOF
		}
		if c.err != nil {
			return c.err
		}
		g.lastByte = time.Now()
		if c.content {
			g.lastContent = g.lastByte
			g.seen.Frames++
			g.gotContent = true
			g.phase = StreamPhaseStream
		}
		g.out = append(g.out, c.data...)
		return nil
	case <-g.timer.C:
		return g.timeout()
	}
}

// timeout names the window that expired and stops the reader. The sentinel is
// what streamfail.go classifies, so the SDK path and the transport report the
// same failure in the same words.
func (g *sseStreamGuard) timeout() error {
	g.stop()
	window := g.stall
	if !g.gotContent {
		window = g.first
	}
	if time.Since(g.lastByte) >= g.idle {
		return watchdogError(errStreamIdle, g.phase, g.idle, g.seen)
	}
	return watchdogError(errStreamStall, g.phase, window, g.seen)
}

// stop closes done and the body so the reader goroutine unblocks and exits. It
// is idempotent, and Close uses it too.
func (g *sseStreamGuard) stop() {
	g.once.Do(func() {
		close(g.done)
		_ = g.r.Close()
	})
}

func (g *sseStreamGuard) Close() error {
	g.stop()
	return nil
}

// read frames the body into SSE lines and hands the survivors over. It runs on
// its own goroutine because a deadline cannot interrupt a blocking read.
func (g *sseStreamGuard) read() {
	defer close(g.chunks)
	br := bufio.NewReader(g.r)
	have := false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			g.seen.Bytes += int64(len(line))
			trimmed := bytes.TrimRight(line, "\r\n")
			switch {
			case len(bytes.TrimSpace(trimmed)) == 0:
				g.seen.LastFrame = "blank"
			case bytes.HasPrefix(bytes.TrimLeft(trimmed, " \t"), []byte(":")):
				g.seen.KeepAlives++
				g.seen.LastFrame = "comment"
			case bytes.HasPrefix(bytes.TrimLeft(trimmed, " \t"), []byte("data:")):
				g.seen.LastFrame = "data"
			default:
				g.seen.LastFrame = "event"
			}
			if keep, content := classifySSELine(line, &have); keep {
				if !g.send(guardChunk{data: line, content: content}) {
					return
				}
			}
		}
		if err != nil {
			g.send(guardChunk{err: err})
			return
		}
	}
}

// send delivers a chunk unless the guard has already stopped reading.
func (g *sseStreamGuard) send(c guardChunk) bool {
	select {
	case g.chunks <- c:
		return true
	case <-g.done:
		return false
	}
}

// classifySSELine decides one raw line's fate. A line is kept unless it is a
// dispatch (blank line) that closes a frame carrying no content: that is exactly
// the frame the SDK cannot decode. content reports a `data:` field with a
// non-whitespace value, which is what resets the stall window.
//
// have carries "a content-carrying data field arrived since the last dispatch"
// across lines.
func classifySSELine(line []byte, have *bool) (keep bool, content bool) {
	body := bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(body)) == 0 {
		if *have {
			*have = false
			return true, false
		}
		return false, false
	}
	name, value, ok := bytes.Cut(bytes.TrimLeft(body, " \t"), []byte(":"))
	if ok && string(name) == "data" && len(bytes.TrimSpace(value)) > 0 {
		*have = true
		return true, true
	}
	return true, false
}
