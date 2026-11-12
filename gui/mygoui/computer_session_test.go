package mygoui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/internal/computeruse"
)

type computerNavigationBackend struct {
	computerUIBackend
	acted bool
}

type computerTransientBackend struct {
	computerUIBackend
	err error
}

func (b *computerTransientBackend) Validate(context.Context, computeruse.Target, bool) error {
	return b.err
}

func (*computerNavigationBackend) Snapshot(context.Context, computeruse.Target) (string, []computeruse.Element, bool, error) {
	return "backend-snapshot", []computeruse.Element{{Ref: "@e1", Role: "AXButton", Name: "1", Enabled: true}}, false, nil
}

func (*computerNavigationBackend) SupportsAction(action string) bool { return action == "click" }
func (b *computerNavigationBackend) Act(context.Context, computeruse.Target, string, string, string, string) error {
	b.acted = true
	return nil
}

func newComputerNavigationSession(t *testing.T) (*computeruse.Session, *computerNavigationBackend) {
	t.Helper()
	backend := &computerNavigationBackend{}
	session := computeruse.New(backend)
	if err := session.Start(context.Background(), computeruse.Target{ID: "calculator", Application: "Calculator"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, backend
}

func TestParkComputerPanelPreservesControlAndSnapshot(t *testing.T) {
	session, backend := newComputerNavigationSession(t)
	snapshot, err := session.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.computerSession, a.computerOpen = session, true
	previewCtx, stopPreview := context.WithCancel(context.Background())
	setupCtx, stopSetup := context.WithCancel(context.Background())
	a.computerPreviewStop, a.computerSetupStop = stopPreview, stopSetup
	a.computerPickerOpen, a.computerBusy = true, true
	a.computerSuggested, a.computerRequestPicker = "Calculator", true
	before := session.Status()
	a.parkComputerPanel()
	if got := session.Status(); got != before {
		t.Fatalf("navigation changed desktop authorization: got %+v, want %+v", got, before)
	}
	if previewCtx.Err() == nil || setupCtx.Err() == nil || a.computerOpen || a.computerSession != nil || a.computerPickerOpen || a.computerBusy || a.computerSuggested != "" || a.computerRequestPicker {
		t.Fatal("navigation retained the previous conversation's view or pending setup")
	}
	if err := session.Act(context.Background(), snapshot.ID, "@e1", "click", ""); err != nil || !backend.acted {
		t.Fatalf("background control lost its existing snapshot: acted=%t err=%v", backend.acted, err)
	}
}

func TestParkComputerPanelDoesNotCancelInFlightControl(t *testing.T) {
	session, _ := newComputerNavigationSession(t)
	a := testApp()
	a.computerSession, a.computerOpen = session, true
	started, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan error, 1)
	defer close(release)
	go func() {
		done <- session.Do(context.Background(), true, func(ctx context.Context, _ computeruse.Backend, _ computeruse.Target) error {
			close(started)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("control did not start")
	}
	a.parkComputerPanel()
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("session navigation interrupted desktop input: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control did not complete")
	}
}

func TestComputerNavigationNeverResumesExplicitPause(t *testing.T) {
	session, _ := newComputerNavigationSession(t)
	a := testApp()
	a.computerSession, a.computerOpen = session, true
	session.Pause()
	a.hideComputerPanel()
	if !session.Status().Paused {
		t.Fatal("hiding the panel must preserve an explicit operator pause")
	}
	a.parkComputerPanel()
	a.computerSession = session // returning to the owning conversation
	a.showComputerPanel()
	if err := session.Do(context.Background(), true, func(context.Context, computeruse.Backend, computeruse.Target) error { return nil }); !errors.Is(err, computeruse.ErrPaused) {
		t.Fatalf("navigation or reopening resumed an explicit pause: %v", err)
	}
}

func TestComputerPanelDoesNotPauseOnTransientValidation(t *testing.T) {
	b := &computerTransientBackend{}
	session := computeruse.New(b)
	if err := session.Start(context.Background(), computeruse.Target{ID: "calculator", Application: "Calculator"}, computeruse.Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	b.err = &computeruse.WindowDiagnostic{Reason: computeruse.WindowAXNoMatch}
	called := false
	err := session.Do(context.Background(), true, func(context.Context, computeruse.Backend, computeruse.Target) error { called = true; return nil })
	if !errors.Is(err, computeruse.ErrStaleObservation) || called || session.Status().Paused {
		t.Fatalf("transient validation sent input or paused: called=%t err=%v", called, err)
	}
	a := testApp()
	a.computerSession, a.computerOpen = session, true
	tt := ui.NewTester(a.view, 1400, 800)
	if !tt.HasText("Agent control permitted") || tt.HasText("Paused · take over in the target window") {
		t.Fatalf("panel incorrectly shows a takeover requirement: %q", tt.Texts())
	}
	b.err = nil
	if err := session.Do(context.Background(), false, func(context.Context, computeruse.Backend, computeruse.Target) error { return nil }); err != nil {
		t.Fatalf("same target did not recover without Resume: %v", err)
	}
}

func TestHideComputerPanelKeepsControlAndStopsOnlyPreview(t *testing.T) {
	session, backend := newComputerNavigationSession(t)
	snapshot, err := session.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.computerSession, a.computerOpen = session, true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.computerPreviewStop = cancel
	tt := ui.NewTester(a.view, 1400, 800)
	if err := tt.Click("Hide Computer Use"); err != nil {
		t.Fatal(err)
	}
	if a.computerOpen || ctx.Err() == nil || a.computerSession != session || session.Status().Paused || !tt.HasText("continues in the background") {
		t.Fatal("hiding the preview paused, lost control, or did not explain background operation")
	}
	if err := session.Act(context.Background(), snapshot.ID, "@e1", "click", ""); err != nil || !backend.acted {
		t.Fatalf("hiding the panel invalidated authorized control: %v", err)
	}
	a.showComputerPanel()
	tt.Frame()
	if err := tt.Click("Pause / take over"); err != nil || !session.Status().Paused {
		t.Fatalf("explicit pause no longer works: %v", err)
	}
}
