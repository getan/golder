package tuiosctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Window is the compact pane description the tools expose. The daemon sends
// more fields; only what a caller can act on is kept.
type Window struct {
	WindowID      string `json:"window_id"`
	Title         string `json:"title"`
	DisplayName   string `json:"display_name"`
	Workspace     int    `json:"workspace"`
	Focused       bool   `json:"focused"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	ForegroundCmd string `json:"foreground_cmd"`
	AgentState    string `json:"agent_state"`
	HistoryRows   int    `json:"history_rows"`
	Revision      uint64 `json:"revision"`
	// Own marks the pane running this golder process, when it is known. It is
	// computed by the tool, not sent by the daemon.
	Own bool `json:"own,omitempty"`
}

// WindowList is one session's panes across every workspace.
type WindowList struct {
	Type             string   `json:"type"`
	Total            int      `json:"total"`
	FocusedWindowID  string   `json:"focused_window_id"`
	CurrentWorkspace int      `json:"current_workspace"`
	Windows          []Window `json:"windows"`
}

// ListWindows returns every pane in the caller's session.
func (c *Client) ListWindows(ctx context.Context) (WindowList, error) {
	var out WindowList
	err := c.CallInto(ctx, "list-windows", map[string]any{"session": c.opts.Session}, &out)
	return out, err
}

// GetWindow returns one pane by exact id or name. It is not used to validate a
// destructive target: an attached TUI may omit window_id from its response, so
// callers use the authoritative ListWindows enumeration for that.
func (c *Client) GetWindow(ctx context.Context, window string) (Window, error) {
	var out Window
	err := c.CallInto(ctx, "get-window", map[string]any{"session": c.opts.Session, "window": window}, &out)
	return out, err
}

// NewWindow creates a pane. command is an argv, never a shell string, so a
// caller cannot smuggle shell syntax through this tool.
func (c *Client) NewWindow(ctx context.Context, p NewWindowParams) (NewWindowResult, error) {
	params := map[string]any{"session": c.opts.Session}
	if p.Name != "" {
		params["name"] = p.Name
	}
	if p.Cwd != "" {
		params["cwd"] = p.Cwd
	}
	if p.Workspace > 0 {
		params["workspace"] = p.Workspace
	}
	if len(p.Command) > 0 {
		params["command"] = p.Command
	}
	if len(p.Grants) > 0 {
		params["grants"] = p.Grants
	}
	params["focus"] = p.Focus
	// close_on_exit is newer than some daemons. Sending false unconditionally
	// makes an older daemon reject the whole new-window call as an unknown
	// parameter, so only send it when it changes the default.
	if p.CloseOnExit {
		params["close_on_exit"] = true
	}
	if p.KeepOpen {
		params["keep_open"] = true
	}
	var out NewWindowResult
	err := c.CallInto(ctx, "new-window", params, &out)
	// An older daemon rejects close_on_exit as an unknown parameter even when
	// true. The pane still exists without it; retry once without the field
	// rather than failing the whole creation.
	if err != nil && p.CloseOnExit && missingParam(err, "close_on_exit") {
		delete(params, "close_on_exit")
		err = c.CallInto(ctx, "new-window", params, &out)
	}
	return out, err
}

// NewWindowInCallerWorkspace creates a helper pane beside the agent's pane,
// independent of whichever workspace the person is currently looking at.
// Never fall back to current_workspace or focused_window_id: doing so recreates
// the focus race this method exists to avoid. An explicitly moved agent pane
// is followed on the next creation because we resolve its location afresh.
func (c *Client) NewWindowInCallerWorkspace(ctx context.Context, p NewWindowParams) (NewWindowResult, error) {
	if c.opts.PaneID == "" {
		return NewWindowResult{}, errors.New("cannot locate caller workspace: no agent pane id")
	}
	list, err := c.ListWindows(ctx)
	if err != nil {
		return NewWindowResult{}, fmt.Errorf("locate agent pane workspace: %w", err)
	}
	for _, window := range list.Windows {
		if window.WindowID == c.opts.PaneID && window.Workspace > 0 {
			p.Workspace = window.Workspace
			p.Focus = false // opening a helper must not steal focus back
			return c.NewWindow(ctx, p)
		}
	}
	return NewWindowResult{}, fmt.Errorf("agent pane %q has no known workspace; refusing to use the focused workspace", c.opts.PaneID)
}

func missingParam(err error, param string) bool {
	var de *Error
	return errors.As(err, &de) && de.Code == "invalid_params" &&
		strings.Contains(de.Message, param)
}

type NewWindowParams struct {
	Name      string
	Cwd       string
	Workspace int
	Command   []string
	// Broker-created helpers receive none, so raw daemon calls made by a
	// shell/browser cannot escape the agent's tool authorization path.
	Grants      []string
	Focus       bool
	CloseOnExit bool
	KeepOpen    bool
}

type NewWindowResult struct {
	Type     string `json:"type"`
	WindowID string `json:"window_id"`
	Name     string `json:"name"`
}

// MoveResult reports where a pane went.
type MoveResult struct {
	Type             string `json:"type"`
	WindowID         string `json:"window_id"`
	FromWorkspace    int    `json:"from_workspace"`
	Workspace        int    `json:"workspace"`
	CurrentWorkspace int    `json:"current_workspace"`
}

// MoveWindow moves an existing pane to another workspace.
func (c *Client) MoveWindow(ctx context.Context, window string, workspace int, follow bool) (MoveResult, error) {
	params := map[string]any{"session": c.opts.Session, "window": window, "workspace": workspace}
	if follow {
		params["follow"] = true
	}
	var out MoveResult
	err := c.CallInto(ctx, "move-window", params, &out)
	return out, err
}

// ResizeResult is the PTY size after a resize.
type ResizeResult struct {
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ResizeWindow changes one pane's PTY size.
func (c *Client) ResizeWindow(ctx context.Context, window string, width, height int) (ResizeResult, error) {
	params := map[string]any{
		"session": c.opts.Session, "width": width, "height": height,
	}
	if window != "" {
		params["window"] = window
	}
	var out ResizeResult
	err := c.CallInto(ctx, "resize", params, &out)
	return out, err
}

// RenameResult is the pane's name after set-window.
type RenameResult struct {
	Type        string `json:"type"`
	WindowID    string `json:"window_id"`
	DisplayName string `json:"display_name"`
	Minimized   bool   `json:"minimized"`
}

// RenameWindow changes a pane's display name.
func (c *Client) RenameWindow(ctx context.Context, window, name string) (RenameResult, error) {
	params := map[string]any{"session": c.opts.Session, "name": name}
	if window != "" {
		params["window"] = window
	}
	var out RenameResult
	err := c.CallInto(ctx, "set-window", params, &out)
	return out, err
}

// SetMinimized minimizes or restores a pane without closing its process.
func (c *Client) SetMinimized(ctx context.Context, window string, minimized bool) (RenameResult, error) {
	params := map[string]any{"session": c.opts.Session, "minimized": minimized}
	if window != "" {
		params["window"] = window
	}
	var out RenameResult
	err := c.CallInto(ctx, "set-window", params, &out)
	return out, err
}

// FocusWindow moves focus to an exact window.
func (c *Client) FocusWindow(ctx context.Context, window string) (string, error) {
	var out struct {
		FocusedWindowID string `json:"focused_window_id"`
	}
	err := c.CallInto(ctx, "focus-window", map[string]any{"session": c.opts.Session, "window": window}, &out)
	if err != nil {
		return "", err
	}
	return out.FocusedWindowID, nil
}

// CloseWindow closes an exact window. The tool layer guards the caller's own
// pane; this method itself only forwards the id.
func (c *Client) CloseWindow(ctx context.Context, window string) error {
	_, err := c.Call(ctx, "close-window", map[string]any{"session": c.opts.Session, "window": window})
	return err
}

// SendText writes literal text to a pane. submit adds the Enter path the
// daemon uses for a harness prompt; a plain shell command should include a
// trailing newline instead.
//
// A daemon older than 0.8.3 has no submit parameter. The text is sent without
// it and Enter is pressed with send-keys; the returned note says so.
func (c *Client) SendText(ctx context.Context, window, text string, submit bool) (string, error) {
	return c.sendText(ctx, window, text, submit, false)
}

// PasteText sends text as a paste, so a multi-line payload is not executed
// line by line by the receiving shell. submit presses Enter after the paste.
func (c *Client) PasteText(ctx context.Context, window, text string, submit bool) (string, error) {
	return c.sendText(ctx, window, text, submit, true)
}

func (c *Client) sendText(ctx context.Context, window, text string, submit, paste bool) (string, error) {
	params := map[string]any{"session": c.opts.Session, "text": text}
	if window != "" {
		params["window"] = window
	}
	if submit {
		params["submit"] = true
	}
	if paste {
		params["paste"] = true
	}
	_, err := c.Call(ctx, "send-text", params)
	note := ""
	needEnter := false
	if err != nil && submit && missingParam(err, "submit") {
		delete(params, "submit")
		needEnter = true
		note = "the daemon predates send-text submit; sent the text and pressed Enter separately"
		_, err = c.Call(ctx, "send-text", params)
	}
	if err != nil && paste && missingParam(err, "paste") {
		delete(params, "paste")
		note = "the daemon does not support bracketed paste; sent the text without it"
		_, err = c.Call(ctx, "send-text", params)
	}
	if err != nil {
		return "", err
	}
	if needEnter {
		if err := c.SendKeys(ctx, window, "Enter", 0); err != nil {
			return "", fmt.Errorf("text was sent, but Enter failed: %w", err)
		}
	}
	return note, nil
}

// SendKeys sends named keys to a pane, such as Enter, Tab or ctrl+c.
func (c *Client) SendKeys(ctx context.Context, window, keys string, repeat int) error {
	params := map[string]any{"session": c.opts.Session, "keys": keys}
	if window != "" {
		params["window"] = window
	}
	if repeat > 1 {
		params["repeat"] = repeat
	}
	_, err := c.Call(ctx, "send-keys", params)
	return err
}

// ScreenshotResult is where the daemon wrote a pane picture.
type ScreenshotResult struct {
	Path     string   `json:"path"`
	Host     string   `json:"host"`
	Format   string   `json:"format"`
	Cols     int      `json:"cols"`
	Rows     int      `json:"rows"`
	Bytes    int      `json:"bytes"`
	Warnings []string `json:"warnings"`
}

// ScreenshotParams asks the daemon to render a pane to a file.
type ScreenshotParams struct {
	Window     string
	Format     string // png, svg, ansi, html, txt
	Frame      string // window, plain, none
	Theme      string
	Out        string
	Scrollback bool
	Lines      int
	Cursor     bool
}

// Screenshot renders one pane to a file on the daemon's machine and returns
// its path. An older daemon without the verb fails with invalid_verb, which the
// caller surfaces as "tuios is too old for screenshots".
func (c *Client) Screenshot(ctx context.Context, p ScreenshotParams) (ScreenshotResult, error) {
	params := map[string]any{"session": c.opts.Session}
	if p.Window != "" {
		params["window"] = p.Window
	}
	if p.Format != "" {
		params["format"] = p.Format
	}
	if p.Frame != "" {
		params["frame"] = p.Frame
	}
	if p.Theme != "" {
		params["theme"] = p.Theme
	}
	if p.Out != "" {
		params["out"] = p.Out
	}
	if p.Scrollback {
		params["scrollback"] = true
	}
	if p.Lines > 0 {
		params["lines"] = p.Lines
	}
	if p.Cursor {
		params["cursor"] = true
	}
	var out ScreenshotResult
	err := c.CallInto(ctx, "screenshot", params, &out)
	return out, err
}

// RunResult is one shell command's answer. Output is plain text from the
// pane's OSC 133 command region.
type RunResult struct {
	Type       string `json:"type"`
	Session    string `json:"session"`
	Window     string `json:"window"`
	Cmdline    string `json:"cmdline"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	CommandSeq uint64 `json:"command_seq"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated"`
}

