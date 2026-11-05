package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/panecontrol"
	"github.com/getan/golder/internal/tuiosctl"
)

func TestExternalPaneClaimNeedsOperatorAndPersistsForSession(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	tool := NewTuiosTool(t.TempDir(), true)
	raw := json.RawMessage(`{"action":"claim","window":"w2","scope":"session","host_control":true}`)

	cfg := newExecCfg(t, tool)
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "claim-denied", Name: "tuios", Arguments: raw}, nil)
	if !msg.IsError || len(tool.Control.externalPanes) != 0 {
		t.Fatalf("claim without exact operator confirmation ran: %+v leases=%v", msg, tool.Control.externalPanes)
	}

	cfg.BeforeToolCall = func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if !call.RequireOperatorConfirmation {
			t.Fatal("claim did not request exact operator confirmation")
		}
		var prepared tuiosArgs
		if err := json.Unmarshal(call.Arguments, &prepared); err != nil {
			t.Fatal(err)
		}
		if prepared.Window != "w2" || len(prepared.TargetIdentity) != 64 {
			t.Fatalf("claim review did not receive a pinned identity: %+v", prepared)
		}
		return &agentcore.BeforeToolCallDecision{OperatorConfirmed: true, HostControl: true}
	}
	msg, _ = executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "claim-approved", Name: "tuios", Arguments: raw}, nil)
	if msg.IsError || !strings.Contains(agentcore.ContentToText(msg.Content), `"claimed":true`) {
		t.Fatalf("approved claim failed: %s", agentcore.ContentToText(msg.Content))
	}

	// The next input has no host_control bit or grant. The session lease, not the
	// earlier one-call context, is what authorizes it. It still passes through the
	// per-command review hook.
	reviewed := false
	cfg.BeforeToolCall = func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		var a tuiosArgs
		_ = json.Unmarshal(call.Arguments, &a)
		if a.Action == "send" {
			reviewed = true
			if a.HostControl || call.RequireOperatorConfirmation {
				t.Fatalf("leased command requested standing authority again: %+v", a)
			}
		}
		return nil
	}
	msg, _ = executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "send-after-claim", Name: "tuios", Arguments: json.RawMessage(`{"action":"send","window":"w2","text":"echo ok","submit":true}`)}, nil)
	if msg.IsError || !reviewed || !f.seen("send-text") {
		t.Fatalf("claimed pane did not remain reviewed and controllable: %+v reviewed=%v", msg, reviewed)
	}
	res, err := tool.Execute(context.Background(), "list", json.RawMessage(`{"action":"list"}`), nil)
	text := agentcore.ContentToText(res.Content)
	for _, want := range []string{`"window_id":"w2"`, `"controlled":true`, `"control_origin":"operator-claimed"`, `"control_scope":"session"`, `"input_allowed":true`} {
		if err != nil || res.IsError || !strings.Contains(text, want) {
			t.Fatalf("claimed list missing %s: %s (%v)", want, text, err)
		}
	}

	res, err = tool.Execute(context.Background(), "revoke", json.RawMessage(`{"action":"revoke","window":"w2"}`), nil)
	if err != nil || res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), `"revoked":true`) {
		t.Fatalf("revoke failed: %+v %v", res, err)
	}
	res, _ = tool.Execute(context.Background(), "send-after-revoke", json.RawMessage(`{"action":"send","window":"w2","text":"echo no"}`), nil)
	if !res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), "not controlled") {
		t.Fatalf("revoked pane remained controllable: %s", agentcore.ContentToText(res.Content))
	}

	// A new tool/guard is another agent session even when it connects to the same
	// tuios session and pane.
	other := NewTuiosTool(t.TempDir(), true)
	res, _ = other.Execute(context.Background(), "other-session", json.RawMessage(`{"action":"send","window":"w2","text":"echo no"}`), nil)
	if !res.IsError {
		t.Fatal("pane lease leaked into another agent session")
	}
}

