package agenttool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/panecontrol"
	"github.com/getan/golder/internal/tuiosctl"
)

func paneTool(tools []agentcore.AgentTool) *TuiosTool {
	for _, raw := range tools {
		if tool, ok := raw.(*TuiosTool); ok && tool.allowControl && tool.Control != nil {
			return tool
		}
	}
	return nil
}

func paneCommandTarget(windows []tuiosctl.Window, raw string) (tuiosctl.Window, error) {
	raw = strings.TrimSpace(raw)
	for _, win := range windows {
		if win.WindowID == raw {
			return win, nil
		}
	}
	return tuiosctl.Window{}, fmt.Errorf("pane target %q is missing; use the exact full window id", raw)
}

func paneCommandAgents(store *panecontrol.Store, session string, workspace int, windows []tuiosctl.Window) ([]panecontrol.Agent, error) {
	agents, err := store.Agents(session, workspace)
	if err != nil {
		return nil, err
	}
	live := make(map[string]tuiosctl.Window, len(windows))
	for _, win := range windows {
		live[win.WindowID] = win
	}
	out := agents[:0]
	for _, agent := range agents {
		win, ok := live[agent.PaneID]
		if ok && win.Workspace == workspace && win.AgentState != "" && win.AgentState != "none" &&
			win.AgentState != "needs_input" && win.AgentState != "needs-input" {
			out = append(out, agent)
		}
	}
	return out, nil
}

// PaneCommand is the user-authored slash-command path. Unlike an agent tool
// call, the person already selected this top-level agent by typing in its own
// composer, so no agent id argument is needed and no model can reach this path.
func PaneCommand(tools []agentcore.AgentTool, args string) string {
	tool := paneTool(tools)
	if tool == nil {
		return "pane control is unavailable outside a top-level tuios agent session"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := tool.newClient(ctx)
	if err != nil {
		return "pane: " + err.Error()
	}
	defer c.Close()
	list, err := c.ListWindows(ctx)
	if err != nil {
		return "pane: " + err.Error()
	}
	tool.Control.touchAgent(c, list.Windows)
	tool.Control.reapExternal(list.Windows)
	store, agent := tool.Control.sharedLeaseState()
	if store == nil || agent.ID == "" {
		return "pane: current agent is not registered; restart this top-level Golder session"
	}
	fields := strings.Fields(args)
	if len(fields) == 0 || fields[0] == "status" {
		var b strings.Builder
		fmt.Fprintf(&b, "Current agent: %s (%s)\n", agent.Label, agent.ID)
		for _, win := range list.Windows {
			state := tool.Control.controlState(win, c.PaneID(), c.Session(), c.Socket())
			if state.ownerID == "" {
				continue
			}
			fmt.Fprintf(&b, "%s  %s  %s  %s\n", win.WindowID, win.DisplayName, state.ownerLabel, state.scope)
		}
		if b.Len() == 0 {
			return "No pane control leases in this workspace."
		}
		return strings.TrimRight(b.String(), "\n")
	}
	switch fields[0] {
	case "agents":
		agents, err := paneCommandAgents(store, c.Session(), agent.Workspace, list.Windows)
		if err != nil {
			return "pane: " + err.Error()
		}
		if len(agents) == 0 {
			return "No eligible top-level agents are online in this workspace."
		}
		var b strings.Builder
		for _, candidate := range agents {
			fmt.Fprintf(&b, "%s  %s  conversation=%s pane=%s\n", candidate.ID, candidate.Label, candidate.ConversationID, candidate.PaneID)
		}
		return strings.TrimRight(b.String(), "\n")
	case "allow-agent":
		if len(fields) < 3 {
			return "usage: /pane allow-agent <full-pane-id> <one-call|session> [--agent <agent-id>] [--transfer]"
		}
		win, err := paneCommandTarget(list.Windows, fields[1])
		if err != nil {
			return "pane: " + err.Error()
		}
		scope := fields[2]
		if scope != panecontrol.ScopeOnce && scope != panecontrol.ScopeSession {
			return "pane: scope must be one-call or session"
		}
		transfer, selected := false, agent
		for i := 3; i < len(fields); i++ {
			switch fields[i] {
			case "--transfer":
				transfer = true
			case "--agent":
				i++
				if i >= len(fields) {
					return "pane: --agent requires an exact agent id from /pane agents"
				}
				want := fields[i]
				agents, err := paneCommandAgents(store, c.Session(), agent.Workspace, list.Windows)
				if err != nil {
					return "pane: " + err.Error()
				}
				selected = panecontrol.Agent{}
				for _, candidate := range agents {
					if candidate.ID == want {
						selected = candidate
						break
					}
				}
				if selected.ID == "" {
					return "pane: selected agent is not an eligible online top-level agent in this workspace"
				}
			default:
				return "usage: /pane allow-agent <full-pane-id> <one-call|session> [--agent <agent-id>] [--transfer]"
			}
		}
		if err := tool.Control.claimRefusal(win, c.PaneID()); err != nil {
			return "pane: " + err.Error()
		}
		var lease panecontrol.Lease
		if selected.ID == agent.ID {
			identity, err := tool.Control.claimTargetIdentity(c, win)
			if err != nil {
				return "pane: " + err.Error()
			}
			local, err := tool.Control.claimExternalWith(c, win, c.PaneID(), identity, scope, transfer)
			if err != nil {
				return "pane: " + err.Error()
			}
			lease = panecontrol.Lease{WindowID: local.WindowID, Scope: local.Scope, AgentLabel: selected.Label}
		} else {
			daemonID, err := paneDaemonIdentity(c.Socket())
			if err != nil {
				return "pane: " + err.Error()
			}
			lease, err = store.Claim(panecontrol.ClaimRequest{Lease: panecontrol.Lease{
				SessionID: c.Session(), WindowID: win.WindowID, Workspace: win.Workspace,
				AgentID: selected.ID, ConversationID: selected.ConversationID, Scope: scope, DaemonID: daemonID,
				HostHint: win.CwdHost, Foreground: strings.TrimSpace(win.ForegroundCmd), Remote: tuiosRemoteHint(win),
			}, Transfer: transfer})
			if err != nil {
				return "pane: " + err.Error()
			}
		}
		return fmt.Sprintf("Pane %s write control granted to %s (%s). Commands remain individually reviewed.", lease.WindowID, selected.Label, lease.Scope)
	case "revoke-agent":
		if len(fields) != 2 {
			return "usage: /pane revoke-agent <full-pane-id>"
		}
		win, err := paneCommandTarget(list.Windows, fields[1])
		if err != nil {
			return "pane: " + err.Error()
		}
		revoked := tool.Control.revokeExternalWith(win.WindowID, true)
		if !revoked {
			return "pane: no write-control lease exists for this pane"
		}
		return "Pane write control revoked. Queued input will fail revalidation before send."
	default:
		return "usage: /pane [status|agents|allow-agent <full-pane-id> <one-call|session> [--agent <agent-id>] [--transfer]|revoke-agent <full-pane-id>]"
	}
}
