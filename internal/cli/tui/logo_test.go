package tui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var logoANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

// logoFrameRaw renders a frame with styling stripped, keeping trailing blanks
// so tests can measure the block itself.
func logoFrameRaw(frame int) []string {
	return strings.Split(logoANSI.ReplaceAllString(renderLogo(frame), ""), "\n")
}

// logoFramePlain renders a frame with styling stripped and trailing blanks
// trimmed, so tests can measure the visible word.
func logoFramePlain(frame int) []string {
	lines := logoFrameRaw(frame)
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
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

// logoLetterBand is letter k's column range [start, start+width) in the block,
// including the gap columns that precede it.
func logoLetterBand(k int) (start, width int) {
	for i := 0; i < k; i++ {
		start += logoGlyphWidth(logoWordGlyphs[i]) + logoLetterGap
	}
	return start, logoGlyphWidth(logoWordGlyphs[k])
}

// logoBandHasInk reports whether any cell of a letter's band is drawn on a
// frame.
func logoBandHasInk(frame, k int) bool {
	start, width := logoLetterBand(k)
	for _, line := range logoFrameRaw(frame) {
		cells := []rune(line)
		for col := start; col < start+width && col < len(cells); col++ {
			if cells[col] != ' ' {
				return true
			}
		}
	}
	return false
}

// TestLogoBlockGeometryIsConstant pins the banner layout invariant: every frame
// keeps the same block size, so the config panel joined beside it never moves
// while the word types itself in.
func TestLogoBlockGeometryIsConstant(t *testing.T) {
	want := logoWordWidth()
	for frame := 0; frame <= logoFrames; frame++ {
		rows := logoFrameRaw(frame)
		if len(rows) != logoRows {
			t.Fatalf("frame %d has %d rows, want %d", frame, len(rows), logoRows)
		}
		for i, row := range rows {
			if w := utf8.RuneCountInString(row); w != want {
				t.Fatalf("frame %d row %d width %d, want %d (%q)", frame, i, w, want, row)
			}
		}
	}
}

// TestLogoTypesInLeftToRight pins the entrance: nothing is drawn on the first
// frame, each letter is still blank just before its staggered start, and every
// letter is inked by the time it has had its fade-in frames.
func TestLogoTypesInLeftToRight(t *testing.T) {
	if logoFrameHasInk(0) {
		t.Fatal("frame 0 should be blank — the word has not started typing yet")
	}
	for k := range logoWordGlyphs {
		start := 1 + k*logoRevealStagger
		if logoBandHasInk(start-1, k) {
			t.Errorf("letter %d has ink at frame %d, before its start frame %d", k, start-1, start)
		}
		if !logoBandHasInk(start+logoRevealSteps-1, k) {
			t.Errorf("letter %d is still blank at frame %d, want it revealed", k, start+logoRevealSteps-1)
		}
	}
}

// TestLogoSettles makes the resting position seamless: once the sweep has
// passed, every remaining frame is identical and equals the final frame, so the
// splash does not jump when the tick chain stops.
func TestLogoSettles(t *testing.T) {
	want := renderLogo(logoFrames)
	for frame := logoShimmerTo + 1; frame <= logoFrames; frame++ {
		if got := renderLogo(frame); got != want {
			t.Fatalf("frame %d differs from the final frame", frame)
		}
	}
	for k := range logoWordGlyphs {
		if !logoBandHasInk(logoFrames, k) {
			t.Errorf("letter %d missing from the settled wordmark", k)
		}
	}
}
