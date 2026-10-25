package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/tuiosctl"
)

// Protocol-only legacy fixtures deliberately model an explicitly authorized
// host. Security tests below use the real default Auto guard, never this helper.
func trustFakeControl(g *ControlGuard) { g.Mode.Set(permissions.FullAccess) }

type controlRunner struct {
	argv    []string
	policy  seatbelt.CallPolicy
	fail    bool
	cleaned bool
}

func (r *controlRunner) SandboxCall(argv []string, _ string, p seatbelt.CallPolicy) ([]string, func(), error) {
	r.argv = append([]string(nil), argv...)
	r.policy = p
	if r.fail {
		return nil, nil, errors.New("sandbox unavailable")
	}
	return append([]string{"fake-sandbox"}, argv...), func() { r.cleaned = true }, nil
}

func TestControlGuardDefaultAndPerCallHostAuthority(t *testing.T) {
	g := NewControlGuard(t.TempDir())
	w := tuiosctl.Window{WindowID: "user-shell"}
	ctx := context.Background()
	for _, request := range []bool{false, true} {
		if g.checkPane(ctx, w, "agent", request, true) == nil {
			t.Fatal("argument granted host authority")
		}
	}
	granted := agentcore.WithNetworkGrant(agentcore.WithHostControlGrant(ctx))
	if g.checkPane(granted, w, "agent", false, true) == nil {
		t.Fatal("grant used without an explicit request")
	}
	if err := g.checkPane(granted, w, "agent", true, true); err != nil {
		t.Fatal(err)
	}
	if g.checkPane(ctx, w, "agent", true, true) == nil {
		t.Fatal("host grant leaked to next call")
	}
	if g.checkPane(agentcore.WithSandboxRequest(granted), w, "agent", true, true) == nil {
		t.Fatal("unisolated host satisfied containment")
	}
}

func TestControlGuardCannotAnswerApprovals(t *testing.T) {
	g := NewControlGuard(t.TempDir())
	ctx := agentcore.WithHostControlGrant(context.Background())
	for _, w := range []tuiosctl.Window{
		{WindowID: "self"}, {WindowID: "other", AgentState: "needs_input"}, {WindowID: "other", ForegroundCmd: "golder"},
	} {
		if g.checkPane(ctx, w, "self", true, true) == nil {
			t.Fatalf("allowed approval/agent input: %+v", w)
		}
	}
}

func TestControlGuardManagedCeiling(t *testing.T) {
	g := NewControlGuard(t.TempDir())
	g.panes["helper"] = controlledPane{isolated: true, network: true, policy: controlPolicyStamp()}
	w := tuiosctl.Window{WindowID: "helper"}
	if g.checkPane(context.Background(), w, "self", false, true) == nil {
		t.Fatal("network-enabled pane used without network grant")
	}
	ctx := agentcore.WithNetworkGrant(context.Background())
	if err := g.checkPane(ctx, w, "self", false, true); err != nil {
		t.Fatal(err)
	}
	p := g.panes["helper"]
	p.sealed = true
	g.panes["helper"] = p
	if g.checkPane(ctx, w, "self", false, true) == nil {
		t.Fatal("one-call grant became reusable")
	}
	p.sealed = false
	p.policy = [32]byte{}
	g.panes["helper"] = p
	if g.checkPane(ctx, w, "self", false, true) == nil {
		t.Fatal("stale sandbox policy remained usable")
	}
	p.policy = controlPolicyStamp()
	p.browser = true
	g.panes["helper"] = p
	if g.checkPane(ctx, w, "self", false, true) == nil {
		t.Fatal("browser policy bypassed using PTY input")
	}
}

