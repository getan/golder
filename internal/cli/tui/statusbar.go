package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/getan/golder/internal/cli/ui"
)

// statusBar renders the persistent bottom line described in the SPEC (US-003,
// Section 5.1): the provider (the block the app badge used to hold), model name,
// thinking level, cwd (with $HOME abbreviated to ~), git branch + dirty/ahead
// markers, context-usage %, and the current task text.
// It holds no styling of its own beyond the Theme's StatusBar style; all width
// fitting is done against ui.Width so CJK/emoji count as two columns.
//
// The component is a plain value: Update-side code copies it into the Model,
// mutates the exported-to-package snapshot fields via the setters, and calls
// Render(width) from View. It never performs I/O — the git probe lives in
// gitinfo.go and feeds it via SetGit.
type statusBar struct {
	theme Theme

	// provider / model / thinking are the live run configuration, re-read from
	// cli.LiveConfig through the setters whenever a command moves it (/model,
	// /provider, and the /resume that adopts another session's configuration).
	// provider fills the left-most block, where the app badge used to sit: which
	// gateway the conversation is talking to is identity the rest of the bar
	// cannot express — a model id does not name its gateway — while "golder" was
	// already on every line of the transcript.
	provider string

	// Static-ish config sourced from Options.
	model    string
	thinking string

	// cwd is the launch directory with $HOME already abbreviated to "~".
	cwd string

	// git is the latest probe result; rendered only when git.ok is true.
	git gitInfoMsg

	// contextPct is the latest context-window utilization in percent [0,100],
	// derived from telemetryMsg (ContextUtilization * 100). -1 means unknown, so
	// the segment is hidden until the first telemetry arrives.
	contextPct int

	// tokens is the most recently observed context-token count (ContextTokens),
	// shown alongside the percentage. 0 means unknown/not yet reported.
	tokens int

	// task is the current activity text (e.g. the running tool or turn state).
	task string

	// plain drops the powerline's filled blocks in favor of colored text on the
	// terminal's own background. It is set from the terminal's color profile (see
	// SetColorProfile): the ribbon spends one distinct background per segment, and
	// a 16-color terminal cannot supply them — its palette maps the blocks onto a
	// single color, which is how model, thinking level and path all came out the
	// same shade with the separators between them invisible. Fewer colors means
	// less to draw with, not a worse version of the same picture.
	plain bool
}

// newStatusBar builds a status bar from the theme, resolved Options, and the
// launch directory. contextPct starts at -1 (unknown) so the token segment stays
// hidden until telemetry arrives.
func newStatusBar(theme Theme, opts Options, cwd string) statusBar {
	return statusBar{
		theme:      theme,
		provider:   opts.ProviderName,
		model:      opts.Model,
		thinking:   string(opts.ThinkingLevel),
		cwd:        abbreviateHome(cwd),
		contextPct: -1,
	}
}

// SetGit stores the latest git probe result.
func (s *statusBar) SetGit(g gitInfoMsg) { s.git = g }

// SetColorProfile records the terminal's color capability, choosing which of the
// bar's two renderings to draw with. The ribbon spends one distinct background per
// field; a 16-color terminal has sixteen colors in total and its own idea of what
// each one looks like, so it cannot supply them. Approximating instead is what
// painted the model, thinking level and path the same blue with the separators
// between them invisible; below 256 the bar therefore drops the blocks and colors
// the text.
func (s *statusBar) SetColorProfile(p colorprofile.Profile) {
	s.plain = p < colorprofile.ANSI256
}

// SetProvider updates the displayed provider (gateway) name. The value is live
// state like the model: /provider, /model and a /resume that adopted another
// session's configuration all move it. An empty name hides the segment rather
// than printing a guess.
func (s *statusBar) SetProvider(provider string) { s.provider = provider }

// SetModel updates the displayed model name after a /model switch.
func (s *statusBar) SetModel(model string) { s.model = model }

// SetThinking updates the displayed reasoning-effort level after a /model switch
// (including its `think` form).
func (s *statusBar) SetThinking(level string) { s.thinking = level }

// SetTelemetry updates the context-usage percentage from a telemetry event.
// A zero/unknown window (ContextWindow == 0) leaves the segment hidden.
func (s *statusBar) SetTelemetry(ev telemetryEventView) {
	if ev.window <= 0 {
		s.contextPct = -1
		s.tokens = 0
		return
	}
	pct := int(ev.util*100 + 0.5)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	s.contextPct = pct
	if ev.tokens > 0 {
		s.tokens = ev.tokens
	}
}