// Run types one command at a shell prompt, waits for its OSC 133 finish mark,
// and returns its output. It is the no-poll path for "run and give me the
// result".
func (c *Client) Run(ctx context.Context, window, command string, timeout time.Duration, lines int) (RunResult, error) {
	params := map[string]any{"session": c.opts.Session, "command": command}
	if window != "" {
		params["window"] = window
	}
	if timeout > 0 {
		params["timeout"] = timeout.Milliseconds()
	}
	if lines > 0 {
		params["lines"] = lines
	}
	var out RunResult
	callCtx, cancel := withCallTimeout(ctx, timeout)
	defer cancel()
	err := c.CallInto(callCtx, "run", params, &out)
	return out, err
}

// Capture is a pane snapshot. It is never used as a polling loop; callers use
// WaitFor for that.
type Capture struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Content     string `json:"content"`
	HistoryRows int    `json:"history_rows"`
	Revision    uint64 `json:"revision"`
	BootID      string `json:"boot_id"`
}

// CapturePane takes one snapshot of a pane's visible content or recent
// scrollback.
func (c *Client) CapturePane(ctx context.Context, window, source string, lines int, styled bool) (Capture, error) {
	params := map[string]any{"session": c.opts.Session}
	if window != "" {
		params["window"] = window
	}
	if source != "" {
		params["source"] = source
	}
	if lines > 0 {
		params["lines"] = lines
	}
	if styled {
		params["styled"] = true
	}
	var out Capture
	err := c.CallInto(ctx, "capture-pane", params, &out)
	return out, err
}

