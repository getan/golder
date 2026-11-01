package agenttool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/tuiosctl"
)

// Input is pinned to a full id before claiming. A slow command on one server
// must not serialize a different server's requests. Entries disappear when
// the last holder/waiter leaves, including cancelled waits.
type tuiosInputClaim struct {
	held chan struct{}
	refs int
}

func (t *TuiosTool) claimInput(ctx context.Context, id string) (func(), error) {
	t.inputMu.Lock()
	if t.inputs == nil {
		t.inputs = make(map[string]*tuiosInputClaim)
	}
	p := t.inputs[id]
	if p == nil {
		p = &tuiosInputClaim{held: make(chan struct{}, 1)}
		t.inputs[id] = p
	}
	p.refs++
	t.inputMu.Unlock()
	drop := func() {
		t.inputMu.Lock()
		p.refs--
		if p.refs == 0 {
			delete(t.inputs, id)
		}
		t.inputMu.Unlock()
	}
	select {
	case p.held <- struct{}{}:
		return func() { <-p.held; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// CwdHost is an OSC report, not host authentication. Process names are also
// hints. Neither broadens authority; they prevent treating a remote path as a
// local reviewed cwd or claiming that a local sandbox contains remote effects.
func tuiosRemoteHint(w tuiosctl.Window) bool {
	if w.CwdHost != "" {
		return true
	}
	fields := strings.Fields(w.ForegroundCmd)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	name = name[strings.LastIndex(name, "/")+1:]
	switch name {
	case "ssh", "mosh", "mosh-client", "autossh":
		return true
	}
	return false
}

func tuiosRunUnknown(window string, err error) agentcore.AgentToolResult {
	out := map[string]any{"window": window, "execution_state": "unknown", "retryable": false, "error": err.Error(), "hint": "No fallback or resend was attempted. Inspect the exact pane before doing anything else: no_shell_integration may be reported AFTER a command ran. For a new SSH task at a blank prompt, use run with script."}
	return agentcore.AgentToolResult{IsError: true, Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(out))}, Details: out}
}

// Put routing into model-visible text, not just UI-only Details. Existing JSON
// result shapes remain JSON; raw terminal captures get a separate JSON header.
// Names and host hints are display data only. Full ids remain the routing key.
func tuiosTagResult(callID string, a tuiosArgs, result agentcore.AgentToolResult) agentcore.AgentToolResult {
	details, _ := result.Details.(map[string]any)
	if details == nil {
		details = map[string]any{}
	}
	window := a.Window
	if id, ok := details["window"].(string); ok && id != "" {
		window = id
	}
	origin := map[string]any{"request_id": callID, "action": a.Action, "session": tuiosctl.FromEnv().Session, "from_pane": tuiosctl.FromEnv().PaneID, "target_pane": window}
	if a.TargetLabel != "" {
		origin["name_hint"] = a.TargetLabel
	}
	if a.HostHint != "" {
		origin["host_hint"] = a.HostHint
	}
	details["origin"] = origin
	result.Details = details
	if len(result.Content) == 0 {
		return result
	}
	text, ok := result.Content[0].(agentcore.TextContent)
	if !ok {
		return result
	}
	var object map[string]any
	if json.Unmarshal([]byte(text.Text), &object) == nil && object != nil {
		if id, ok := object["window"].(string); ok && id != "" {
			origin["target_pane"] = id
		}
		object["origin"] = origin
		text.Text = tuiosJSON(object)
	} else {
		text.Text = tuiosJSON(map[string]any{"origin": origin}) + "\n" + text.Text
	}
	result.Content[0] = text
	return result
}
