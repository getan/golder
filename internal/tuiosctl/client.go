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
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
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
	return o.Socket != "" && o.Session != ""
}

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
// protocol processes one request per connection at a time. A call that times
// out closes the connection so the next call starts from a clean hello.
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
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultCallTTL)
	}
	_ = c.conn.SetDeadline(deadline)
	defer c.conn.SetDeadline(time.Time{})

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
	if _, err := c.conn.Write(b); err != nil {
		_ = c.closeLocked()
		return nil, fmt.Errorf("tuios: write %s: %w", verb, err)
	}

	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		_ = c.closeLocked()
		if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("tuios: %s timed out", verb)
		}
		return nil, fmt.Errorf("tuios: read %s: %w", verb, err)
	}
	if len(line) > maxLineBytes {
		_ = c.closeLocked()
		return nil, fmt.Errorf("tuios: %s response exceeds %d bytes", verb, maxLineBytes)
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
