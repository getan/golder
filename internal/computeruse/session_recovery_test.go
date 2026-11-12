package computeruse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"testing"
	"time"
)

type cancellablePreviewBackend struct {
	started chan struct{}
}

func (*cancellablePreviewBackend) Validate(context.Context, Target, bool) error { return nil }
func (*cancellablePreviewBackend) Close() error                                 { return nil }
func (b *cancellablePreviewBackend) Capture(ctx context.Context, _ Target) ([]byte, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestControlPreemptsLocalPreviewWithoutPausing(t *testing.T) {
	b := &cancellablePreviewBackend{started: make(chan struct{})}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	previewDone := make(chan error, 1)
	go func() { _, err := s.CapturePreview(ctx); previewDone <- err }()
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("preview did not start")
	}
	called := false
	if err := s.Do(ctx, true, func(context.Context, Backend, Target) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("control could not preempt preview: called=%t err=%v", called, err)
	}
	if err := <-previewDone; !errors.Is(err, context.Canceled) || s.Status().Paused {
		t.Fatalf("preview cancellation changed session: err=%v status=%+v", err, s.Status())
	}
}

func TestPreviewNeverQueuesBehindControlOrIssuesFrameLease(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	b := &frameBoundTestBackend{png: data.Bytes()}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	frame, err := s.CapturePreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClickFrame(ctx, frame.ID, 1, 1); !errors.Is(err, ErrStaleObservation) || b.called != 0 {
		t.Fatalf("preview granted input authority: err=%v calls=%d", err, b.called)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Do(ctx, true, func(context.Context, Backend, Target) error { close(started); <-release; return nil })
	}()
	<-started
	if _, err := s.CapturePreview(ctx); !errors.Is(err, ErrPreviewBusy) {
		close(release)
		t.Fatalf("preview was not skipped: %v", err)
	}
	pane, err := NewPane(s, &bytes.Buffer{}, 1, false)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	response := paneResponse(ctx, pane, PaneRequest{Action: "frame"})
	close(release)
	if err := <-done; err != nil || !response.Busy || response.Error != "" || response.Frame != nil {
		t.Fatalf("operator pane busy response=%+v control err=%v", response, err)
	}
}

func TestResumeDeadlineDoesNotWaitForUnresponsiveOperationOrChangeTarget(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	target := Target{ID: "window"}
	if err := s.Start(ctx, target, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.Do(ctx, true, func(context.Context, Backend, Target) error { close(started); <-release; return nil })
	}()
	<-started
	s.Pause()
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Resume(short) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTargetLost) || !s.Status().Paused {
			t.Fatalf("timeout reported as target loss or resumed: err=%v status=%+v", err, s.Status())
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("resume waited beyond its deadline")
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("paused operation was not canceled: %v", err)
	}
	if err := s.Resume(ctx); err != nil || s.Status().Paused || s.Status().Target != target {
		t.Fatalf("explicit resume did not revalidate the same target: status=%+v err=%v", s.Status(), err)
	}
}

func TestQueuedResumeCannotOverrideNewerPause(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	s.Pause()
	if err := s.opMu.lock(ctx); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			s.opMu.unlock()
		}
	}()
	result := make(chan error, 1)
	go func() { result <- s.Resume(ctx) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for s.opMu.waiting.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("resume did not begin waiting")
		case <-time.After(time.Millisecond):
		}
	}
	s.Pause()
	s.opMu.unlock()
	locked = false
	if err := <-result; !errors.Is(err, context.Canceled) || !s.Status().Paused {
		t.Fatalf("queued resume undid a newer pause: err=%v status=%+v", err, s.Status())
	}
}

type recoveryValidationBackend struct{ err error }

func (b *recoveryValidationBackend) Validate(context.Context, Target, bool) error { return b.err }
func (*recoveryValidationBackend) Close() error                                   { return nil }

