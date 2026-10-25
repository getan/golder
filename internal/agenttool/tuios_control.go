package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func (t *TuiosTool) PrepareCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if t.Control == nil {
		return nil, errors.New("terminal control policy is not configured")
	}
	var a tuiosArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Action == "new" {
		cwd, err := t.Control.cwd(a.Cwd)
		if err != nil {
			return nil, err
		}
		a.Cwd = cwd
	} else if tuiosIsControl(a.Action) && a.Action != "screenshot" {
		c, err := t.newClient(ctx)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		list, err := c.ListWindows(ctx)
		if err != nil {
			return nil, err
		}
		want := a.Window
		if want == "" {
			want = list.FocusedWindowID
		}
		var ids []string
		for _, w := range list.Windows {
			if w.WindowID == want || w.DisplayName == want {
				ids = append(ids, w.WindowID)
			}
		}
		if len(ids) != 1 {
			return nil, fmt.Errorf("pane target %q is missing or ambiguous; use an exact id", want)
		}
		a.Window = ids[0]
		if p, ok := t.Control.pane(a.Window); ok {
			a.Cwd = p.cwd
			if p.network {
				a.Network = "on"
			}
		}
	}
	return json.Marshal(a)
}

func (t *TuiosTool) checkControl(ctx context.Context, a *tuiosArgs) error {
	if !tuiosIsControl(a.Action) {
		return nil
	}
	if t.Control == nil {
		return errors.New("terminal control policy is not configured")
	}
	if t.Control.Mode != nil && t.Control.Mode.Mode() == permissions.ReadOnly {
		return errors.New("read-only mode cannot control panes")
	}
	if a.Action == "screenshot" {
		if agentcore.SandboxRequestedFromContext(ctx) {
			return errors.New("the screenshot writer cannot satisfy required OS containment")
		}
		return nil
	}
	if a.Action == "new" {
		cwd, err := t.Control.cwd(a.Cwd)
		if err != nil {
			return err
		}
		a.Cwd = cwd
		return nil
	}
	c, err := t.newClient(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	id, err := resolveWindow(ctx, c, a.Window)
	if err != nil {
		return err
	}
	win, err := exactWindow(ctx, c, id)
	if err != nil {
		return err
	}
	input := a.Action == "run" || a.Action == "send" || a.Action == "press"
	if a.Action == "close" && win.WindowID == c.PaneID() {
		return errors.New("refusing to close the pane golder is running in")
	}
	if err := t.Control.checkPane(ctx, win, c.PaneID(), a.HostControl, input); err != nil {
		return err
	}
	a.Window = win.WindowID
	if p, ok := t.Control.pane(a.Window); ok && a.Action == "run" {
		// A command is reviewed relative to this fixed cwd, not whatever a
		// previous input (or the person) left in the interactive shell.
		a.Command = "cd " + quoteControlArg(p.cwd) + " && " + a.Command
	}
	return nil
}

func quoteControlArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
