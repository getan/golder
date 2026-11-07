package tui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/permissions"
)

type computerPickerBackend struct{ closed chan struct{} }

func (*computerPickerBackend) Targets(context.Context) ([]computeruse.Target, error) { return nil, nil }
func (*computerPickerBackend) Validate(context.Context, computeruse.Target, bool) error {
	return nil
}
func (b *computerPickerBackend) Close() error {
	close(b.closed)
	return nil
}

func TestComputerPickerRequiresSeparateReadOnlyGrantAndCancels(t *testing.T) {
	s := newRemoteTestSession(t)
	s.perms = permissions.New(permissions.ReadOnly)
	setup := newComputerSetup(s.header.ID)
	backend := &computerPickerBackend{closed: make(chan struct{})}
	if !setup.installBackend(backend) {
		t.Fatal("backend was not installed")
	}
	s.computerSetup = setup
	m := NewModel(Options{})
	m.session = s
	got, _ := m.computerTargetsReady(computerTargetsMsg{setup: setup, targets: []computeruse.Target{{ID: "opaque", Application: "Calculator", Window: "Calculator"}}})
	m = got.(Model)
	if m.menu.pickKind != "computer-target" || len(m.menu.pick) != 1 {
		t.Fatalf("target picker not shown: %+v", m.menu)
	}
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	if m.menu.pickKind != "computer-grant" || len(m.menu.pick) != 1 || m.menu.pick[0].Value != "observe" {
		t.Fatalf("read-only mode exposed a control grant: %+v", m.menu.pick)
	}
	got, _ = m.Update(keyPress("ctrl+c"))
	m = got.(Model)
	if m.menu.picking() || s.computerSetup != nil {
		t.Fatal("cancel did not close the selection")
	}
	select {
	case <-backend.closed:
	case <-time.After(time.Second):
		t.Fatal("canceled picker leaked its backend")
	}
}

func TestComputerToolIsRevokedWhenConversationChanges(t *testing.T) {
	ctx := context.Background()
	backend := &computerPickerBackend{closed: make(chan struct{})}
	desktop := computeruse.New(backend)
	if err := desktop.Start(ctx, computeruse.Target{ID: "window"}, computeruse.Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	s := newRemoteTestSession(t)
	s.reg = agenttool.NewToolRegistry()
	tool := &agenttool.ComputerTool{Session: desktop, Mode: s.perms}
	if err := s.reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	s.agentCtx.Tools = append(s.agentCtx.Tools, tool)
	s.computerTool = tool
	s.stopComputer()
	if desktop.Status().Active || s.computerTool != nil || len(s.agentCtx.Tools) != 0 {
		t.Fatal("computer authority survived session cleanup")
	}
	if _, ok := s.reg.Get("computer"); ok {
		t.Fatal("computer tool remained registered")
	}
	_ = desktop.Close()
}

func TestSuggestedComputerTargetStartsUniqueAndHandlesAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		targets  []computeruse.Target
		wantKind string
		wantRows int
		wantCmd  bool
	}{
		{"unique", []computeruse.Target{{ID: "a", Application: "Calculator"}, {ID: "b", Application: "Notes"}}, "", 0, true},
		{"multiple", []computeruse.Target{{ID: "a", Application: "Calculator"}, {ID: "b", Application: "Calculator"}, {ID: "c", Application: "Notes"}}, "computer-target", 2, false},
		{"missing", []computeruse.Target{{ID: "a", Application: "Notes"}}, "computer-target", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRemoteTestSession(t)
			setup := newComputerSetup(s.header.ID)
			setup.suggested = "Calculator"
			backend := &computerPickerBackend{closed: make(chan struct{})}
			setup.installBackend(backend)
			s.computerSetup = setup
			m := NewModel(Options{})
			m.session = s
			got, cmd := m.computerTargetsReady(computerTargetsMsg{setup: setup, targets: tc.targets})
			m = got.(Model)
			if (cmd != nil) != tc.wantCmd || m.menu.pickKind != tc.wantKind || len(m.menu.pick) != tc.wantRows || s.computerTool != nil {
				t.Fatalf("kind=%q rows=%d tool=%v cmd=%v", m.menu.pickKind, len(m.menu.pick), s.computerTool, cmd)
			}
			if tc.wantCmd {
				_ = backend.Close() // command was deliberately not run
			}
			s.cancelComputerSetup()
		})
	}
}