func TestPaneClaimPinsIdentityAcrossReview(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	host := "nas-a"
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"focused_window_id": "w2", "windows": []map[string]any{
				{"window_id": "w1", "display_name": "golder"},
				{"window_id": "w2", "display_name": "nas", "cwd_host": host, "foreground_cmd": "ssh root@" + host},
			}}
		}
		return map[string]any{"type": "ok"}
	}
	tool := NewTuiosTool(t.TempDir(), true)
	raw := json.RawMessage(`{"action":"claim","window":"w2","scope":"session","host_control":true}`)
	prepared, err := tool.PrepareCall(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithHostControlGrant(context.Background())
	ctx = agentcore.WithOperatorConfirmation(ctx, "claim", tool.Name(), prepared)
	host = "nas-b"
	res, err := tool.Execute(ctx, "claim", prepared, nil)
	if err != nil || !res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), "identity changed") {
		t.Fatalf("changed SSH target spent earlier confirmation: %+v %v", res, err)
	}
	if len(tool.Control.externalPanes) != 0 {
		t.Fatal("identity race created a lease")
	}
}

func TestPaneClaimRejectsProtectedTargetsBeforeReview(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"windows": []map[string]any{
				{"window_id": "w1", "display_name": "self"},
				{"window_id": "shell", "display_name": "shell", "agent_state": "none"},
				{"window_id": "agent", "display_name": "agent", "agent_state": "idle"},
				{"window_id": "browser", "display_name": "browser", "foreground_cmd": "kitty-browse"},
				{"window_id": "approval", "display_name": authorizationPanePrefix + " consent"},
				{"window_id": "computer", "display_name": computeruse.OperatorPanePrefix + "fixture"},
			}}
		}
		return map[string]any{"type": "ok"}
	}
	tool := NewTuiosTool(t.TempDir(), true)
	for _, id := range []string{"w1", "agent", "browser", "approval", "computer"} {
		raw := json.RawMessage(`{"action":"claim","window":"` + id + `","scope":"session","host_control":true}`)
		if _, err := tool.PrepareCall(context.Background(), raw); err == nil {
			t.Errorf("protected target %q reached review", id)
		}
	}
	if _, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"claim","window":"agent","scope":"session","host_control":true}`)); err == nil {
		t.Fatal("agent target was claimable")
	}
	if _, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"claim","window":"shell","scope":"session","host_control":true}`)); err != nil {
		t.Fatalf("the daemon's explicit none state made an ordinary shell look like an Agent: %v", err)
	}
}

