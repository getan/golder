package tui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var logoANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

// logoFramePlain renders a frame with styling stripped and trailing blanks
// trimmed, so tests can measure the visible silhouette.
func logoFramePlain(frame int) []string {
	lines := strings.Split(logoANSI.ReplaceAllString(renderLogo(frame), ""), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

// logoFrameWidth returns the widest visible row of a frame, in cells.
func logoFrameWidth(frame int) int {
	width := 0
	for _, line := range logoFramePlain(frame) {
		if w := utf8.RuneCountInString(line); w > width {
			width = w
		}
	}
	return width
}

// logoFrameHasInk reports whether a frame draws anything at all.
func logoFrameHasInk(frame int) bool {
	for _, line := range logoFramePlain(frame) {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// logoFrameRowsContiguous reports whether the inked rows form one unbroken run,
// i.e. compression never drops a row between two painted ones.
func logoFrameRowsContiguous(frame int) bool {
	seen, gap := false, false
	for _, line := range logoFramePlain(frame) {
		if strings.TrimSpace(line) != "" {
			if gap {
				return false
			}
			seen = true
			continue
		}
		if seen {
			gap = true
		}
	}
	return true
}

// TestLogoSpinsOnVerticalAxis pins the silhouette contract of the spin: the
// front-facing frame is the widest, the quarter-turned frame is compressed
// toward edge-on but never disappears or breaks up, and each row keeps its
// height.
func TestLogoSpinsOnVerticalAxis(t *testing.T) {
	front, edge := logoFrameWidth(0), logoFrameWidth(logoFrames/4)
	if front <= edge {
		t.Fatalf("front frame width %d should exceed edge-on width %d", front, edge)
	}
	if edge == 0 {
		t.Fatal("edge-on frame vanished; logoMinScale should keep the stroke visible")
	}

	rows := len(logoFramePlain(0))
	for _, frame := range []int{0, logoFrames / 4, logoFrames / 2, 3 * logoFrames / 4} {
		if got := len(logoFramePlain(frame)); got != rows {
			t.Errorf("frame %d has %d rows, want %d", frame, got, rows)
		}
		if !logoFrameHasInk(frame) {
			t.Errorf("frame %d is empty", frame)
		}
		if !logoFrameRowsContiguous(frame) {
			t.Errorf("frame %d has a blank row between painted rows", frame)
		}
	}
}

// TestLogoRestsWhereItStarted makes the settle seamless: the revolution ends on
// the same front-facing frame it began from, so the splash does not jump when
// the tick chain stops.
func TestLogoRestsWhereItStarted(t *testing.T) {
	start := strings.Join(logoFramePlain(0), "\n")
	rest := strings.Join(logoFramePlain(logoFrames), "\n")
	if rest != start {
		t.Fatal("final frame differs from the starting frame")
	}

	// A two-sided sign shows its face again at the half turn; the letter must
	// not appear mirrored in the second half of the revolution.
	if mid := logoFrameWidth(logoFrames / 2); mid != logoFrameWidth(0) {
		t.Fatalf("half-turn width %d, want front width %d", mid, logoFrameWidth(0))
	}
}

// TestLogoBlockWidthIsConstant pins the banner layout invariant: the mark
// occupies a fixed-width block on every frame, so the info panel joined beside
// it never shifts as the letter turns (only the glyph inside the block moves).
func TestLogoBlockWidthIsConstant(t *testing.T) {
	for frame := 0; frame < logoFrames; frame++ {
		rows := logoGrid(logoScale(frame))
		if len(rows) != logoRows {
			t.Fatalf("frame %d has %d rows, want %d", frame, len(rows), logoRows)
		}
		for i, row := range rows {
			if w := utf8.RuneCountInString(row); w != logoCols {
				t.Fatalf("frame %d row %d width %d, want %d (%q)", frame, i, w, logoCols, row)
			}
		}
	}
}