func TestControlLaunchFailsClosedAndScrubsEnvironment(t *testing.T) {
	g := NewControlGuard(t.TempDir())
	g.Runner = nil
	if _, _, _, err := g.launchArgv(context.Background(), []string{"sh"}, g.Root, false); err == nil {
		t.Fatal("missing runner ran bare")
	}
	r := &controlRunner{}
	g.Runner = r
	t.Setenv("CONTROL_TEST_API_TOKEN", "must-not-inherit")
	if _, _, _, err := g.launchArgv(context.Background(), []string{"sh"}, g.Root, true); err == nil {
		t.Fatal("network argument became authorization")
	}
	argv, cleanup, isolated, err := g.launchArgv(agentcore.WithNetworkGrant(context.Background()), []string{"sh", "-c", "echo ok"}, g.Root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !isolated || argv[0] != "fake-sandbox" || !r.policy.Network {
		t.Fatalf("wrong envelope: %v, %+v", argv, r.policy)
	}
	if len(r.argv) < 2 || r.argv[1] != "-i" || strings.Contains(strings.Join(r.argv, " "), "must-not-inherit") {
		t.Fatal("daemon environment was not scrubbed")
	}
}

func TestControlLaunchSendsSandboxAndNoDaemonGrants(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "new-window" {
			return map[string]any{"window_id": "created"}
		}
		return map[string]any{"type": "ok"}
	}
	g := NewControlGuard(t.TempDir())
	r := &controlRunner{}
	g.Runner = r
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = g.launch(context.Background(), c, tuiosctl.NewWindowParams{Cwd: g.Root, Command: []string{"sh"}}, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer g.release("created")
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, verb := range f.verbs {
		if verb == "new-window" {
			p := f.params[i]
			cmd, _ := p["command"].([]any)
			grants, _ := p["grants"].([]any)
			if len(cmd) == 0 || cmd[0] != "fake-sandbox" || len(grants) != 1 || grants[0] != "none" {
				t.Fatalf("unprotected daemon spawn: %v", p)
			}
			return
		}
	}
	t.Fatal("no launch")
}

func TestTuiosControlRefusesUnownedBeforeWriting(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	tool := NewTuiosTool(t.TempDir(), true)
	for _, raw := range []string{`{"action":"run","window":"w2","command":"echo ok"}`, `{"action":"send","window":"w2","text":"yes","host_control":true}`, `{"action":"press","window":"w1","keys":"Enter","host_control":true}`} {
		res, err := tool.Execute(context.Background(), "x", json.RawMessage(raw), nil)
		if err != nil || !res.IsError {
			t.Fatalf("unowned input accepted: %s %+v %v", raw, res, err)
		}
	}
	if f.seen("run") || f.seen("send-text") || f.seen("send-keys") {
		t.Fatal("input reached daemon before authorization")
	}
}

func TestBrowserControlNeedsNetworkAndOwnership(t *testing.T) {
	f := &fakeBrowser{replies: map[string]string{}}
	tool := NewBrowserTool(t.TempDir(), true)
	tool.newClient = func() (*browserctl.Client, error) { return browserctl.NewWithTransport("stub", f), nil }
	call := json.RawMessage(`{"action":"click","ref":"@e1"}`)
	for _, ctx := range []context.Context{context.Background(), agentcore.WithNetworkGrant(context.Background())} {
		res, _ := tool.Execute(ctx, "x", call, nil)
		if !res.IsError {
			t.Fatal("unowned browser accepted")
		}
	}
	if len(f.actions) != 0 {
		t.Fatal("browser action sent before authorization")
	}
	tool.boundSocket = "stub"
	tool.Control.browsers["browser-1"] = controlledBrowser{socket: canonicalBrowserSocket("stub"), isolated: true, policy: controlPolicyStamp()}
	res, err := tool.Execute(agentcore.WithNetworkGrant(context.Background()), "owned", call, nil)
	if err != nil || res.IsError || len(f.actions) != 1 {
		t.Fatalf("owned authorized browser could not act: %+v %v", res, err)
	}
}

func TestControlledBrowserProfilesArePrivateToSession(t *testing.T) {
	root := shortRoot(t)
	if err := ConfigureEmbeddedBrowser(os.Args[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureEmbeddedBrowser("") })
	a, err := controlledBrowserSpec(NewControlGuard(root), root, "browser", "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	b, err := controlledBrowserSpec(NewControlGuard(root), root, "browser", "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if a.Profile == b.Profile || a.Profile == browserProfileDir(root, "browser") {
		t.Fatal("shared login profile reused")
	}
	if len(a.Socket) > unixSocketPathLimit {
		t.Fatal("private profile made socket too long")
	}
}

// No PTY is needed: run the exact argv that would be handed to the daemon.
func TestControlledPaneActualOSIsolation(t *testing.T) {
	if !seatbelt.Available() {
		t.Skip("OS sandbox not available")
	}
	root := t.TempDir()
	outside := t.TempDir()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	secret := filepath.Join(outside, "fixture.txt")
	if err := os.WriteFile(secret, []byte("private-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	g := NewControlGuard(root)
	// First prove that this envelope can actually run a normal command. A
	// broken sandbox startup must not masquerade as a successful negative test.
	t.Setenv("CONTROL_TEST_API_TOKEN", "do-not-inherit")
	baseline, cleanBaseline, _, err := g.launchArgv(context.Background(), []string{"sh", "-c", "printf 'ok:%s' \"${CONTROL_TEST_API_TOKEN-unset}\""}, root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanBaseline()
	baselineCmd := exec.Command(baseline[0], baseline[1:]...)
	baselineCmd.Dir = root // the daemon also sets the pane's cwd before spawn
	baseOut, baseErr := baselineCmd.CombinedOutput()
	if strings.Contains(string(baseOut), "sandbox_apply: Operation not permitted") {
		t.Skip("parent sandbox does not permit nested sandbox_apply")
	}
	if baseErr != nil || string(baseOut) != "ok:unset" {
		t.Fatalf("sandbox baseline failed: %s (%v)", baseOut, baseErr)
	}
	argv, cleanup, _, err := g.launchArgv(context.Background(), []string{"sh", "-c", "cat " + quoteControlArg(secret)}, root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "sandbox_apply: Operation not permitted") {
		t.Skip("parent sandbox does not permit nested sandbox_apply")
	}
	if err == nil || strings.Contains(string(out), "private-fixture") {
		t.Fatalf("outside fixture leaked: %s (%v)", out, err)
	}
}

func TestTuiosPinsTargetBeforeReview(t *testing.T) {
	f, sock := startTuiosFake(t)
	useTuiosFake(t, sock)
	var changed atomic.Bool
	f.answer = func(verb string, _ map[string]any) any {
		if verb == "list-windows" {
			focus := "w2"
			if changed.Load() {
				focus = "w3"
			}
			return map[string]any{"focused_window_id": focus, "windows": []map[string]any{{"window_id": "w2"}, {"window_id": "w3"}}}
		}
		return map[string]any{"type": "ok"}
	}
	tool := NewTuiosTool(t.TempDir(), true)
	tool.Control.panes["w2"] = controlledPane{cwd: tool.cwd, isolated: true, policy: controlPolicyStamp()}
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		var a tuiosArgs
		_ = json.Unmarshal(call.Arguments, &a)
		if a.Window != "w2" {
			t.Fatalf("review saw an unpinned target: %+v", a)
		}
		changed.Store(true)
		return nil
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "x", Name: "tuios", Arguments: json.RawMessage(`{"action":"send","text":"echo ok"}`)}, nil)
	if msg.IsError {
		t.Fatal(agentcore.ContentToText(msg.Content))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, verb := range f.verbs {
		if verb == "send-text" {
			if f.params[i]["window"] != "w2" {
				t.Fatal("focus change redirected input")
			}
			return
		}
	}
	t.Fatal("no approved input")
}

func TestBrowserPinsIdentityTabAndPageBeforeReview(t *testing.T) {
	tool := newFakeBrowserTool(t, true, t.TempDir())
	tool.boundSocket = "stub"
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		var a browserArgs
		_ = json.Unmarshal(call.Arguments, &a)
		if a.Browser != "browser-1" || a.Tab != "tab-2" || a.PageURL != "http://localhost:3000/login" {
			t.Fatalf("review did not see actual page: %+v", a)
		}
		return &agentcore.BeforeToolCallDecision{Block: true}
	}
	msg, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "x", Name: "browser", Arguments: json.RawMessage(`{"action":"click","ref":"@e1"}`)}, nil)
	if !msg.IsError {
		t.Fatal("review denial was lost")
	}
}
