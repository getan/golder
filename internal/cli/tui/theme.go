package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/getan/golder/internal/cli/ui"
)

// Theme bundles the lipgloss styles for every visual element the TUI paints so
// the transcript, tool cards and status bar share one palette instead of each
// call site hand-rolling colors (see tasks/spec-tui-agent.md Sections 2.2, 5.1).
// The reference palette is: success green, error/warn red & yellow, file/accent
// blue, and gray for secondary chrome. Styles are plain value types, so a Theme
// is cheap to copy and safe to pass by value.
type Theme struct {
	// User styles the human's turns in the transcript.
	User lipgloss.Style
	// Assistant styles the model's turns in the transcript.
	Assistant lipgloss.Style
	// System styles system / meta notices (secondary gray).
	System lipgloss.Style
	// ToolVerb styles the "Running"/"Ran" verb of a tool invocation headline
	// (bold, terminal default color — codex renders its exec verb the same way;
	// the old all-blue headline read like a link).
	ToolVerb lipgloss.Style
	// ToolName styles the tool's name in the headline (cyan, codex's accent for
	// tool verbs like Read/Search).
	ToolName lipgloss.Style
	// ToolCmd styles the command/argument text of a headline and its `│ `
	// continuation lines (near-white, so a command reads as code, not as a
	// colored label).
	ToolCmd lipgloss.Style
	// ToolBody styles the body/output region of a tool card.
	ToolBody lipgloss.Style
	// TodoActive styles the row of the todo item currently in progress (light
	// green bold, so the active step is the first thing the eye lands on).
	TodoActive lipgloss.Style
	// TodoDone styles completed todo rows (muted gray, struck through —
	// codex's crossed-out dim treatment).
	TodoDone lipgloss.Style
	// TodoPending styles not-yet-started todo rows (muted gray, same weight as
	// ToolBody so only the active step draws color).
	TodoPending lipgloss.Style
	// Toast styles the transient bottom-right notice (e.g. "↓ 8 new lines ·
	// Ctrl+E to jump"): a filled block that floats over the transcript and
	// fades on its own, deliberately outside the permanent status bar.
	Toast lipgloss.Style
	// StatusBar styles the persistent bottom status bar.
	StatusBar lipgloss.Style
	// Accent styles file names and other highlighted tokens (blue).
	Accent lipgloss.Style
	// Error styles failure messages (red).
	Error lipgloss.Style
	// Warn styles warnings (yellow).
	Warn lipgloss.Style
	// Success styles successful outcomes (green).
	Success lipgloss.Style
	// ScrollThumb styles the transcript scrollbar thumb (medium gray block).
	ScrollThumb lipgloss.Style
	// ScrollTrack styles the transcript scrollbar track (dim shaded column).
	ScrollTrack lipgloss.Style
	// Spinner styles the animated "working" indicator glyph + verb (warm coral).
	Spinner lipgloss.Style
	// MenuHeader styles the category headers in the slash-command popup (bold,
	// terminal default color, so the groups stand out above the dimmed rows).
	MenuHeader lipgloss.Style
	// DiffAdd styles added lines in a rendered diff (green).
	DiffAdd lipgloss.Style
	// DiffDel styles removed lines in a rendered diff (red).
	DiffDel lipgloss.Style
	// DiffHunk styles @@ hunk markers in a rendered diff (cyan).
	DiffHunk lipgloss.Style
	// DiffCtx styles unchanged context lines and file headers in a rendered
	// diff (dim gray).
	DiffCtx lipgloss.Style
}

// Palette color numbers use the ANSI 256-color cube so the theme renders
// consistently across terminals without depending on true-color support.
const (
	colorSuccess    = "42"  // green
	colorError      = "196" // red
	colorWarn       = "214" // yellow/amber
	colorAccent     = "39"  // blue (file names, highlights)
	colorGray       = "245" // secondary / muted text
	colorScroll     = "250" // scrollbar thumb (bright gray pill, clearly visible)
	colorTrack      = "240" // scrollbar groove (dim gray, visible but recessive)
	colorTodoActive = "120" // in-progress todo row (light green, bold)
	colorUser       = "15"  // bright white
	colorUserBg     = "237" // user turn bar background (dark gray, codex history-cell parity)
	colorAssist     = "252" // near-white
	colorToastFg    = "231" // toast text (bright white, dark-gray block)
	colorToastBg    = "238" // toast background (neutral dark gray, readable over both themes)
	colorStatus     = "62"  // status bar background (violet)
	colorSpinner    = "173" // spinner glyph/verb (warm coral, matches Claude Code)
	colorDiffMeta   = "37"  // diff @@ hunk markers (cyan, git convention)

	colorToolName = "37" // tool name in a card headline (cyan, codex parity)

	// Inline-code colors for rendered Markdown. The stock glamour dark palette
	// paints inline code coral (256-color 203) on a chip, so prose dense with
	// `identifiers` — exactly what a coding assistant writes — becomes a wall of
	// red. Codex renders inline code and links in plain cyan; mirror that.
	colorInlineCodeDark  = "37" // light cyan, legible on dark terminals
	colorInlineCodeLight = "30" // dark cyan, legible on light terminals
)

