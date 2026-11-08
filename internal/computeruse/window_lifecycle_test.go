package computeruse

import (
	"context"
	"errors"
	"testing"
)

type closeWindowBackend struct {
	testBackend
	called Target
	err    error
}

func (b *closeWindowBackend) CloseWindow(_ context.Context, target Target) error {
	b.called = target
	return b.err
}

func TestCloseWindowIsTargetBoundAndPauses(t *testing.T) {
	ctx := context.Background()
	b := &closeWindowBackend{}
	s := New(b)
	target := Target{ID: "selected", Application: "Editor"}
	if !s.SupportsCloseWindow() {
		t.Fatal("window close capability not detected")
	}
	if err := s.Start(ctx, target, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	s.snapshot = &snapshotLease{publicID: "old"}
	s.frames = map[string]frameLease{"old": {targetID: target.ID}}
	if err := s.CloseWindow(ctx); err != nil || b.called != target || !s.Status().Paused {
		t.Fatalf("close err=%v called=%+v status=%+v", err, b.called, s.Status())
	}
	if s.snapshot != nil || s.frames != nil {
		t.Fatal("window close retained stale observations")
	}
	if err := s.CloseWindow(ctx); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused close = %v", err)
	}
}

func TestCloseWindowRespectsAuthorizationAndUncertainResult(t *testing.T) {
	ctx := context.Background()
	b := &closeWindowBackend{err: context.Canceled}
	s := New(b)
	if err := s.Start(ctx, Target{ID: "selected"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWindow(ctx); !errors.Is(err, ErrNotAllowed) || b.called.ID != "" {
		t.Fatalf("read-only close err=%v called=%+v", err, b.called)
	}
	s.Stop()
	if err := s.Start(ctx, Target{ID: "selected"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWindow(ctx); !errors.Is(err, context.Canceled) || !s.Status().Paused {
		t.Fatalf("uncertain close err=%v status=%+v", err, s.Status())
	}
}

func TestCloseWindowRejectsLostTargetBeforeDispatch(t *testing.T) {
	b := &closeWindowBackend{}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "selected"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	b.invalid = true
	if err := s.CloseWindow(ctx); !errors.Is(err, ErrTargetLost) || b.called.ID != "" || !s.Status().Paused {
		t.Fatalf("lost target close err=%v called=%+v status=%+v", err, b.called, s.Status())
	}
}
