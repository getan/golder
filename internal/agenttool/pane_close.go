package agenttool

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/getan/golder/internal/tuiosctl"
)

const browserCloseTimeout = 10 * time.Second

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

// closePane keeps normal browser teardown ahead of tuios' hard pane close.
// Only the daemon's foreground program or a workspace browser we started is
// used to identify kitty-browse: a title, selected socket or focused pane is
// not evidence that this is the browser the caller meant to close.
func closePane(ctx context.Context, c paneCloseClient, win tuiosctl.Window, browserHint, force bool, timeout time.Duration) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if win.WindowID == "" {
		return nil, errors.New("close requires an exact window id")
	}
	if win.WindowID == c.PaneID() {
		return nil, errors.New("refusing to close the pane golder is running in")
	}
	browser := filepath.Base(win.ForegroundCmd) == "kitty-browse"
	if browserHint && win.ForegroundCmd == "" {
		browser = true
	}
	if browserHint && !browser && !force {
		return nil, fmt.Errorf("pane %s now runs %q, not kitty-browse; inspect it before closing, or use force:true", win.WindowID, win.ForegroundCmd)
	}
	if !browser {
		if err := c.CloseWindow(ctx, win.WindowID); err != nil {
			return nil, err
		}
		if browserHint {
			return map[string]any{"close_mode": "forced", "warning": "the pane no longer reports kitty-browse"}, nil
		}
		return map[string]any{"close_mode": "pane"}, nil
	}

	if timeout <= 0 || timeout > tuiosMaxTimeout {
		timeout = browserCloseTimeout
	}
	grace, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := requestBrowserExit(grace, c, win.WindowID)
	var exited tuiosctl.WaitResult
	if err == nil {
		exited, err = c.WaitFor(grace, tuiosctl.WaitParams{
			Condition: "window-exit", Window: win.WindowID, Timeout: timeout,
		})
		if err == nil && !exited.Matched {
			err = errors.New("browser exit was not confirmed")
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err() // cancellation is never permission to hard-kill
	}
	if err != nil {
		// CloseOnExit may remove the pane before send-keys/wait-for resolves it.
		var derr *tuiosctl.Error
		if errors.As(err, &derr) && derr.Code == "window_not_found" {
			present, probeErr := paneStillPresent(ctx, c, win.WindowID)
			if probeErr != nil {
				return nil, probeErr
			}
			if !present {
				return map[string]any{"close_mode": "already-closed"}, nil
			}
		}
		if !force {
			return nil, fmt.Errorf("kitty-browse exit was not confirmed (budget %s): %w; no hard close was sent; retry or explicitly use force:true", timeout, err)
		}
		if closeErr := c.CloseWindow(ctx, win.WindowID); closeErr != nil {
			return nil, closeErr
		}
		return map[string]any{"close_mode": "forced", "warning": err.Error()}, nil
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
	out := map[string]any{"close_mode": "browser-exit"}
	if exited.ExitCode != nil {
		out["exit_code"] = *exited.ExitCode
	}
	return out, nil
}

// requestBrowserExit uses kitty-browse's normal return path, which runs its
// browser/socket defers. The current control API has no browser-quit action.
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