// SetTask records the current activity text shown at the far right / high
// priority slot of the bar.
func (s *statusBar) SetTask(task string) { s.task = task }

// telemetryEventView is the minimal projection of agentcore.TelemetryEvent the
// status bar needs, so the caller (model.go) adapts the event rather than this
// file depending on agentcore directly for a two-field read.
type telemetryEventView struct {
	util   float64
	window int
	tokens int
}

// Glyphs prefixing each segment plus the powerline separator, matching the
// decorated Claude-Code-plugin look. The segment icons are common Unicode; the
// separator (sepArrow) is a powerline glyph in the private-use area that Nerd
// Fonts and most modern terminal fonts render. Each measures one display column
// and ui.Width accounts for it during truncation.
const (
	glyphGit   = "⎇" // git branch
	glyphDirty = "●" // uncommitted changes
	glyphAhead = "⇡" // commits ahead of upstream
	glyphModel = "✱" // model name
	glyphThink = "✽" // thinking level
	glyphCwd   = "▸" // working directory
	glyphCtx   = "◔" // context-window usage
	glyphTask  = "⏵" // current activity

	sepArrow = "" // filled right arrow — used at a background transition
)

// Powerline palette (ANSI 256-color cube, so it renders without true-color).
// Every segment is its own colored block. The arrow between two segments is
// drawn in the LEFT block's background color so it reads as that item's color
// spilling into the next; a closing arrow caps the final block back to the bar.
const (
	sbBarBg = "236" // bar background behind the trailing pad

	sbProvFg = "233" // provider text (dark, on light gray)
	sbProvBg = "252" // provider block (light gray)

	sbGitFg = "231" // git text
	sbGitBg = "65"  // git block (muted green)

	sbModelFg = "231" // model text
	sbModelBg = "97"  // model block (muted purple)

	sbThinkFg = "231" // thinking text
	sbThinkBg = "60"  // thinking block (slate)

	sbCwdFg = "231" // cwd text
	sbCwdBg = "67"  // cwd block (steel blue)

	sbCtxFg = "236" // context text (dark, on amber)
	sbCtxBg = "179" // context block (amber/gold)

	sbTaskFg = "231" // task text
	sbTaskBg = "131" // task block (muted terracotta)

	// The plain-mode foregrounds, one per field. These are the first sixteen ANSI
	// indexes, which every terminal defines and every profile can express, so the
	// fields stay distinguishable by color alone when the blocks are gone. They
	// are picked to differ in hue rather than in brightness, since the terminal's
	// theme decides how light each one is.
	sbPlainProv  = "15" // bright white
	sbPlainGit   = "10" // bright green
	sbPlainModel = "13" // bright magenta
	sbPlainThink = "12" // bright blue
	sbPlainCwd   = "14" // bright cyan
	sbPlainCtx   = "11" // bright yellow
	sbPlainTask  = "9"  // bright red
)

// sepPlain joins the fields in plain mode. A dim bullet reads as a separator
// without competing with the labels, and — unlike the ribbon's arrow, which is
// drawn in a block's own color — it needs no second color to be visible.
const sepPlain = " · "

// segment is one labelled field of the bar together with its colors and
// truncation priority. bg == "" means the segment sits on the bar background;
// a non-empty bg gives it a filled powerline block. Higher priority survives
// longer when the terminal is too narrow.
type segment struct {
	text string
	fg   string
	bg   string // "" => bar background
	// plainFg colors the segment's TEXT when the bar is in plain mode, where
	// there are no blocks to distinguish fields by. It is one of the sixteen ANSI
	// colors (indexes 0-15), so the terminal's own theme decides the shade and the
	// color survives both the 16-color and the 256-color profile unchanged.
	plainFg  string
	priority int // larger = kept longer under truncation
}

// Priority order (SPEC: task > model/app > token > git > cwd). Higher is more
// important and dropped/truncated last.
const (
	prioCwd      = 0
	prioGit      = 1
	prioToken    = 2
	prioModel    = 3
	prioProvider = 3 // the provider rides at the model tier
	prioTask     = 4
)

