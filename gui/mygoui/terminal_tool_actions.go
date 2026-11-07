package mygoui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/getan/golder/gui/terminalaccess"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/seatbelt"
)

func (t *terminalTool) executePane(ctx context.Context, pane *terminalPane, req terminalToolArgs) (agentcore.AgentToolResult, error) {
	registry := t.app.terminalRegistry()
	switch req.Action {
	case "capture":
		var text string
		err := registry.Use(pane.id, t.agent.id, false, func() error {
			if _, err := t.checkSession(); err != nil {
				return err
			}
			if err := t.checkTarget(ctx, pane, req); err != nil {
				return err
			}
			text = terminalTail(pane.term.Text(), req.Lines)
			return nil
		})
		if err != nil {
			return terminalError(err)
		}
		revision, ended := pane.conn.Revision()
		return terminalResult(map[string]any{"pane": pane.id, "text": text, "revision": revision, "ended": ended})
	case "wait":
		waitCtx, cancel := context.WithTimeout(ctx, terminalTimeout(req.TimeoutMS))
		defer cancel()
		for {
			if _, err := t.checkSession(); err != nil {
				return terminalError(err)
			}
			if err := t.checkTarget(ctx, pane, req); err != nil {
				return terminalError(err)
			}
			accessChanged, err := registry.WatchRead(pane.id, t.agent.id)
			if err != nil {
				return terminalError(err)
			}
			revision, ended, outputChanged := pane.conn.Observe(req.Revision)
			if outputChanged == nil {
				if err := registry.Use(pane.id, t.agent.id, false, func() error { return t.checkTarget(ctx, pane, req) }); err != nil {
					return terminalError(err)
				}
				return terminalResult(map[string]any{"pane": pane.id, "revision": revision, "ended": ended})
			}
			select {
			case <-waitCtx.Done():
				return terminalError(waitCtx.Err())
			case <-accessChanged:
			case <-outputChanged:
			}
		}
	case "send", "press", "run":
		input := []byte(req.Text)
		if req.Action == "press" {
			var err error
			input, err = terminalKeys(req.Keys)
			if err != nil {
				return terminalError(err)
			}
		} else if req.Action == "run" {
			command := req.Command
			if pane.envelope != nil {
				command = "cd '" + strings.ReplaceAll(t.agent.project, "'", "'\\''") + "' && " + command
			}
			input = []byte(command + "\r")
		}
		n, err := pane.conn.AgentWriteChecked(t.agent.id, input, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := t.checkSession(); err != nil {
				return err
			}
			return t.checkTarget(ctx, pane, req)
		})
		if err != nil {
			return terminalError(fmt.Errorf("terminal: input failed (%v); execution state may be unknown, inspect before retrying", err))
		}
		return terminalResult(map[string]any{"pane": pane.id, "bytes_sent": n, "completion": "unknown"})
	case "focus", "resize", "rename", "close":
		value, err := t.ui(ctx, func() (any, error) {
			if t.app.findTerminalPane(t.agent.project, pane.id) != pane {
				return nil, errors.New("terminal: pane was closed")
			}
			if _, err := t.checkSession(); err != nil {
				return nil, err
			}
			if err := t.checkTarget(ctx, pane, req); err != nil {
				return nil, err
			}
			if req.Action == "close" {
				if err := registry.RemoveAgentPane(pane.id, t.agent.id); err != nil {
					return nil, err
				}
				t.app.closeTerminalPane(pane)
				return map[string]any{"pane": pane.id, "closed": true}, nil
			}
			err := registry.Use(pane.id, t.agent.id, true, func() error {
				switch req.Action {
				case "focus":
					if t.app.terminalProjectPath() != t.agent.project {
						return errors.New("terminal: switch to this project before focusing its pane")
					}
					t.app.terminalOpen, pane.focus = true, true
				case "resize":
					workspace := t.app.terminals[pane.project]
					if workspace == nil || !workspace.root.resizePane(pane.id, req.Percent) {
						return errors.New("terminal: this pane has no split to resize")
					}
				case "rename":
					pane.name = strings.TrimSpace(req.Name)
				}
				return nil
			})
			return map[string]any{"pane": pane.id, "action": req.Action}, err
		})
		if err != nil {
			return terminalError(err)
		}
		return terminalResult(value)
	}
	return terminalError(errors.New("terminal: unsupported pane action"))
}

func (t *terminalTool) create(ctx context.Context, req terminalToolArgs) (agentcore.AgentToolResult, error) {
	if req.Cwd != t.agent.project || req.HostControl && req.Action == "new" {
		return terminalError(errors.New("terminal: reviewed project identity changed"))
	}
	if req.Remote && (agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce) {
		return terminalError(errors.New("terminal: local sandbox containment cannot guarantee remote effects"))
	}
	if req.Action == "split" {
		value, err := t.ui(ctx, func() (any, error) {
			p := t.app.findTerminalPane(t.agent.project, req.Pane)
			if p == nil || p.attached || p.term == nil {
				return nil, errors.New("terminal: split target is unavailable")
			}
			return p, nil
		})
		if err != nil {
			return terminalError(err)
		}
		if err := t.checkTarget(ctx, value.(*terminalPane), req); err != nil {
			return terminalError(err)
		}
	}
	envelope, err := t.guard.PrepareGUITerminal(ctx, req.Argv, req.Network == "on")
	if err != nil {
		return terminalError(err)
	}
	parent := ""
	if req.Action == "split" {
		parent = req.Pane
	}
	value, err := t.ui(ctx, func() (any, error) {
		return t.app.reserveAgentTerminal(t.agent, parent, req.Direction, req.Name, envelope)
	})
	if err != nil {
		if envelope.Cleanup != nil {
			envelope.Cleanup()
		}
		return terminalError(err)
	}
	pane := value.(*terminalPane)
	cleanupReservation := func() {
		if t.app.win != nil {
			t.app.win.Update(func() { t.app.cleanupTerminalReservation(pane) })
		} else if t.dispatch != nil {
			_, _ = t.dispatch(context.Background(), func() (any, error) { t.app.cleanupTerminalReservation(pane); return nil, nil })
		}
	}
	var process *terminalaccess.Process
	consumed := false
	err = t.app.terminalRegistry().Use(pane.id, t.agent.id, true, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := t.checkSession(); err != nil {
			return err
		}
		if err := t.guard.CheckGUITerminalInput(ctx, envelope); err != nil {
			return err
		}
		consumed = true
		var err error
		process, err = terminalaccess.StartProcess(terminalaccess.ProcessOptions{Argv: envelope.Argv, Dir: t.agent.project, Cleanup: envelope.Cleanup})
		return err
	})
	if err != nil {
		if !consumed && envelope.Cleanup != nil {
			envelope.Cleanup()
		}
		cleanupReservation()
		return terminalError(err)
	}
	_, err = t.ui(ctx, func() (any, error) {
		if _, err := t.checkSession(); err != nil {
			return nil, err
		}
		return nil, t.app.attachAgentTerminal(t.agent, pane, process)
	})
	if err != nil {
		_ = process.Close()
		cleanupReservation()
		return terminalError(err)
	}
	return terminalResult(map[string]any{"pane": pane.id, "isolated": envelope.Isolated, "network": envelope.Network})
}
