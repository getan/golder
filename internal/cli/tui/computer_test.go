package tui

import (
	"context"
	"testing"
	"time"

	"github.com/getan/golder/internal/agenttool"
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