// WaitResult is the successful answer to wait-for.
type WaitResult struct {
	Type       string `json:"type"`
	Condition  string `json:"condition"`
	Matched    bool   `json:"matched"`
	Window     string `json:"window"`
	Pattern    string `json:"pattern"`
	State      string `json:"state"`
	Cmdline    string `json:"cmdline"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	CommandSeq uint64 `json:"command_seq"`
}

// WaitParams selects one wait-for condition. It is deliberately a struct, not
// a verb+params map: the tool schema and the daemon params stay one shape.
type WaitParams struct {
	Condition  string
	Window     string
	Pattern    string
	Idle       time.Duration
	Until      []string
	CommandSeq uint64
	Timeout    time.Duration
}

// WaitFor blocks on the daemon's event hub and returns as soon as the
// condition matches. A daemon timeout is returned as a *Error with code
// "timeout"; the tool turns that into a structured result for the model.
func (c *Client) WaitFor(ctx context.Context, p WaitParams) (WaitResult, error) {
	if p.Condition == "" {
		return WaitResult{}, errors.New("tuios: wait condition is required")
	}
	params := map[string]any{"session": c.opts.Session, "condition": p.Condition}
	if p.Window != "" {
		params["window"] = p.Window
	}
	if p.Pattern != "" {
		params["pattern"] = p.Pattern
	}
	if p.Idle > 0 {
		params["idle"] = p.Idle.Milliseconds()
	}
	if len(p.Until) > 0 {
		params["until"] = strings.Join(p.Until, ",")
	}
	if p.CommandSeq > 0 {
		params["command_seq"] = p.CommandSeq
	}
	if p.Timeout > 0 {
		params["timeout"] = p.Timeout.Milliseconds()
	}
	var out WaitResult
	callCtx, cancel := withCallTimeout(ctx, p.Timeout)
	defer cancel()
	err := c.CallInto(callCtx, "wait-for", params, &out)
	return out, err
}

func withCallTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	// A little slack lets the daemon's own timeout error arrive before the
	// transport deadline, so the caller can distinguish timeout from a broken
	// connection.
	return context.WithTimeout(ctx, timeout+5*time.Second)
}

// Decode is for tests that need a typed result from a raw fixture.
func Decode[T any](raw json.RawMessage) (T, error) {
	var out T
	err := json.Unmarshal(raw, &out)
	if err != nil {
		return out, fmt.Errorf("tuios: decode result: %w", err)
	}
	return out, nil
}
