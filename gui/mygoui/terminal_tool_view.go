package mygoui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo/plugins/terminal"

	"github.com/getan/golder/gui/terminalaccess"
	"github.com/getan/golder/internal/agenttool"
)

type terminalUIReply struct {
	value any
	err   error
}

// terminalOnUI is the sole cross-goroutine route into the view tree. If a
// callback has started, wait for its result even on cancellation: otherwise a
// reservation could be created after its caller gave up and lost its identity.
func (a *App) terminalOnUI(ctx context.Context, fn func() (any, error)) (any, error) {
	if a.win == nil {
		return nil, errors.New("terminal: GUI window unavailable")
	}
	return dispatchTerminalUI(ctx, a.win.Update, func() (any, error) {
		if a.terminalShuttingDown {
			return nil, errors.New("terminal: GUI is shutting down")
		}
		return fn()
	})
}

func dispatchTerminalUI(ctx context.Context, post func(func()), fn func() (any, error)) (any, error) {
	reply := make(chan terminalUIReply, 1)
	var state atomic.Uint32 // 0 pending, 1 running, 2 canceled before start
	post(func() {
		if !state.CompareAndSwap(0, 1) {
			return
		}
		defer func() {
			if value := recover(); value != nil {
				reply <- terminalUIReply{err: fmt.Errorf("terminal: GUI operation panicked: %v", value)}
			}
		}()
		if err := ctx.Err(); err != nil {
			reply <- terminalUIReply{err: err}
			return
		}
		value, err := fn()
		reply <- terminalUIReply{value: value, err: err}
	})
	select {
	case result := <-reply:
		return result.value, result.err
	case <-ctx.Done():
		if state.CompareAndSwap(0, 2) {
			return nil, ctx.Err()
		}
		result := <-reply
		return result.value, result.err
	}
}

type terminalPaneStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Running  bool   `json:"running"`
	Read     bool   `json:"read_allowed"`
	Write    bool   `json:"input_allowed"`
	Revision uint64 `json:"revision,omitempty"`
	Isolated *bool  `json:"isolated,omitempty"`
	Network  *bool  `json:"network,omitempty"`
}

func (a *App) terminalStatuses(agent *terminalAgent) []terminalPaneStatus {
	var out []terminalPaneStatus
	workspace := a.terminals[agent.project]
	if workspace == nil {
		return out
	}
	workspace.root.each(func(p *terminalPane) {
		read, write, _ := a.terminalRegistry().Access(p.id, agent.id)
		name := p.name
		if !read && !write {
			name = "Private terminal"
		}
		kind := "user"
		if p.creator != "" {
			kind = "agent"
		} else if p.attached {
			kind = "attached-bash-tty"
		}
		status := terminalPaneStatus{ID: p.id, Name: name, Kind: kind, Running: p.running() || p.starting, Read: read, Write: write}
		if read || write {
			isolated, network := false, true // an operator shell's ceiling is unknown
			if p.envelope != nil {
				isolated, network = p.envelope.Isolated, p.envelope.Network
			}
			status.Isolated, status.Network = &isolated, &network
		}
		if p.conn != nil && read {
			status.Revision, _ = p.conn.Revision()
		}
		out = append(out, status)
	})
	return out
}

// reserveAgentTerminal validates the parent lease and creates a placeholder
// before starting a process. Revocation before this point prevents the split.
func (a *App) reserveAgentTerminal(agent *terminalAgent, parent, direction, name string, envelope agenttool.GUITerminalEnvelope) (*terminalPane, error) {
	if a.terminalShuttingDown {
		return nil, errors.New("terminal: GUI is shutting down")
	}
	if _, err := agent.controller.CheckGUITerminalSession(agent.session, agent.project); err != nil {
		return nil, err
	}
	workspace := a.terminalProject(agent.project)
	if parent != "" && a.findTerminalPane(agent.project, parent) == nil {
		return nil, errors.New("terminal: split target no longer exists")
	}
	a.terminalSeq++
	id := fmt.Sprintf("terminal-%d", a.terminalSeq)
	if err := a.terminalRegistry().AddAgentPane(id, agent.id, parent); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("Agent terminal %d", a.terminalSeq)
	}
	// Cleanup is owned by the PTY process; pane metadata retains only the
	// immutable policy facts needed to revalidate subsequent input.
	envelope.Argv, envelope.Cleanup = nil, nil
	pane := &terminalPane{id: id, name: name, project: agent.project, creator: agent.id, envelope: &envelope, starting: true}
	if workspace.root == nil {
		if parent != "" {
			a.terminalRegistry().RemovePane(id)
			return nil, errors.New("terminal: split target disappeared")
		}
		workspace.root = &terminalLayout{pane: pane}
	} else if parent == "" {
		workspace.root = &terminalLayout{first: workspace.root, second: &terminalLayout{pane: pane}, percent: 50}
	} else if !workspace.root.split(parent, pane, direction == "down") {
		a.terminalRegistry().RemovePane(id)
		return nil, errors.New("terminal: split target disappeared")
	}
	if a.terminalProjectPath() == agent.project {
		a.terminalOpen = true
	}
	return pane, nil
}

// attachAgentTerminal installs the already-started, sandboxed process in its
// reserved pane. A closed pane or a revoked lease kills the orphan instead.
func (a *App) attachAgentTerminal(agent *terminalAgent, pane *terminalPane, process *terminalaccess.Process) error {
	if a.findTerminalPane(agent.project, pane.id) != pane {
		return errors.New("terminal: reserved pane was closed")
	}
	_, write, _ := a.terminalRegistry().Access(pane.id, agent.id)
	if !write {
		return errors.New("terminal: Agent control was revoked before attach")
	}
	win := a.win
	invalidate := func() {
		if win != nil {
			win.Invalidate()
		}
	}
	conn, err := terminalaccess.NewConnection(a.terminalRegistry(), pane.id, process, func(string) { invalidate() })
	if err != nil {
		return err
	}
	conn.SetViewLocked(true) // no user input can have preceded this new view
	term, err := terminal.New(terminal.Options{Conn: conn, ClipboardRead: terminal.ClipboardAsk,
		CopyOnSelect: terminal.CopyOnSelectClipboard, OnTitle: func(string) { invalidate() }, OnExit: func(int) { invalidate() }})
	if err != nil {
		_ = conn.Close()
		return err
	}
	pane.conn, pane.term, pane.process, pane.starting, pane.enqueue = conn, term, process, false, term.Send
	return nil
}

func (a *App) cleanupTerminalReservation(pane *terminalPane) {
	if pane != nil && a.findTerminalPane(pane.project, pane.id) == pane {
		a.closeTerminalPane(pane)
	}
}
