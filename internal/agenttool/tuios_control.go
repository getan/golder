package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/tuiosctl"
)

func (t *TuiosTool) PrepareCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if t.Control == nil {
		return nil, errors.New("terminal control policy is not configured")
	}
	var a tuiosArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	// These are execution facts derived from the selected pane, never authority
	// or identity supplied by model arguments.
	a.TargetIdentity, a.ControlOrigin, a.ControlScope, a.CurrentController = "", "", "", ""
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
		t.Control.touchAgent(c, list.Windows)
		t.Control.reapExternal(list.Windows)
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
		var target tuiosctl.Window
		for _, win := range list.Windows {
			if win.WindowID == a.Window {
				target = win
				break
			}
		}
		if a.Action == "claim" {
			if strings.TrimSpace(want) != target.WindowID {
				return nil, errors.New("claim requires the exact full window id from tuios list")
			}
			if a.Scope != "one-call" && a.Scope != "session" {
				return nil, errors.New("claim requires scope=one-call or scope=session")
			}
			if !a.HostControl {
				return nil, errors.New("claim requires host_control:true and exact operator confirmation")
			}
			if err := t.Control.claimRefusal(target, c.PaneID()); err != nil {
				return nil, err
			}
			if store, agent := t.Control.sharedLeaseState(); store != nil {
				if current, ok, err := store.Get(c.Session(), target.WindowID); err != nil {
					return nil, err
				} else if ok && current.AgentID != agent.ID {
					if !a.Transfer {
						return nil, fmt.Errorf("pane is controlled by %s; retry claim with transfer=true to ask for an explicit transfer", current.AgentLabel)
					}
					a.CurrentController = current.AgentLabel
				}
			}
			a.TargetIdentity, err = t.Control.claimTargetIdentity(c, target)
			if err != nil {
				return nil, err
			}
			a.TargetLabel, a.HostHint = target.DisplayName, target.CwdHost
			return json.Marshal(a)
		}
		if a.Action == "revoke" {
			if a.HostControl {
				return nil, errors.New("revoke does not use host_control")
			}
			return json.Marshal(a)
		}
		state := t.Control.controlState(target, c.PaneID(), c.Session(), c.Socket())
		if state.controlled {
			a.ControlOrigin, a.ControlScope = state.origin, state.scope
		}
		remote := a.Remote
		for _, win := range list.Windows {
			if win.WindowID == a.Window {
				remote = remote || tuiosRemoteHint(win)
			}
		}
		if remote {
			a.Cwd = ""
			a.Network = "on"
			a.Remote = true
		}
		if p, ok := t.Control.pane(a.Window); ok && !remote {
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
	if a.Action == "revoke" {
		return nil
	}
	if t.Control.Mode != nil && t.Control.Mode.Mode() == permissions.ReadOnly {
		return errors.New("read-only mode cannot control panes")
	}
	if a.Action == "claim" {
		if agentcore.SandboxRequestedFromContext(ctx) {
			return errors.New("a session pane claim cannot satisfy required OS containment")
		}
		return nil
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
	if err := t.Control.checkPaneAtLease(ctx, win, c.PaneID(), c.Session(), c.Socket(), a.HostControl, input, a.ConsumeLease); err != nil {
		return err
	}
	a.Window = win.WindowID
	a.TargetLabel, a.HostHint = win.DisplayName, win.CwdHost
	a.Remote = a.Remote || tuiosRemoteHint(win)
	if input && a.Remote && !t.Control.network(ctx) {
		return errors.New("SSH pane input requires reviewed network authority")
	}
	if input && a.Remote && agentcore.SandboxRequestedFromContext(ctx) {
		return errors.New("a local pane sandbox cannot promise containment of commands on an SSH server")
	}
	if p, ok := t.Control.pane(a.Window); ok && a.Action == "run" && !a.Remote {
		// A command is reviewed relative to this fixed cwd, not whatever a
		// previous input (or the person) left in the interactive shell.
		if a.Script != "" {
			a.Script = "cd " + quoteControlArg(p.cwd) + " || exit\n" + a.Script
		} else {
			a.Command = "cd " + quoteControlArg(p.cwd) + " && " + a.Command
		}
	}
	return nil
}

func quoteControlArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// RequiresOperatorConfirmationFor is trusted tool policy. A session lease is a
// standing capability and therefore cannot be invented by the automatic
// reviewer or full-access mode; the existing ask-mode dialog binds the person's
// answer to the exact prepared pane identity.
func (t *TuiosTool) RequiresOperatorConfirmationFor(raw json.RawMessage) bool {
	var a tuiosArgs
	_ = json.Unmarshal(raw, &a)
	return a.Action == "claim"
}
