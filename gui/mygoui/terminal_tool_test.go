package mygoui

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/gui/terminalaccess"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/session"
)

type recordingTerminalConn struct {
	mu     sync.Mutex
	writes []string
}

func (*recordingTerminalConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *recordingTerminalConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, string(b))
	return len(b), nil
}
func (*recordingTerminalConn) Close() error          { return nil }
func (*recordingTerminalConn) Resize(int, int) error { return nil }

func terminalToolFixture(t *testing.T) (*terminalTool, *terminalPane, *recordingTerminalConn) {
	t.Helper()
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_SANDBOX", "off")
	project := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctrl := appsession.New(appsession.Options{
		GUI: true, Cwd: project, Store: store, Model: "test", ProviderName: "fake",
		Provider:    &guiBlockingProvider{started: make(chan struct{}), release: make(chan struct{})},
		Credentials: provider.NewCredentialStore(nil),
		Tools:       []agentcore.AgentTool{&agenttool.TodoTool{Store: agenttool.NewTodoStore()}},
	})
	if err := ctrl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })
	a := testApp()
	a.ctrl, a.project, a.selectedProject = ctrl, project, project
	agent := a.registerTerminalAgent(ctrl)
	if agent == nil || agent.tool == nil {
		t.Fatal("terminal tool was not installed")
	}
	agent.tool.dispatch = func(ctx context.Context, fn func() (any, error)) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fn()
	}
	pane := &terminalPane{id: "test-pane", name: "private", project: agent.project, term: &terminal.Terminal{}}
	if err := a.terminalRegistry().AddPane(pane.id, pane.project); err != nil {
		t.Fatal(err)
	}
	source := &recordingTerminalConn{}
	pane.conn, err = terminalaccess.NewConnection(a.terminalRegistry(), pane.id, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.terminalProject(pane.project).root = &terminalLayout{pane: pane}
	return agent.tool, pane, source
}

