package mygoui

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agenttool"
)

// terminalAgent identifies one live controller incarnation, not merely a saved
// conversation ID which may be reopened by a different controller later.
type terminalAgent struct {
	id         string
	session    string
	project    string
	controller *appsession.Controller
	guard      *agenttool.ControlGuard
	tool       *terminalTool
}

// registerTerminalAgent runs on the UI thread. Closing the controller revokes
// its registry entry independently of whether that conversation is visible.
func (a *App) registerTerminalAgent(controller *appsession.Controller) *terminalAgent {
	if controller == nil || !controller.TerminalEligible() {
		return nil
	}
	snapshot := controller.Snapshot()
	project := canonicalTerminalProject(snapshot.Project)
	if snapshot.SessionID == "" || project == "" {
		return nil
	}
	if a.terminalAgents == nil {
		a.terminalAgents = make(map[*appsession.Controller]*terminalAgent)
	}
	registry := a.terminalRegistry()
	if old := a.terminalAgents[controller]; old != nil {
		if old.session == snapshot.SessionID && old.project == project {
			return old
		}
		registry.RemoveAgent(old.id)
	}
	a.terminalAgentSeq++
	agent := &terminalAgent{
		id: fmt.Sprintf("gui-agent-%d", a.terminalAgentSeq), session: snapshot.SessionID,
		project: project, controller: controller,
	}
	if err := registry.AddAgent(agent.id, project); err != nil {
		a.banner, a.bannerErr = "Terminal agent unavailable: "+err.Error(), true
		return nil
	}
	agent.guard = agenttool.NewControlGuard(project)
	agent.tool = &terminalTool{app: a, agent: agent, guard: agent.guard}
	mode, err := controller.InstallGUITerminalTool(agent.session, project, agent.tool)
	if err != nil {
		registry.RemoveAgent(agent.id)
		a.banner, a.bannerErr = "Terminal tool unavailable: "+err.Error(), true
		return nil
	}
	agent.guard.Mode = mode
	a.terminalAgents[controller] = agent
	win := a.win
	go func() {
		<-controller.Done()
		registry.RemoveAgent(agent.id)
		if win != nil {
			win.Invalidate()
		}
	}()
	return agent
}

func (a *App) activeTerminalAgents(project string) []*terminalAgent {
	var agents []*terminalAgent
	for _, agent := range a.terminalAgents {
		if agent.project != project || !agent.controller.TerminalEligible() {
			continue
		}
		if snapshot := agent.controller.Snapshot(); snapshot.SessionID != agent.session || canonicalTerminalProject(snapshot.Project) != project {
			continue
		}
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].session < agents[j].session })
	return agents
}

func terminalAgentLabel(agent *terminalAgent) string {
	snapshot := agent.controller.Snapshot()
	title := oneLine(snapshot.Title, 50)
	if title == "" {
		title = "Untitled session"
	}
	return title + " · " + agent.session
}

func (a *App) findTerminalPane(project, id string) *terminalPane {
	workspace := a.terminals[project]
	if workspace == nil {
		return nil
	}
	var result *terminalPane
	workspace.root.each(func(p *terminalPane) {
		if p.id == id {
			result = p
		}
	})
	return result
}

func (a *App) requestTerminalGrant(pane *terminalPane, agent *terminalAgent, read bool) {
	a.terminalGrantPane, a.terminalGrantAgent = pane, agent
	a.terminalGrantRead, a.terminalGrantOpen = read, true
}

func (a *App) confirmTerminalGrant() {
	pane, agent := a.terminalGrantPane, a.terminalGrantAgent
	if pane == nil || agent == nil || pane.attached || a.findTerminalPane(pane.project, pane.id) != pane ||
		!agent.controller.TerminalEligible() {
		a.banner, a.bannerErr = "Terminal or Agent is no longer available.", true
		return
	}
	snapshot := agent.controller.Snapshot()
	if snapshot.SessionID != agent.session || canonicalTerminalProject(snapshot.Project) != pane.project {
		a.banner, a.bannerErr = "The Agent session changed; no terminal access was granted.", true
		return
	}
	if a.terminalGrantRead {
		if err := a.terminalRegistry().Grant(pane.id, agent.id, true, false, false); err != nil {
			a.banner, a.bannerErr = "Cannot grant terminal access: "+err.Error(), true
		}
		return
	}
	if pane.conn == nil || pane.enqueue == nil || pane.grantPending {
		a.banner, a.bannerErr = "Terminal input is unavailable or another transfer is pending.", true
		return
	}
	// Revoke the previous writer immediately. The next frame disables local
	// input before queuing a barrier behind any already-typed bytes.
	a.terminalRegistry().TakeBack(pane.id)
	pane.conn.SetViewLocked(false)
	pane.grantSeq++
	pane.grantAgent, pane.grantPending, pane.grantStarted = agent, true, false
}

func (a *App) cancelTerminalGrant(pane *terminalPane) {
	if pane == nil || !pane.grantPending {
		return
	}
	pane.grantSeq++
	pane.grantAgent, pane.grantPending, pane.grantStarted = nil, false, false
}

// startTerminalGrantBarrier is called while building the frame that disables
// the terminal view. MyGo enqueues the marker after all prior local input.
func (a *App) startTerminalGrantBarrier(pane *terminalPane) {
	if pane == nil || !pane.grantPending || pane.grantStarted || pane.conn == nil || pane.enqueue == nil {
		return
	}
	pane.grantStarted = true
	agent, seq, conn, enqueue, win := pane.grantAgent, pane.grantSeq, pane.conn, pane.enqueue, a.win
	run := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return conn.Barrier(ctx, enqueue)
	}
	if win == nil {
		// Headless UI tests use a synchronous fake input queue.
		a.finishTerminalGrant(pane, agent, seq, run())
		return
	}
	go func() {
		err := run()
		win.Update(func() { a.finishTerminalGrant(pane, agent, seq, err) })
	}()
}

func (a *App) finishTerminalGrant(pane *terminalPane, agent *terminalAgent, seq uint64, err error) {
	if pane == nil || agent == nil || a.findTerminalPane(pane.project, pane.id) != pane ||
		!pane.grantPending || pane.grantSeq != seq || pane.grantAgent != agent {
		return
	}
	pane.grantAgent, pane.grantPending, pane.grantStarted = nil, false, false
	if err == nil {
		_, err = agent.controller.CheckGUITerminalSession(agent.session, agent.project)
	}
	if err == nil {
		pane.conn.SetViewLocked(true)
		err = a.terminalRegistry().Grant(pane.id, agent.id, false, true, false)
	}
	if err != nil {
		pane.conn.SetViewLocked(false)
		a.banner, a.bannerErr = "Cannot grant terminal control: "+err.Error(), true
	}
}
