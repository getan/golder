package agenttool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/panecontrol"
	"github.com/getan/golder/internal/tuiosctl"
)

// externalPaneLease is explicit operator authority over one exact pane for the
// lifetime of this ControlGuard (one agent session). It is deliberately separate
// from HostControl, which remains a one-call execution-context capability.
type externalPaneLease struct {
	WindowID       string
	SessionID      string
	Workspace      int
	Socket         string
	Daemon         os.FileInfo
	DaemonID       string
	HostHint       string
	Foreground     string
	Remote         bool
	Scope          string
	AgentID        string
	ConversationID string
	GrantedBy      string
	CreatedAt      time.Time
}

type paneControlState struct {
	controlled   bool
	origin       string
	scope        string
	inputAllowed bool
	sealed       bool
	reason       string
	ownerID      string
	ownerLabel   string
}

func agentProjectLabel(root string) string {
	root = filepath.Clean(root)
	name := filepath.Base(root)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "golder"
	}
	return name
}

func agentIDPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && b.String()[b.Len()-1] != '-':
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// ConfigurePaneAgent registers the top-level interactive agent represented by
// tools in the daemon-scoped cross-process registry. Read-only sub-agent tool
// sets are intentionally ignored.
func ConfigurePaneAgent(ctx context.Context, tools []agentcore.AgentTool, conversationID string) error {
	ConfigureBrowserSession(tools, conversationID)
	if !tuiosctl.Available() {
		return nil
	}
	seen := map[*ControlGuard]bool{}
	for _, raw := range tools {
		tool, ok := raw.(*TuiosTool)
		if !ok || !tool.allowControl || tool.Control == nil || seen[tool.Control] {
			continue
		}
		seen[tool.Control] = true
		c, err := tool.newClient(ctx)
		if err != nil {
			return err
		}
		list, err := c.ListWindows(ctx)
		if err != nil {
			_ = c.Close()
			return err
		}
		var own tuiosctl.Window
		for _, win := range list.Windows {
			if win.WindowID == c.PaneID() {
				own = win
				break
			}
		}
		if own.WindowID == "" {
			_ = c.Close()
			return errors.New("cannot register pane-control agent: this process's pane is absent from tuios list")
		}
		err = tool.Control.configureAgent(c, own, conversationID)
		_ = c.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func ClosePaneAgent(tools []agentcore.AgentTool) {
	seen := map[*ControlGuard]bool{}
	for _, raw := range tools {
		tool, ok := raw.(*TuiosTool)
		if !ok || tool.Control == nil || seen[tool.Control] {
			continue
		}
		seen[tool.Control] = true
		tool.Control.stopAgentRegistration()
	}
}

// SetPaneAgentState keeps the shared candidate list from offering an Agent that
// is currently waiting on an approval. It never grants or revokes by itself;
// conversation/workspace changes and explicit control operations own that.
func SetPaneAgentState(tools []agentcore.AgentTool, state string) {
	seen := map[*ControlGuard]bool{}
	for _, raw := range tools {
		tool, ok := raw.(*TuiosTool)
		if !ok || tool.Control == nil || seen[tool.Control] {
			continue
		}
		seen[tool.Control] = true
		g := tool.Control
		g.mu.Lock()
		store, agent := g.leaseStore, g.agentIdentity
		if agent.ID != "" {
			agent.State = state
			g.agentIdentity = agent
		}
		g.mu.Unlock()
		if store != nil && agent.ID != "" {
			_ = store.Register(agent)
		}
	}
}

func (g *ControlGuard) configureAgent(c *tuiosctl.Client, own tuiosctl.Window, conversationID string) error {
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("cannot register pane-control agent without a conversation id")
	}
	g.stopAgentRegistration()
	project := agentProjectLabel(g.Root)
	part := agentIDPart(project)
	if part == "" {
		part = "golder"
	}
	agent := panecontrol.Agent{
		ID: part + "-main-" + g.id, Label: project + " · Main Agent", Project: project, Role: "main",
		ConversationID: conversationID, SessionID: c.Session(), PaneID: own.WindowID, Workspace: own.Workspace,
		Mode: g.Mode.Mode().String(), PID: os.Getpid(),
	}
	store := panecontrol.New(c.Socket())
	if err := store.Register(agent); err != nil {
		return err
	}
	stop, done := make(chan struct{}), make(chan struct{})
	g.mu.Lock()
	g.leaseStore, g.agentIdentity, g.leaseStop, g.leaseDone = store, agent, stop, done
	g.mu.Unlock()
	go g.heartbeatAgent(store, agent.ID, stop, done)
	return nil
}

func (g *ControlGuard) heartbeatAgent(store *panecontrol.Store, agentID string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			g.mu.Lock()
			agent := g.agentIdentity
			if agent.ID == agentID && g.Mode != nil {
				agent.Mode = g.Mode.Mode().String()
				g.agentIdentity = agent
			}
			g.mu.Unlock()
			if agent.ID == agentID {
				_ = store.Register(agent)
			}
		}
	}
}

