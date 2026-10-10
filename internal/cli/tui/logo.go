package tui

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file draws the startup wordmark: "GOLDER" set in the block face the
// tuios splash uses — figlet's ANSI Shadow — and animated in with a short,
// self-stopping entrance.
//
// In that face every letter is drawn twice: the letter itself in full blocks
// (█), and a copy offset one row down and one column left behind it in
// double-line strokes (╔ ═ ╗ ║ ╚ ╝) that reads as a drop shadow. The shadow
// carries the mark's weight, so the blocks need no bold of their own, and the
// seven characters are all the mark asks a font to carry.
//
// Coverage was measured with CoreText on macOS 27.0.1 before choosing the face:
// SF Mono (every upright weight, Bold included), Menlo Regular and Italic, and
// Courier New carry all seven glyphs; Monaco carries █ but none of the six
// double-line strokes, and Menlo Bold, Menlo Bold Italic and every SF Mono
// italic are the same. On those faces the mark falls back per glyph, which is
// what tuios's splash ships with, and why no bold attribute is applied here: a
// bold run asks the terminal for Menlo Bold and would send all six strokes to
// fallback at once. A fallback face can draw a glyph a cell wider than the one
// it replaces, and the constant-width invariant below — the thing that keeps
// the config panel beside the mark from moving while the word types itself in —
// holds only as long as every glyph keeps its columns.
//
// The Linux side was measured the same way against the fonts a distribution
// actually installs (the .deb files from the Ubuntu archive, read with
// CoreText): DejaVu Sans Mono in all four styles, Hack, JetBrains Mono and
// Liberation Mono in Regular and Bold, and Ubuntu Mono in Regular, Medium and
// Bold carry all seven glyphs, and every glyph advances the same width in every
// one of them — Bold included. So the macOS caveat above is a macOS caveat: on
// Linux the common terminal fonts do not fall back at all.
//
// The entrance borrows codex's splash mechanics: a fixed frame count on a
// wall-clock tick, each frame a pure function of its index, and one settle on a
// resting frame that then stays behind as static history. The letters type
// themselves in left to right, a highlight sweeps across the finished word
// once, and the mark rests in plain base ink.

const (
	// logoFrames and logoFrameTick set the entrance's length: 36 frames at 80ms
	// (~2.9s), after which the tick chain stops and the mark stays put.
	logoFrames    = 36
	logoFrameTick = 80 * time.Millisecond

	// logoRows is the mark's height. The face is seven rows tall, but its last
	// row is blank in every letter (the font's baseline padding), so the mark
	// keeps six.
	logoRows = 6

	// Reveal timing: letter k starts fading in at frame 1+k*logoRevealStagger
	// and reaches full ink logoRevealSteps frames later, so the cascade takes
	// about half the entrance.
	logoRevealStagger = 3
	logoRevealSteps   = 4

	// The highlight sweep: frames logoShimmerFrom..logoShimmerTo walk a bright
	// band across the word; after it the mark settles at base ink.
	logoShimmerFrom = 20
	logoShimmerTo   = 30
)

// The palette is one light-blue family: the faint ink a letter fades in from,
// the base it settles at, and the halo/shine the sweep carries.
var (
	logoRevealRamp = [logoRevealSteps]string{"25", "33", "75", "117"}
	logoInkBase    = "117"
	logoInkHalo    = "153"
	logoInkShimmer = "159"
)

// logoWordGlyphs spells "GOLDER" in the face's own advances, lifted from the
// font. Rows are ragged — the shadow's slopes and the round letters' side
// bearings — and renderLogo pads each one to its glyph's widest row, so the
// columns a row looks blank in are still columns the letter owns. No gap is
// inserted between letters: the side bearings are part of the advance, and the
// settled mark is 50 cells wide because of them.
var logoWordGlyphs = [][]string{
	{ // G
		" ██████╗",
		"██╔════╝",
		"██║  ███╗",
		"██║   ██║",
		"╚██████╔╝",
		" ╚═════╝",
	},
	{ // O
		" ██████╗",
		"██╔═══██╗",
		"██║   ██║",
		"██║   ██║",
		"╚██████╔╝",
		" ╚═════╝",
	},
	{ // L
		"██╗",
		"██║",
		"██║",
		"██║",
		"███████╗",
		"╚══════╝",
	},
	{ // D
		"██████╗",
		"██╔══██╗",
		"██║  ██║",
		"██║  ██║",
		"██████╔╝",
		"╚═════╝",
	},
	{ // E
		"███████╗",
		"██╔════╝",
		"█████╗",
		"██╔══╝",
		"███████╗",
		"╚══════╝",
	},
	{ // R
		"██████╗",
		"██╔══██╗",
		"██████╔╝",
		"██╔══██╗",
		"██║  ██║",
		"╚═╝  ╚═╝",
	},
}

