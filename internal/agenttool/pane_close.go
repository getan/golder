package agenttool

// Closing a browser pane has to do three things in this order: prove that the
// pane is the browser golder started, ask that browser to leave on its own, and
// only then remove the pane.
//
// The proof is what makes this safe. golder's own `browse` runs as a program
// called "golder", which any shell can also be, and after the browser exits the
// pane's foreground program changes anyway. A socket path alone is no better:
// the same path can serve a second browser. So close uses the exact pane id
// plus the control socket and browserId recorded when the pane was started, and
// refuses — without sending keys, a quit or a hard close — when any of them no
// longer matches. force:true is the caller's explicit "close the pane anyway",
// never something cancellation, a timeout or a failed probe turns on by itself.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/tuiosctl"
)

const browserCloseTimeout = 10 * time.Second

// browserControl is the part of the control client close needs: what the
// endpoint says it is, and whether it can quit.
type browserControl interface {
	Info(context.Context) (browserctl.Info, error)
	Quit(context.Context) error
	CloseIdleConnections()
}

type paneCloseClient interface {
	PaneID() string
	ListWindows(context.Context) (tuiosctl.WindowList, error)
	SendKeys(context.Context, string, string, int) error
	WaitFor(context.Context, tuiosctl.WaitParams) (tuiosctl.WaitResult, error)
	CloseWindow(context.Context, string) error
}

func paneCloseTimeout(ms int) time.Duration {
	if ms <= 0 {
		return browserCloseTimeout
	}
	return tuiosTimeout(ms)
}

// browserPane is what golder knows about a pane it started as a browser. The
// zero value means "golder did not start this pane", which limits close to
// closing a plain pane.
type browserPane struct {
	Root      string
	Name      string
	Managed   bool
	Embedded  bool
	WindowID  string
	Socket    string
	Program   string
	BrowserID string
}

// paneIdentity resolves the recorded identity for one exact pane id.
func paneIdentity(cwd, windowID string) browserPane {
	record, ok := managedBrowserPane(cwd, windowID)
	if !ok {
		return browserPane{}
	}
	return browserPane{
		Root: cwd, Name: record.Name, Managed: true, Embedded: record.Embedded,
		WindowID: record.WindowID, Socket: record.Socket, Program: record.Program,
		BrowserID: record.BrowserID,
	}
}

// closePane keeps normal browser teardown ahead of tuios' hard pane close.
func closePane(ctx context.Context, c paneCloseClient, win tuiosctl.Window, pane browserPane, force bool, timeout time.Duration) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if win.WindowID == "" {
		return nil, errors.New("close requires an exact window id")
	}
	if win.WindowID == c.PaneID() {
		return nil, errors.New("refusing to close the pane golder is running in")
	}
	if pane.Managed && pane.WindowID != "" && pane.WindowID != win.WindowID {
		return nil, fmt.Errorf("pane %s is not the browser pane %q golder started; nothing was closed", win.WindowID, pane.WindowID)
	}
	program := filepath.Base(strings.TrimSpace(win.ForegroundCmd))
	// Golder's own browse reports the binary's name (golder), and the record
	// keeps the exact base name used at launch so a versioned or renamed
	// install still matches. Only the daemon's foreground program counts; a
	// pane title or display name never identifies a browser here.
	embedded := program == "golder" ||
		(pane.Embedded && pane.Program != "" && program == filepath.Base(pane.Program))
	external := program == "kitty-browse" ||
		(!pane.Embedded && pane.Program != "" && program == filepath.Base(pane.Program))
	if !embedded && !external {
		// A pane that is not (or is no longer) a browser. Nothing is sent into
		// it: the daemon's foreground program decides, not the pane's title or
		// display name.
		if !pane.Managed {
			if err := c.CloseWindow(ctx, win.WindowID); err != nil {
				return nil, err
			}
			return map[string]any{"close_mode": "pane"}, nil
		}
		if !force {
			return nil, fmt.Errorf("pane %s now runs %q, not the browser golder started; nothing was sent. Inspect the pane, or use force:true", win.WindowID, win.ForegroundCmd)
		}
		return hardClose(ctx, c, win, pane, fmt.Sprintf("the pane no longer reports a browser (foreground %q)", win.ForegroundCmd))
	}
	if !pane.Managed || pane.Socket == "" || pane.BrowserID == "" || (embedded && !pane.Embedded) {
		// golder cannot prove which endpoint belongs to this pane, so a quit or
		// a keystroke could reach a different browser.
		if !force {
			return nil, fmt.Errorf("pane %s runs %s, but golder did not start it (or has no complete record of it) and cannot prove which control endpoint belongs to it; no keys and no quit were sent. Inspect the pane, or use force:true", win.WindowID, program)
		}
		return hardClose(ctx, c, win, pane, fmt.Sprintf("pane %s is an unverified %s pane", win.WindowID, program))
	}

	info, ctl, err := identifyBrowserPane(ctx, pane)
	if err != nil {
		if !force {
			return nil, err
		}
		return hardClose(ctx, c, win, pane, err.Error())
	}
	defer ctl.CloseIdleConnections()

	if timeout <= 0 || timeout > tuiosMaxTimeout {
		timeout = browserCloseTimeout
	}
	grace, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mode := "browser-quit"
	var quitErr error
	switch {
	case info.HasCapability(browserctl.CapQuit):
		quitErr = ctl.Quit(grace)
	case !embedded:
		// A positively identified external kitty-browse that predates
		// /v1/quit: its own Escape/Ctrl+Q return path is the only graceful exit
		// it has. Identity was proved above, so these keys reach that browser
		// and no other.
		mode = "browser-exit"
		quitErr = requestBrowserExit(grace, c, win.WindowID)
	default:
		// A verified endpoint that cannot quit. Never fall back to keys for a
		// process golder started: no evidence says it reads them as exit.
		if !force {
			return nil, fmt.Errorf("browser %s in pane %s does not advertise %q; no keys and no hard close were sent; retry with force:true to close the pane", info.BrowserID, win.WindowID, browserctl.CapQuit)
		}
		return hardClose(ctx, c, win, pane, fmt.Sprintf("browser %s does not advertise %q", info.BrowserID, browserctl.CapQuit))
	}
	var exited tuiosctl.WaitResult
	if quitErr == nil {
		exited, quitErr = c.WaitFor(grace, tuiosctl.WaitParams{
			Condition: "window-exit", Window: win.WindowID, Timeout: timeout,
		})
		if quitErr == nil && !exited.Matched {
			quitErr = errors.New("browser exit was not confirmed")
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err() // cancellation is never permission to hard-kill
	}
	if quitErr != nil {
		// CloseOnExit may remove the pane before quit/wait-for resolves it.
		var derr *tuiosctl.Error
		if errors.As(quitErr, &derr) && derr.Code == "window_not_found" {
			present, probeErr := paneStillPresent(ctx, c, win.WindowID)
			if probeErr != nil {
				return nil, probeErr
			}
			if !present {
				forgetBrowserPane(pane.Root, win.WindowID)
				return map[string]any{"close_mode": "already-closed", "browser_id": info.BrowserID}, nil
			}
		}
		if !force {
			return nil, fmt.Errorf("%s exit was not confirmed (budget %s): %w; no hard close was sent; retry or explicitly use force:true", program, timeout, quitErr)
		}
		return hardClose(ctx, c, win, pane, quitErr.Error())
	}

	// A successful window-exit wait observes process exit, not screen silence or
	// socket disappearance. Close only the remaining pane shell; CloseOnExit
	// panes have already gone. Do not interpret a failed listing as absence.
	present, err := paneStillPresent(ctx, c, win.WindowID)
	if err != nil {
		return nil, err
	}
	if present {
		if err := c.CloseWindow(ctx, win.WindowID); err != nil {
			var derr *tuiosctl.Error
			if !errors.As(err, &derr) || derr.Code != "window_not_found" {
				return nil, err
			}
		}
	}
	forgetBrowserPane(pane.Root, win.WindowID)
	out := map[string]any{"close_mode": mode, "browser_id": info.BrowserID}
	if exited.ExitCode != nil {
		out["exit_code"] = *exited.ExitCode
	}
	return out, nil
}

