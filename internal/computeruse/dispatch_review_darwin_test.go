//go:build darwin && cgo

package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"
)

// Exercise Capture -> lease consumption -> the actual macOS pre-dispatch gate
// -> Session.do, substituting OS checks and the event sink, not the gate.
type reviewedFrameBackend struct {
	frameBoundTestBackend
	finalErr, focusErr error
	focusChanged       bool
	finalChecks        int
}

func (b *reviewedFrameBackend) CaptureBound(context.Context, Target) ([]byte, string, error) {
	return b.png, `{"Target":"window","W":4,"H":4}`, nil
}

func (b *reviewedFrameBackend) ClickFrame(_ context.Context, target Target, token string, _, _ float64) error {
	return dispatchReviewedFrame(target, token, func() (string, error) { return token, nil },
		func() (bool, error) { return b.focusChanged, b.focusErr },
		func() error { b.finalChecks++; return b.finalErr },
		func(macFrameBinding) error { b.called++; return nil }, 0)
}

func TestReviewedFrameRefusalThroughSession(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	diagnostic := &WindowDiagnostic{Reason: WindowAXAmbiguous, Matches: 2}
	for _, tc := range []struct {
		name, code  string
		loss, focus error
		changed     bool
	}{
		{"final identity loss", "", diagnostic, nil, false},
		{"Space refusal", "window_off_desktop", nil, macActivationError(1), false},
		{"focus changed", "focus_restored", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &reviewedFrameBackend{frameBoundTestBackend: frameBoundTestBackend{png: data.Bytes()}, finalErr: tc.loss, focusErr: tc.focus, focusChanged: tc.changed}
			s := New(b)
			ctx := context.Background()
			if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
				t.Fatal(err)
			}
			f, err := s.Capture(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = s.ClickFrame(ctx, f.ID, 1, 1)
			if b.called != 0 || !errors.Is(err, ErrNoInput) || s.Status().Paused != (tc.loss != nil) || StaleCode(err) != tc.code {
				t.Fatalf("err=%v sends=%d status=%+v", err, b.called, s.Status())
			}
			if tc.loss != nil && (b.finalChecks != 1 || !errors.Is(err, diagnostic) || !errors.Is(err, ErrTargetLost) || errors.Is(err, ErrStaleObservation)) {
				t.Fatalf("lost identity diagnostic: %v", err)
			}
		})
	}
}