// logoFrameMsg advances the startup wordmark by one frame. The model re-issues
// the tick after each frame until the entrance completes (see Model.tickLogo).
type logoFrameMsg struct{ frame int }

// renderLogo paints one frame of the wordmark. frame runs from 0 (nothing yet
// typed) to logoFrames (the settled word). Hidden letters still occupy their
// cells, so the block keeps a constant width on every frame and the config
// panel beside it never moves.
func renderLogo(frame int) string {
	if frame < 0 {
		frame = 0
	}
	if frame > logoFrames {
		frame = logoFrames
	}
	colors := logoLetterColors(frame)

	rows := make([]strings.Builder, logoRows)
	for k, glyph := range logoWordGlyphs {
		width := logoGlyphWidth(glyph)
		color := colors[k]
		for r := 0; r < logoRows; r++ {
			if color == "" {
				rows[r].WriteString(strings.Repeat(" ", width))
				continue
			}
			rows[r].WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color(color)).
				Render(logoGlyphRow(glyph, r, width)))
		}
	}

	out := make([]string, logoRows)
	for r := range rows {
		out[r] = rows[r].String()
	}
	return strings.Join(out, "\n")
}

// logoLetterColors returns each letter's ink color on a frame; "" means the
// letter has not started to appear yet.
func logoLetterColors(frame int) []string {
	colors := make([]string, len(logoWordGlyphs))
	for k := range logoWordGlyphs {
		colors[k] = logoLetterColor(frame, k)
	}
	return colors
}

// logoLetterColor is the entrance's whole clock for one letter: fade in at its
// staggered start, then take the sweep's highlight while the band passes over
// it, and otherwise rest at base ink.
func logoLetterColor(frame, letter int) string {
	start := 1 + letter*logoRevealStagger
	if frame < start {
		return ""
	}
	if age := frame - start; age < logoRevealSteps {
		return logoRevealRamp[age]
	}
	if frame >= logoShimmerFrom && frame <= logoShimmerTo {
		// pos walks the letter indices from just before the first to just past
		// the last, so the band enters and leaves cleanly.
		span := float64(logoShimmerTo - logoShimmerFrom)
		pos := -1 + 8*float64(frame-logoShimmerFrom)/span
		switch d := math.Abs(float64(letter) - pos); {
		case d < 0.6:
			return logoInkShimmer
		case d < 1.6:
			return logoInkHalo
		}
	}
	return logoInkBase
}

// logoGlyphWidth is a glyph's cell width: the widest of its rows.
func logoGlyphWidth(glyph []string) int {
	width := 0
	for _, row := range glyph {
		if n := utf8.RuneCountInString(row); n > width {
			width = n
		}
	}
	return width
}

// logoGlyphRow returns one glyph row padded to the glyph's full width, so a
// ragged row (the l and r stems, the g's tail) still fills its whole cell.
func logoGlyphRow(glyph []string, row, width int) string {
	text := ""
	if row < len(glyph) {
		text = glyph[row]
	}
	if n := utf8.RuneCountInString(text); n < width {
		return text + strings.Repeat(" ", width-n)
	}
	return text
}

// logoWordWidth is the settled wordmark's width in cells: the glyph advances
// added up, since no gap separates them. Every frame's block is padded to it.
func logoWordWidth() int {
	width := 0
	for _, glyph := range logoWordGlyphs {
		width += logoGlyphWidth(glyph)
	}
	return width
}

// tickLogo schedules the next startup-wordmark frame. The model re-issues it
// after every frame until Update receives the final frame, so the animation
// stops without a goroutine once the word has settled.
func (m Model) tickLogo(frame int) tea.Cmd {
	return tea.Tick(logoFrameTick, func(time.Time) tea.Msg { return logoFrameMsg{frame: frame} })
}