// identifyBrowserPane proves that the endpoint recorded for the pane still
// serves the browser golder started there. Everything close does afterwards
// depends on it, so any mismatch is an error rather than a warning.
func identifyBrowserPane(ctx context.Context, pane browserPane) (browserctl.Info, browserControl, error) {
	ctl, err := browserctl.Dial(pane.Socket)
	if err != nil {
		return browserctl.Info{}, nil, fmt.Errorf("pane %s: recorded control socket %s: %w; no keys and no quit were sent", pane.WindowID, pane.Socket, err)
	}
	probe, cancel := context.WithTimeout(ctx, browserProbeTimeout)
	defer cancel()
	info, err := ctl.Info(probe)
	if err != nil {
		ctl.CloseIdleConnections()
		return browserctl.Info{}, nil, fmt.Errorf("browser in pane %s cannot be verified: %w; no keys and no quit were sent", pane.WindowID, err)
	}
	if info.APIVersion != browserctl.APIVersion || info.BrowserID == "" {
		ctl.CloseIdleConnections()
		return browserctl.Info{}, nil, fmt.Errorf("control socket %s for pane %s is not a compatible browser; no keys and no quit were sent", pane.Socket, pane.WindowID)
	}
	if info.BrowserID != pane.BrowserID {
		ctl.CloseIdleConnections()
		return browserctl.Info{}, nil, fmt.Errorf("control socket %s now serves browser %q, not the %q golder started in pane %s; no keys and no quit were sent", pane.Socket, info.BrowserID, pane.BrowserID, pane.WindowID)
	}
	return info, ctl, nil
}

// hardClose is the explicit force path: it removes the pane and says what was
// not confirmed. It never sends keys or a quit, and only runs when the caller
// asked for force.
func hardClose(ctx context.Context, c paneCloseClient, win tuiosctl.Window, pane browserPane, warning string) (map[string]any, error) {
	if err := c.CloseWindow(ctx, win.WindowID); err != nil {
		return nil, err
	}
	forgetBrowserPane(pane.Root, win.WindowID)
	return map[string]any{"close_mode": "forced", "warning": warning}, nil
}

// requestBrowserExit uses an older external kitty-browse's normal return path,
// which runs its browser/socket defers. It is only reachable after identify
// proved the endpoint, and only for a browser that does not advertise /v1/quit.
// Six ESC bytes yield at least three Escape events even when its key reader
// coalesces pairs. Ctrl+Q is a separate write after its 15ms Escape ambiguity
// window: concatenating ESC and Ctrl+Q can swallow the quit key as Alt input.
// This is input pacing, not polling for exit. No shell command, process-name
// kill or machine-wide browser action is used.
func requestBrowserExit(ctx context.Context, c paneCloseClient, window string) error {
	if err := c.SendKeys(ctx, window, "Escape", 6); err != nil {
		return err
	}
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return c.SendKeys(ctx, window, "ctrl+q", 0)
}

func paneStillPresent(ctx context.Context, c paneCloseClient, id string) (bool, error) {
	windows, err := c.ListWindows(ctx)
	if err != nil {
		return false, err
	}
	for _, win := range windows.Windows {
		if win.WindowID == id {
			return true, nil
		}
	}
	return false, nil
}
