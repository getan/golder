package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"
)

type preparePointFixture struct {
	frameBoundTestBackend
	verdict string
	raises  int
}

func (b *preparePointFixture) InspectPoint(context.Context, Target, string, float64, float64) (PointInspection, error) {
	return PointInspection{PointVerification: b.verdict, PointUnobscured: b.verdict == "verified"}, nil
}

func (b *preparePointFixture) PrepareForeground(context.Context, Target) (bool, error) {
	b.raises++
	return true, nil
}

func TestPreparePointForeground(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewGray(image.Rect(0, 0, 7, 9))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		verdict string
		control bool
		raise   bool
	}{
		{"foreign_hit", true, true}, {"target_not_visible", true, true},
		{"verified", true, false}, {"unverified", true, false}, {"foreign_hit", false, false},
	} {
		t.Run(tc.verdict, func(t *testing.T) {
			b := &preparePointFixture{frameBoundTestBackend: frameBoundTestBackend{png: raw.Bytes()}, verdict: tc.verdict}
			s := New(b)
			ctx := context.Background()
			if err := s.Start(ctx, Target{ID: "w"}, Capabilities{Observe: true, Control: tc.control}); err != nil {
				t.Fatal(err)
			}
			f, err := s.Capture(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.InspectPoint(ctx, f.ID, 2, 3); err != nil || b.raises != 0 {
				t.Fatalf("read-only probe raised window: %v", err)
			}
			_, err = s.PreparePoint(ctx, f.ID, 2, 3)
			switch {
			case !tc.control:
				if !errors.Is(err, ErrNotAllowed) {
					t.Fatalf("observe-only preparation: %v", err)
				}
			case tc.raise:
				if StaleCode(err) != "focus_restored" || !errors.Is(err, ErrNoInput) {
					t.Fatalf("foreground change: %v", err)
				}
				if _, err := s.InspectPoint(ctx, f.ID, 2, 3); !errors.Is(err, ErrStaleObservation) {
					t.Fatalf("pre-focus frame still usable: %v", err)
				}
			case err != nil:
				t.Fatal(err)
			}
			if (b.raises == 1) != tc.raise || b.called != 0 {
				t.Fatalf("raises=%d inputs=%d", b.raises, b.called)
			}
		})
	}
}
