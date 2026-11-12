package computeruse

import (
	"context"
	"errors"
	"testing"
)

func TestLocalOperatorViewDoesNotExposeAnUnpausedInputPath(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	view := NewOperatorView(s)
	if err := view.ClickFrame(ctx, "unissued", 0, 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("input allowed without takeover: %v", err)
	}
	if err := view.TakeOver(); err != nil || !s.Status().Paused || !s.Status().OperatorControl {
		t.Fatalf("local view did not pause Agent: %v", err)
	}
	if err := s.Do(ctx, true, func(context.Context, Backend, Target) error { t.Fatal("Agent entered local control"); return nil }); !errors.Is(err, ErrPaused) {
		t.Fatalf("Agent not excluded: %v", err)
	}
	s.Stop()
	if err := view.TypeTextFrame(ctx, "old", "text"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("stopped view allowed input: %v", err)
	}
	if err := NewOperatorView(nil).TakeOver(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil session: %v", err)
	}
}
