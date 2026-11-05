package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"testing"
)

type previewTestBackend struct {
	captureTestBackend
	confirmed string
	discarded string
}

func (*previewTestBackend) PrepareOperatorPreview(context.Context, Target) (OperatorPreview, error) {
	return OperatorPreview{}, ErrUnavailable
}

func (b *previewTestBackend) ConfirmOperatorPreview(_ context.Context, _ Target, id string) error {
	b.confirmed = id
	return nil
}

func (b *previewTestBackend) DiscardOperatorPreview(_ context.Context, _ Target, id string) error {
	b.discarded = id
	return nil
}

func TestOperatorPreviewBlocksAgentUntilLocalConfirmation(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	backend := &previewTestBackend{captureTestBackend: captureTestBackend{png: imageData.Bytes()}}
	session := New(backend)
	if err := session.Start(context.Background(), Target{ID: "target"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.InstallOperatorPreview(OperatorPreview{ID: "candidate", PNG: imageData.Bytes()}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Capture(context.Background()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("model capture before confirmation: %v", err)
	}
	pane, err := NewPane(session, io.Discard, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if reply := paneResponse(context.Background(), pane, PaneRequest{Action: "confirm-preview", Sequence: 1}); reply.Error == "" {
		t.Fatal("confirmation succeeded before local preview was delivered")
	}
	if reply := paneResponse(context.Background(), pane, PaneRequest{Action: "candidate"}); reply.Error != "" || reply.Frame == nil || reply.Frame.ID != "" {
		t.Fatalf("local candidate: %+v", reply)
	}
	if reply := paneResponse(context.Background(), pane, PaneRequest{Action: "confirm-preview", Sequence: 2}); reply.Error != "" {
		t.Fatalf("local confirmation: %+v", reply)
	}
	if err := session.WaitOperatorPreview(context.Background()); err != nil || backend.confirmed != "candidate" || session.Status().PendingPreview {
		t.Fatalf("confirmation state: %q %v", backend.confirmed, err)
	}
	if _, err := session.Capture(context.Background()); err != nil {
		t.Fatalf("capture after confirmation: %v", err)
	}
}

func TestOperatorPreviewStopUnblocksWait(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	session := New(&previewTestBackend{})
	if err := session.Start(context.Background(), Target{ID: "target"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := session.InstallOperatorPreview(OperatorPreview{ID: "candidate", PNG: imageData.Bytes()}); err != nil {
		t.Fatal(err)
	}
	session.Stop()
	if err := session.WaitOperatorPreview(context.Background()); !errors.Is(err, ErrNotActive) {
		t.Fatalf("wait after stop: %v", err)
	}
	if err := session.Start(context.Background(), Target{ID: "other"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := session.WaitOperatorPreview(context.Background()); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("stale wait after restart: %v", err)
	}
}

func TestOperatorPreviewPauseRevokesPendingConfirmation(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	session := New(&previewTestBackend{})
	if err := session.Start(context.Background(), Target{ID: "target"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := session.InstallOperatorPreview(OperatorPreview{ID: "candidate", PNG: imageData.Bytes()}); err != nil {
		t.Fatal(err)
	}
	session.Pause()
	if err := session.WaitOperatorPreview(context.Background()); !errors.Is(err, ErrNotActive) {
		t.Fatalf("wait after pause: %v", err)
	}
}
