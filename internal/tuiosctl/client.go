// Package tuiosctl is golder's narrow Go client for the tuios daemon's
// line-delimited JSON protocol. It is deliberately not a general tuios CLI:
// only the verbs the pane tools need are exposed, each with a typed request
// and response.
package tuiosctl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// EnvEnv marks a process started inside a tuios pane.
	EnvEnv = "TUIOS_ENV"
	// EnvSocket is the daemon socket a pane belongs to.
	EnvSocket = "TUIOS_SOCKET"
	// EnvSession is the pane's session name.
	EnvSession = "TUIOS_SESSION"
	// EnvPaneID is the pane's window id.
	EnvPaneID = "TUIOS_PANE_ID"
	// EnvPaneToken proves the pane id where the kernel cannot.
	EnvPaneToken = "TUIOS_PANE_TOKEN"

	protocolVersion = 1
	maxLineBytes    = 16 << 20
	defaultCallTTL  = 10 * time.Second
)

// Options locates one daemon and the pane the process runs in.
type Options struct {
	Socket  string
	Session string
	PaneID  string
	// Proof is the pane's TUIOS_PANE_TOKEN. The field is named for what it
	// does, and is never logged.
	Proof string
	// Version is sent in hello. Tests use it to identify a fake client.
	Version string
}

// FromEnv reads the tuios variables every pane receives.
func FromEnv() Options {
	opts := Options{}
	opts.Socket = strings.TrimSpace(os.Getenv(EnvSocket))
	opts.Session = strings.TrimSpace(os.Getenv(EnvSession))
	opts.PaneID = strings.TrimSpace(os.Getenv(EnvPaneID))
	opts.Proof = strings.TrimSpace(os.Getenv(EnvPaneToken))
	opts.Version = "golder"
	return opts
}

// Available reports whether this process is running inside a tuios pane that
// exposes a daemon socket and session name.
func Available() bool {
	o := FromEnv()
	return strings.TrimSpace(os.Getenv(EnvEnv)) == "1" &&
		o.Socket != "" && o.Session != "" && o.PaneID != ""
}

// PaneID is the pane this process runs in, or "" outside one. The tool layer
// uses it to mark the pane it must never close.
func (c *Client) PaneID() string { return c.opts.PaneID }

