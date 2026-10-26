package tui

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
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

	// mode is the live approval mode ("auto", "ask", "read-only",
	// "full-access"), published by the session. It answers the question the
	// dialog and the notes keep answering per call — who decides whether a tool
	// may run — once, permanently, so a person does not have to open a dialog
	// to remember whether they are being asked at all. It is empty until a
	// session exists, and hidden then rather than guessed at.
	mode string

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
	ascii bool // SSH uses simple glyphs/separators, never PUA or filled blocks

	// revealLeft / revealMax / scroll carry the one-pass motion a long value gets
	// right after it changes: while revealLeft > 0 the elided fields slide from
	// their head to their tail (offset scroll, capped at revealMax), dwell for
	// revealDwell ticks, and settle onto the static elision. A bar that is still by
	// default and moves only when something changed is a bar that can be ignored
	// until it has something to say.
	revealLeft int
	revealMax  int
	scroll     int

	// pin is the segment a click fixed open; the zero value means "none" (see
	// segFocus). It is interaction state the Model owns and sets; the layout reads
	// it through foldLayout. There is deliberately no hover state: motion without a
	// button held needs xterm 1003, whose event volume is not worth the expansion.
	pin segFocus

	// protocol reports the wire the live provider is actually using, including a
	// wire the adaptive fallback moved the model to mid-session. It is read on
	// every render, so the segment changes as soon as the provider does; a nil
	// source hides the segment.
	protocol func() string
}

// Fold: every field but the pinned one is collapsed.
//
// The bar carries more fields than a terminal has room for, and every one of them
// is wanted (they come from the SPEC's own list). Collapsing is the compromise:
// each field keeps its glyph and as much of its value as the row can afford, and
// the spare columns are shared among the folded fields rather than left as a
// sliver apiece. No field is privileged — the provider and the branch fold like
// every other one — so a single rule explains the whole bar.
//
// A click expands one: it pins the segment under the pointer, and a second click
// on it — or a click on the bar's background — releases it. What is painted and
// what the pointer hits come from the same layout (see statusBar.row), so the two
// can never disagree.

// segFocus is a status-bar segment index that can be unset: the zero value means
// "nothing focused", so a Model built as a struct literal is already correct
// without an initializer.
type segFocus int

// focusIndex converts a 0-based segment index into a focus value.
func focusIndex(i int) segFocus { return segFocus(i + 1) }

// index returns the 0-based segment index, or -1 when nothing is focused.
func (f segFocus) index() int { return int(f) - 1 }

// valid reports whether f names a segment that exists in a list of n.
func (f segFocus) valid(n int) bool {
	i := f.index()
	return i >= 0 && i < n
}

// statusCell is one column of the bar. A double-width rune occupies its own cell
// plus a continuation cell (r == 0), so a CJK path or task text stays aligned
// while the row remains addressable column by column — which is what makes hit
// testing exact.
type statusCell struct {
	r  rune
	fg string
	bg string
	// seg is the owning segment, shifted one up so a zeroed cell means "bar
	// background" (see focusIndex).
	seg int
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
		protocol:   protocolSource(opts.Provider, opts.Protocol),
	}
}

// protocolSource builds the live protocol readout: the provider's current wire
// when it can report one, otherwise the protocol the user selected. The closure is
// deliberately read per render — a wire fallback can change it mid-turn, and the
// status bar should follow without an event or a timer.
func protocolSource(p provider.Provider, protocol string) func() string {
	initial := protocolWireLabel(protocol)
	return func() string {
		if p != nil {
			if wr, ok := p.(provider.WireReporter); ok {
				if w := wr.CurrentWire(); w != "" {
					return string(w)
				}
			}
		}
		return initial
	}
}

