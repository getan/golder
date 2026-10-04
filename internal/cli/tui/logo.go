package tui

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file draws the startup wordmark: "golder" set in a rounded, hollow line
// face and animated in with a short, self-stopping entrance.
//
// The face is drawn with box-drawing strokes — rounded corners (╭╮╰╯), straight
// runs (─│), and junctions (┤┘╯) where a bowl meets a stem — so the word reads
// as a light geometric sans rather than a bitmap blob. Every letter shares one
// baseline; the l and d ascend above it and the g's tail descends below, which
// is what fixes the block's height.
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

	// logoRows is the block's height: one ascender row, four rows of x-height,
	// and the row the g's tail descends into.
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

	// logoLetterGap is the number of blank columns between letters.
	logoLetterGap = 2
)

// The palette is one light-blue family: the faint ink a letter fades in from,
// the base it settles at, and the halo/shine the sweep carries.
var (
	logoRevealRamp = [logoRevealSteps]string{"25", "33", "75", "117"}
	logoInkBase    = "117"
	logoInkHalo    = "153"
	logoInkShimmer = "159"
)

// logoWordGlyphs spells "golder". Each glyph is a fixed number of rows (padded
// to a common width at render time); rows above the x-height and below the
// baseline are left blank, which keeps every glyph the same height.
var logoWordGlyphs = [][]string{
	{ // g — bowl with a tail that hooks left beneath it
		"",
		"╭──────╮",
		"│      │",
		"│      │",
		"╰──────┤",
		"   ╰───╯",
	},
	{ // o
		"",
		"╭──────╮",
		"│      │",
		"│      │",
		"╰──────╯",
		"",
	},
	{ // l — one stem, full ascender
		"│",
		"│",
		"│",
		"│",
		"│",
		"",
	},
	{ // d — stem with a bowl hung on its left
		"       │",
		"╭──────┤",
		"│      │",
		"│      │",
		"╰──────┘",
		"",
	},
	{ // e — bar meets the upper right arc; the lower right stays open
		"",
		"╭─────╮",
		"│     │",
		"├─────╯",
		"╰────╯",
		"",
	},
	{ // r — stem with a shoulder
		"",
		"╭───╮",
		"│",
		"│",
		"│",
		"",
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
			if k > 0 {
				rows[r].WriteString(strings.Repeat(" ", logoLetterGap))
			}
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

// logoWordWidth is the settled wordmark's width in cells; every frame's block
// is padded to it.
func logoWordWidth() int {
	width := 0
	for k, glyph := range logoWordGlyphs {
		if k > 0 {
			width += logoLetterGap
		}
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