// Error is a daemon-side verb error. It is separate from transport errors so a
// caller can act on the stable code (for example timeout vs window_not_found)
// without parsing prose.
type Error struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Hint    json.RawMessage `json:"hint,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "tuios: unknown error"
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Client is one reusable JSON connection. Calls are serialized because the
// protocol processes one request per connection at a time. A call that fails at
// the transport level — a timeout, a broken write, a reply that cannot be read
// or that answers another request's id — closes the connection, so the next
// call starts from a clean hello rather than reading a late reply as its own
// answer. A daemon restart therefore costs the call in flight, never the
// client; a verb the daemon declined leaves the connection in step and usable.
type Client struct {
	mu     sync.Mutex
	opts   Options
	conn   net.Conn
	reader *bufio.Reader
	nextID int
}

// Session is the session this client is bound to. Tools never accept a
// caller-supplied session.
func (c *Client) Session() string { return c.opts.Session }

// Dial connects, sends hello, and returns a ready client. hello is advisory in
// the tuios protocol, so an older daemon's unknown_verb is not fatal; every
// other handshake failure is.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	if opts.Socket == "" {
		return nil, errors.New("tuios: TUIOS_SOCKET is empty")
	}
	c := &Client{opts: opts}
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) connect(ctx context.Context) error {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "unix", c.opts.Socket)
	if err != nil {
		return fmt.Errorf("tuios: connect %s: %w", c.opts.Socket, err)
	}
	c.conn = conn
	c.reader = bufio.NewReaderSize(conn, 64<<10)

	// hello is optional, but it is the cheapest way to fail early against a
	// daemon whose protocol is incompatible.
	_, err = c.callLocked(ctx, "hello", map[string]any{
		"client": "golder", "version": c.opts.Version, "protocol": protocolVersion,
	}, nil)
	if err != nil {
		var derr *Error
		if errors.As(err, &derr) && derr.Code == "unknown_verb" {
			return nil
		}
		_ = c.closeLocked()
		return err
	}
	return nil
}

// Close closes the connection. It is safe to call more than once.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.reader = nil
	return err
}

// Call invokes one verb and returns its raw result. The context deadline bounds
// the round trip; without one, defaultCallTTL applies.
func (c *Client) Call(ctx context.Context, verb string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		if err := c.connect(ctx); err != nil {
			return nil, err
		}
	}
	return c.callLocked(ctx, verb, params, nil)
}

// CallInto invokes one verb and decodes its result into out.
func (c *Client) CallInto(ctx context.Context, verb string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		if err := c.connect(ctx); err != nil {
			return err
		}
	}
	_, err := c.callLocked(ctx, verb, params, out)
	return err
}

// callLocked sends one request and reads exactly one response. The caller holds
// c.mu.
func (c *Client) callLocked(ctx context.Context, verb string, params any, out any) (json.RawMessage, error) {
	if c.conn == nil {
		return nil, errors.New("tuios: connection is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultCallTTL)
	}
	conn := c.conn
	_ = conn.SetDeadline(deadline)
	// A context cancellation must interrupt the blocking read. The deadline
	// alone can be minutes away (a wait-for), which would leave the tool call
	// holding its connection and its caller's goroutine after the pane closed.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer func() {
		stop()
		if ctx.Err() == nil {
			_ = conn.SetDeadline(time.Time{})
		}
	}()

	id := c.nextID + 1
	c.nextID = id
	req := struct {
		ID     int    `json:"id"`
		Verb   string `json:"verb"`
		Params any    `json:"params,omitempty"`
	}{ID: id, Verb: verb, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("tuios: encode %s: %w", verb, err)
	}
	b = append(b, '\n')
	if err := writeAll(conn, b); err != nil {
		_ = c.closeLocked()
		// A cancelled or expired context is the reason the write failed, and it
		// is what the caller can act on: the deadline alone may be minutes away
		// (a wait-for), so reporting the raw socket error would hide the cause.
		if ctxErr := ctxError(ctx, verb); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("tuios: write %s: %w", verb, err)
	}

	line, err := readLine(c.reader, maxLineBytes)
	if err != nil {
		_ = c.closeLocked()
		if ctxErr := ctxError(ctx, verb); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, errLineTooLong) {
			// The bytes of that line are still on the wire and the connection
			// is one line out of step, so it is dropped rather than reused.
			return nil, fmt.Errorf("tuios: %s response exceeds %d bytes", verb, maxLineBytes)
		}
		if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			// Wrapped so a caller can test for a deadline with errors.Is while
			// the message stays what a person reads.
			return nil, fmt.Errorf("tuios: %s timed out: %w", verb, context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("tuios: read %s: %w", verb, err)
	}
	line = bytes.TrimSpace(line)
	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		_ = c.closeLocked()
		return nil, fmt.Errorf("tuios: decode %s response: %w", verb, err)
	}
	if resp.ID != id {
		_ = c.closeLocked()
		return nil, fmt.Errorf("tuios: %s response id %d, want %d", verb, resp.ID, id)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return nil, fmt.Errorf("tuios: decode %s result: %w", verb, err)
		}
	}
	return resp.Result, nil
}

// ctxError is the error a call that failed because its context ended reports.
// A deadline keeps the "timed out" wording a person reads while staying
// testable with errors.Is; a cancellation is returned as it is, because the
// caller cancelled rather than the daemon being slow.
func ctxError(ctx context.Context, verb string) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("tuios: %s timed out: %w", verb, err)
	}
	return err
}

// writeAll hands every byte to the connection. A net.Conn is allowed to make a
// short write; treating the first Write's nil error as success would truncate a
// patch or command at exactly the size boundary.
func writeAll(conn net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := conn.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// errLineTooLong is readLine's refusal of a line past the caller's limit. It is
// separate from the other read errors because the caller's connection is one
// line out of step after it, not merely broken.
var errLineTooLong = errors.New("tuios: response line exceeds the limit")

// readLine reads one newline-terminated line, refusing to buffer more than
// limit bytes.
//
// bufio.Reader.ReadBytes grows its buffer until the delimiter arrives, so a
// check on the result runs only after the allocation has already happened: a
// daemon (or a socket that is not the daemon) that never sends a newline could
// make this client allocate without bound. ReadSlice hands back the bytes it
// has and lets this loop stop at the limit.
//
// A line that ends exactly at limit is accepted; one byte more is refused.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(line)+len(frag) > limit {
			return nil, errLineTooLong
		}
		line = append(line, frag...)
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			// The delimiter is still coming; the limit above bounds the wait.
			continue
		}
		// A partial line with a real error is not a line: the caller treats
		// every other error as a broken connection, and half a response parsed
		// as a whole one would be worse than no response.
		if errors.Is(err, io.EOF) && len(line) > 0 {
			// The daemon went away mid-reply, which says more than a bare EOF:
			// a bug or a restart, rather than a request it never answered.
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
}

// Restrict gives up authority on this connection. Read tools use own/read_only
// so the daemon enforces the same limit the tool layer intends.
func (c *Client) Restrict(ctx context.Context, readOnly bool) error {
	params := map[string]any{"scope": "own", "read_only": readOnly}
	if c.opts.PaneID != "" {
		params["pane_id"] = c.opts.PaneID
	}
	if c.opts.Proof != "" {
		params["pane_token"] = c.opts.Proof
	}
	_, err := c.Call(ctx, "restrict-connection", params)
	return err
}