func TestPaneLeaseRetainsManualLocalUseAndRevokesRemoteChange(t *testing.T) {
	_, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	g := NewControlGuard(t.TempDir())

	local := tuiosctl.Window{WindowID: "local", ForegroundCmd: "zsh"}
	identity, err := g.claimTargetIdentity(c, local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.claimExternal(c, local, "w1", identity); err != nil {
		t.Fatal(err)
	}
	busy := local
	busy.ForegroundCmd = "vim notes.txt"
	if err = g.checkPaneAt(context.Background(), busy, "w1", c.Session(), c.Socket(), false, true); err == nil || !strings.Contains(err.Error(), "lease is retained") {
		t.Fatalf("manual foreground was not a temporary refusal: %v", err)
	}
	if _, ok := g.externalLease(local.WindowID); !ok {
		t.Fatal("manual input revoked the session lease")
	}
	if err = g.checkPaneAt(context.Background(), local, "w1", c.Session(), c.Socket(), false, true); err != nil {
		t.Fatalf("returning to the shell did not restore use: %v", err)
	}

	g.revokeExternal(local.WindowID)
	remote := tuiosctl.Window{WindowID: "remote", ForegroundCmd: "ssh root@nas-a", CwdHost: "nas-a"}
	identity, _ = g.claimTargetIdentity(c, remote)
	if _, err = g.claimExternal(c, remote, "w1", identity); err != nil {
		t.Fatal(err)
	}
	granted := agentcore.WithNetworkGrant(context.Background())
	if err = g.checkPaneAt(granted, remote, "w1", c.Session(), c.Socket(), false, true); err != nil {
		t.Fatalf("claimed SSH pane was unusable: %v", err)
	}
	changed := remote
	changed.CwdHost, changed.ForegroundCmd = "nas-b", "ssh root@nas-b"
	if err = g.checkPaneAt(granted, changed, "w1", c.Session(), c.Socket(), false, true); err == nil || !strings.Contains(err.Error(), "lease revoked") {
		t.Fatalf("SSH host change kept the lease: %v", err)
	}
	if _, ok := g.externalLease(remote.WindowID); ok {
		t.Fatal("revoked SSH lease remained in the table")
	}
}

func TestPaneLeaseUsesFullIDAndReapsClosedPane(t *testing.T) {
	_, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	g := NewControlGuard(t.TempDir())
	old := tuiosctl.Window{WindowID: "full-old", DisplayName: "nas", ForegroundCmd: "zsh"}
	identity, _ := g.claimTargetIdentity(c, old)
	if _, err = g.claimExternal(c, old, "w1", identity); err != nil {
		t.Fatal(err)
	}
	newPane := tuiosctl.Window{WindowID: "full-new", DisplayName: "nas", ForegroundCmd: "zsh"}
	if err = g.checkPaneAt(context.Background(), newPane, "w1", c.Session(), c.Socket(), false, true); err == nil {
		t.Fatal("same-name new pane reused the old lease")
	}
	g.reapExternal([]tuiosctl.Window{newPane})
	if _, ok := g.externalLease(old.WindowID); ok {
		t.Fatal("closed pane lease was not reaped")
	}
}

func TestOneCallPathGrantCannotCreateReusablePane(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	tool := NewTuiosTool(t.TempDir(), true)
	for _, ctx := range []context.Context{
		agentcore.WithReadGrant(context.Background(), t.TempDir()),
		agentcore.WithWriteGrant(context.Background(), t.TempDir()),
	} {
		res, err := tool.Execute(ctx, "new", json.RawMessage(`{"action":"new","argv":["sh"]}`), nil)
		if err != nil || !res.IsError || !strings.Contains(agentcore.ContentToText(res.Content), "one-call path grant") {
			t.Fatalf("one-call pane creation was not rejected clearly: %+v %v", res, err)
		}
	}
	if f.seen("new-window") {
		t.Fatal("daemon created a pane before one-call grant rejection")
	}
}

func TestTuiosListExposesSealedPaneState(t *testing.T) {
	_, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	tool := NewTuiosTool(t.TempDir(), true)
	tool.Control.panes["w2"] = controlledPane{isolated: true, sealed: true, policy: controlPolicyStamp()}
	res, err := tool.Execute(context.Background(), "list", json.RawMessage(`{"action":"list"}`), nil)
	text := agentcore.ContentToText(res.Content)
	for _, want := range []string{`"controlled":true`, `"control_origin":"agent-created"`, `"control_scope":"one-call"`, `"input_allowed":false`, `"sealed":true`, `"control_reason"`} {
		if err != nil || res.IsError || !strings.Contains(text, want) {
			t.Fatalf("sealed list missing %s: %s (%v)", want, text, err)
		}
	}
}

func TestClaimedSSHPaneUsesScriptWithoutRepeatedHostControl(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	remote := tuiosctl.Window{WindowID: "w2", DisplayName: "nas", ForegroundCmd: "ssh root@nas", CwdHost: "nas"}
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"focused_window_id": "w2", "windows": []tuiosctl.Window{
				{WindowID: "w1", DisplayName: "golder"}, remote,
			}}
		}
		return map[string]any{"type": "ok"}
	}
	tool := NewTuiosTool(t.TempDir(), true)
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := tool.Control.claimTargetIdentity(c, remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tool.Control.claimExternal(c, remote, c.PaneID(), identity); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"run","window":"w2","script":"hostname\nid\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	var scriptCall tuiosArgs
	if err = json.Unmarshal(prepared, &scriptCall); err != nil {
		t.Fatal(err)
	}
	if scriptCall.HostControl || !scriptCall.Remote || scriptCall.Network != "on" {
		t.Fatalf("claimed SSH script was prepared incorrectly: %+v", scriptCall)
	}
	if err = tool.checkControl(agentcore.WithNetworkGrant(context.Background()), &scriptCall); err != nil {
		t.Fatalf("claimed SSH script still needed host_control: %v", err)
	}
	if err = validateRun(scriptCall); err != nil {
		t.Fatalf("valid run+script was rejected: %v", err)
	}

	prepared, err = tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"run","window":"w2","command":"hostname"}`))
	if err != nil {
		t.Fatal(err)
	}
	var commandCall tuiosArgs
	_ = json.Unmarshal(prepared, &commandCall)
	if err = tool.checkControl(agentcore.WithNetworkGrant(context.Background()), &commandCall); err != nil {
		t.Fatalf("lease validation failed before SSH command policy: %v", err)
	}
	if err = validateRun(commandCall); err == nil || !strings.Contains(err.Error(), "SSH commands require run with script") {
		t.Fatalf("ordinary command bypassed SSH script requirement: %v", err)
	}
}

func TestSharedPaneLeaseIsExclusiveAndTransferCancelsQueuedAuthority(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	target := tuiosctl.Window{WindowID: "nas", DisplayName: "NAS", Workspace: 1, ForegroundCmd: "zsh"}
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"windows": []tuiosctl.Window{
				{WindowID: "agent-a", DisplayName: "annotation-workbench", Workspace: 1},
				{WindowID: "agent-b", DisplayName: "label-studio", Workspace: 1}, target,
			}}
		}
		return map[string]any{"type": "ok"}
	}
	dial := func(pane string) *tuiosctl.Client {
		t.Helper()
		c, err := tuiosctl.Dial(context.Background(), tuiosctl.Options{Socket: socket, Session: "work", PaneID: pane, Version: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cA, cB := dial("agent-a"), dial("agent-b")
	defer cA.Close()
	defer cB.Close()
	projects := t.TempDir()
	rootA, rootB := filepath.Join(projects, "annotation-workbench"), filepath.Join(projects, "label-studio")
	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatal(err)
	}
	gA, gB := NewControlGuard(rootA), NewControlGuard(rootB)
	defer gA.stopAgentRegistration()
	defer gB.stopAgentRegistration()
	if err := gA.configureAgent(cA, tuiosctl.Window{WindowID: "agent-a", Workspace: 1}, "conv-a"); err != nil {
		t.Fatal(err)
	}
	if err := gB.configureAgent(cB, tuiosctl.Window{WindowID: "agent-b", Workspace: 1}, "conv-b"); err != nil {
		t.Fatal(err)
	}
	idA, _ := gA.claimTargetIdentity(cA, target)
	idB, _ := gB.claimTargetIdentity(cB, target)
	if _, err := gA.claimExternalWith(cA, target, "agent-a", idA, "session", false); err != nil {
		t.Fatal(err)
	}
	if _, err := gB.claimExternalWith(cB, target, "agent-b", idB, "session", false); err == nil || !strings.Contains(err.Error(), "transfer=true") {
		t.Fatalf("second agent bypassed exclusive ownership: %v", err)
	}
	if _, err := gB.claimExternalWith(cB, target, "agent-b", idB, "session", true); err != nil {
		t.Fatalf("explicit transfer failed: %v", err)
	}
	if err := gA.checkPaneAt(context.Background(), target, "agent-a", "work", socket, false, true); err == nil || !strings.Contains(err.Error(), "label-studio") {
		t.Fatalf("old agent retained authority after transfer: %v", err)
	}
	if err := gB.checkPaneAt(context.Background(), target, "agent-b", "work", socket, false, true); err != nil {
		t.Fatalf("new agent lacked authority: %v", err)
	}
	state := gA.controlState(target, "agent-a", "work", socket)
	if state.controlled || state.ownerID == "" || !strings.Contains(state.ownerLabel, "label-studio") {
		t.Fatalf("other controller was hidden from list state: %+v", state)
	}

	// Agent A obtains a fresh session lease and passes the first (pre-queue)
	// check. A transfer while it waits must make the second check fail before any
	// bytes are sent.
	if _, err := gA.claimExternalWith(cA, target, "agent-a", idA, "session", true); err != nil {
		t.Fatal(err)
	}
	if err := gA.checkPaneAtLease(context.Background(), target, "agent-a", "work", socket, false, true, false); err != nil {
		t.Fatalf("pre-queue authority failed: %v", err)
	}
	if _, err := gB.claimExternalWith(cB, target, "agent-b", idB, "session", true); err != nil {
		t.Fatal(err)
	}
	if err := gA.checkPaneAtLease(context.Background(), target, "agent-a", "work", socket, false, true, true); err == nil {
		t.Fatal("queued input survived a control transfer")
	}

	// A one-call lease is visible during preflight and consumed only by the
	// revalidation immediately before input.
	if _, err := gB.claimExternalWith(cB, target, "agent-b", idB, "one-call", false); err != nil {
		t.Fatal(err)
	}
	if err := gB.checkPaneAtLease(context.Background(), target, "agent-b", "work", socket, false, true, false); err != nil {
		t.Fatalf("one-call preflight failed: %v", err)
	}
	if err := gB.checkPaneAtLease(context.Background(), target, "agent-b", "work", socket, false, true, true); err != nil {
		t.Fatalf("one-call operation failed: %v", err)
	}
	if err := gB.checkPaneAtLease(context.Background(), target, "agent-b", "work", socket, false, true, false); err == nil {
		t.Fatal("one-call lease was not consumed")
	}
}

func TestPaneSlashUsesCurrentAgentAndCanExplicitlyTransferToAnother(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	target := tuiosctl.Window{WindowID: "nas", DisplayName: "nas-terminal", Workspace: 1, ForegroundCmd: "zsh"}
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"windows": []tuiosctl.Window{
				{WindowID: "w1", Workspace: 1, AgentState: "idle"},
				{WindowID: "agent-b", Workspace: 1, AgentState: "idle"},
				target,
			}}
		}
		return map[string]any{"type": "ok"}
	}
	root := filepath.Join(t.TempDir(), "annotation-workbench")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewTuiosTool(root, true)
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if err = tool.Control.configureAgent(c, tuiosctl.Window{WindowID: "w1", Workspace: 1}, "conversation-a"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	defer tool.Control.stopAgentRegistration()
	tools := []agentcore.AgentTool{tool}
	if got := PaneCommand(tools, "agents"); !strings.Contains(got, "annotation-workbench") {
		t.Fatalf("agent list = %q", got)
	}
	if got := PaneCommand(tools, "allow-agent nas session"); !strings.Contains(got, "granted to annotation-workbench") {
		t.Fatalf("current-agent grant = %q", got)
	}
	if got := PaneCommand(tools, "allow-agent nas-terminal session"); !strings.Contains(got, "exact full window id") {
		t.Fatalf("display name was accepted as authority: %q", got)
	}
	if got := PaneCommand(tools, "status"); !strings.Contains(got, "nas-terminal") || !strings.Contains(got, "annotation-workbench") {
		t.Fatalf("pane status = %q", got)
	}

	store, current := tool.Control.sharedLeaseState()
	other := panecontrol.Agent{ID: "label-studio-main-9c21", Label: "label-studio · Main Agent", Project: "label-studio", Role: "main",
		ConversationID: "conversation-b", SessionID: "work", PaneID: "agent-b", Workspace: 1, Mode: "ask", PID: os.Getpid()}
	if err := store.Register(other); err != nil {
		t.Fatal(err)
	}
	cmd := "allow-agent nas session --agent " + other.ID
	if got := PaneCommand(tools, cmd); !strings.Contains(got, "transfer=true") {
		t.Fatalf("transfer without confirmation flag = %q", got)
	}
	if got := PaneCommand(tools, cmd+" --transfer"); !strings.Contains(got, "granted to label-studio") {
		t.Fatalf("explicit transfer = %q", got)
	}
	if _, err := store.Authorize("work", "nas", current.ID, current.ConversationID, false); err == nil {
		t.Fatal("slash transfer left the previous Agent authorized")
	}
	if got := PaneCommand(tools, "revoke-agent nas"); !strings.Contains(got, "revoked") {
		t.Fatalf("operator could not revoke another Agent: %q", got)
	}
	if _, err := store.Authorize("work", "nas", other.ID, other.ConversationID, false); err == nil {
		t.Fatal("operator revoke left the transferred Agent authorized")
	}
}

func TestOneCallLeaseIsConsumedByNonInputPaneMutation(t *testing.T) {
	f, socket := startTuiosFake(t)
	useTuiosFake(t, socket)
	target := tuiosctl.Window{WindowID: "w2", Workspace: 1, ForegroundCmd: "zsh"}
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			return map[string]any{"windows": []tuiosctl.Window{{WindowID: "w1", Workspace: 1}, target}}
		}
		return map[string]any{"type": "ok"}
	}
	tool := NewTuiosTool(t.TempDir(), true)
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if err := tool.Control.configureAgent(c, tuiosctl.Window{WindowID: "w1", Workspace: 1}, "conversation"); err != nil {
		t.Fatal(err)
	}
	defer tool.Control.stopAgentRegistration()
	identity, err := tool.Control.claimTargetIdentity(c, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Control.claimExternalWith(c, target, "w1", identity, panecontrol.ScopeOnce, false); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()

	res, err := tool.Execute(context.Background(), "focus", json.RawMessage(`{"action":"focus","window":"w2"}`), nil)
	if err != nil || res.IsError || !f.seen("focus-window") {
		t.Fatalf("one-call focus failed: %+v err=%v", res, err)
	}
	store, agent := tool.Control.sharedLeaseState()
	if _, err := store.Authorize("work", "w2", agent.ID, agent.ConversationID, false); err == nil {
		t.Fatal("one-call lease survived a focus operation")
	}
}