// DefaultTheme returns the built-in palette described in the SPEC: success
// green, error/warn red & yellow, file/accent blue, and gray for secondary
// chrome. It performs no I/O and never panics, so callers can construct it
// eagerly at startup.
func DefaultTheme() Theme {
	return Theme{
		User: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorUser)).
			Background(lipgloss.Color(colorUserBg)).
			Bold(true),
		Assistant: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorAssist)),
		System: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)).
			Italic(true),
		ToolVerb: lipgloss.NewStyle().
			Bold(true),
		ToolName: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorToolName)).
			Bold(true),
		ToolCmd: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorAssist)),
		ToolBody: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)),
		TodoActive: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorTodoActive)).
			Bold(true),
		TodoDone: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)).
			Strikethrough(true).
			Faint(true),
		TodoPending: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)),
		Toast: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorToastFg)).
			Background(lipgloss.Color(colorToastBg)).
			Bold(true),
		StatusBar: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorUser)).
			Background(lipgloss.Color(colorStatus)),
		Accent: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorAccent)),
		Error: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorError)).
			Bold(true),
		Warn: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorWarn)),
		Success: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorSuccess)),
		ScrollThumb: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorScroll)),
		ScrollTrack: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorTrack)),
		Spinner: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorSpinner)).
			Bold(true),
		MenuHeader: lipgloss.NewStyle().
			Bold(true),
		DiffAdd: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorSuccess)),
		DiffDel: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorError)),
		DiffHunk: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorDiffMeta)),
		DiffCtx: lipgloss.NewStyle().
			Foreground(lipgloss.Color(colorGray)),
	}
}

// ellipsis is the single rune appended to truncated strings. It is itself a
// single display column, so it is cheap to reserve room for.
const ellipsis = "…"

// WrapToWidth wraps s to at most width display columns per line, measuring width
// by terminal cells (CJK and emoji count as two) via ui.Width rather than byte
// length. It never splits inside a multi-byte rune or a double-width character:
// a rune that would overflow the current line starts a new line instead. Any
// existing newlines in s are preserved as hard breaks. A non-positive width is
// treated as "no wrapping" and s is returned unchanged.
func WrapToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}

	var out strings.Builder
	lines := strings.Split(s, "\n")
	for li, line := range lines {
		if li > 0 {
			out.WriteByte('\n')
		}
		wrapLine(&out, line, width)
	}
	return out.String()
}

// wrapLine wraps a single newline-free line into out, breaking on rune
// boundaries so no double-width rune is ever cut in half.
func wrapLine(out *strings.Builder, line string, width int) {
	cur := 0 // display width accumulated on the current output line
	first := true
	for _, r := range line {
		rw := ui.Width(string(r))
		if !first && cur+rw > width {
			out.WriteByte('\n')
			cur = 0
		}
		out.WriteRune(r)
		cur += rw
		first = false
	}
}

// TruncateToWidth returns s clipped to at most width display columns, appending
// an ellipsis "…" when it removes content. Width is measured in terminal cells
// (CJK and emoji count as two) via ui.Width, and truncation happens on rune
// boundaries so a double-width character is never sliced. The returned string's
// display width is guaranteed to be <= width. A non-positive width yields the
// empty string.
func TruncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ui.Width(s) <= width {
		return s
	}

	// Reserve room for the ellipsis. If width is too small to even hold the
	// ellipsis plus one column, fall back to fitting bare runes into width.
	budget := width - ui.Width(ellipsis)
	if budget <= 0 {
		return fitRunes(s, width)
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := ui.Width(string(r))
		if used+rw > budget {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString(ellipsis)
	return b.String()
}

// fitRunes packs as many leading runes of s as fit within width columns without
// any ellipsis, breaking on rune boundaries.
func fitRunes(s string, width int) string {
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := ui.Width(string(r))
		if used+rw > width {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}