// Render lays the bar out to exactly the configured width, in one of two forms.
// The ribbon (the default) draws each segment as a filled block joined to the next
// by an arrow in the left block's background color, padded with the bar
// background; the plain form (below 256 colors) draws the same fields as colored
// text joined by a separator. Both take their fields, order and priorities from
// segments(), so the two forms differ only in what they paint with.
//
// Either way a bar too wide for the terminal drops whole segments from lowest to
// highest priority, and if even the single highest-priority segment still
// overflows its text is truncated. The rendered row's display width (ui.Width,
// which ignores ANSI) is always exactly width for width > 0; a non-positive width
// yields the empty string.
func (s statusBar) Render(width int) string {
	if width <= 0 {
		return ""
	}

	if s.plain {
		return s.renderPlain(width)
	}
	segs := s.segments()
	for len(segs) > 0 {
		ribbon, w := renderRibbon(segs)
		if w <= width {
			return ribbon + barPad(width-w)
		}
		if len(segs) == 1 {
			break
		}
		segs = dropLowest(segs)
	}

	// Even a single highest-priority segment overflows: hard-truncate its text
	// onto the bar background (no separators, so width stays bounded).
	txt := TruncateToWidth(s.highestText(), width)
	base := lipgloss.NewStyle().Foreground(lipgloss.Color(sbProvFg)).Background(lipgloss.Color(sbBarBg))
	return base.Render(txt) + barPad(width-ui.Width(txt))
}

// renderRibbon builds the styled powerline string for segs and returns it with
// its visible width (excluding ANSI). The left edge starts at the first
// segment's background; a closing arrow caps any trailing block back to the bar.
func renderRibbon(segs []segment) (string, int) {
	resolve := func(bg string) string {
		if bg == "" {
			return sbBarBg
		}
		return bg
	}

	var b strings.Builder
	vis := 0
	for i, seg := range segs {
		curBg := resolve(seg.bg)
		if i > 0 {
			// The separator arrow is filled with the LEFT block's background, so
			// it matches the item it flows out of, sitting on the next block's bg.
			leftBg := resolve(segs[i-1].bg)
			b.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color(leftBg)).
				Background(lipgloss.Color(curBg)).
				Render(sepArrow))
			vis += ui.Width(sepArrow)
		}
		content := " " + seg.text + " "
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(seg.fg)).
			Background(lipgloss.Color(curBg)).
			Render(content))
		vis += ui.Width(content)
	}

	// Cap a trailing colored block with an arrow back to the bar background.
	if lastBg := resolve(segs[len(segs)-1].bg); lastBg != sbBarBg {
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(lastBg)).
			Background(lipgloss.Color(sbBarBg)).
			Render(sepArrow))
		vis += ui.Width(sepArrow)
	}
	return b.String(), vis
}

// renderPlain draws the bar for a terminal with fewer than 256 colors: the same
// fields as colored text on the terminal's own background, joined by a separator.
//
// It reuses the ribbon's truncation policy — drop whole segments lowest-priority
// first, then truncate the last one's text — so the two forms agree about what is
// important, and it pads with unstyled spaces rather than the bar background,
// because painting a background is the thing this form exists to avoid.
func (s statusBar) renderPlain(width int) string {
	segs := s.segments()
	for len(segs) > 0 {
		line, w := s.plainLine(segs)
		if w <= width {
			return line + strings.Repeat(" ", width-w)
		}
		if len(segs) == 1 {
			break
		}
		segs = dropLowest(segs)
	}
	// Even one field overflows: truncate its text, keeping its color.
	seg := segs[0]
	text := TruncateToWidth(seg.text, width)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(seg.plainFg)).Render(text)
}

// plainLine renders segs and reports their display width. Each field keeps its
// own color; the separator between them is dim, so it separates without
// competing with the labels.
func (s statusBar) plainLine(segs []segment) (string, int) {
	var b strings.Builder
	vis := 0
	for i, seg := range segs {
		if i > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(colorGray)).Render(sepPlain))
			vis += ui.Width(sepPlain)
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(seg.plainFg)).Render(seg.text))
		vis += ui.Width(seg.text)
	}
	return b.String(), vis
}

// barPad returns n spaces painted with the bar background so the row fills the
// full terminal width. n <= 0 yields the empty string.
func barPad(n int) string {
	if n <= 0 {
		return ""
	}
	return lipgloss.NewStyle().
		Background(lipgloss.Color(sbBarBg)).
		Render(strings.Repeat(" ", n))
}