func (g *ControlGuard) stopAgentRegistration() {
	g.mu.Lock()
	store, agent, stop, done := g.leaseStore, g.agentIdentity, g.leaseStop, g.leaseDone
	g.leaseStore, g.agentIdentity, g.leaseStop, g.leaseDone = nil, panecontrol.Agent{}, nil, nil
	g.mu.Unlock()
	if stop != nil {
		close(stop)
		if done != nil {
			<-done
		}
	}
	if store != nil && agent.ID != "" {
		_ = store.Unregister(agent.ID)
	}
}

func (g *ControlGuard) touchAgent(c *tuiosctl.Client, windows []tuiosctl.Window) {
	g.mu.Lock()
	store, agent := g.leaseStore, g.agentIdentity
	g.mu.Unlock()
	if store == nil || agent.ID == "" || agent.SessionID != c.Session() {
		return
	}
	for _, win := range windows {
		if win.WindowID == c.PaneID() {
			agent.PaneID, agent.Workspace = win.WindowID, win.Workspace
			break
		}
	}
	if g.Mode != nil {
		agent.Mode = g.Mode.Mode().String()
	}
	if err := store.Register(agent); err == nil {
		g.mu.Lock()
		if g.agentIdentity.ID == agent.ID {
			g.agentIdentity = agent
		}
		g.mu.Unlock()
	}
}

func (g *ControlGuard) sharedLeaseState() (*panecontrol.Store, panecontrol.Agent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leaseStore, g.agentIdentity
}

