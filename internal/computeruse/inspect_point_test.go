package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

type probeFixture struct {
	frameBoundTestBackend
	probes int
}

func (b *probeFixture) InspectPoint(_ context.Context, _ Target, _ string, x, y float64) (PointInspection, error) {
	b.probes++
	return PointInspection{WindowVerified: true, Role: "AXGroup"}, nil
}

func TestInspectPointIsObservationNotControl(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewGray(image.Rect(0, 0, 7, 9))); err != nil {
		t.Fatal(err)
	}
	b := &probeFixture{frameBoundTestBackend: frameBoundTestBackend{png: raw.Bytes()}}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "w"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	f, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := s.InspectPoint(ctx, f.ID, 2, 3)
	if err != nil || hit.Role != "AXGroup" || hit.EditableVerified {
		t.Fatalf("%+v, %v", hit, err)
	}
	if _, err := s.InspectPoint(ctx, f.ID, 7, 3); !errors.Is(err, ErrNotAllowed) {
		t.Fatal(err)
	}
	if err := s.ClickFrame(ctx, f.ID, 2, 3); !errors.Is(err, ErrNotAllowed) {
		t.Fatal(err)
	}
	s.Pause()
	if _, err := s.InspectPoint(ctx, f.ID, 2, 3); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if b.probes != 1 || b.called != 0 {
		t.Fatalf("probes=%d clicks=%d", b.probes, b.called)
	}
}

func TestPointTileLocatesChangeWithoutRelaxingDigest(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 7, 9))
	fingerprint := func() frameFingerprint {
		var raw bytes.Buffer
		if err := png.Encode(&raw, img); err != nil {
			t.Fatal(err)
		}
		f, err := fingerprintFrame(raw.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	a := fingerprint()
	img.SetGray(3, 7, color.Gray{Y: 1})
	b := fingerprint()
	if comparePointTile(a.tiles, b.tiles, [2]float64{3. / 7, 7. / 9}) != "changed" || comparePointTile(a.tiles, b.tiles, [2]float64{0, 0}) != "unchanged" {
		t.Fatal("incorrect tile or whole-frame digest")
	}
}

func TestInspectPointRejectsFrameFromEarlierSequence(t *testing.T) {
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewGray(image.Rect(0, 0, 7, 9))); err != nil {
		t.Fatal(err)
	}
	b := &probeFixture{frameBoundTestBackend: frameBoundTestBackend{png: raw.Bytes()}}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "w"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	f, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.sequence++
	s.mu.Unlock()
	if _, err := s.InspectPoint(ctx, f.ID, 2, 3); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("pre-sequence frame was accepted: %v", err)
	}
}
