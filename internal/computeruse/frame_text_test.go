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

type frameTextTestBackend struct {
	frameBoundTestBackend
	text string
	err  error
}

func (b *frameTextTestBackend) TypeTextFrame(_ context.Context, _ Target, _, text string) error {
	b.text = text
	b.called++
	return b.err
}

func TestFrameTextRequiresPlainTextAndSingleUseFrame(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	b := &frameTextTestBackend{frameBoundTestBackend: frameBoundTestBackend{png: data.Bytes()}}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	if !s.SupportsFrameAction("type_text") {
		t.Fatal("typing not advertised")
	}
	f, err := s.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "a\n", "a\t", "\x00", string([]byte{0xff}), strings.Repeat("a", 4097)} {
		if err := s.TypeTextFrame(ctx, f.ID, text); !errors.Is(err, ErrNoInput) || b.called != 0 {
			t.Fatalf("invalid text dispatched: %v", err)
		}
	}
	if err := s.TypeTextFrame(ctx, f.ID, "大明1566🙂"); err != nil || b.text != "大明1566🙂" {
		t.Fatalf("Unicode typing: %v", err)
	}
	if err := s.TypeTextFrame(ctx, f.ID, "again"); !errors.Is(err, ErrNoInput) || b.called != 1 {
		t.Fatalf("frame reused: %v", err)
	}
	s.Pause()
	if err := s.TypeTextFrame(ctx, f.ID, "paused"); !errors.Is(err, ErrPaused) || b.called != 1 {
		t.Fatalf("paused typing: %v", err)
	}
}

func TestFrameTextReadOnly(t *testing.T) {
	b := &frameTextTestBackend{}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.TypeTextFrame(ctx, "fake", "text"); !errors.Is(err, ErrNotAllowed) || b.called != 0 {
		t.Fatalf("read-only typing: %v", err)
	}
}
