package mygoui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/terminalaccess"
	"github.com/getan/golder/internal/agenttool"
)

// A terminal belongs to a project, not to whichever conversation happens to be
// selected. An attached bash session is owned by its originating controller.
type terminalPane struct {
	id           string
	name         string
	project      string
	attached     bool
	term         *terminal.Terminal
	conn         *terminalaccess.Connection
	process      *terminalaccess.Process
	creator      string
	envelope     *agenttool.GUITerminalEnvelope
	starting     bool
	focus        bool
	enqueue      func([]byte)
	grantAgent   *terminalAgent
	grantPending bool
	grantStarted bool
	grantSeq     uint64
}

type terminalProject struct {
	root *terminalLayout
}

func (a *App) terminalProjectPath() string {
	path := strings.TrimSpace(a.selectedProject)
	if path == "" {
		path = strings.TrimSpace(a.project)
	}
	return canonicalTerminalProject(path)
}

func canonicalTerminalProject(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func (a *App) terminalProject(path string) *terminalProject {
	if a.terminals == nil {
		a.terminals = make(map[string]*terminalProject)
	}
	project := a.terminals[path]
	if project == nil {
		project = &terminalProject{}
		a.terminals[path] = project
	}
	return project
}

func (a *App) terminalRegistry() *terminalaccess.Registry {
	if a.terminalAccess == nil {
		a.terminalAccess = terminalaccess.New()
	}
	return a.terminalAccess
}

func (p *terminalPane) running() bool {
	if p == nil || p.term == nil {
		return false
	}
	select {
	case <-p.term.Done():
		return false
	default:
		return true
	}
}

// addTerminal starts a user shell or attaches a view to an already-authorized
// bash TTY. Closing an attached pane only closes its subscription.
func (a *App) addTerminal(project, beside string, vertical bool, conn io.ReadWriteCloser, label string) {
	if a.terminalShuttingDown {
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	if project == "" {
		if conn != nil {
			_ = conn.Close()
		}
		a.banner, a.bannerErr = "Choose a project before opening a terminal.", true
		return
	}
	workspace := a.terminalProject(project)
	if workspace.root != nil && beside == "" {
		workspace.root.each(func(p *terminalPane) {
			if beside == "" {
				beside = p.id
			}
		})
	}
	a.terminalSeq++
	if label == "" {
		label = fmt.Sprintf("Terminal %d", a.terminalSeq)
	}
	pane := &terminalPane{id: fmt.Sprintf("terminal-%d", a.terminalSeq), name: label, project: project, attached: conn != nil}
	registry := a.terminalRegistry()
	if err := registry.AddPane(pane.id, project); err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		a.banner, a.bannerErr = "Terminal unavailable: "+err.Error(), true
		return
	}
	if conn == nil {
		process, err := terminalaccess.StartProcess(terminalaccess.ProcessOptions{Argv: terminalaccess.UserShell(), Dir: project})
		if err != nil {
			registry.RemovePane(pane.id)
			a.banner, a.bannerErr = "Terminal unavailable: "+err.Error(), true
			return
		}
		pane.process = process
		conn = process
	}
	win := a.win
	invalidate := func() {
		if win != nil {
			win.Invalidate()
		}
	}
	wrapped, err := terminalaccess.NewConnection(registry, pane.id, conn, func(string) { invalidate() })
	if err != nil {
		_ = conn.Close()
		registry.RemovePane(pane.id)
		a.banner, a.bannerErr = "Terminal unavailable: "+err.Error(), true
		return
	}
	pane.conn = wrapped
	opts := terminal.Options{
		Conn: wrapped, ClipboardRead: terminal.ClipboardAsk,
		CopyOnSelect: terminal.CopyOnSelectClipboard,
		OnTitle:      func(string) { invalidate() },
		OnExit:       func(int) { invalidate() },
	}
	term, err := terminal.New(opts)
	if err != nil {
		_ = wrapped.Close()
		registry.RemovePane(pane.id)
		a.banner, a.bannerErr = "Terminal unavailable: "+err.Error(), true
		return
	}
	pane.term, pane.enqueue = term, term.Send
	if workspace.root == nil {
		workspace.root = &terminalLayout{pane: pane}
	} else if !workspace.root.split(beside, pane, vertical) {
		_ = term.Close()
		registry.RemovePane(pane.id)
		a.banner, a.bannerErr = "The terminal to split is no longer available.", true
		return
	}
	a.terminalOpen = true
}

func (a *App) toggleTerminal() {
	if a.terminalShuttingDown {
		return
	}
	a.terminalOpen = !a.terminalOpen
	if !a.terminalOpen {
		return
	}
	path := a.terminalProjectPath()
	if path == "" {
		a.terminalOpen = false
		a.banner, a.bannerErr = "Choose a project before opening a terminal.", true
		return
	}
	if a.terminalProject(path).root == nil {
		a.addTerminal(path, "", false, nil, "")
	}
}

func (a *App) terminalPanel(c *ui.Context) {
	project := a.terminalProjectPath()
	workspace := a.terminalProject(project)
	t := c.Theme()
	ui.Column(c).Fill().MinWidth(0).Background(t.Background).Children(func() {
		header := ui.Row(c).Height(36).Padding(6, 10).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
		header.ContextMenu(func(m *ui.Menu) {
			if m.Item("New terminal").Chosen() {
				a.addTerminal(project, "", false, nil, "")
			}
		})
		header.Children(func() {
			ui.Icon(c, terminalMonochromeIcon).Size(15, 15)
			ui.Text(c, "Terminal").FontSize(12).FontWeight(600).Padding(0, 8).Grow(1)
			if subtleButton(c, "×").Label("Hide terminal panel").Tooltip("Hide terminal panel").Clicked() {
				a.terminalOpen = false
			}
		})
		ui.Text(c, "Right-click a pane title to split it or manage Agent access.").
			FontSize(10).TextColor(t.TextMuted).Padding(3, 10).Wrap()
		if workspace.root == nil {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "Right-click the terminal header to open a terminal.").FontSize(12).TextColor(t.TextMuted).Wrap()
			})
		} else {
			a.terminalLayoutView(c, workspace.root)
		}
	})
}