func TestComputerApplicationAmbiguityRequiresChoice(t *testing.T) {
	s := newRemoteTestSession(t)
	setup := newComputerSetup(s.header.ID)
	setup.suggested = "Calculator"
	backend := &computerPickerBackend{closed: make(chan struct{})}
	setup.installBackend(backend)
	s.computerSetup = setup
	m := NewModel(Options{})
	m.session = s
	apps := []computeruse.Application{{ID: "one", Name: "Calculator", Detail: "/one"}, {ID: "two", Name: "Calculator", Detail: "/two"}}
	next, cmd := m.computerTargetsReady(computerTargetsMsg{setup: setup, applications: apps})
	m = next.(Model)
	if cmd != nil || m.menu.pickKind != "computer-application" || len(m.menu.pick) != 2 {
		t.Fatalf("application ambiguity was not presented: kind=%q rows=%d", m.menu.pickKind, len(m.menu.pick))
	}
	_, cmd = m.submitSlashSelected()
	if cmd == nil {
		t.Fatal("selecting an application did not schedule launch")
	}
	s.cancelComputerSetup()
}

func TestNewTurnCancelsPendingComputerSetupBeforeStarting(t *testing.T) {
	s := newRemoteTestSession(t)
	setup := newComputerSetup(s.header.ID)
	backend := &computerPickerBackend{closed: make(chan struct{})}
	setup.installBackend(backend)
	s.computerSetup = setup
	m := NewModel(Options{})
	m.session = s
	m.startRunFn = func(string) (chan tea.Msg, tea.Cmd) {
		if setup.ctx.Err() == nil || s.computerSetup != nil {
			t.Error("new turn started before desktop setup was canceled")
		}
		return make(chan tea.Msg, 1), nil
	}
	_, _ = m.startPrompt("another request")
	select {
	case <-backend.closed:
	case <-time.After(time.Second):
		t.Fatal("canceled desktop setup leaked its backend")
	}
}

func TestComputerRequestHonorsToolPolicyAndOnlyQueuesSelection(t *testing.T) {
	t.Setenv("TUIOS_ENV", "1")
	t.Setenv("TUIOS_SOCKET", "/tmp/example.sock")
	t.Setenv("TUIOS_SESSION", "test")
	t.Setenv("TUIOS_PANE_ID", "agent-pane")
	s := newRemoteTestSession(t)
	base := &agenttool.TodoTool{Store: agenttool.NewTodoStore()}
	s.computerEnabled = true
	s.agentCtx.Tools = append(s.agentCtx.Tools, base)
	s.reg = agenttool.NewToolRegistry()
	s.toolPolicy = run.NewToolPolicy(nil, []string{"computer"})
	s.installComputerRequest()
	if s.computerRequest != nil {
		t.Fatal("request tool bypassed the computer deny policy")
	}
	s.toolPolicy = run.ToolPolicy{}
	s.installComputerRequest()
	if s.computerRequest == nil {
		t.Fatal("operator request tool was not installed")
	}
	s.approvalCh = newEventChan()
	result, err := s.computerRequest.Execute(context.Background(), "request", json.RawMessage(`{"application":"Calculator"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("request failed: %+v %v", result, err)
	}
	select {
	case msg := <-s.approvalCh:
		req, ok := msg.(computerRequestedMsg)
		if !ok || req.sessionID != s.header.ID || req.application != "Calculator" || s.computerTool != nil {
			t.Fatalf("unsafe request event: %#v", msg)
		}
	default:
		t.Fatal("request did not reach the local operator interface")
	}
	s.computerRequest.Reset()
	side := context.WithValue(context.Background(), computerSideThreadKey{}, true)
	result, err = s.computerRequest.Execute(side, "request", json.RawMessage(`{"application":"Calculator"}`), nil)
	if err != nil || !result.IsError {
		t.Fatalf("side-thread request was not rejected: %+v %v", result, err)
	}
	select {
	case msg := <-s.approvalCh:
		t.Fatalf("side thread reached the local picker: %#v", msg)
	default:
	}
}

func TestComputerRequestOpensPickerAfterTurnWithoutGrantingAccess(t *testing.T) {
	t.Setenv("TUIOS_ENV", "1")
	t.Setenv("TUIOS_SOCKET", "/tmp/example.sock")
	t.Setenv("TUIOS_SESSION", "test")
	t.Setenv("TUIOS_PANE_ID", "agent-pane")
	s := newRemoteTestSession(t)
	s.computerEnabled = true
	s.agentCtx.Tools = append(s.agentCtx.Tools, &agenttool.TodoTool{Store: agenttool.NewTodoStore()})
	s.reg = agenttool.NewToolRegistry()
	m := NewModel(Options{})
	m.session = s
	m.running = true
	m.runCh = newEventChan()
	next, _ := m.Update(computerRequestedMsg{sessionID: s.header.ID, application: "Calculator"})
	m = next.(Model)
	if m.computerSuggested != "Calculator" || s.computerTool != nil {
		t.Fatal("request did not remain an unauthorised suggestion")
	}
	next, cmd := m.Update(runEndMsg{})
	m = next.(Model)
	if cmd == nil || s.computerSetup == nil || s.computerSetup.suggested != "Calculator" || s.computerTool != nil {
		t.Fatalf("picker was not scheduled safely: setup=%+v tool=%v", s.computerSetup, s.computerTool)
	}
	s.cancelComputerSetup()
}
