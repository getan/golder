package tui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file draws the startup logo: the letter G sketched in ASCII, spinning
// around its vertical axis the way codex's welcome mark turns. The mechanics and
// the look are both borrowed from codex:
//
//   - codex-rs/tui/src/ascii_animation.rs and frames.rs: a fixed frame count on a
//     wall-clock tick, one revolution per cycle, each frame a pure function of
//     its index, and one settle on a resting frame;
//   - codex's welcome frames are ~35x15 character drawings whose outline is
//     scattered over the glyph cells with marks like * + ' " / \ | - ~ =, so the
//     mark reads as a hand-sketched shape rather than a vector outline.
//
// The frames here are generated from geometry instead of shipping hand-drawn
// art, so the bowl stays a clean ellipse and one constant re-proportions the
// whole mark. The projection is the same one codex's frames show: the
// silhouette narrows to edge-on twice per revolution and widens again in
// between.

const (
	// logoFrames and logoFrameTick match codex's welcome cadence (36 frames at
	// FRAME_TICK_DEFAULT = 80ms): one revolution in ~2.9s, after which the tick
	// chain stops and the splash rests on the front-facing frame.
	logoFrames    = 36
	logoFrameTick = 80 * time.Millisecond

	// logoCols/logoRows size the character grid like codex's welcome frames
	// (which run about 35 columns by 15 rows).
	logoCols = 35
	logoRows = 15

	// logoRX/RY are the bowl ellipse's radii: x in columns, y in row units (a
	// terminal cell is about twice as tall as it is wide, so one row is two
	// units), sized so the glyph fills the grid.
	logoRX = 16.0
	logoRY = 13.6

	// logoGap is the bowl's aperture: it opens just above 3 o'clock, so the
	// upper terminal rests at ~2 o'clock and the lower one meets the crossbar
	// at the midline.
	logoGap = 32 * math.Pi / 180

	// logoBarInner is where the crossbar stops on its way toward the center, as
	// a fraction of logoRX.
	logoBarInner = 0.12

	// logoMinScale floors the spin's horizontal projection: a flat glyph turns
	// into a hairline at exactly 90°, so edge-on frames stay a readable stroke
	// instead of vanishing for a few ticks.
	logoMinScale = 0.18

	// colorLogoInk is the mark's one color: a light blue that reads as a single
	// pale sketch on both dark and light terminals.
	colorLogoInk = "117"
)

// logoFrameMsg advances the startup logo by one frame. The model re-issues the
// tick after each frame until the revolution completes (see Model.tickLogo).
type logoFrameMsg struct{ frame int }

// renderLogo paints one frame of the spinning mark. frame wraps modulo
// logoFrames; the turn starts and ends at the front-facing frame. Every row is
// kept at the full logoCols width: the banner joins the mark with the info
// panel beside it, so a block that narrowed with the projection would drag the
// panel left and right as the letter turns.
func renderLogo(frame int) string {
	frame = ((frame % logoFrames) + logoFrames) % logoFrames
	rows := logoGrid(logoScale(frame))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colorLogoInk)).
		Render(strings.Join(rows, "\n"))
}

// logoScale is the horizontal projection of a frame: |cos| compresses the
// letter toward the turning vertical axis, with |cos| rather than cos because
// the glyph is a two-sided sign — a full 360° turn passes edge-on at the
// quarter marks and shows the front face again halfway through, instead of a
// mirrored letter during the second half.
func logoScale(frame int) float64 {
	scale := math.Abs(math.Cos(2 * math.Pi * float64(frame) / logoFrames))
	if scale < logoMinScale {
		scale = logoMinScale
	}
	return scale
}

// logoGrid rasterizes the letter at the given horizontal projection into the
// ASCII character grid, one character per cell, so the stroke stays a hollow
// single-cell outline.
func logoGrid(scale float64) []string {
	grid := make([][]rune, logoRows)
	for y := range grid {
		grid[y] = make([]rune, logoCols)
		for x := range grid[y] {
			grid[y][x] = ' '
		}
	}
	cx, cy := float64(logoCols-1)/2, float64(logoRows-1)/2

	// stamp projects one point of the flat glyph onto the grid and paints the
	// character its tangent calls for, returning the cell (-1 when outside).
	stamp := func(x, y, tx, ty float64) (int, int) {
		col := int(math.Round(cx + x*scale))
		row := int(math.Round(cy + y/2))
		if col < 0 || col >= logoCols || row < 0 || row >= logoRows {
			return -1, -1
		}
		grid[row][col] = logoPen(tx*scale, ty, col, row)
		return col, row
	}

	// Bowl: sweep the parametrised ellipse from the crossbar junction (t=0,
	// 3 o'clock) clockwise around to the open terminal above it (t=2π-gap).
	// Walking the curve finely keeps the painted cells a connected run.
	const steps = 1440
	endCol, endRow := -1, -1
	for i := 0; i <= steps; i++ {
		t := (2*math.Pi - logoGap) * float64(i) / steps
		col, row := stamp(
			logoRX*math.Cos(t), logoRY*math.Sin(t),
			-logoRX*math.Sin(t), logoRY*math.Cos(t),
		)
		if col >= 0 {
			endCol, endRow = col, row
		}
	}

	// Crossbar: from the bowl's lower terminal toward the center; its inner end
	// is the pen's other stop.
	nibCol, nibRow := -1, -1
	for x := logoRX; x >= logoBarInner*logoRX; x -= 0.25 {
		col, row := stamp(x, 0, 1, 0)
		if col >= 0 {
			nibCol, nibRow = col, row
		}
	}

	// Roughen: a deterministic sprinkle of *, + and " over the outline gives the
	// stroke codex's hand-drawn texture instead of a clean vector line. Keyed on
	// the cell, so a frame is still a pure function of its index.
	for y := range grid {
		for x := range grid[y] {
			if grid[y][x] == ' ' {
				continue
			}
			switch (x*13 + y*7) % 23 {
			case 0:
				grid[y][x] = '*'
			case 7:
				grid[y][x] = '+'
			case 15:
				grid[y][x] = '"'
			}
		}
	}
	// Nibs: the aperture terminal and the crossbar's inner stop mark where the
	// pen lifted, so they always get a star.
	if endCol >= 0 {
		grid[endRow][endCol] = '*'
	}
	if nibCol >= 0 {
		grid[nibRow][nibCol] = '*'
	}

	rows := make([]string, len(grid))
	for i, row := range grid {
		rows[i] = string(row)
	}
	return rows
}

// logoPen maps a stroke's local tangent (x in columns, y in row units, positive
// y down) to the character that draws it: dashes for horizontals, bars for
// verticals, slashes for diagonals, with an occasional ~ or = so long runs stay
// hand-drawn. The cell keys the variation, so texture does not shimmer while
// the letter turns.
func logoPen(tx, ty float64, col, row int) rune {
	switch {
	case math.Abs(tx) > 2*math.Abs(ty):
		switch (col + row*3) % 7 {
		case 2:
			return '~'
		case 5:
			return '='
		default:
			return '-'
		}
	case math.Abs(ty) > 2*math.Abs(tx):
		return '|'
	case tx*ty < 0:
		return '/'
	default:
		return '\\'
	}
}

// tickLogo schedules the next startup-logo frame. The model re-issues it after
// every frame until Update receives the final frame, so the animation stops
// without a goroutine once the splash settles.
func (m Model) tickLogo(frame int) tea.Cmd {
	return tea.Tick(logoFrameTick, func(time.Time) tea.Msg { return logoFrameMsg{frame: frame} })
}