// segments builds the ordered list of visible segments. Order in the slice is
// the left-to-right display order; priority governs truncation, not position.
func (s statusBar) segments() []segment {
	var segs []segment

	// The provider leads the bar as a filled block, in the slot the app badge
	// used to hold. It is live state (the setters move it with the model), and it
	// answers what the model segment cannot: which gateway this conversation is
	// talking to. An unknown provider renders nothing rather than a placeholder.
	if s.provider != "" {
		segs = append(segs, segment{text: s.provider, fg: sbProvFg, bg: sbProvBg, plainFg: sbPlainProv, priority: prioProvider})
	}

	if s.git.ok {
		segs = append(segs, segment{text: s.gitText(), fg: sbGitFg, bg: sbGitBg, plainFg: sbPlainGit, priority: prioGit})
	}
	if s.model != "" {
		segs = append(segs, segment{text: glyphModel + " " + s.model, fg: sbModelFg, bg: sbModelBg, plainFg: sbPlainModel, priority: prioModel})
	}
	if s.thinking != "" {
		// Thinking rides with the model priority — it is cheap and contextual.
		segs = append(segs, segment{text: glyphThink + " " + s.thinking, fg: sbThinkFg, bg: sbThinkBg, plainFg: sbPlainThink, priority: prioModel})
	}
	if s.cwd != "" {
		segs = append(segs, segment{text: glyphCwd + " " + s.cwd, fg: sbCwdFg, bg: sbCwdBg, plainFg: sbPlainCwd, priority: prioCwd})
	}
	if s.contextPct >= 0 {
		// The context readout is the highlighted amber block on the right.
		segs = append(segs, segment{text: s.ctxText(), fg: sbCtxFg, bg: sbCtxBg, plainFg: sbPlainCtx, priority: prioToken})
	}
	if s.task != "" {
		segs = append(segs, segment{text: glyphTask + " " + s.task, fg: sbTaskFg, bg: sbTaskBg, plainFg: sbPlainTask, priority: prioTask})
	}
	return segs
}

// gitText formats the git segment, e.g. "⎇ master ●3 ⇡4": branch, then "●N" for
// N dirty entries and "⇡N" for N commits ahead, each shown only when non-zero.
func (s statusBar) gitText() string {
	var b strings.Builder
	b.WriteString(glyphGit + " " + s.git.branch)
	if s.git.dirty > 0 {
		fmt.Fprintf(&b, " %s%d", glyphDirty, s.git.dirty)
	}
	if s.git.ahead > 0 {
		fmt.Fprintf(&b, " %s%d", glyphAhead, s.git.ahead)
	}
	return b.String()
}

// ctxText formats the context segment, e.g. "◔ 90,866 (46%)" when the token
// count is known, or "◔ 46%" before the first token count arrives.
func (s statusBar) ctxText() string {
	if s.tokens > 0 {
		return fmt.Sprintf("%s %s (%d%%)", glyphCtx, humanizeInt(s.tokens), s.contextPct)
	}
	return fmt.Sprintf("%s %d%%", glyphCtx, s.contextPct)
}

// humanizeInt renders n with thousands separators, e.g. 90866 → "90,866".
func humanizeInt(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// highestText returns the text of the highest-priority segment, used as the last
// thing standing when the terminal cannot even fit one full segment.
func (s statusBar) highestText() string {
	segs := s.segments()
	if len(segs) == 0 {
		return ""
	}
	best := segs[0]
	for _, seg := range segs[1:] {
		if seg.priority > best.priority {
			best = seg
		}
	}
	return best.text
}

// dropLowest removes one occurrence of the lowest-priority segment, preserving
// display order among the rest. It returns the shortened slice.
func dropLowest(segs []segment) []segment {
	if len(segs) == 0 {
		return segs
	}
	lowIdx := 0
	for i, seg := range segs {
		if seg.priority < segs[lowIdx].priority {
			lowIdx = i
		}
	}
	out := make([]segment, 0, len(segs)-1)
	out = append(out, segs[:lowIdx]...)
	out = append(out, segs[lowIdx+1:]...)
	return out
}

// abbreviateHome replaces a leading $HOME in path with "~" so the status bar
// stays compact. It leaves paths outside $HOME untouched and never fails.
func abbreviateHome(path string) string {
	home := homeDir()
	if home == "" || path == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// homeDir returns the user's home directory, or "" when it cannot be
// determined. Kept as a tiny wrapper so abbreviateHome stays testable.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
