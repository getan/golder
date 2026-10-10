package tui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBannerPanelColumnIsFixed pins the reported regression: while the logo
// spins, the config panel beside it must not move. The mark keeps a constant
// block width (see TestLogoBlockWidthIsConstant), so the Version row starts at
// the same column on every frame.
func TestBannerPanelColumnIsFixed(t *testing.T) {
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	col := -1
	for frame := 0; frame < logoFrames; frame++ {
		out := ansi.ReplaceAllString(
			renderBannerFrame(DefaultTheme(), Options{Version: "dev", Model: "m"}, "/tmp/proj", frame), "")
		idx := -1
		for _, line := range strings.Split(out, "\n") {
			// The wordmark's block glyphs and double-line shadow strokes are
			// multi-byte runes, so the column must be counted in runes rather
			// than bytes (the panel column is a cell position, not a byte
			// offset).
			if c := strings.Index(line, "Version"); c >= 0 {
				idx = utf8.RuneCountInString(line[:c])
				break
			}
		}
		if idx < 0 {
			t.Fatalf("frame %d: Version row missing from banner", frame)
		}
		if col < 0 {
			col = idx
			continue
		}
		if idx != col {
			t.Fatalf("frame %d: Version column %d, want %d — panel must not move", frame, idx, col)
		}
	}
}
