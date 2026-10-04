package tui

// Regression tests for wrapToWidth, the expanded sub-agent view's wrapper.
//
// v1.1.0 shipped a crash: the function advanced with
// `s = s[len(TruncateToWidth(s, width)):]`, and TruncateToWidth appends a
// three-byte ellipsis that is not in the source, so the slice could run past
// the end ("slice bounds out of range [141:140]" — reported from a live
// session). The same expression also skipped real bytes wherever the prefix
// carried zero-width content (ANSI escapes), silently dropping output.

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func timeZeroForTest() time.Time { return time.Unix(0, 0) }

// wrapInputs cover the shapes that used to break: a short ASCII tail whose
// bytes are fewer than the ellipsis, ANSI escapes (zero width, many bytes),
// CJK (wide), emoji, and mixed content.
var wrapInputs = []string{
	"abx",
	"a",
	"",
	"hello world, this is a longer line of plain ascii text",
	"\x1b[38;5;245mab\x1b[0m",
	"\x1b[31mred\x1b[0m and \x1b[32mgreen\x1b[0m and a tail",
	"你好世界你好世界",
	"mixed 中文 and english text 混排",
	"emoji 🎉 and 👨‍👩‍👧‍👦 family sequences",
	"éééééééééé",
	"line with trailing\ttab",
}

// TestWrapToWidthTerminatesAndFits is the crash regression: every input and
// width must return, each segment must fit the width, and no segment may be
// empty unless the input was empty (an empty segment would make the caller's
// line count meaningless).
func TestWrapToWidthTerminatesAndFits(t *testing.T) {
	for _, in := range wrapInputs {
		for width := 1; width <= 12; width++ {
			segs := wrapToWidth(in, width)
			if in == "" {
				if len(segs) != 1 || segs[0] != "" {
					t.Errorf("wrapToWidth(%q, %d) = %q, want one empty segment", in, width, segs)
				}
				continue
			}
			if len(segs) == 0 {
				t.Errorf("wrapToWidth(%q, %d) returned no segments", in, width)
				continue
			}
			for i, seg := range segs {
				if seg == "" {
					t.Errorf("wrapToWidth(%q, %d) segment %d is empty", in, width, i)
				}
				// The final segment may overflow when no further split is
				// possible — a grapheme wider than the whole line (a CJK
				// character at width 1), where the wrapper emits the
				// remainder rather than looping forever. Every earlier
				// segment must fit.
				if w := ansi.StringWidth(seg); w > width && i != len(segs)-1 {
					t.Errorf("wrapToWidth(%q, %d) segment %d width %d exceeds %d",
						in, width, i, w, width)
				}
			}
		}
	}
}

// TestWrapToWidthPartitionsInput locks the content guarantee: the segments
// concatenate back to the input (ANSI escape codes may be folded by the
// library, so the comparison is on display text and per-segment content).
func TestWrapToWidthPartitionsInput(t *testing.T) {
	for _, in := range wrapInputs {
		if in == "" {
			continue
		}
		for width := 2; width <= 12; width++ {
			segs := wrapToWidth(in, width)
			got := strings.Join(segs, "")
			if ansi.Strip(got) != ansi.Strip(in) {
				t.Errorf("wrapToWidth(%q, %d) dropped or duplicated text:\n got %q\nwant %q",
					in, width, ansi.Strip(got), ansi.Strip(in))
			}
		}
	}
}

// TestWrapToWidthNoEllipsisAssertion documents the intended behavior: wrapping
// splits text across lines, it does not truncate it, so no segment carries a
// truncation marker.
func TestWrapToWidthKeepsEveryByteOfPlainText(t *testing.T) {
	const in = "abcdefghijklmnopqrstuvwxyz"
	for width := 1; width <= 10; width++ {
		segs := wrapToWidth(in, width)
		if got := strings.Join(segs, ""); got != in {
			t.Fatalf("wrapToWidth(%q, %d) = %q, want the input back", in, width, got)
		}
		for _, seg := range segs {
			if !utf8.ValidString(seg) {
				t.Errorf("segment %q is not valid UTF-8", seg)
			}
			if strings.Contains(seg, "…") {
				t.Errorf("segment %q carries an ellipsis; wrapping must not truncate", seg)
			}
		}
	}
}

// TestExpandedLinesSurviveHostileOutput drives the caller end to end with the
// output shape that crashed: a long line containing ANSI escapes and CJK,
// viewed in an expanded panel row.
func TestExpandedLinesSurviveHostileOutput(t *testing.T) {
	var p subagentPanel
	p.add("t1", "research", timeZeroForTest())
	hostile := "汇报：域名价格与服务器方案 " +
		"\x1b[38;5;245m" + strings.Repeat("colored segment ", 20) + "\x1b[0m " +
		strings.Repeat("宽字符内容", 40) + " tail"
	for i := 0; i < 60; i++ {
		p.appendOutput("t1", hostile)
	}
	p.selecting = true
	p.selected = 0
	p.expanded = true

	row := p.byID["t1"]
	if row == nil {
		t.Fatal("row missing")
	}
	// The old code panicked here for wide widths; a range of widths (including
	// the live report's 132) must stay panic-free and bounded.
	for _, width := range []int{4, 20, 80, 132, 200} {
		lines := p.expandedLines(row, width)
		if len(lines) > maxExpandedLines {
			t.Errorf("width %d: %d lines exceeds the cap %d", width, len(lines), maxExpandedLines)
		}
		if p.lineCount(width) == 0 {
			t.Errorf("width %d: lineCount should include the expanded output", width)
		}
	}
}
