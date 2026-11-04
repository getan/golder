package main

import (
	"image/color"
	"testing"
)

func TestRenderKeepsMarkAndAddsAntialiasing(t *testing.T) {
	img := render()
	if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
		t.Fatalf("bounds=%v", img.Bounds())
	}
	for _, tc := range []struct {
		name string
		x, y int
		want color.NRGBA
	}{
		{"transparent corner", 0, 0, color.NRGBA{}},
		{"white face", 850, 512, face},
		{"dark chevron", 520, 512, ink},
		{"gold cursor", 700, 512, gold},
	} {
		if got := img.NRGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("%s pixel=%v want=%v", tc.name, got, tc.want)
		}
	}
	partialAlpha := false
	for y := 0; y < size && !partialAlpha; y++ {
		for x := 0; x < size; x++ {
			alpha := img.NRGBAAt(x, y).A
			if alpha > 0 && alpha < 255 {
				partialAlpha = true
				break
			}
		}
	}
	if !partialAlpha {
		t.Fatal("render has no antialiased edge pixels")
	}
}
