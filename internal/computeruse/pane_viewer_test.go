package computeruse

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestOperatorPaneAvoidsUnchangedFrameRedrawAndClearsOnPause(t *testing.T) {
	makePNG := func(c color.RGBA) []byte {
		t.Helper()
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		img.SetRGBA(0, 0, c)
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	var out bytes.Buffer
	renderer, err := NewKittyRenderer(&out, 42, true)
	if err != nil {
		t.Fatal(err)
	}
	var display operatorPaneDisplay
	state := paneViewerState{graphics: true, status: Status{Active: true}, frame: &Frame{ID: "first", PNG: makePNG(color.RGBA{R: 255, A: 255})}}
	if err := display.draw(&out, renderer, state, 80, 24); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[?2026h") || !strings.Contains(out.String(), "\x1b[?2026l") {
		t.Fatal("redraw did not delimit synchronized output")
	}
	out.Reset()
	state.frame = &Frame{ID: "new lease", PNG: append([]byte(nil), state.frame.PNG...)}
	if err := display.draw(&out, renderer, state, 80, 24); err != nil || out.Len() != 0 {
		t.Fatalf("unchanged image was redrawn: %q err=%v", out.String(), err)
	}
	state.frame = &Frame{PNG: makePNG(color.RGBA{B: 255, A: 255})}
	if err := display.draw(&out, renderer, state, 80, 24); err != nil || !strings.Contains(out.String(), "a=T") {
		t.Fatalf("changed image was not redrawn: err=%v", err)
	}
	out.Reset()
	state.localPaused = true
	if err := display.draw(&out, renderer, state, 80, 24); err != nil || !strings.Contains(out.String(), "a=d,d=I") || strings.Contains(out.String(), "a=T") {
		t.Fatalf("paused preview not removed safely: %q err=%v", out.String(), err)
	}
	out.Reset()
	if err := display.draw(&out, renderer, state, 81, 24); err != nil || out.Len() == 0 {
		t.Fatalf("resize was not redrawn: err=%v", err)
	}
}

func TestPaneFillInputKeepsUnicodeAndDoesNotEchoText(t *testing.T) {
	line := "fill @e2 世界  two"
	ref, text, ok := paneFillArguments(line)
	if !ok || ref != "@e2" || text != "世界  two" {
		t.Fatalf("fill arguments: ref=%q text=%q ok=%t", ref, text, ok)
	}
	if display := paneInputDisplay(line); strings.Contains(display, "世界") || strings.Contains(display, "two") || !strings.Contains(display, "@e2") {
		t.Fatalf("typed value appeared in pane output: %q", display)
	}
	if _, _, ok := paneFillArguments("fill @e2"); ok {
		t.Fatal("missing text would silently clear a field")
	}
}