func TestTerminalToolUserPaneRequiresSeparateGrantsAndExactReview(t *testing.T) {
	tool, pane, source := terminalToolFixture(t)
	ctx := context.Background()
	if _, err := tool.PrepareCall(ctx, json.RawMessage(`{"action":"capture","pane":"test-pane"}`)); err == nil {
		t.Fatal("private pane content was readable without a grant")
	}
	if err := tool.app.terminalRegistry().Grant(pane.id, tool.agent.id, false, true, false); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"action":"send","pane":"test-pane","text":"echo safe\r","host_control":false,"network":"off","control_origin":"agent-created","isolated":true}`)
	prepared, err := tool.PrepareCall(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var req terminalToolArgs
	if err := json.Unmarshal(prepared, &req); err != nil {
		t.Fatal(err)
	}
	if !req.HostControl || req.Network != "on" || req.ControlOrigin != "operator-claimed" || req.Isolated {
		t.Fatalf("prepared target facts: %+v", req)
	}
	if !tool.RequiresOperatorConfirmationFor(prepared) {
		t.Fatal("host input did not require operator confirmation")
	}
	result, _ := tool.Execute(ctx, "call", prepared, nil)
	if !result.IsError || len(source.writes) != 0 {
		t.Fatalf("ungated input ran: %+v %q", result, source.writes)
	}
	authorized := agentcore.WithExternalToolAuthorization(ctx)
	authorized = agentcore.WithHostControlGrant(authorized)
	authorized = agentcore.WithNetworkGrant(authorized)
	result, _ = tool.Execute(authorized, "call", prepared, nil)
	if !result.IsError || len(source.writes) != 0 {
		t.Fatal("input ran without exact operator confirmation")
	}
	authorized = agentcore.WithOperatorConfirmation(authorized, "call", "terminal", prepared)
	result, _ = tool.Execute(authorized, "call", prepared, nil)
	if result.IsError || len(source.writes) != 1 || source.writes[0] != "echo safe\r" {
		t.Fatalf("authorized input = %+v %q", result, source.writes)
	}
	tool.app.terminalRegistry().TakeBack(pane.id)
	result, _ = tool.Execute(authorized, "call", prepared, nil)
	if !result.IsError || len(source.writes) != 1 {
		t.Fatal("revoked control still accepted the previously reviewed input")
	}
}

func TestTerminalToolRejectsUnreviewableArguments(t *testing.T) {
	tool, _, _ := terminalToolFixture(t)
	for _, args := range []map[string]any{
		{"action": "new", "text": strings.Repeat("x", 8193)},
		{"action": "new", "argv": []string{strings.Repeat("x", 4097)}},
		{"action": "new", "network": "unknown"},
		{"action": "new", "name": strings.Repeat("x", 101)},
	} {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.PrepareCall(context.Background(), raw); err == nil {
			t.Errorf("accepted unreviewable arguments %q", raw)
		}
	}
}

func TestTerminalToolCaptureAndModeBoundaries(t *testing.T) {
	tool, pane, _ := terminalToolFixture(t)
	if err := tool.app.terminalRegistry().Grant(pane.id, tool.agent.id, true, false, false); err != nil {
		t.Fatal(err)
	}
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"capture","pane":"test-pane","lines":4}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithExternalToolAuthorization(context.Background())
	result, _ := tool.Execute(ctx, "read", prepared, nil)
	if result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), `"pane":"test-pane"`) {
		t.Fatalf("granted capture = %+v", result)
	}
	contained := agentcore.WithSandboxRequest(ctx)
	result, _ = tool.Execute(contained, "read", prepared, nil)
	if !result.IsError {
		t.Fatal("user terminal was treated as sandbox-contained")
	}
	tool.app.terminalRegistry().Revoke(pane.id, tool.agent.id)
	result, _ = tool.Execute(ctx, "read", prepared, nil)
	if !result.IsError {
		t.Fatal("capture still worked after revocation")
	}
}

func TestTerminalFocusDoesNotClaimHiddenPaneIsVisible(t *testing.T) {
	tool, pane, _ := terminalToolFixture(t)
	tool.guard.Mode.Set(permissions.FullAccess)
	if err := tool.app.terminalRegistry().Grant(pane.id, tool.agent.id, false, true, false); err != nil {
		t.Fatal(err)
	}
	tool.app.filesOpen = true
	req := terminalToolArgs{Action: "focus", Pane: pane.id, ControlOrigin: "operator-claimed", HostControl: true}
	result, _ := tool.executePane(context.Background(), pane, req)
	if !result.IsError || pane.focus || !tool.app.filesOpen || !strings.Contains(agentcore.ContentToText(result.Content), "hidden by the right-side layout") {
		t.Fatalf("hidden focus must fail without displacing Files: %+v", result)
	}
	tool.app.filesOpen = false
	result, _ = tool.executePane(context.Background(), pane, req)
	if result.IsError || !pane.focus {
		t.Fatalf("visible pane could not be focused: %+v", result)
	}
}

func TestTerminalReservationRevalidatesSplitAndCleansUp(t *testing.T) {
	tool, parent, _ := terminalToolFixture(t)
	registry := tool.app.terminalRegistry()
	if _, err := tool.app.reserveAgentTerminal(tool.agent, parent.id, "right", "child", agenttool.GUITerminalEnvelope{}); err == nil {
		t.Fatal("split without a parent lease was accepted")
	}
	if err := registry.Grant(parent.id, tool.agent.id, false, true, false); err != nil {
		t.Fatal(err)
	}
	child, err := tool.app.reserveAgentTerminal(tool.agent, parent.id, "down", "child", agenttool.GUITerminalEnvelope{Isolated: true})
	if err != nil {
		t.Fatal(err)
	}
	read, write, _ := registry.Access(child.id, tool.agent.id)
	if !read || !write || tool.app.findTerminalPane(parent.project, child.id) != child {
		t.Fatal("reserved Agent pane lost its identity or creator grants")
	}
	registry.TakeBack(parent.id)
	if _, err := tool.app.reserveAgentTerminal(tool.agent, parent.id, "right", "late", agenttool.GUITerminalEnvelope{}); err == nil {
		t.Fatal("split after user takeover was accepted")
	}
	tool.app.cleanupTerminalReservation(child)
	if tool.app.findTerminalPane(parent.project, child.id) != nil {
		t.Fatal("failed creation left its reserved pane in the layout")
	}
	if _, write, _ := registry.Access(child.id, tool.agent.id); write {
		t.Fatal("failed creation retained its lease")
	}
}

func TestTerminalWaitUsesEventsAndRechecksReadAccess(t *testing.T) {
	tool, pane, _ := terminalToolFixture(t)
	registry := tool.app.terminalRegistry()
	if err := registry.Grant(pane.id, tool.agent.id, true, false, false); err != nil {
		t.Fatal(err)
	}
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"wait","pane":"test-pane","revision":0,"timeout_ms":100}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = pane.conn.Read(make([]byte, 1)) // fake connection reports EOF
	ctx := agentcore.WithExternalToolAuthorization(context.Background())
	result, _ := tool.Execute(ctx, "wait", prepared, nil)
	if result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), `"ended":true`) {
		t.Fatalf("wait did not report the event: %+v", result)
	}
	registry.Revoke(pane.id, tool.agent.id)
	result, _ = tool.Execute(ctx, "wait", prepared, nil)
	if !result.IsError {
		t.Fatal("wait returned an event after read access was revoked")
	}
}

func TestTerminalWaitStopsImmediatelyOnRevocation(t *testing.T) {
	tool, pane, _ := terminalToolFixture(t)
	registry := tool.app.terminalRegistry()
	if err := registry.Grant(pane.id, tool.agent.id, true, false, false); err != nil {
		t.Fatal(err)
	}
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"wait","pane":"test-pane","revision":0,"timeout_ms":10000}`))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	tool.dispatch = func(_ context.Context, fn func() (any, error)) (any, error) {
		value, err := fn()
		close(started)
		return value, err
	}
	done := make(chan agentcore.AgentToolResult, 1)
	go func() {
		result, _ := tool.Execute(agentcore.WithExternalToolAuthorization(context.Background()), "wait", prepared, nil)
		done <- result
	}()
	<-started
	registry.Revoke(pane.id, tool.agent.id)
	select {
	case result := <-done:
		if !result.IsError {
			t.Fatal("wait succeeded after its read grant was revoked")
		}
	case <-time.After(time.Second):
		t.Fatal("read revocation did not wake the pending wait")
	}
}

