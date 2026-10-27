// Package browserctl talks to a running browser over its owner-only
// Unix-domain control socket. It is the typed client the browser tool uses:
// /v1/info for discovery, /v1/actions for one action, /v1/screenshot for a
// picture, and /v1/quit for a graceful shutdown.
//
// The socket file's mode is the access control. A socket another user can
// write is refused rather than used, because an action puts input into a real
// browser session.
package browserctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// EnvSocket is the control socket path a kitty-browse pane exposes.
	EnvSocket = "KITTY_BROWSE_SOCKET"
	// APIVersion is the only contract this client speaks.
	APIVersion = "v1"
	// CapQuit is the advertised capability for /v1/quit. A browser that does
	// not advertise it predates the endpoint; asking anyway would be a guess.
	CapQuit = "quit"
	// maxBody bounds one JSON reply or screenshot. The browser tool's own
	// per-action caps (screenshot pixels, snapshot result size) are stricter;
	// this is the transport-level ceiling that keeps a bad daemon from making the
	// agent retain an unbounded response.
	maxBody = 8 << 20
)

// SocketFromEnv returns the configured control socket, or "".
func SocketFromEnv() string {
	return strings.TrimSpace(os.Getenv(EnvSocket))
}

// Client is one browser's control socket.
type Client struct {
	socket string
	http   *http.Client
}

// CloseIdleConnections releases pooled connections after a discovery probe.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

// Dial validates the socket and returns a client. It does not call the
// browser: a caller that wants to prove it is alive calls Info.
func Dial(socket string) (*Client, error) {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return nil, fmt.Errorf("no %s is set; start kitty-browse with -control-socket", EnvSocket)
	}
	if err := checkSocket(socket); err != nil {
		return nil, err
	}
	return NewWithTransport(socket, nil), nil
}

// NewWithTransport builds a client for an already-trusted connection: a nil
// transport dials the Unix socket, and a test or embedder may supply its own.
// It skips the socket-permission check, which only Dial can make.
func NewWithTransport(socket string, rt http.RoundTripper) *Client {
	if rt == nil {
		rt = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(ctx, "unix", socket)
			},
		}
	}
	return &Client{socket: socket, http: &http.Client{Transport: rt, Timeout: 130 * time.Second}}
}

// checkSocket refuses a path that is not an owner-only Unix socket in a
// directory other users cannot write. The browser accepts typed input, so the
// socket's permissions are the whole authorization.
func checkSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("control socket %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a Unix socket", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("control socket %s is accessible by other users (%04o); it must be owner-only", path, perm)
	}
	dir := filepath.Dir(path)
	if di, err := os.Stat(dir); err == nil {
		// A sticky directory such as /tmp is acceptable only because the socket
		// itself is owner-only; a plain world-writable one lets someone swap it.
		if di.Mode().Perm()&0o002 != 0 && di.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("control socket directory %s is world-writable", dir)
		}
	}
	return nil
}

// Tab is one browser tab with its stable ids.
type Tab struct {
	ID       string `json:"id"`
	TargetID string `json:"targetId"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Index    int    `json:"index"`
	Active   bool   `json:"active"`
}

// Capability is one action the browser implements, with its input schema.
type Capability struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	ReadOnly    bool            `json:"readOnly"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Info is /v1/info.
type Info struct {
	APIVersion   string       `json:"apiVersion"`
	BrowserID    string       `json:"browserId"`
	ActiveTabID  string       `json:"activeTabId"`
	Backend      string       `json:"backend"`
	Tabs         []Tab        `json:"tabs"`
	Capabilities []Capability `json:"capabilities"`
}

// HasCapability reports whether the browser advertises one action. Callers use
// it to decide whether an endpoint can do something at all, instead of sending
// a request and interpreting "unknown_action".
func (info Info) HasCapability(name string) bool {
	for _, capability := range info.Capabilities {
		if capability.Name == name {
			return true
		}
	}
	return false
}

// Target pins one tab. The browser refuses an action whose ids do not match.
type Target struct {
	BrowserID string `json:"browserId,omitempty"`
	TabID     string `json:"tabId,omitempty"`
	TargetID  string `json:"targetId,omitempty"`
}

// Meta is what the browser reports about where the action ran.
type Meta struct {
	BrowserID string `json:"browserId"`
	TabID     string `json:"tabId"`
	TargetID  string `json:"targetId"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Backend   string `json:"backend"`
}

// Error is the action-level failure. OK false with a code is the branch to
// read; HTTP status alone is not enough.
type Error struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Retryable bool            `json:"retryable"`
	Details   json.RawMessage `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "browser: unknown error"
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type actionRequest struct {
	APIVersion string  `json:"apiVersion,omitempty"`
	ID         string  `json:"id"`
	Action     string  `json:"action"`
	Params     any     `json:"params,omitempty"`
	Target     *Target `json:"target,omitempty"`
	TimeoutMS  int     `json:"timeoutMs,omitempty"`
}

type actionResponse struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
	Meta   Meta            `json:"meta"`
}

// QuitResult is /v1/quit's result: the browser accepted the request and is
// unwinding. It is not a promise that the process has exited yet, which is why
// a close waits for window-exit as well.
type QuitResult struct {
	Quitting bool `json:"quitting"`
}

// The reply envelope is shared by every endpoint: apiVersion, the echoed id
// (null when the request carried none), ok, and either result or error.
type quitResponse struct {
	APIVersion string          `json:"apiVersion"`
	ID         json.RawMessage `json:"id"`
	OK         bool            `json:"ok"`
	Result     QuitResult      `json:"result"`
	Error      *Error          `json:"error"`
}

// Info discovers the browser's identity, tabs and capabilities.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	if err := c.get(ctx, "/v1/info", &out); err != nil {
		return Info{}, err
	}
	if out.APIVersion != APIVersion {
		return Info{}, fmt.Errorf("browser speaks apiVersion %q, want %q", out.APIVersion, APIVersion)
	}
	if out.Backend != "native-cdp" {
		return Info{}, fmt.Errorf("browser backend is %q, want native-cdp", out.Backend)
	}
	return out, nil
}

