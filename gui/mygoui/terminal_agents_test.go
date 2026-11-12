package mygoui

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/gui/terminalaccess"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/session"
)

type terminalTestConn struct{}

func (terminalTestConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (terminalTestConn) Write(b []byte) (int, error) { return len(b), nil }
func (terminalTestConn) Close() error                { return nil }

func TestTerminalMenuGrantsAreExplicitAndSessionBound(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
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
	a.ctrl, a.project, a.selectedProject, a.terminalOpen = ctrl, project, project, true
	a.status.SessionID = ctrl.SessionID()
	agent := a.registerTerminalAgent(ctrl)
	if agent == nil {
		t.Fatal("live GUI Agent was not registered")
	}
	registry := a.terminalRegistry()
	if err := registry.AddPane("test-pane", canonicalTerminalProject(project)); err != nil {
		t.Fatal(err)
	}
	conn, err := terminalaccess.NewConnection(registry, "test-pane", terminalTestConn{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pane := &terminalPane{id: "test-pane", name: "test pane", project: canonicalTerminalProject(project), session: agent.session, conn: conn}
	pane.enqueue = func(b []byte) { _, _ = conn.Write(b) }
	a.terminalProject(pane.scope()).root = &terminalLayout{pane: pane}
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.RightClick("Terminal pane test pane"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Assign Agent control", terminalAgentLabel(agent)); err != nil {
		t.Fatal(err)
	}
	if _, write, _ := registry.Access(pane.id, agent.id); write {
		t.Fatal("menu selection granted control before confirmation")
	}
	if err := tt.Click("Grant access"); err != nil {
		t.Fatal(err)
	}
	read, write, _ := registry.Access(pane.id, agent.id)
	if read || !write {
		t.Fatalf("control grant = read %t write %t", read, write)
	}
	if err := tt.RightClick("Terminal pane test pane"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Take back control"); err != nil {
		t.Fatal(err)
	}
	if _, write, _ := registry.Access(pane.id, agent.id); write {
		t.Fatal("take back did not revoke control")
	}
}

func TestTerminalAgentIsNotInstalledWhenToolsDisabled(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	project := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctrl := appsession.New(appsession.Options{
		GUI: true, Cwd: project, Store: store, Model: "test", ProviderName: "fake",
		Provider:    &guiBlockingProvider{started: make(chan struct{}), release: make(chan struct{})},
		Credentials: provider.NewCredentialStore(nil),
	})
	if err := ctrl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })
	if ctrl.TerminalEligible() || testApp().registerTerminalAgent(ctrl) != nil {
		t.Fatal("GUI terminal tool was enabled despite disabled tools")
	}
}

func TestTerminalAgentsCannotCrossConversationBoundary(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	project := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newController := func() *appsession.Controller {
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
		return ctrl
	}
	aCtrl, bCtrl := newController(), newController()
	a := testApp()
	a.ctrl, a.project, a.selectedProject, a.terminalOpen = aCtrl, project, project, true
	a.status.SessionID = aCtrl.SessionID()
	first, second := a.registerTerminalAgent(aCtrl), a.registerTerminalAgent(bCtrl)
	if first == nil || second == nil {
		t.Fatal("same-project Agents were not registered")
	}
	if err := aCtrl.Submit(context.Background(), "修复登录按钮并补充覆盖这一功能的所有相关回归测试，确保非常长的标题会被截断"); err != nil {
		t.Fatal(err)
	}
	if err := bCtrl.Submit(context.Background(), "检查缓存失效问题"); err != nil {
		t.Fatal(err)
	}
	if terminalAgentLabel(first) == terminalAgentLabel(second) {
		t.Fatal("same-project Agents do not have distinct labels")
	}
	if label := terminalAgentLabel(first); !strings.Contains(label, "修复登录按钮") || !strings.HasSuffix(label, "…") {
		t.Fatalf("first message is missing or not shortened: %q", label)
	}
	if label := terminalAgentLabel(second); !strings.Contains(label, "检查缓存失效问题") {
		t.Fatalf("second message is missing: %q", label)
	}
	registry := a.terminalRegistry()
	paneProject := canonicalTerminalProject(project)
	if err := registry.AddPane("test-pane", paneProject); err != nil {
		t.Fatal(err)
	}
	conn, err := terminalaccess.NewConnection(registry, "test-pane", terminalTestConn{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pane := &terminalPane{id: "test-pane", name: "test pane", project: paneProject, session: first.session, conn: conn}
	pane.enqueue = func(b []byte) { _, _ = conn.Write(b) }
	a.terminalProject(pane.scope()).root = &terminalLayout{pane: pane}
	if err := registry.Grant(pane.id, first.id, false, true, false); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Agent: " + terminalAgentLabel(first)) {
		t.Fatalf("terminal header does not distinguish owner: %q", tt.Texts())
	}
	if agents := a.activeTerminalAgents(pane.scope()); len(agents) != 1 || agents[0] != first {
		t.Fatalf("other conversation was offered access: %+v", agents)
	}
	if len(a.terminalStatuses(second)) != 0 || a.findTerminalPane(second.scope(), pane.id) != nil {
		t.Fatal("other conversation discovered the pane")
	}
	second.tool.dispatch = func(ctx context.Context, fn func() (any, error)) (any, error) { return fn() }
	if _, err := second.tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"capture","pane":"test-pane"}`)); err == nil {
		t.Fatal("other conversation targeted a pane by ID")
	}
	a.requestTerminalGrant(pane, second, false)
	a.confirmTerminalGrant()
	if got := registry.Owner(pane.id); got != first.id {
		t.Fatalf("cross-conversation grant changed ownership: %q", got)
	}
	a.terminalOpen = false
	a.status.SessionID = second.session
	child, err := a.reserveAgentTerminal(first, "", "", "background", agenttool.GUITerminalEnvelope{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.cleanupTerminalReservation(child)
	if a.terminalOpen || a.findTerminalPane(second.scope(), child.id) != nil || a.findTerminalPane(first.scope(), child.id) != child {
		t.Fatal("background Agent's pane leaked into the selected conversation")
	}
}