type allowTerminalReviewer struct{}

func (allowTerminalReviewer) Classify(context.Context, string, json.RawMessage) judge.Verdict {
	return judge.Verdict{Level: judge.Allow, Source: "test", Confidence: 1}
}

func TestTerminalToolExecutorCarriesExactHostAndNetworkApproval(t *testing.T) {
	tool, pane, source := terminalToolFixture(t)
	if err := tool.app.terminalRegistry().Grant(pane.id, tool.agent.id, false, true, false); err != nil {
		t.Fatal(err)
	}
	reg := agenttool.NewToolRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	mode := permissions.New(permissions.Ask)
	questions := 0
	gate := judge.ConsentGate(judge.ConsentOpts{
		Mode: mode, Cwd: tool.agent.project, Classifier: judge.FirstParty{Next: allowTerminalReviewer{}},
		Confirm: func(_ context.Context, req judge.ConsentRequest) judge.ConsentAnswer {
			questions++
			host, network := false, false
			for _, need := range req.Needs {
				host = host || need.Cap == judge.CapHostControl
				network = network || need.Cap == judge.CapNetwork
			}
			if !host || !network {
				t.Errorf("dialog omitted host/network authority: %+v", req.Needs)
			}
			return judge.ConsentAnswer{Answered: true, Allow: true, Needs: req.Needs}
		},
	})
	call := agentcore.AgentToolCall{ID: "one", Name: "terminal", Arguments: json.RawMessage(`{"action":"send","pane":"test-pane","text":"pwd\r"}`)}
	results, _ := agenttool.ExecuteToolCalls(context.Background(), agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
		Registry: reg, BeforeToolCall: gate,
	}}, []agentcore.AgentToolCall{call}, nil)
	if len(results) != 1 || results[0].IsError || questions != 1 || len(source.writes) != 1 {
		t.Fatalf("reviewed input: results=%+v questions=%d writes=%q", results, questions, source.writes)
	}
	mode.Set(permissions.Auto)
	results, _ = agenttool.ExecuteToolCalls(context.Background(), agenttool.BatchConfig{ToolExecutorConfig: agenttool.ToolExecutorConfig{
		Registry: reg, BeforeToolCall: gate,
	}}, []agentcore.AgentToolCall{call}, nil)
	if len(results) != 1 || !results[0].IsError || questions != 1 || len(source.writes) != 1 {
		t.Fatalf("automatic mode bypassed operator confirmation: results=%+v questions=%d writes=%q", results, questions, source.writes)
	}
}

func TestNetworkedPaneDoesNotClaimRemoteContainment(t *testing.T) {
	tool, pane, source := terminalToolFixture(t)
	tool.guard.Mode.Set(permissions.FullAccess)
	ctx := agentcore.WithExternalToolAuthorization(context.Background())
	envelope, err := tool.guard.PrepareGUITerminal(ctx, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer envelope.Cleanup()
	pane.creator, pane.envelope = tool.agent.id, &envelope
	if err := tool.app.terminalRegistry().Grant(pane.id, tool.agent.id, false, true, false); err != nil {
		t.Fatal(err)
	}
	prepared, err := tool.PrepareCall(ctx, json.RawMessage(`{"action":"send","pane":"test-pane","text":"hostname\r","network":"off","remote":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var req terminalToolArgs
	if err := json.Unmarshal(prepared, &req); err != nil {
		t.Fatal(err)
	}
	if !req.Remote || req.Network != "on" {
		t.Fatalf("networked pane hid remote risk: %+v", req)
	}
	contained := agentcore.WithSandboxRequest(agentcore.WithNetworkGrant(ctx))
	result, _ := tool.Execute(contained, "send", prepared, nil)
	if !result.IsError || len(source.writes) != 0 {
		t.Fatal("local containment incorrectly allowed potentially remote input")
	}
	if _, err := tool.PrepareCall(ctx, json.RawMessage(`{"action":"run","pane":"test-pane","command":"pwd"}`)); err == nil {
		t.Fatal("run claimed a fixed cwd in a potentially remote pane")
	}
	launch, err := tool.PrepareCall(ctx, json.RawMessage(`{"action":"new","network":"on","argv":["ssh","example"]}`))
	if err != nil {
		t.Fatal(err)
	}
	result, _ = tool.Execute(contained, "new", launch, nil)
	if !result.IsError {
		t.Fatal("sandbox request incorrectly allowed a remote initial program")
	}
}