// Quit asks the browser to shut down normally: it flushes its profile, closes
// the page and removes its own control socket. It must only be called on an
// endpoint the caller has already identified (see the pane close path), because
// a quit aimed at the wrong browser is as destructive as a stray click.
func (c *Client) Quit(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{
		"apiVersion": APIVersion,
		"id":         fmt.Sprintf("golder-quit-%d", time.Now().UnixNano()),
	})
	if err != nil {
		return fmt.Errorf("encode quit: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/quit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("browser: quit: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("browser: quit: read reply: %w", err)
	}
	var qr quitResponse
	if err := json.Unmarshal(raw, &qr); err != nil {
		return fmt.Errorf("browser: quit: parse reply: %w (status %d)", err, resp.StatusCode)
	}
	if qr.APIVersion != APIVersion {
		return fmt.Errorf("browser: quit: reply speaks apiVersion %q, want %q", qr.APIVersion, APIVersion)
	}
	if !qr.OK {
		if qr.Error != nil {
			return qr.Error
		}
		return fmt.Errorf("browser: quit failed (status %d)", resp.StatusCode)
	}
	if !qr.Result.Quitting {
		return fmt.Errorf("browser: quit answered ok without reporting quitting (status %d)", resp.StatusCode)
	}
	return nil
}

// Action runs one typed action against one target tab.
func (c *Client) Action(ctx context.Context, action string, params any, target Target, timeout time.Duration) (json.RawMessage, Meta, error) {
	ms := int(timeout / time.Millisecond)
	if ms <= 0 {
		ms = 30000
	}
	if ms > 120000 {
		ms = 120000
	}
	body, err := json.Marshal(actionRequest{
		APIVersion: APIVersion,
		ID:         fmt.Sprintf("golder-%d", time.Now().UnixNano()),
		Action:     action,
		Params:     params,
		Target:     &target,
		TimeoutMS:  ms,
	})
	if err != nil {
		return nil, Meta{}, fmt.Errorf("encode action: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/actions", bytes.NewReader(body))
	if err != nil {
		return nil, Meta{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Meta{}, fmt.Errorf("browser: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, Meta{}, fmt.Errorf("browser: read reply: %w", err)
	}
	var ar actionResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, Meta{}, fmt.Errorf("browser: parse reply: %w (status %d)", err, resp.StatusCode)
	}
	if !ar.OK {
		if ar.Error != nil {
			return nil, ar.Meta, ar.Error
		}
		return nil, ar.Meta, fmt.Errorf("browser: action failed (status %d)", resp.StatusCode)
	}
	return ar.Result, ar.Meta, nil
}

// Screenshot returns the active tab's viewport as PNG.
func (c *Client) Screenshot(ctx context.Context, fullPage bool) ([]byte, error) {
	path := "/v1/screenshot"
	if fullPage {
		path += "?fullPage=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("browser: screenshot status %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("browser: screenshot exceeds %d-byte limit", maxBody)
	}
	return raw, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("browser: read reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("browser: %s status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// IsConnectionRefused reports whether err means the browser is gone, so a tool
// can say that instead of printing a socket error.
func IsConnectionRefused(err error) bool {
	return err != nil && (errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "connect: connection refused") ||
		strings.Contains(err.Error(), "no such file or directory"))
}