func TestTransientValidationRefusesInputWithoutLatchingPause(t *testing.T) {
	b := &recoveryValidationBackend{}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	b.err = &WindowDiagnostic{Reason: WindowAXNoMatch}
	called := false
	err := s.Do(ctx, true, func(context.Context, Backend, Target) error { called = true; return nil })
	if !errors.Is(err, ErrStaleObservation) || errors.Is(err, ErrTargetLost) || called || s.Status().Paused {
		t.Fatalf("transient mismatch caused input or pause: err=%v called=%t status=%+v", err, called, s.Status())
	}
	b.err = nil
	if err := s.Do(ctx, false, func(context.Context, Backend, Target) error { return nil }); err != nil {
		t.Fatalf("same target did not recover: %v", err)
	}
	b.err = &WindowDiagnostic{Reason: WindowAXAmbiguous}
	if err := s.Do(ctx, true, func(context.Context, Backend, Target) error { t.Fatal("ambiguous input"); return nil }); !errors.Is(err, ErrTargetLost) || !s.Status().Paused {
		t.Fatalf("ambiguous target did not pause: err=%v status=%+v", err, s.Status())
	}
}

func TestTransientReadFailureDoesNotResumeOrReplayControl(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	diag := &WindowDiagnostic{Reason: WindowAXGeometry}
	err := s.Do(ctx, false, func(context.Context, Backend, Target) error { return diag })
	if !errors.Is(err, ErrStaleObservation) || errors.Is(err, ErrTargetLost) || s.Status().Paused {
		t.Fatalf("read-only transient latched pause: err=%v status=%+v", err, s.Status())
	}
	err = s.Do(ctx, true, func(context.Context, Backend, Target) error { return diag })
	if !errors.Is(err, ErrTargetLost) || !s.Status().Paused {
		t.Fatalf("possibly dispatched control was treated as safe to retry: err=%v status=%+v", err, s.Status())
	}
}

func TestPreviewFailureCannotLatchControlSession(t *testing.T) {
	b := &recoveryValidationBackend{}
	s := New(b)
	if err := s.Start(context.Background(), Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	b.err = &WindowDiagnostic{Reason: WindowUnavailable}
	if _, err := s.CapturePreview(context.Background()); err == nil || s.Status().Paused {
		t.Fatalf("local preview changed control state: err=%v status=%+v", err, s.Status())
	}
}

type settlingResumeBackend struct {
	failures int
	target   Target
	checks   int
}

func (b *settlingResumeBackend) Validate(_ context.Context, target Target, _ bool) error {
	if target != b.target {
		return ErrTargetLost
	}
	b.checks++
	if b.failures > 0 {
		b.failures--
		return &WindowDiagnostic{Reason: WindowAXGeometry}
	}
	return nil
}
func (*settlingResumeBackend) Close() error { return nil }

func TestOperatorResumeWaitsForSameWindowWithoutReplayingInput(t *testing.T) {
	target := Target{ID: "window"}
	b := &settlingResumeBackend{target: target}
	s := New(b)
	if err := s.Start(context.Background(), target, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	s.Pause()
	b.failures = 2
	if err := s.Resume(context.Background()); err != nil || s.Status().Paused || s.Status().Target != target || b.checks != 4 {
		t.Fatalf("resume did not settle the pinned target: checks=%d status=%+v err=%v", b.checks, s.Status(), err)
	}
}

type deadlineValidationBackend struct{ blocked bool }

func (b *deadlineValidationBackend) Validate(ctx context.Context, _ Target, _ bool) error {
	if b.blocked {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (*deadlineValidationBackend) Close() error { return nil }

func TestResumeValidationTimeoutIsNotReportedAsTargetLoss(t *testing.T) {
	b := &deadlineValidationBackend{}
	s := New(b)
	if err := s.Start(context.Background(), Target{ID: "window"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	s.Pause()
	b.blocked = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Resume(ctx); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTargetLost) || !s.Status().Paused {
		t.Fatalf("deadline was misclassified or resumed: err=%v status=%+v", err, s.Status())
	}
}

func TestBackendValidationDeadlineDoesNotMasqueradeAsTargetChange(t *testing.T) {
	b := &recoveryValidationBackend{}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	b.err = fmt.Errorf("%w: %w", ErrTargetLost, context.DeadlineExceeded)
	called := false
	err := s.Do(ctx, true, func(context.Context, Backend, Target) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTargetLost) || s.Status().Paused || called {
		t.Fatalf("pre-dispatch timeout was treated as target loss: called=%t status=%+v err=%v", called, s.Status(), err)
	}
	s.Pause()
	err = s.Resume(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTargetLost) || !s.Status().Paused {
		t.Fatalf("resume timeout was treated as target loss: status=%+v err=%v", s.Status(), err)
	}
}