func (a *App) terminalLayoutView(c *ui.Context, node *terminalLayout) {
	if node == nil {
		return
	}
	if node.pane != nil {
		a.terminalPaneView(c.Key(node.pane.id), node.pane)
		return
	}
	first := func() { a.terminalLayoutView(c, node.first) }
	second := func() { a.terminalLayoutView(c, node.second) }
	if node.vertical {
		ui.SplitVerticalPercent(c, &node.percent, first, second).Grow(1).FillWidth().MinHeight(0)
	} else {
		ui.SplitPercent(c, &node.percent, first, second).Grow(1).FillWidth().MinWidth(0)
	}
}

func (a *App) terminalPaneView(c *ui.Context, pane *terminalPane) {
	t := c.Theme()
	owner := a.terminalRegistry().Owner(pane.id)
	if owner == "" && !pane.grantPending && pane.conn != nil && pane.conn.ViewLocked() {
		pane.conn.SetViewLocked(false)
	}
	ui.Column(c).Grow(1).FillWidth().MinWidth(0).MinHeight(0).Children(func() {
		header := ui.Row(c).Height(28).Padding(4, 8).Gap(5).AlignItems(ui.Center).Background(t.Surface).
			Tooltip("Right-click this title to split or manage Agent access")
		header.ContextMenu(func(m *ui.Menu) { a.terminalPaneMenu(m, pane) })
		header.Children(func() {
			ui.Text(c, pane.name).FontSize(11).FontWeight(600).Grow(1).SingleLine().Ellipsis("…").Label("Terminal pane " + pane.name)
			state := "exited"
			if pane.starting {
				state = "starting"
			} else if pane.running() {
				state = "running"
			}
			if pane.attached {
				state = "agent TTY · " + state
			} else if pane.grantPending {
				state = "assigning Agent…"
			} else if owner != "" {
				state = "Agent control"
				for _, agent := range a.activeTerminalAgents(pane.project) {
					if agent.id == owner {
						state = "Agent: " + oneLine(agent.controller.Snapshot().Title, 24)
						break
					}
				}
			}
			ui.Text(c, state).FontSize(10).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(140)
		})
		if pane.term != nil {
			view := terminal.View(c, pane.term).Grow(1).FillWidth().MinHeight(0).Disabled(pane.grantPending || owner != "")
			if pane.focus {
				view.Focus()
				pane.focus = false
			}
		}
		if pane.grantPending && !pane.grantStarted {
			a.startTerminalGrantBarrier(pane)
		}
	})
}

