package computeruse

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"
)

type captureTestBackend struct{ png []byte }

func (*captureTestBackend) Validate(context.Context, Target, bool) error { return nil }
func (*captureTestBackend) Close() error                                 { return nil }
func (b *captureTestBackend) Capture(context.Context, Target) ([]byte, error) {
	return b.png, nil
}

type gatedCaptureTestBackend struct {
	captureTestBackend
	available bool
}

func (b *gatedCaptureTestBackend) CaptureAvailable() bool { return b.available }

func TestCaptureAvailabilityIsNotAdvertisedBeforeConfirmation(t *testing.T) {
	backend := &gatedCaptureTestBackend{}
	session := New(backend)
	if session.SupportsCapture() {
		t.Fatal("unconfirmed capture was advertised")
	}
	backend.available = true
	if !session.SupportsCapture() {
		t.Fatal("confirmed capture was not advertised")
	}
}

func TestCaptureIsBoundedAndRequiresAuthorization(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	backend := &captureTestBackend{png: encoded.Bytes()}
	s := New(backend)
	if _, err := s.Capture(context.Background()); err == nil {
		t.Fatal("capture without an authorized target succeeded")
	}
	if err := s.Start(context.Background(), Target{ID: "one"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	frame, err := s.Capture(context.Background())
	if err != nil || frame.ID == "" || frame.Width != 20 || frame.Height != 10 || !bytes.Equal(frame.PNG, backend.png) {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	backend.png = []byte("not a PNG")
	if _, err := s.Capture(context.Background()); err == nil {
		t.Fatal("invalid PNG was accepted")
	}
}
