package computeruse

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestKittyPaneFrameAndCleanup(t *testing.T) {
	var pngData, output bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	r, err := NewKittyRenderer(&output, 42, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Frame(Frame{PNG: pngData.Bytes()}, 20, 10); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\x1b_Ga=T,f=100,t=d,i=42,c=20,r=10,C=1,q=2,m=0;") {
		t.Fatalf("incorrect image transfer: %q", output.String())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\x1b_Ga=d,d=I,i=42,q=2\x1b\\") {
		t.Fatal("pane did not delete its own image")
	}
}

func TestKittyPaneWithoutGraphicsEmitsNoEscapeSequences(t *testing.T) {
	var output bytes.Buffer
	r, err := NewKittyRenderer(&output, 17, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Frame(Frame{}, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil || output.Len() != 0 {
		t.Fatalf("unsupported terminal received graphics: %q err=%v", output.String(), err)
	}
}