func (a *App) terminalPaneMenu(m *ui.Menu, pane *terminalPane) {
	if m.Item("Split right").Chosen() {
		a.addTerminal(pane.project, pane.id, false, nil, "")
	}
	if m.Item("Split down").Chosen() {
		a.addTerminal(pane.project, pane.id, true, nil, "")
	}
	m.Separator()
	agents := a.activeTerminalAgents(pane.project)
	ended := false
	if pane.conn != nil {
		_, ended = pane.conn.Revision()
	}
	m.Submenu("Allow Agent to view", func(sub *ui.Menu) {
		for _, agent := range agents {
			agent := agent
			read, _, _ := a.terminalRegistry().Access(pane.id, agent.id)
			if sub.Item(terminalAgentLabel(agent)).Checked(read).Chosen() {
				a.requestTerminalGrant(pane, agent, true)
			}
		}
	}).Disabled(pane.attached || pane.conn == nil || len(agents) == 0)
	m.Submenu("Assign Agent control", func(sub *ui.Menu) {
		for _, agent := range agents {
			agent := agent
			_, write, _ := a.terminalRegistry().Access(pane.id, agent.id)
			if sub.Item(terminalAgentLabel(agent)).Checked(write).Chosen() {
				a.requestTerminalGrant(pane, agent, false)
			}
		}
	}).Disabled(pane.attached || pane.conn == nil || ended || len(agents) == 0)
	m.Submenu("Revoke Agent access", func(sub *ui.Menu) {
		for _, agent := range agents {
			agent := agent
			read, write, _ := a.terminalRegistry().Access(pane.id, agent.id)
			pending := pane.grantPending && pane.grantAgent == agent
			if sub.Item(terminalAgentLabel(agent)).Disabled(!read && !write && !pending).Chosen() {
				if pending {
					a.cancelTerminalGrant(pane)
				}
				a.terminalRegistry().Revoke(pane.id, agent.id)
				if a.terminalRegistry().Owner(pane.id) == "" && pane.conn != nil {
					pane.conn.SetViewLocked(false)
				}
			}
		}
	}).Disabled(pane.attached || pane.conn == nil || len(agents) == 0)
	if m.Item("Take back control").Disabled(a.terminalRegistry().Owner(pane.id) == "" && !pane.grantPending).Chosen() {
		a.cancelTerminalGrant(pane)
		a.terminalRegistry().TakeBack(pane.id)
		if pane.conn != nil {
			pane.conn.SetViewLocked(false)
		}
	}
	m.Separator()
	if m.Item("Rename…").Chosen() {
		a.terminalRenamePane = pane
		a.terminalRenameDraft = pane.name
		a.terminalRenameOpen = true
	}
	if m.Item("Close terminal").Chosen() {
		if (pane.starting || pane.running()) && !pane.attached {
			a.terminalClosePane = pane
			a.terminalCloseOpen = true
		} else {
			a.closeTerminalPane(pane)
		}
	}
}

func (a *App) closeTerminalPane(pane *terminalPane) {
	if pane == nil {
		return
	}
	workspace := a.terminals[pane.project]
	if workspace == nil {
		return
	}
	var removed *terminalPane
	workspace.root, removed = workspace.root.remove(pane.id)
	if removed != nil {
		if removed.term != nil {
			_ = removed.term.Close()
		} else {
			if removed.process != nil {
				_ = removed.process.Close()
			}
		}
		a.terminalRegistry().RemovePane(removed.id)
	}
}

func (a *App) hasRunningTerminals() bool {
	for _, workspace := range a.terminals {
		running := false
		workspace.root.each(func(p *terminalPane) { running = running || p.starting || p.running() })
		if running {
			return true
		}
	}
	return false
}

// detachTerminalPanes runs on the UI thread; closing the returned processes can
// then happen on a worker without blocking callbacks needed by an Agent turn.
func (a *App) detachTerminalPanes() []*terminalPane {
	var panes []*terminalPane
	for _, workspace := range a.terminals {
		workspace.root.each(func(p *terminalPane) { panes = append(panes, p) })
		workspace.root = nil
	}
	return panes
}

