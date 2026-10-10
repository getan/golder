package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/getan/golder/internal/cli/ui"
)

// statusBar renders the persistent bottom line described in the SPEC (US-003,
// Section 5.1): model name, thinking level, cwd (with $HOME abbreviated to ~),
// git branch + dirty/ahead markers, context-usage %, and the current task text.
// It holds no styling of its own beyond the Theme's StatusBar style; all width
// fitting is done against ui.Width so CJK/emoji count as two columns.
//
// The component is a plain value: Update-side code copies it into the Model,
// mutates the exported-to-package snapshot fields via the setters, and calls
// Render(width) from View. It never performs I/O — the git probe lives in
// gitinfo.go and feeds it via SetGit.
type statusBar struct {
	theme Theme

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
}

// newStatusBar builds a status bar from the theme, resolved Options, and the
// launch directory. contextPct starts at -1 (unknown) so the token segment stays
// hidden until telemetry arrives.
func newStatusBar(theme Theme, opts Options, cwd string) statusBar {
	return statusBar{
		theme:      theme,
		model:      opts.Model,
		thinking:   string(opts.ThinkingLevel),
		cwd:        abbreviateHome(cwd),
		contextPct: -1,
	}
}

// SetGit stores the latest git probe result.
func (s *statusBar) SetGit(g gitInfoMsg) { s.git = g }

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

// appName is the badge shown at the far left of the bar.
const appName = "golder"

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

	sbAppFg = "233" // app badge text (dark, on light gray)
	sbAppBg = "252" // app badge block (light gray)

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
)

// segment is one labelled field of the bar together with its colors and
// truncation priority. bg == "" means the segment sits on the bar background;
// a non-empty bg gives it a filled powerline block. Higher priority survives
// longer when the terminal is too narrow.
type segment struct {
	text     string
	fg       string
	bg       string // "" => bar background
	priority int    // larger = kept longer under truncation
}

// Priority order (SPEC: task > model/app > token > git > cwd). Higher is more
// important and dropped/truncated last.
const (
	prioCwd   = 0
	prioGit   = 1
	prioToken = 2
	prioModel = 3
	prioApp   = 3 // the app badge rides at the model tier
	prioTask  = 4
)

// Render lays the bar out to exactly the configured width as a colored powerline
// ribbon. Each segment is a filled block joined to the next by an arrow drawn in
// the left block's background color, and the tail is padded with the bar
// background so the whole row is filled. When the ribbon would exceed the width
// it drops whole segments from lowest to highest priority; if even the single
// highest-priority segment still overflows it hard-truncates that segment's
// text. The rendered row's display width (ui.Width, which ignores ANSI) is
// always exactly width for width > 0; a non-positive width yields the empty
// string.
func (s statusBar) Render(width int) string {
	if width <= 0 {
		return ""
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
	base := lipgloss.NewStyle().Foreground(lipgloss.Color(sbAppFg)).Background(lipgloss.Color(sbBarBg))
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

	// App badge leads the bar as a filled block.
	segs = append(segs, segment{text: appName, fg: sbAppFg, bg: sbAppBg, priority: prioApp})

	if s.git.ok {
		segs = append(segs, segment{text: s.gitText(), fg: sbGitFg, bg: sbGitBg, priority: prioGit})
	}
	if s.model != "" {
		segs = append(segs, segment{text: glyphModel + " " + s.model, fg: sbModelFg, bg: sbModelBg, priority: prioModel})
	}
	if s.thinking != "" {
		// Thinking rides with the model priority — it is cheap and contextual.
		segs = append(segs, segment{text: glyphThink + " " + s.thinking, fg: sbThinkFg, bg: sbThinkBg, priority: prioModel})
	}
	if s.cwd != "" {
		segs = append(segs, segment{text: glyphCwd + " " + s.cwd, fg: sbCwdFg, bg: sbCwdBg, priority: prioCwd})
	}
	if s.contextPct >= 0 {
		// The context readout is the highlighted amber block on the right.
		segs = append(segs, segment{text: s.ctxText(), fg: sbCtxFg, bg: sbCtxBg, priority: prioToken})
	}
	if s.task != "" {
		segs = append(segs, segment{text: glyphTask + " " + s.task, fg: sbTaskFg, bg: sbTaskBg, priority: prioTask})
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