func foregroundProgram(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

func paneIsAgent(win tuiosctl.Window) bool {
	state := strings.TrimSpace(win.AgentState)
	if state != "" && state != "none" {
		return true
	}
	return foregroundProgram(win.ForegroundCmd) == "golder"
}

func (g *ControlGuard) paneIsBrowser(win tuiosctl.Window) bool {
	if foregroundProgram(win.ForegroundCmd) == "kitty-browse" {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if p, ok := g.panes[win.WindowID]; ok && p.browser {
		return true
	}
	for _, browser := range g.browsers {
		if browser.window == win.WindowID {
			return true
		}
	}
	return false
}

func (g *ControlGuard) claimRefusal(win tuiosctl.Window, own string) error {
	switch {
	case strings.TrimSpace(win.WindowID) == "":
		return errors.New("claim requires a full window id")
	case sensitivePane(win):
		return errors.New("authorization panes are operator-only and cannot be claimed")
	case win.WindowID == own:
		return errors.New("the pane running this agent cannot be claimed")
	case paneIsAgent(win):
		return errors.New("another agent pane cannot be claimed or receive agent input")
	case g.paneIsBrowser(win):
		return errors.New("browser panes cannot be claimed through tuios; use the browser tool")
	}
	if _, managed := g.pane(win.WindowID); managed {
		return errors.New("pane is already an agent-created controlled helper and does not need a claim")
	}
	return nil
}

func canonicalPaneSocket(socket string) string {
	if resolved, err := filepath.EvalSymlinks(socket); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(socket)
}

func paneDaemonIdentity(socket string) (string, error) {
	info, err := os.Stat(socket)
	if err != nil {
		return "", err
	}
	raw := fmt.Sprintf("pane-daemon-v1\x00%s\x00%d\x00%s", canonicalPaneSocket(socket), info.ModTime().UnixNano(), info.Mode())
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

// claimTargetIdentity binds operator confirmation to the daemon instance, tuios
// session, exact full window id, and the remote/shell context observed before
// review. g.id makes the digest unusable in another agent session.
func (g *ControlGuard) claimTargetIdentity(c *tuiosctl.Client, win tuiosctl.Window) (string, error) {
	info, err := os.Stat(c.Socket())
	if err != nil {
		return "", fmt.Errorf("identify tuios daemon: %w", err)
	}
	raw := strings.Join([]string{
		"pane-lease-v1", g.id, c.Session(), canonicalPaneSocket(c.Socket()),
		fmt.Sprintf("%d", info.ModTime().UnixNano()), info.Mode().String(),
		win.WindowID, win.CwdHost, strings.TrimSpace(win.ForegroundCmd),
		fmt.Sprintf("%t", tuiosRemoteHint(win)),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:]), nil
}

func (g *ControlGuard) claimExternal(c *tuiosctl.Client, win tuiosctl.Window, own, identity string) (externalPaneLease, error) {
	return g.claimExternalWith(c, win, own, identity, panecontrol.ScopeSession, false)
}

func (g *ControlGuard) claimExternalWith(c *tuiosctl.Client, win tuiosctl.Window, own, identity, scope string, transfer bool) (externalPaneLease, error) {
	if err := g.claimRefusal(win, own); err != nil {
		return externalPaneLease{}, err
	}
	want, err := g.claimTargetIdentity(c, win)
	if err != nil {
		return externalPaneLease{}, err
	}
	if identity == "" || identity != want {
		return externalPaneLease{}, errors.New("pane identity changed after review; claim was not created")
	}
	info, err := os.Stat(c.Socket())
	if err != nil {
		return externalPaneLease{}, fmt.Errorf("identify tuios daemon: %w", err)
	}
	daemonID, err := paneDaemonIdentity(c.Socket())
	if err != nil {
		return externalPaneLease{}, fmt.Errorf("identify tuios daemon: %w", err)
	}
	if scope != panecontrol.ScopeOnce && scope != panecontrol.ScopeSession {
		return externalPaneLease{}, errors.New("pane claim scope must be one-call or session")
	}
	store, agent := g.sharedLeaseState()
	if store != nil {
		if agent.ID == "" || agent.ConversationID == "" {
			return externalPaneLease{}, errors.New("pane-control agent identity is not registered")
		}
		if win.Workspace != agent.Workspace {
			return externalPaneLease{}, errors.New("pane is in another workspace; move it into this agent's workspace before claiming")
		}
		if _, err := store.Claim(panecontrol.ClaimRequest{Lease: panecontrol.Lease{
			SessionID: c.Session(), WindowID: win.WindowID, Workspace: win.Workspace,
			AgentID: agent.ID, ConversationID: agent.ConversationID, Scope: scope, DaemonID: daemonID,
			HostHint: win.CwdHost, Foreground: strings.TrimSpace(win.ForegroundCmd), Remote: tuiosRemoteHint(win),
		}, Transfer: transfer}); err != nil {
			return externalPaneLease{}, err
		}
	}
	lease := externalPaneLease{
		WindowID: win.WindowID, SessionID: c.Session(), Workspace: win.Workspace,
		Socket: canonicalPaneSocket(c.Socket()), Daemon: info, DaemonID: daemonID,
		HostHint: win.CwdHost, Foreground: strings.TrimSpace(win.ForegroundCmd), Remote: tuiosRemoteHint(win),
		Scope: scope, AgentID: agent.ID, ConversationID: agent.ConversationID,
		GrantedBy: "operator", CreatedAt: time.Now(),
	}
	g.mu.Lock()
	if g.externalPanes == nil {
		g.externalPanes = make(map[string]externalPaneLease)
	}
	g.externalPanes[win.WindowID] = lease
	g.mu.Unlock()
	return lease, nil
}

func (g *ControlGuard) externalLease(id string) (externalPaneLease, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	lease, ok := g.externalPanes[id]
	return lease, ok
}

func (g *ControlGuard) revokeExternal(id string) bool {
	return g.revokeExternalWith(id, false)
}

func (g *ControlGuard) revokeExternalWith(id string, operator bool) bool {
	store, agent := g.sharedLeaseState()
	shared := false
	if store != nil {
		shared, _ = store.Revoke(agent.SessionID, id, agent.ID, operator)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.externalPanes[id]
	delete(g.externalPanes, id)
	return ok || shared
}

func (g *ControlGuard) leaseEndpointValid(lease externalPaneLease, session, socket string) bool {
	if lease.SessionID != session || lease.Socket != canonicalPaneSocket(socket) || lease.Daemon == nil {
		return false
	}
	info, err := os.Stat(socket)
	return err == nil && os.SameFile(lease.Daemon, info)
}

// checkExternalLease returns claimed=true when a lease exists. Identity and
// remote-context changes revoke it. A local foreground program change is only a
// temporary input refusal: manual commands must not silently destroy a lease,
// but the agent must wait until the original shell prompt is back.
func (g *ControlGuard) checkExternalLease(win tuiosctl.Window, session, socket string, input, consume bool) (claimed bool, err error) {
	lease, ok := g.externalLease(win.WindowID)
	store, agent := g.sharedLeaseState()
	if store != nil {
		shared, authErr := store.Authorize(session, win.WindowID, agent.ID, agent.ConversationID, consume)
		if authErr != nil {
			var notController *panecontrol.NotControllerError
			if errors.As(authErr, &notController) && notController.Current == nil {
				g.mu.Lock()
				delete(g.externalPanes, win.WindowID)
				g.mu.Unlock()
				return false, nil
			}
			g.mu.Lock()
			delete(g.externalPanes, win.WindowID)
			g.mu.Unlock()
			return false, authErr
		}
		lease = externalPaneLease{
			WindowID: shared.WindowID, SessionID: shared.SessionID, Workspace: shared.Workspace, Socket: canonicalPaneSocket(socket),
			DaemonID: shared.DaemonID, HostHint: shared.HostHint, Foreground: shared.Foreground,
			Remote: shared.Remote, Scope: shared.Scope, AgentID: shared.AgentID,
			ConversationID: shared.ConversationID, CreatedAt: shared.CreatedAt,
		}
		g.mu.Lock()
		if g.externalPanes == nil {
			g.externalPanes = make(map[string]externalPaneLease)
		}
		g.externalPanes[win.WindowID] = lease
		g.mu.Unlock()
		ok = true
	}
	if !ok {
		return false, nil
	}
	currentDaemon, daemonErr := paneDaemonIdentity(socket)
	hardReason := ""
	switch {
	case daemonErr != nil || lease.DaemonID != "" && lease.DaemonID != currentDaemon:
		hardReason = "tuios daemon identity changed"
	case lease.Workspace > 0 && win.Workspace != lease.Workspace:
		hardReason = "pane moved to another workspace"
	case store == nil && !g.leaseEndpointValid(lease, session, socket):
		hardReason = "tuios daemon or session identity changed"
	case sensitivePane(win):
		hardReason = "pane became an authorization surface"
	case paneIsAgent(win):
		hardReason = "pane became another agent"
	case g.paneIsBrowser(win):
		hardReason = "pane became a browser"
	case tuiosRemoteHint(win) != lease.Remote:
		hardReason = "pane remote context changed"
	case lease.Remote && (win.CwdHost != lease.HostHint || strings.TrimSpace(win.ForegroundCmd) != lease.Foreground):
		hardReason = "SSH host or foreground identity changed"
	}
	if hardReason != "" {
		g.revokeExternalWith(win.WindowID, false)
		return false, fmt.Errorf("pane control lease revoked: %s; claim the exact pane again", hardReason)
	}
	if input && !lease.Remote {
		current := foregroundProgram(win.ForegroundCmd)
		claimedProgram := foregroundProgram(lease.Foreground)
		if current != "" && claimedProgram != "" && current != claimedProgram {
			return true, errors.New("claimed pane is not at its original shell foreground; return it to a blank prompt before agent input (the session lease is retained)")
		}
	}
	return true, nil
}

func (g *ControlGuard) controlState(win tuiosctl.Window, own, session, socket string) paneControlState {
	if p, ok := g.pane(win.WindowID); ok {
		state := paneControlState{controlled: true, origin: "agent-created", scope: "session", inputAllowed: true}
		switch {
		case p.sealed:
			state.scope, state.inputAllowed, state.sealed = "one-call", false, true
			state.reason = "outside-path one-call grant expired; create a new pane with a session grant"
		case p.policy != controlPolicyStamp():
			state.inputAllowed = false
			state.reason = "standing sandbox grants changed; create a new controlled pane"
		case p.browser || g.paneIsBrowser(win):
			state.inputAllowed = false
			state.reason = "browser pane requires the browser tool"
		case win.WindowID == own:
			state.inputAllowed = false
			state.reason = "agent cannot input into its own pane"
		case paneIsAgent(win):
			state.inputAllowed = false
			state.reason = "agent and approval panes are operator-only"
		}
		return state
	}
	store, agent := g.sharedLeaseState()
	if store != nil {
		if shared, ok, _ := store.Get(session, win.WindowID); ok {
			state := paneControlState{
				controlled: shared.AgentID == agent.ID, origin: "operator-claimed", scope: shared.Scope,
				inputAllowed: shared.AgentID == agent.ID, ownerID: shared.AgentID, ownerLabel: shared.AgentLabel,
			}
			if shared.AgentID != agent.ID {
				state.reason = "exclusive write control belongs to " + shared.AgentLabel
				return state
			}
		}
	}
	claimed, err := g.checkExternalLease(win, session, socket, true, false)
	if claimed {
		lease, _ := g.externalLease(win.WindowID)
		state := paneControlState{controlled: true, origin: "operator-claimed", scope: lease.Scope, inputAllowed: err == nil, ownerID: lease.AgentID}
		if agent.Label != "" {
			state.ownerLabel = agent.Label
		}
		if err != nil {
			state.reason = err.Error()
		}
		return state
	}
	return paneControlState{}
}

func (g *ControlGuard) reapExternal(windows []tuiosctl.Window) {
	live := make(map[string]bool, len(windows))
	for _, win := range windows {
		live[win.WindowID] = true
	}
	if store, agent := g.sharedLeaseState(); store != nil {
		_ = store.PruneWindows(agent.SessionID, live)
	}
	g.mu.Lock()
	for id := range g.externalPanes {
		if !live[id] {
			delete(g.externalPanes, id)
		}
	}
	g.mu.Unlock()
}

func (t *TuiosTool) claimPane(ctx context.Context, callID string, raw json.RawMessage, a tuiosArgs) (agentcore.AgentToolResult, error) {
	if (a.Scope != panecontrol.ScopeOnce && a.Scope != panecontrol.ScopeSession) || !a.HostControl {
		return tuiosError(errors.New("claim requires scope=one-call|session and host_control:true")), nil
	}
	if !agentcore.OperatorConfirmedFor(ctx, callID, t.Name(), raw) {
		return tuiosError(errors.New("claim requires your exact confirmation; switch /permissions to ask and approve this pane for the current agent session")), nil
	}
	if !agentcore.HostControlGrantedFromContext(ctx) {
		return tuiosError(errors.New("claim did not receive the reviewed host-control capability")), nil
	}
	c, err := t.newClient(ctx)
	if err != nil {
		return tuiosError(err), nil
	}
	defer c.Close()
	win, err := exactWindow(ctx, c, a.Window)
	if err != nil {
		return tuiosError(err), nil
	}
	lease, err := t.Control.claimExternalWith(c, win, c.PaneID(), a.TargetIdentity, a.Scope, a.Transfer)
	if err != nil {
		return tuiosError(err), nil
	}
	out := map[string]any{
		"claimed": true, "window": lease.WindowID, "control_origin": "operator-claimed",
		"control_scope": lease.Scope, "input_allowed": true, "session": lease.SessionID,
		"remote": lease.Remote, "created_at": lease.CreatedAt.UTC().Format(time.RFC3339Nano),
		"note": "The lease ends with this agent session and does not approve later commands; each action is still reviewed.",
	}
	if lease.HostHint != "" {
		out["host_hint"] = lease.HostHint
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(out))}, Details: out}, nil
}

func (t *TuiosTool) revokePane(ctx context.Context, a tuiosArgs) (agentcore.AgentToolResult, error) {
	c, err := t.newClient(ctx)
	if err != nil {
		return tuiosError(err), nil
	}
	defer c.Close()
	win, err := exactWindow(ctx, c, a.Window)
	if err != nil {
		return tuiosError(err), nil
	}
	revoked := t.Control.revokeExternal(win.WindowID)
	out := map[string]any{"revoked": revoked, "window": win.WindowID, "control_scope": "session"}
	if !revoked {
		out["note"] = "No operator-claimed lease existed for this pane in the current agent session."
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(out))}, Details: out}, nil
}