func closeTerminalPanes(panes []*terminalPane, registry *terminalaccess.Registry) {
	var done []<-chan struct{}
	for _, p := range panes {
		if p.term != nil {
			if p.process != nil {
				done = append(done, p.process.Done())
			} else {
				done = append(done, p.term.Done())
			}
			_ = p.term.Close()
		}
		if registry != nil {
			registry.RemovePane(p.id)
		}
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for _, ch := range done {
		select {
		case <-ch:
		case <-deadline.C:
			return
		}
	}
}

func (a *App) closeTerminals() {
	closeTerminalPanes(a.detachTerminalPanes(), a.terminalAccess)
}

// beginTerminalShutdown keeps the window event loop alive while tools and
// private authorization flows finish; otherwise a tool waiting on a UI
// callback and a synchronous Controller.Close could deadlock one another.
func (a *App) beginTerminalShutdown() {
	if a.terminalShuttingDown || a.win == nil {
		return
	}
	a.terminalShuttingDown = true
	if a.mcpOAuth != nil && a.mcpOAuth.cancel != nil {
		a.mcpOAuth.cancel()
	}
	if a.extensionCancel != nil {
		a.extensionCancel()
	}
	if a.diffCancel != nil {
		a.diffCancel()
		a.diffCancel = nil
	}
	panes, registry, win, hub := a.detachTerminalPanes(), a.terminalAccess, a.win, a.hub
	a.banner, a.bannerErr = "Closing terminals and sessions…", false
	win.Invalidate()
	go func() {
		closeTerminalPanes(panes, registry)
		if hub != nil {
			_ = hub.Close()
		}
		win.Update(func() {
			a.terminalShutdownDone = true
			win.Close()
		})
	}()
}

func (a *App) terminalModals(c *ui.Context) {
	ui.Modal(c, &a.terminalGrantOpen, func() {
		ui.Column(c).Width(470).Gap(10).Children(func() {
			title := "Allow Agent to control this terminal?"
			message := "This is a user shell outside the Agent sandbox. The selected Agent may send commands in your environment. Control transfers from any previous Agent; right-click the title to take it back."
			if a.terminalGrantPane != nil && a.terminalGrantPane.envelope != nil && a.terminalGrantPane.envelope.Isolated {
				message = "The selected Agent may control this sandboxed terminal within its existing limits. Control transfers from any previous Agent; right-click the title to take it back."
			}
			if a.terminalGrantRead {
				title = "Allow Agent to read this terminal?"
				message = "Terminal content can include private information and secrets. This grants viewing separately from control."
			}
			ui.Text(c, title).FontSize(16).Bold()
			if a.terminalGrantAgent != nil {
				ui.Text(c, terminalAgentLabel(a.terminalGrantAgent)).FontSize(12).Wrap()
			}
			ui.Text(c, message).FontSize(12).Wrap()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if subtleButton(c, "Cancel").Clicked() {
					a.terminalGrantOpen = false
				}
				if subtlePrimaryButton(c, "Grant access").Clicked() {
					a.confirmTerminalGrant()
					a.terminalGrantOpen = false
				}
			})
		})
	})
	ui.Modal(c, &a.terminalRenameOpen, func() {
		ui.Column(c).Width(380).Gap(10).Children(func() {
			ui.Text(c, "Rename terminal").FontSize(16).Bold()
			ui.TextInput(c, &a.terminalRenameDraft).AutoFocus().FillWidth()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if subtleButton(c, "Cancel").Clicked() {
					a.terminalRenameOpen = false
				}
				if subtlePrimaryButton(c, "Rename").Clicked() {
					if a.terminalRenamePane != nil && strings.TrimSpace(a.terminalRenameDraft) != "" {
						a.terminalRenamePane.name = strings.TrimSpace(a.terminalRenameDraft)
					}
					a.terminalRenameOpen = false
				}
			})
		})
	})
	ui.Modal(c, &a.terminalCloseOpen, func() {
		ui.Column(c).Width(410).Gap(10).Children(func() {
			ui.Text(c, "Close running terminal?").FontSize(16).Bold()
			ui.Text(c, "The terminal process will be terminated.").FontSize(12).Wrap()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if subtleButton(c, "Cancel").Clicked() {
					a.terminalCloseOpen = false
				}
				if subtlePrimaryButton(c, "Close terminal").Clicked() {
					a.closeTerminalPane(a.terminalClosePane)
					a.terminalClosePane = nil
					a.terminalCloseOpen = false
				}
			})
		})
	})
	ui.Modal(c, &a.terminalExitOpen, func() {
		ui.Column(c).Width(430).Gap(10).Children(func() {
			ui.Text(c, "Quit with running terminals?").FontSize(16).Bold()
			ui.Text(c, "Running terminal processes will be closed. Attached Agent terminals will be detached.").FontSize(12).Wrap()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if subtleButton(c, "Cancel").Clicked() {
					a.terminalExitOpen = false
				}
				if subtlePrimaryButton(c, "Quit").Clicked() && a.win != nil {
					a.terminalExitOpen = false
					a.terminalExitApproved = true
					a.win.Close()
				}
			})
		})
	})
}
