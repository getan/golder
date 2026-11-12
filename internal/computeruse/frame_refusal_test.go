package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

// The simulated native dispatcher returns before sending an event, just like
// the macOS coordinate guards on a focus/occlusion/geometry refusal.
type refusedFrameBackend struct {
	frameBoundTestBackend
	refuse        bool
	revalidateErr error
}

func (b *refusedFrameBackend) ClickFrame(ctx context.Context, _ Target, _ string, _, _ float64) error {
	if b.refuse {
		return frameInputRefused(ctx, func() error { return b.revalidateErr })
	}
	b.called++
	return nil
}

func TestRefusedInputKeepsSameTargetActiveWithoutReplaying(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	b := &refusedFrameBackend{frameBoundTestBackend: frameBoundTestBackend{png: imageData.Bytes()}, refuse: true}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	frame, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.ClickFrame(ctx, frame.ID, 1, 1)
	if !errors.Is(err, ErrStaleObservation) || !strings.Contains(err.Error(), "no input was sent") || b.called != 0 || s.Status().Paused {
		t.Fatalf("unsafe refusal or unnecessary pause: calls=%d status=%+v err=%v", b.called, s.Status(), err)
	}
	if err := s.ClickFrame(ctx, frame.ID, 1, 1); !errors.Is(err, ErrStaleObservation) || b.called != 0 {
		t.Fatal("a refused frame must remain consumed")
	}
	b.refuse = false
	fresh, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClickFrame(ctx, fresh.ID, 1, 1); err != nil || b.called != 1 {
		t.Fatalf("fresh explicit action failed: calls=%d err=%v", b.called, err)
	}
	s.Pause()
	if err := s.ClickFrame(ctx, fresh.ID, 1, 1); !errors.Is(err, ErrPaused) || b.called != 1 {
		t.Fatal("explicit operator pause must remain latched")
	}
}

func TestRefusedInputPausesIfPinnedTargetCannotBeProved(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	err := s.Do(ctx, true, func(ctx context.Context, _ Backend, _ Target) error {
		return frameInputRefused(ctx, func() error { return &WindowDiagnostic{Reason: WindowAXAmbiguous, Matches: 2} })
	})
	if !errors.Is(err, ErrTargetLost) || !s.Status().Paused {
		t.Fatalf("unproven target did not pause: status=%+v err=%v", s.Status(), err)
	}
}

func TestRefusedInputDoesNotRevalidateAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := frameInputRefused(ctx, func() error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled refusal revalidated: called=%t err=%v", called, err)
	}
}

func TestSemanticPreDispatchRefusalNeverReplaysOrPausesWhenTargetIsValid(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	err := s.Do(ctx, true, func(ctx context.Context, _ Backend, _ Target) error {
		return semanticInputRefused(ctx, func() error { checks++; return nil })
	})
	if !errors.Is(err, ErrStaleObservation) || errors.Is(err, ErrTargetLost) || checks != 1 || s.Status().Paused {
		t.Fatalf("safe semantic refusal: checks=%d status=%+v err=%v", checks, s.Status(), err)
	}
	err = s.Do(ctx, true, func(ctx context.Context, _ Backend, _ Target) error {
		return semanticInputRefused(ctx, func() error { return &WindowDiagnostic{Reason: WindowAXAmbiguous} })
	})
	if !errors.Is(err, ErrTargetLost) || !s.Status().Paused {
		t.Fatalf("unproven semantic target did not pause: status=%+v err=%v", s.Status(), err)
	}
}
