package mygoui

import (
	"context"
	"io"
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
	pane := &terminalPane{id: "test-pane", name: "test pane", project: canonicalTerminalProject(project), conn: conn}
	pane.enqueue = func(b []byte) { _, _ = conn.Write(b) }
	a.terminalProject(pane.project).root = &terminalLayout{pane: pane}
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
