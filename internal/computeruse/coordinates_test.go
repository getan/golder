package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"math"
	"testing"
	"time"
)

type frameBoundTestBackend struct {
	png    []byte
	called int
	x, y   float64
	key    string
	dx, dy int
}

func (*frameBoundTestBackend) Validate(context.Context, Target, bool) error { return nil }
func (*frameBoundTestBackend) Close() error                                 { return nil }
func (b *frameBoundTestBackend) Capture(context.Context, Target) ([]byte, error) {
	return b.png, nil
}
func (b *frameBoundTestBackend) CaptureBound(context.Context, Target) ([]byte, string, error) {
	return b.png, "opaque-platform-geometry", nil
}
func (b *frameBoundTestBackend) ClickFrame(_ context.Context, _ Target, token string, x, y float64) error {
	if token != "opaque-platform-geometry" {
		return errors.New("incorrect geometry binding")
	}
	b.called++
	b.x, b.y = x, y
	return nil
}

func (b *frameBoundTestBackend) PressFrame(_ context.Context, _ Target, _ string, key string) error {
	b.key = key
	return nil
}

func (b *frameBoundTestBackend) ScrollFrame(_ context.Context, _ Target, _ string, x, y float64, dx, dy int) error {
	b.x, b.y, b.dx, b.dy = x, y, dx, dy
	return nil
}

type gatedFrameTestBackend struct {
	frameBoundTestBackend
	available bool
}

func (b *gatedFrameTestBackend) CaptureAvailable() bool { return b.available }

type limitedKeyTestBackend struct{ frameBoundTestBackend }

func (*limitedKeyTestBackend) SupportsFrameKey(key string) bool {
	return key == "enter" || key == "space"
}

func TestFrameKeyAvailabilityRestrictsDispatch(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	backend := &limitedKeyTestBackend{frameBoundTestBackend: frameBoundTestBackend{png: imageData.Bytes()}}
	session := New(backend)
	ctx := context.Background()
	if err := session.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	if !session.SupportsFrameKey("enter") || session.SupportsFrameKey("tab") || session.SupportsFrameKey("control") {
		t.Fatal("key availability does not reflect the backend")
	}
	frame, err := session.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.PressFrame(ctx, frame.ID, "tab"); !errors.Is(err, ErrUnavailable) || backend.key != "" {
		t.Fatalf("unsupported key was dispatched: %q %v", backend.key, err)
	}
}

func TestFrameActionsNotAdvertisedBeforeCaptureApproval(t *testing.T) {
	backend := &gatedFrameTestBackend{}
	session := New(backend)
	if session.SupportsCoordinates() || session.SupportsFrameAction("press") || session.SupportsFrameAction("scroll") {
		t.Fatal("frame actions advertised before capture approval")
	}
	backend.available = true
	if !session.SupportsCoordinates() || !session.SupportsFrameAction("press") || !session.SupportsFrameAction("scroll") {
		t.Fatal("frame actions missing after capture approval")
	}
}

func TestCoordinateClickRequiresRecentSingleUseFrame(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 200, 100))); err != nil {
		t.Fatal(err)
	}
	backend := &frameBoundTestBackend{png: imageData.Bytes()}
	s := New(backend)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	frame, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ClickFrame(ctx, frame.ID, 50, 25); err != nil || backend.called != 1 || backend.x != .25 || backend.y != .25 {
		t.Fatalf("click: called=%d point=(%v,%v) err=%v", backend.called, backend.x, backend.y, err)
	}
	if err := s.ClickFrame(ctx, frame.ID, 50, 25); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("reused frame: %v", err)
	}
	frame, _ = s.Capture(ctx)
	if err := s.ClickFrame(ctx, frame.ID, math.NaN(), 0); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("invalid point: %v", err)
	}
	s.Pause()
	if err := s.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ClickFrame(ctx, frame.ID, 0, 0); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("pre-pause frame: %v", err)
	}
	frame, _ = s.Capture(ctx)
	s.mu.Lock()
	lease := s.frames[frame.ID]
	lease.created = time.Now().Add(-frameLeaseLifetime - time.Second)
	s.frames[frame.ID] = lease
	s.mu.Unlock()
	if err := s.ClickFrame(ctx, frame.ID, 0, 0); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("expired frame: %v", err)
	}
}

func TestFrameKeyAndScrollAreBoundedAndSingleUse(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 200, 100))); err != nil {
		t.Fatal(err)
	}
	backend := &frameBoundTestBackend{png: imageData.Bytes()}
	s := New(backend)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	if !s.SupportsFrameAction("press") || !s.SupportsFrameAction("scroll") || s.SupportsFrameAction("other") {
		t.Fatal("incorrect frame-action capabilities")
	}
	frame, _ := s.Capture(ctx)
	if err := s.PressFrame(ctx, frame.ID, "cmd+q"); !errors.Is(err, ErrNotAllowed) || backend.key != "" {
		t.Fatalf("unsafe key: key=%q err=%v", backend.key, err)
	}
	if err := s.PressFrame(ctx, frame.ID, "enter"); err != nil || backend.key != "enter" {
		t.Fatalf("permitted key: key=%q err=%v", backend.key, err)
	}
	if err := s.PressFrame(ctx, frame.ID, "enter"); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("reused key frame: %v", err)
	}
	frame, _ = s.Capture(ctx)
	if err := s.ScrollFrame(ctx, frame.ID, 100, 50, 0, 6); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unbounded scroll: %v", err)
	}
	if err := s.ScrollFrame(ctx, frame.ID, 100, 50, 0, -2); err != nil || backend.x != .5 || backend.y != .5 || backend.dy != -2 {
		t.Fatalf("scroll: point=(%v,%v) delta=%d err=%v", backend.x, backend.y, backend.dy, err)
	}
}