// protocolWireLabel maps the user-facing protocol selector to the wire name the
// status bar shows. An empty or unknown selector stays empty, which hides the
// segment rather than guessing.
func protocolWireLabel(protocol string) string {
	switch protocol {
	case provider.ProtocolOpenAI:
		return string(provider.WireChat)
	case provider.ProtocolOpenAIResponses:
		return string(provider.WireResponses)
	case provider.ProtocolAnthropic:
		return string(provider.WireMessages)
	case provider.ProtocolGemini:
		return string(provider.WireGemini)
	default:
		return strings.TrimSpace(protocol)
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
	s.plain = s.ascii || p < colorprofile.ANSI256
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

// SetMode publishes the live approval mode. It is set with the other live
// fields (see Model.syncLiveDisplay), so /permissions is visible the moment it
// takes effect rather than only when the next dialog appears.
func (s *statusBar) SetMode(mode string) { s.mode = mode }

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
	glyphMode  = "⚖" // approval mode (who decides a tool call)
	glyphThink = "✽" // thinking level
	glyphCwd   = "▸" // working directory
	glyphCtx   = "◔" // context-window usage
	glyphTask  = "⏵" // current activity
	glyphProto = "⇄" // wire protocol actually in use

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

	sbModeFg = "234" // approval mode text (dark, on mid gray)
	sbModeBg = "245" // approval mode block
	// full-access wears the alert block: it is the one mode where nothing else
	// on the bar warns that review and the sandbox are both switched off.
	sbModeWarnFg = "231"
	sbModeWarnBg = "131"

	sbModelFg = "231" // model text
	sbModelBg = "97"  // model block (muted purple)

	sbThinkFg = "231" // thinking text
	sbThinkBg = "60"  // thinking block (slate)

	sbCwdFg = "231" // cwd text
	sbCwdBg = "67"  // cwd block (steel blue)

	sbProtoFg = "231" // protocol text
	sbProtoBg = "31"  // protocol block (teal)

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
	sbPlainMode  = "7"  // white for the default modes…
	sbPlainWarn  = "9"  // …and bright red for full-access
	sbPlainModel = "13" // bright magenta
	sbPlainThink = "12" // bright blue
	sbPlainCwd   = "14" // bright cyan
	sbPlainProto = "6"  // cyan
	sbPlainCtx   = "11" // bright yellow
	sbPlainTask  = "9"  // bright red
)

// sepPlain joins the fields in plain mode. A dim bullet reads as a separator
// without competing with the labels, and — unlike the ribbon's arrow, which is
// drawn in a block's own color — it needs no second color to be visible.
const sepPlain = " · "

func (s statusBar) plainSeparator() string {
	if s.ascii {
		return " | "
	}
	return sepPlain
}

func (s statusBar) glyph(normal, ascii string) string {
	if s.ascii {
		return ascii
	}
	return normal
}

// segment is one labelled field of the bar together with its colors and
// truncation priority. bg == "" means the segment sits on the bar background;
// a non-empty bg gives it a filled powerline block. Higher priority survives
// longer when the terminal is too narrow.
type segment struct {
	text string
	// scrollable marks a field whose value can be longer than the bar can afford
	// (the model name, the cwd). Such a field is never dropped for space: it is
	// windowed and scrolled instead, because a bar that loses the model or the
	// path is worse than one whose long value moves. win is the window width in
	// display columns, decided per render (0 = the full text).
	scrollable bool
	win        int
	// short is a compact form of the value, spent before the field starts to
	// scroll: a path abbreviates to the directory it ends in ("…/golder"), which
	// fits where the whole thing does not and keeps the bar still. A field with
	// nothing to give up (the model name) leaves it empty and windows instead.
	short string
	fg    string
	bg    string // "" => bar background
	// plainFg colors the segment's TEXT when the bar is in plain mode, where
	// there are no blocks to distinguish fields by. It is one of the sixteen ANSI
	// colors (indexes 0-15), so the terminal's own theme decides the shade and the
	// color survives both the 16-color and the 256-color profile unchanged.
	plainFg  string
	priority int // larger = kept longer under truncation
	// folded marks a segment drawn in its collapsed form (see foldLayout): its
	// value reduced to the glyph plus as much of the tail as its window affords.
	folded bool
	// id is the segment's stable focus value. It is assigned once in segments()
	// and survives layout drops, so a pin still names the same field when a narrow
	// terminal removes its neighbours.
	id segFocus
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

// Render lays the bar out to exactly the configured width as one row of cells,
// in one of two forms. The ribbon (the default) draws each segment as a filled
// block joined to the next by an arrow in the left block's background color; the
// plain form (below 256 colors) draws the same fields as colored text joined by a
// separator. Both take their fields, order and priorities from segments(), so the
// two forms differ only in what they paint with.
//
// The fold is applied either way (see the fold comment above). A focused field is
// elided rather than dropped when it cannot fit — the model name and the cwd keep
// their tail behind an ellipsis (see elideTail), so the model is still
// recognisable and the path still names the directory — and only when the fixed
// fields alone cannot fit (a terminal narrower than one block) does the SPEC
// priority order drop one. The row is always exactly width columns wide; a
// non-positive width yields the empty string.
func (s statusBar) Render(width int) string {
	if width <= 0 {
		return ""
	}
	return renderStatusRow(s.row(width))
}

// row lays the bar out as a fixed-width cell row with the fold applied. Both the
// renderer and the hit test read this one function, and each cell carries the
// segment it belongs to, so what is painted and what the pointer hits cannot
// disagree and a span is never computed twice.
func (s statusBar) row(width int) []statusCell {
	cells := make([]statusCell, width)
	for i := range cells {
		cells[i] = s.fillerCell()
	}
	segs := s.foldLayout(width)

	x := 0
	for i, seg := range segs {
		if x >= width {
			break
		}
		x = s.drawSeparator(cells, segs, i, x)
		fg, bg := seg.fg, s.blockBg(seg)
		if s.plain {
			// The plain form has no blocks to separate: a field keeps its own
			// color on the terminal's background.
			fg, bg = seg.plainFg, ""
		}
		own := seg.id
		if !s.plain {
			x = drawStatusText(cells, x, " ", fg, bg, own)
		}
		x = drawStatusText(cells, x, s.segText(seg), fg, bg, own)
		if !s.plain {
			x = drawStatusText(cells, x, " ", fg, bg, own)
		}
	}
	// Cap a trailing colored block with an arrow back to the bar background.
	if !s.plain && len(segs) > 0 {
		if lastBg := s.blockBg(segs[len(segs)-1]); lastBg != sbBarBg {
			drawStatusText(cells, x, sepArrow, lastBg, sbBarBg, 0)
		}
	}
	return cells
}

// drawSeparator paints what sits between segment i-1 and segment i: the ribbon's
// arrow (filled with the LEFT block's background so it reads as that item's color
// spilling into the next) or the plain form's dim bullet. Folded segments use the
// same separator as expanded ones: a block boundary reads as a hard vertical cut,
// while the arrow is the bar's own way of saying "the next field follows".
func (s statusBar) drawSeparator(cells []statusCell, segs []segment, i, x int) int {
	switch {
	case i == 0:
		return x
	case s.plain:
		return drawStatusText(cells, x, s.plainSeparator(), colorGray, "", 0)
	default:
		return drawStatusText(cells, x, sepArrow, s.blockBg(segs[i-1]), s.blockBg(segs[i]), 0)
	}
}

// fillerCell is one column of bar background: the ribbon paints it, and the plain
// form leaves a plain space, because painting a background is the thing the plain
// form exists to avoid.
func (s statusBar) fillerCell() statusCell {
	if s.plain {
		return statusCell{r: ' ', fg: colorGray}
	}
	return statusCell{r: ' ', fg: sbBarBg, bg: sbBarBg}
}

// blockBg resolves a segment's background, falling back to the bar's own for a
// segment that has none.
func (s statusBar) blockBg(seg segment) string {
	if seg.bg == "" {
		return sbBarBg
	}
	return seg.bg
}

// drawStatusText writes text into cells from x, attributing every cell to own (a
// segment focus, 0 for the bar itself) and returning the new x. A double-width
// rune claims a continuation cell so the columns after it stay addressable; text
// that would run past the row is dropped, which is the only truncation the canvas
// needs — the row is exactly as wide as the terminal.
func drawStatusText(cells []statusCell, x int, text, fg, bg string, own segFocus) int {
	for _, r := range text {
		w := 1
		if r >= 0x80 { // the only runes that can be double width
			w = ui.Width(string(r))
		}
		if w <= 0 {
			continue
		}
		if x+w > len(cells) {
			return len(cells)
		}
		cells[x] = statusCell{r: r, fg: fg, bg: bg, seg: int(own)}
		if w == 2 {
			cells[x+1] = statusCell{r: 0, fg: fg, bg: bg, seg: int(own)}
		}
		x += w
	}
	return x
}

// renderStatusRow turns the cell row into the styled line the terminal draws.
// Runs of cells sharing a style are rendered together, so a full-width bar costs
// a handful of style sequences rather than one per segment.
func renderStatusRow(cells []statusCell) string {
	var b strings.Builder
	for i := 0; i < len(cells); {
		if cells[i].r == 0 {
			// The continuation of a wide rune already written.
			i++
			continue
		}
		j := i
		var run strings.Builder
		for j < len(cells) && cells[j].fg == cells[i].fg && cells[j].bg == cells[i].bg {
			if cells[j].r != 0 {
				run.WriteRune(cells[j].r)
			}
			j++
		}
		b.WriteString(cellStyle(cells[i].fg, cells[i].bg).Render(run.String()))
		i = j
	}
	return b.String()
}

// cellStyle builds the style for one run. A cell with no background (the plain
// form) keeps the terminal's own.
func cellStyle(fg, bg string) lipgloss.Style {
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
	if bg != "" {
		st = st.Background(lipgloss.Color(bg))
	}
	return st
}

// segmentAt maps a column of the bar to the segment drawn there, or the zero
// focus when the column is bar background or outside the row.
//
// It walks the same fold layout row paints — the same separators, the same
// windows — without building the cell row, so a click costs a layout pass rather
// than a canvas. TestStatusBarSegmentAtAgreesWithTheRow pins the two against each
// other, column by column.
func (s statusBar) segmentAt(x, width int) segFocus {
	if x < 0 || x >= width {
		return 0
	}
	segs := s.foldLayout(width)
	pos := 0
	found := segFocus(0)
	for i, seg := range segs {
		switch {
		case i == 0:
		case s.plain:
			pos += ui.Width(s.plainSeparator())
		default:
			pos += ui.Width(sepArrow)
		}
		block := ui.Width(s.segText(seg))
		if !s.plain {
			block += 2 // the ribbon pads every block with one space on each side
		}
		if x >= pos && x < pos+block {
			found = seg.id
		}
		pos += block
	}
	return found
}

// foldLayout fits the bar to width with the fold applied.
//
// The folded fields are not fixed-width slivers: after the lead and the focused
// field have what they need, every remaining column is shared among them, so a
// folded field shows as much of its value as the row can afford (the model name,
// the last path components, the context percentage). A narrow terminal shrinks
// them back toward the three-column minimum and then drops the lowest-priority
// fields; a wide one shows them whole.
//
// The pinned field takes its natural width first, so the path shows in full while
// it is open, and the fields after it are the ones that give the columns back.
// Only when even that cannot fit does the pin itself get elided.
func (s statusBar) foldLayout(width int) []segment {
	segs := s.segments()
	if len(segs) == 0 || width <= 0 {
		return nil
	}
	focus := s.focus(len(segs))
	focusID := segFocus(0)
	if focus >= 0 {
		focusID = focusIndex(focus)
	}
	for i := range segs {
		if i != focus {
			segs[i].folded = true
		}
	}

	pad := 0
	sep := ui.Width(s.plainSeparator())
	closing := 0
	if !s.plain {
		pad = 2
		sep = ui.Width(sepArrow)
		closing = ui.Width(sepArrow)
	}
	seps := func(n int) int {
		switch {
		case n <= 0:
			return 0
		case n == 1:
			return closing
		default:
			return (n-1)*sep + closing
		}
	}
	// A folded field's block is exactly the window the layout gave it: the text
	// is padded to that width (see segText), so the columns promised here are the
	// columns painted and the row can be filled exactly.
	cost := func(seg segment) int {
		if seg.folded {
			return seg.win + pad
		}
		return ui.Width(s.segText(seg)) + pad
	}
	total := func() int {
		n := seps(len(segs))
		for _, seg := range segs {
			n += cost(seg)
		}
		return n
	}

	// The narrowest form: folded fields keep their glyph and one value character;
	// the lead and the focus ask for their whole value.
	for i := range segs {
		if segs[i].folded {
			segs[i].win = foldMinWidth(segs[i].text)
		} else {
			segs[i].win = 0
		}
	}

	// Drop by priority while even the narrow form cannot fit.
	for total() > width && len(segs) > 1 {
		idx := dropCandidate(segs, focusID)
		if idx < 0 {
			break
		}
		segs = append(segs[:idx], segs[idx+1:]...)
	}
	// Then shrink what is left, the focus last: a field at one column is still a
	// field, and the alternative is an overflowing row.
	for total() > width {
		idx := shrinkCandidate(segs, focusID)
		if idx < 0 {
			break
		}
		seg := &segs[idx]
		natural := ui.Width(seg.text)
		cur := seg.win
		if cur <= 0 {
			cur = natural
		}
		if cur > 1 {
			seg.win = cur - 1
			continue
		}
		drop := dropCandidate(segs, focusID)
		if drop < 0 || len(segs) <= 1 {
			break
		}
		segs = append(segs[:drop], segs[drop+1:]...)
	}

	// Spend every remaining column on the folded fields, a round at a time so a
	// short value stops growing while its neighbours keep going.
	extra := width - total()
	for extra > 0 {
		var cand []int
		for i, seg := range segs {
			if seg.folded && seg.win < ui.Width(seg.text) {
				cand = append(cand, i)
			}
		}
		if len(cand) == 0 {
			break
		}
		share := extra / len(cand)
		if share < 1 {
			share = 1
		}
		progressed := false
		for _, i := range cand {
			if extra == 0 {
				break
			}
			grow := ui.Width(segs[i].text) - segs[i].win
			if grow > share {
				grow = share
			}
			if grow > extra {
				grow = extra
			}
			if grow > 0 {
				segs[i].win += grow
				extra -= grow
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return segs
}

// dropCandidate is the field to give up when the row cannot fit: the lowest
// priority, with the pinned field pushed to the end of the queue so it does not
// disappear while open. Ties go to the wider field, which frees more.
func dropCandidate(segs []segment, focusID segFocus) int {
	best, bestKey, bestW := -1, 0, 0
	for i, seg := range segs {
		key := seg.priority * 2
		if seg.id == focusID {
			key += 100
		}
		w := ui.Width(seg.text)
		if best < 0 || key < bestKey || (key == bestKey && w > bestW) {
			best, bestKey, bestW = i, key, w
		}
	}
	return best
}

// shrinkCandidate is the field to take one column from: the same priority order,
// the focus last, and among equals the widest first.
func shrinkCandidate(segs []segment, focusID segFocus) int {
	best, bestKey, bestW := -1, 0, 0
	for i, seg := range segs {
		cur := seg.win
		if cur <= 0 {
			cur = ui.Width(seg.text)
		}
		if cur <= 1 {
			continue
		}
		key := seg.priority * 2
		if seg.id == focusID {
			key += 100
		}
		if best < 0 || key < bestKey || (key == bestKey && cur > bestW) {
			best, bestKey, bestW = i, key, cur
		}
	}
	return best
}

// focus is the segment currently expanded: the one a click pinned, if any.
func (s statusBar) focus(n int) int {
	if s.pin.valid(n) {
		return s.pin.index()
	}
	return -1
}

// foldMinWidth is the narrowest a folded field is allowed to become: its glyph,
// a space, and the first character of its value (or just the glyph when the value
// has nothing after it).
func foldMinWidth(text string) int {
	natural := ui.Width(text)
	rs := []rune(text)
	if len(rs) == 0 {
		return 0
	}
	if natural <= 2 {
		return natural
	}
	glyphW := ui.Width(string(rs[0]))
	for _, r := range rs[1:] {
		if unicode.IsSpace(r) {
			continue
		}
		min := glyphW + 1 + ui.Width(string(r))
		if min > natural {
			return natural
		}
		return min
	}
	return natural
}

// foldWindow is a folded field's value in the window the layout gave it: the
// glyph stays, and the value keeps its tail (the end of a model name, the last
// path components, the percentage after the token count). A one-column window
// keeps only the glyph; a window with room for one value character shows that
// character, which is more useful than an ellipsis.
func foldWindow(text string, win int) string {
	natural := ui.Width(text)
	if win <= 0 || win >= natural {
		return text
	}
	rs := []rune(text)
	if len(rs) == 0 {
		return ""
	}
	glyph := string(rs[0])
	glyphW := ui.Width(glyph)
	if win <= glyphW {
		return TruncateToWidth(glyph, win)
	}
	value := strings.TrimSpace(string(rs[1:]))
	if value == "" {
		return glyph
	}
	remaining := win - glyphW - 1 // the space after the glyph
	if remaining <= 0 {
		return glyph
	}
	runes := []rune(value)
	switch {
	case remaining == 1:
		return glyph + " " + string(runes[0])
	case remaining == 2:
		return glyph + " " + string(runes[0]) + "…"
	default:
		return glyph + " " + elideTail(value, remaining)
	}
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
	if s.mode != "" {
		// The approval mode sits beside git: it is state about the session, not
		// about the conversation, and it is the one segment that says whether a
		// tool call will be decided by a human or by review.
		fg, bg, plainFg := sbModeFg, sbModeBg, sbPlainMode
		if s.mode == permissions.FullAccess.String() {
			fg, bg, plainFg = sbModeWarnFg, sbModeWarnBg, sbPlainWarn
		}
		segs = append(segs, segment{text: s.glyph(glyphMode, "!") + " " + s.mode, fg: fg, bg: bg, plainFg: plainFg, priority: prioGit})
	}
	if s.model != "" {
		segs = append(segs, segment{text: s.glyph(glyphModel, "*") + " " + s.model, scrollable: true, fg: sbModelFg, bg: sbModelBg, plainFg: sbPlainModel, priority: prioModel})
	}
	// The protocol sits immediately after the model: which API dialect the model
	// is actually reached over right now. The provider can move a model between
	// wires mid-session (the adaptive fallback), and the closure reads that live
	// state, so the segment follows the move rather than repeating the launch flag.
	if s.protocol != nil {
		if name := strings.TrimSpace(s.protocol()); name != "" {
			segs = append(segs, segment{text: s.glyph(glyphProto, "=") + " " + name, fg: sbProtoFg, bg: sbProtoBg, plainFg: sbPlainProto, priority: prioModel})
		}
	}
	if s.thinking != "" {
		// Thinking rides with the model priority — it is cheap and contextual.
		segs = append(segs, segment{text: s.glyph(glyphThink, "~") + " " + s.thinking, fg: sbThinkFg, bg: sbThinkBg, plainFg: sbPlainThink, priority: prioModel})
	}
	if s.cwd != "" {
		segs = append(segs, segment{
			text:       s.glyph(glyphCwd, ">") + " " + s.cwd,
			short:      s.glyph(glyphCwd, ">") + " " + shortenPath(s.cwd),
			scrollable: true,
			fg:         sbCwdFg,
			bg:         sbCwdBg,
			plainFg:    sbPlainCwd,
			priority:   prioCwd,
		})
	}
	if s.contextPct >= 0 {
		// The context readout is the highlighted amber block on the right.
		segs = append(segs, segment{text: s.ctxText(), fg: sbCtxFg, bg: sbCtxBg, plainFg: sbPlainCtx, priority: prioToken})
	}
	if s.task != "" {
		segs = append(segs, segment{text: s.glyph(glyphTask, ">") + " " + s.task, fg: sbTaskFg, bg: sbTaskBg, plainFg: sbPlainTask, priority: prioTask})
	}
	// Stable ids: a layout drop removes segments, but the pin still has to name
	// the same field it was set on.
	for i := range segs {
		segs[i].id = focusIndex(i)
	}
	return segs
}

// gitText formats the git segment, e.g. "⎇ master ●3 ⇡4": branch, then "●N" for
// N dirty entries and "⇡N" for N commits ahead, each shown only when non-zero.
func (s statusBar) gitText() string {
	var b strings.Builder
	b.WriteString(s.glyph(glyphGit, "g") + " " + s.git.branch)
	if s.git.dirty > 0 {
		fmt.Fprintf(&b, " %s%d", s.glyph(glyphDirty, "*"), s.git.dirty)
	}
	if s.git.ahead > 0 {
		fmt.Fprintf(&b, " %s%d", s.glyph(glyphAhead, "^"), s.git.ahead)
	}
	return b.String()
}

// ctxText formats the context segment, e.g. "◔ 90,866 (46%)" when the token
// count is known, or "◔ 46%" before the first token count arrives.
func (s statusBar) ctxText() string {
	if s.tokens > 0 {
		return fmt.Sprintf("%s %s (%d%%)", s.glyph(glyphCtx, "%"), humanizeInt(s.tokens), s.contextPct)
	}
	return fmt.Sprintf("%s %d%%", s.glyph(glyphCtx, "%"), s.contextPct)
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

// elideFloor is the narrowest a long field is squeezed before the layout stops
// giving it room: below a handful of columns there is nothing left to recognise.
const elideFloor = 6

// shortenPath compacts a display path to the directory name it ends in, behind an
// ellipsis ("~/work/harness/golder" → "…/golder"), so a cwd that does not fit at
// full length can still say which checkout the session is editing — without the
// bar having to move. A bare "~" has nothing to give up and comes back unchanged.
func shortenPath(p string) string {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(parts) < 2 {
		return p
	}
	return "…/" + parts[len(parts)-1]
}

// shrinkToShorts replaces every field's value with its compact form, reporting
// whether any field had one to spend.
func shrinkToShorts(segs []segment) bool {
	changed := false
	for i := range segs {
		if segs[i].short == "" || segs[i].short == segs[i].text {
			continue
		}
		segs[i].text = segs[i].short
		segs[i].short = ""
		changed = true
	}
	return changed
}

// elided is the settled (still) form of a field: the whole value when it fits,
// otherwise its tail behind an ellipsis (see elideTail).
func (s statusBar) elided(seg segment) string {
	if seg.win <= 0 || ui.Width(seg.text) <= seg.win {
		return seg.text
	}
	return elideTail(seg.text, seg.win)
}

// segText returns what a segment shows now: its settled form, or — while a change
// reveal is running — a window of the same width sliding from the head toward the
// tail. Matching the settled width is what keeps the fields around it from moving
// as the motion stops.
func (s statusBar) segText(seg segment) string {
	if seg.folded {
		// A folded field shows its window as-is: the change reveal slides a long
		// value into view, and a folded field is already the settled slice the
		// layout chose for it.
		text := foldWindow(seg.text, seg.win)
		if w := ui.Width(text); w < seg.win {
			text += strings.Repeat(" ", seg.win-w)
		}
		return text
	}
	settled := s.elided(seg)
	if s.revealLeft <= 0 || settled == seg.text {
		return settled
	}
	return slideWindow(seg.text, ui.Width(settled), s.scroll)
}

// revealDwell is how many ticks a change reveal rests on the tail before settling
// onto the static elision, so the end of the value is readable.
const revealDwell = 3

// StartReveal arms the one-pass motion a value gets right after it changes (a
// /model or /provider switch, a session adopted, the first frame — wherever the
// bar's configuration is republished). The step count is how far the widest elided
// field must slide to show its tail; with nothing elided there is nothing to
// reveal and the bar simply stays still.
func (s *statusBar) StartReveal(width int) {
	s.revealLeft, s.revealMax, s.scroll = 0, 0, 0
	if width <= 0 {
		return
	}
	steps := 0
	for _, seg := range s.foldLayout(width) {
		// A folded field is still by design: its window is the settled slice the
		// layout chose, not something to slide into view. Only a focused field
		// moves.
		if seg.folded || seg.win <= 0 || seg.win >= ui.Width(seg.text) {
			continue
		}
		if need := ui.Width(seg.text) - ui.Width(s.elided(seg)); need > steps {
			steps = need
		}
	}
	if steps <= 0 {
		return
	}
	s.revealMax, s.revealLeft = steps, steps+revealDwell
}

// AdvanceReveal steps the reveal one tick and reports whether it is still running.
// The chain ends by itself: there is no clock on the bar unless something changed.
func (s *statusBar) AdvanceReveal() bool {
	if s.revealLeft <= 0 {
		return false
	}
	s.revealLeft--
	if s.scroll < s.revealMax {
		s.scroll++
	}
	return s.revealLeft > 0
}

// slideWindow returns the w-column window of text at offset off, clamped so the
// window never runs past either end — the motion a change reveal makes, head to
// tail. Display width is honoured and the result measures exactly w columns.
func slideWindow(text string, w, off int) string {
	if w <= 0 {
		return ""
	}
	cols := displayColumns(text)
	if len(cols) <= w {
		return text
	}
	start := off
	if start > len(cols)-w {
		start = len(cols) - w
	}
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	used := 0
	for c := start; c < len(cols) && used < w; {
		r := cols[c]
		if r == 0 {
			b.WriteByte(' ')
			used++
			c++
			continue
		}
		rw := ui.Width(string(r))
		if used+rw > w {
			b.WriteByte(' ')
			used++
			c++
			continue
		}
		b.WriteRune(r)
		used += rw
		c += rw
	}
	return b.String()
}

// elideTail shortens a segment's text to width display columns, keeping the tail —
// the part that names the thing — behind a leading ellipsis. A path is cut at a
// component boundary ("▸ …/harness/golder"), so a directory name is never sliced
// in half; anything else is cut by column ("✱ …haiku-5-5"). The same width always
// yields the same slice: a bar whose long values sit still is a bar the reader can
// ignore.
func elideTail(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if ui.Width(text) <= width {
		return text
	}
	prefix, value := "", text
	if i := strings.IndexByte(text, ' '); i >= 0 {
		prefix, value = text[:i+1], text[i+1:]
	}
	if strings.Contains(value, "/") {
		if elided := elidePathTail(prefix, value, width); elided != "" {
			return elided
		}
	}
	return truncateTail(text, width)
}

// elidePathTail keeps the trailing whole components of a path that fit width
// behind "…/" ("▸ …/harness/golder"), and returns "" when not even the last one
// fits, leaving that case to the column-wise cut.
func elidePathTail(prefix, path string, width int) string {
	parts := strings.Split(path, "/")
	var kept []string
	used := 0
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part == "" {
			continue
		}
		add := ui.Width(part)
		if len(kept) > 0 {
			add++ // the slash joining it to the component kept after it
		}
		if ui.Width(prefix)+ui.Width("…/")+used+add > width {
			break
		}
		kept = append([]string{part}, kept...)
		used += add
	}
	if len(kept) == 0 {
		return ""
	}
	return prefix + "…/" + strings.Join(kept, "/")
}

// truncateTail shortens s to width display columns by dropping leading runes and
// marking the cut with an ellipsis, so the tail survives. A wide rune with no room
// for its second column renders as a blank in that column, which keeps the result
// exactly width columns wide.
func truncateTail(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	cols := displayColumns(s)
	if len(cols) <= width {
		return s
	}
	budget := width - 1 // the ellipsis takes one column
	start := len(cols) - budget
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	b.WriteString("…")
	used := 0
	for c := start; c < len(cols) && used < budget; {
		r := cols[c]
		if r == 0 {
			b.WriteByte(' ')
			used++
			c++
			continue
		}
		w := ui.Width(string(r))
		if used+w > budget {
			b.WriteByte(' ')
			used++
			c++
			continue
		}
		b.WriteRune(r)
		used += w
		c += w
	}
	return b.String()
}

// displayColumns maps a string to one entry per display column: the rune that
// occupies the column, or 0 for the trailing column of a wide rune.
func displayColumns(s string) []rune {
	cols := make([]rune, 0, ui.Width(s))
	for _, r := range s {
		cols = append(cols, r)
		if ui.Width(string(r)) == 2 {
			cols = append(cols, 0)
		}
	}
	return cols
}

// layout decides the window each scrolling field gets at this width. Every other
// field holds a short value (a provider name, a branch, an effort level, a
// percentage) and is drawn whole, so the row's own width decides how much is left
// for the model name and the path; the two share it in proportion to what they
// want to show. A fixed field is dropped only when it cannot fit even alone,
// which keeps the bar bounded on a terminal narrower than a single block — the one
// case where the SPEC priority order still applies.
func (s statusBar) layout(segs []segment, width int) []segment {
	out := make([]segment, len(segs))
	copy(out, segs)

	cost := func(seg segment) int {
		n := ui.Width(s.segText(seg))
		if !s.plain {
			n += 2 // the ribbon pads every block with one space on each side
		}
		return n
	}
	seps := func(n int) int {
		switch {
		case n <= 0:
			return 0
		case s.plain:
			return (n - 1) * ui.Width(sepPlain)
		default:
			return n // an arrow between each pair, plus the closing one
		}
	}
	sum := func() int {
		n := seps(len(out))
		for _, seg := range out {
			n += cost(seg)
		}
		return n
	}

	// Everything fits at full length: nothing to abbreviate, nothing to scroll.
	if sum() <= width {
		return out
	}

	// One column is the narrowest a scrolling field ever gets: below that there
	// is nothing to scroll, so the field would simply vanish. That floor is the
	// row's minimum width, and fixed fields give way while even that cannot fit.
	pad := 0
	if !s.plain {
		pad = 2
	}
	minSum := func() int {
		n := seps(len(out))
		for _, seg := range out {
			if seg.scrollable {
				n += 1 + pad
				continue
			}
			n += cost(seg)
		}
		return n
	}
	for minSum() > width {
		idx := lowestFixed(out)
		if idx < 0 {
			break
		}
		out = append(out[:idx], out[idx+1:]...)
	}

	// A field with a natural abbreviation spends it before it starts to scroll: a
	// path shortened to the directory it ends in ("…/golder") fits where the whole
	// thing does not, and a bar that can stand still should. Scrolling is the
	// answer for what even the short form cannot hold — which is how a roomy
	// terminal (a full-size window) and a phone-sized one get different behaviour
	// out of the same width arithmetic.
	shortened := make([]segment, len(out))
	copy(shortened, out)
	if shrinkToShorts(shortened) {
		total := seps(len(shortened))
		for _, seg := range shortened {
			total += cost(seg)
		}
		if total <= width {
			return shortened
		}
	}

	// The scrolling fields take their own padding too (the ribbon pads every
	// block), so the text budget they share is the room left over once that
	// padding is set aside — otherwise the arithmetic promises columns the
	// renderer cannot deliver and the row overflows into the truncation fallback.
	room := width - fixedWidth(out, cost, seps)
	scrollables := 0
	for _, seg := range out {
		if seg.scrollable {
			scrollables++
		}
	}
	room -= scrollables * pad
	want := 0
	for _, seg := range out {
		if seg.scrollable {
			want += ui.Width(seg.text)
		}
	}
	if want == 0 {
		return out
	}
	for i := range out {
		if !out[i].scrollable {
			continue
		}
		natural := ui.Width(out[i].text)
		share := 1
		if room > 0 {
			if s := room * natural / want; s > share {
				share = s
			}
		}
		if share > natural {
			share = natural
		}
		if share < elideFloor && room >= elideFloor {
			share = elideFloor
		}
		if share < natural {
			out[i].win = share
		}
	}
	// The floor can overshoot when the room is tiny: shave the widest window until
	// the row fits. A window never goes below one column; past that there is
	// nothing left to scroll, and Render's truncation fallback takes over.
	for sum() > width {
		idx := widestWindow(out)
		if idx < 0 || out[idx].win <= 1 {
			break
		}
		out[idx].win--
	}
	return out
}

// fixedWidth is the display width a layout's non-scrolling fields take together
// with every separator.
func fixedWidth(segs []segment, cost func(segment) int, seps func(int) int) int {
	n := seps(len(segs))
	for _, seg := range segs {
		if !seg.scrollable {
			n += cost(seg)
		}
	}
	return n
}

// lowestFixed returns the index of the lowest-priority non-scrolling segment, or
// -1 when there is none left.
func lowestFixed(segs []segment) int {
	idx := -1
	for i, seg := range segs {
		if seg.scrollable {
			continue
		}
		if idx < 0 || seg.priority < segs[idx].priority {
			idx = i
		}
	}
	return idx
}

// widestWindow returns the index of the scrolling field with the widest window, or
// -1 when none is windowed.
func widestWindow(segs []segment) int {
	idx := -1
	for i, seg := range segs {
		if seg.win <= 0 {
			continue
		}
		if idx < 0 || seg.win > segs[idx].win {
			idx = i
		}
	}
	return idx
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
