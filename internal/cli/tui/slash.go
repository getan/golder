// This file implements slash-commands and their autocomplete popup for the
// full-screen TUI (US-008, FR-15). It is the TUI counterpart to the REPL's
// slash handling (internal/cli/repl/repl.go): both front-ends consult the SAME
// shared registry assembled by internal/cli/prompts.BuildSlashRegistry (#383),
// so /model, /help, user-declared templates (~/.golder/{commands,prompts}),
// config/CLI prompt templates, plugin commands and ~/.agents/skills /skill-name
// commands are identical across the two surfaces.
//
// tui deliberately imports prompts/runtime/cli (the shared lower layers), never
// repl: prompts sits below both front-ends, so there is no import cycle.
//
// The autocomplete popup (slashMenu) activates while the input buffer is a
// "/name" being typed (a leading "/" with no whitespace yet). It filters the
// registry by the typed prefix, is navigated with the arrow keys, completed with
// Tab, and run with Enter — the model intercepts those keys before delegating to
// the textarea (see model.handleKey).
package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// maxMenuRows caps how many candidate rows the popup shows at once; a longer
// filtered list scrolls a window around the selection so the overlay stays a few
// lines tall regardless of how many commands are registered.
const maxMenuRows = 8

// Line caps for the highlighted row's detail block — the wrapped description plus
// the usage hint — before the tail is cut with an ellipsis.
//
// The cap is tiered by source because the two populations are written for
// different readers. A built-in description is ours, is written to be read in
// this menu, and with its hint still fits in four lines down to a ~68-column
// terminal — so four is a backstop, not a cut users meet routinely. An
// extension's description is frontmatter written for the system prompt, where
// length costs nothing and nobody is scanning a list: a skill or template body
// legitimately runs to a paragraph. Those get a two-line preview, which is enough
// to say what the command is; the text the agent receives is untouched, and
// whoever wants the rest can read the file it came from.
const (
	maxBuiltinDetailLines   = 4
	maxExtensionDetailLines = 2
)

// commandDetailLineCap returns the detail-block line cap for a command. It keys
// off the same source test CategoryOf uses to group commands, so "the Extensions
// block" and "the two-line cap" are the same set by construction rather than by
// two lists that could drift apart.
func commandDetailLineCap(c runtime.SlashCommand) int {
	if c.Source != runtime.SourceBuiltin {
		return maxExtensionDetailLines
	}
	return maxBuiltinDetailLines
}

// minCommandDescWidth is the narrowest description column worth wrapping into.
// Under it the terminal is too narrow for a hanging column to hold anything
// readable — a four-column well of chopped words is worse than the one-line row
// it would replace — so the plain row stands.
const minCommandDescWidth = 24

// pickTone colors a picker badge or detail note by meaning: configured/ready
// (green), needs attention (yellow), explanatory (gray), or plain.
type pickTone int

const (
	pickPlain pickTone = iota
	pickOK
	pickWarn
	pickMuted
)

// pickInfoLine is one labeled line of a picker row's detail block, rendered
// only for the highlighted row: the label is dim, the value plain, and the
// note carries the status color. It separates "what you can configure here"
// (label/value) from "where it stands right now" (note) instead of cramming
// every fact into the selectable row.
type pickInfoLine struct {
	Label string
	Value string
	Note  string
	Tone  pickTone
}

// newSlashRegistry assembles the shared slash-command registry for the TUI the
// same way the REPL does: built-ins seeded from runtime, the live-state /model
// and /help commands bound to live (so a /model switch mutates the very config
// the run loop reads), user/plugin/skill/template commands from disk. A load
// error is non-fatal — BuildSlashRegistry still returns a registry with the
// built-ins, so the TUI stays usable and the failure is surfaced on stderr.
func newSlashRegistry(opts Options, live *cli.LiveConfig) *runtime.SlashRegistry {
	// A credentials store resolved from the same flags the run uses, so
	// "/model" live catalog listing (issue #566) can authenticate against the live endpoint.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(opts.ProviderName, opts.APIKey)
	reg, err := prompts.BuildSlashRegistry(live, creds, opts.Skills, opts.Plugins, prompts.PromptTemplateSources{
		Settings: opts.ConfigPrompts,
		CLI:      opts.CliPrompts,
		Disable:  opts.NoPromptTemplates,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "golder: slash-commands: %v\n", err)
	}
	return reg
}

// slashMenu is the autocomplete popup state. It holds the candidates matching
// the current "/prefix" and the highlighted row; it is inactive (rendered as
// nothing) whenever the buffer is not a slash-command being typed or no command
// matches the prefix.
type slashMenu struct {
	theme    Theme
	active   bool
	filtered []runtime.SlashCommand
	selected int
	// pick, when non-nil, puts the menu in picker mode: rows are plain items
	// (no "/" prefix) and Enter confirms the highlight through the pick path
	// instead of slash resolution. Used by the /model and /resume pickers.
	pick []pickItem
	// pickMark annotates the row whose Value matches (e.g. the current model
	// or session) in picker mode.
	pickMark string
	// pickKind selects the confirm action: "model" re-runs /model, "resume"
	// re-runs /resume with the picked Value, "model-level" is stage 2 of the
	// /model flow and runs /model <model> <level> with the pending model.
	pickKind string
	// pickModel is the stage-1 model kept pending while the stage-2 reasoning
	// level picker is open (pickKind "model-level"); "" otherwise.
	pickModel string
	// pickHint is the persistent key legend rendered under the picker. A
	// transcript line scrolls away; a hint that sits under the list is how a
	// key nobody typed before is discoverable at the moment it matters.
	pickHint string
	// pickDefault is set while the level stage is choosing a DEFAULT level (the
	// model picker's `d` key) rather than switching this session.
	pickDefault bool
}

// pickItem is one picker row: Title renders first, Detail (dimmer context)
// second, Value is handed to the confirm action.
type pickItem struct {
	Title string
	// Status is a short state badge after the title ("key set", "key needed");
	// StatusTone colors it. Keep it to a couple of words so rows stay scannable.
	Status     string
	StatusTone pickTone
	// Detail is a dim context suffix (kept for pickers that do not use the
	// labeled Info block, e.g. resume dates).
	Detail string
	Value  string
	// Current marks the row as the live selection; view renders a "current" tag.
	Current bool
	// Default marks the row as the provider new sessions start on (the
	// config.toml default); view renders a "default" tag. It is separate from
	// Current on purpose: this session and the next one are different answers,
	// and conflating them would hide which one a switch changed.
	Default bool
	// Heading marks a non-selectable section title inside a picker: the arrow
	// keys step over it, Enter on it does nothing, and it renders as a dim rule
	// rather than a row. It exists because a picker that must show something it
	// cannot switch to — the reviewer's decision model, which answers typed
	// questions rather than chat — has only two honest options: name it in a
	// section of its own, or leave it out and let its absence read as an
	// oversight.
	Heading bool
	// Info is the highlighted row's labeled detail block, rendered indented
	// under the list so it reads as an explanation, not more rows.
	Info []pickInfoLine
}

// newSlashMenu builds an inactive menu bound to the theme used for its rows.
func newSlashMenu(theme Theme) slashMenu { return slashMenu{theme: theme} }

// slashToken reports whether buffer is a slash-command name still being typed
// and returns the text after the leading "/". It is true only for a leading "/"
// with no whitespace yet: once the user types a space the name is complete and
// the buffer has moved on to arguments, so name-completion stops.
func slashToken(buffer string) (token string, ok bool) {
	trimmed := strings.TrimLeft(buffer, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	rest := trimmed[1:]
	if strings.ContainsAny(rest, " \t\n") {
		return "", false
	}
	return rest, true
}

// refresh recomputes the menu from the current buffer and registry. It activates
// only when the buffer is a "/name" prefix that matches at least one command;
// otherwise it deactivates and clears its candidates. The selection is clamped
// so it stays in range as the filtered set shrinks.
func (mn *slashMenu) refresh(buffer string, reg *runtime.SlashRegistry) {
	if mn.picking() {
		return
	}
	token, ok := slashToken(buffer)
	if !ok || reg == nil {
		mn.close()
		return
	}
	var out []runtime.SlashCommand
	for _, c := range reg.List() {
		if strings.HasPrefix(c.Name, token) {
			out = append(out, c)
		}
	}
	// Group order: category first (CategoryOrder), then name. The popup renders
	// one header per contiguous category block, so the sort is what makes the
	// grouping contiguous.
	slices.SortStableFunc(out, func(a, b runtime.SlashCommand) int {
		if ra, rb := categoryRank(runtime.CategoryOf(a)), categoryRank(runtime.CategoryOf(b)); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Name, b.Name)
	})
	mn.filtered = out
	mn.active = len(out) > 0
	if mn.selected >= len(out) || mn.selected < 0 {
		mn.selected = 0
	}
}

// categoryRank returns the position of a category in runtime.CategoryOrder;
// unknown categories sort after the known ones.
func categoryRank(cat string) int {
	for i, c := range runtime.CategoryOrder {
		if c == cat {
			return i
		}
	}
	return len(runtime.CategoryOrder)
}

// rows reports how many terminal rows the popup occupies when rendered, so the
// model can reserve that space above the input line during relayout. It is zero
// while inactive and otherwise the visible window height (min of the candidate
// count and maxMenuRows).
// row renders one command candidate as "/name  description". Picker rows use
// pickRowLine (segmented colors and badges) instead.
func (mn slashMenu) row(i int) string {
	return commandRowText(mn.filtered[i])
}

// pickRowLine renders one selectable picker row: caret + title (accent when
// highlighted, terminal default otherwise), a colored status badge, the dim
// detail suffix, and a "current" tag. Only selectable rows render at this
// indent; the highlighted row's explanation is the Info block below.
func (mn slashMenu) pickRowLine(i, rowWidth int) string {
	it := mn.pick[i]
	if it.Heading {
		// A rule, not a row: no caret, no badge, dim, and it never carries the
		// highlight because navigation steps over headings.
		return ansi.Truncate(mn.theme.System.Render("  "+it.Title), rowWidth, "…")
	}
	var b strings.Builder
	if i == mn.selected {
		b.WriteString(mn.theme.Accent.Render("› "))
		b.WriteString(mn.theme.Accent.Render(it.Title))
	} else {
		b.WriteString("  " + it.Title)
	}
	if it.Status != "" {
		b.WriteString("  " + mn.toneStyle(it.StatusTone).Render(it.Status))
	}
	if it.Detail != "" {
		b.WriteString("  " + mn.theme.System.Render(it.Detail))
	}
	if it.Current || (it.Value != "" && it.Value == mn.pickMark) {
		b.WriteString("  " + mn.theme.Accent.Render("(current)"))
	}
	if it.Default {
		b.WriteString("  " + mn.theme.Accent.Render("(default)"))
	}
	return ansi.Truncate(b.String(), rowWidth, "…")
}

// pickInfoLines renders the highlighted row's detail block: a status/info card
// indented under the list, its first line joined by "└" so it cannot be
// mistaken for another selectable row. Labels are padded into one dim column,
// values stay plain, and notes carry the status color.
func (mn slashMenu) pickInfoLines(rowWidth int) []string {
	if mn.selected < 0 || mn.selected >= len(mn.pick) {
		return nil
	}
	info := mn.pick[mn.selected].Info
	if len(info) == 0 {
		return nil
	}
	labelW := 0
	for _, in := range info {
		if w := ansi.StringWidth(in.Label); w > labelW {
			labelW = w
		}
	}
	out := make([]string, 0, len(info))
	for idx, in := range info {
		prefix := "    "
		if idx == 0 {
			prefix = "  └ "
		}
		var b strings.Builder
		b.WriteString(prefix)
		b.WriteString(mn.theme.System.Render(fmt.Sprintf("%-*s", labelW, in.Label)))
		if in.Value != "" {
			b.WriteString("  " + in.Value)
		}
		if in.Note != "" {
			b.WriteString("  " + mn.toneStyle(in.Tone).Render(in.Note))
		}
		out = append(out, ansi.Truncate(b.String(), rowWidth, "…"))
	}
	return out
}

// toneStyle maps a pick tone to its theme style.
func (mn slashMenu) toneStyle(t pickTone) lipgloss.Style {
	switch t {
	case pickOK:
		return mn.theme.Success
	case pickWarn:
		return mn.theme.Warn
	case pickMuted:
		return mn.theme.System
	default:
		return lipgloss.NewStyle()
	}
}

// commandRowText renders one command candidate as "/name  description".
func commandRowText(c runtime.SlashCommand) string {
	line := "/" + c.Name
	if c.Description != "" {
		line += "  " + c.Description
	}
	return line
}

// menuLine is one display line of a command row: the text, and whether it is the
// command's usage hint rather than its description — the caller styles the two
// differently, because they answer different questions ("what is this" vs "how do
// I call it").
type menuLine struct {
	text string
	hint bool
}

// commandRowLines renders one command candidate as display lines (plain: no
// caret, no styling — the caller adds both). Every row is the single
// "/name  description" line, cut with an ellipsis when it overflows. The
// HIGHLIGHTED row instead wraps its description, and follows it with the
// command's usage hint, all indented under its own description column — so the
// command the user is actually considering is readable in full, and its
// arguments are visible at the moment they are about to be typed.
//
// The asymmetry is the point. Aligning every description into a column, or
// wrapping every row, would turn an eight-row list into a page and make the
// common case — short descriptions you scan past — pay for the rare long one.
// The ellipsis on the other rows still says "there is more here", and arrowing
// onto one is how you read it.
//
// The hint is a field of its own (ArgumentHint reads as the command's signature;
// the REPL's completion and /help render it too), which is why the block is built
// from two sources rather than expecting the description to carry usage. Past the
// cap for the command's own source (commandDetailLineCap) the tail is cut with an
// ellipsis, so neither a verbose built-in nor a paragraph-long skill description
// can push the composer off the screen.
func commandRowLines(c runtime.SlashCommand, selected bool, rowWidth int) []menuLine {
	plain := commandRowText(c)
	if !selected || (c.Description == "" && c.ArgumentHint == "") {
		return []menuLine{{text: TruncateToWidth(plain, rowWidth)}}
	}
	// The description column: every wrapped line hangs under the text, not under
	// the name, which is what keeps the "/name" column readable at a glance.
	col := "/" + c.Name + "  "
	textWidth := rowWidth - ui.Width(col)
	if textWidth < minCommandDescWidth {
		// Too narrow to hang anything readable under: the plain row is the honest
		// answer, ellipsis and all.
		return []menuLine{{text: TruncateToWidth(plain, rowWidth)}}
	}
	var body []menuLine
	for _, text := range wrapBlock(c.Description, textWidth) {
		body = append(body, menuLine{text: text})
	}
	// The hint follows the description: "what it does" reads first, and a usage
	// string that outgrows the cap is the part worth sacrificing, since /help and
	// the completion popup both carry it.
	for _, text := range wrapBlock(c.ArgumentHint, textWidth) {
		body = append(body, menuLine{text: text, hint: true})
	}
	if cap := commandDetailLineCap(c); len(body) > cap {
		body = body[:cap]
		// Mark the dropped remainder on the last kept line, whose own text may
		// well fit — the ellipsis is what says "this is not all of it".
		last := body[cap-1]
		if !strings.HasSuffix(last.text, ellipsis) {
			last.text = TruncateToWidth(last.text+ellipsis, textWidth)
		}
		body[cap-1] = last
	}
	indent := strings.Repeat(" ", ui.Width(col))
	lines := make([]menuLine, 0, len(body))
	for i, ln := range body {
		if i == 0 {
			ln.text = col + ln.text
		} else {
			ln.text = indent + ln.text
		}
		lines = append(lines, ln)
	}
	return lines
}

// wrapBlock wraps one block of display text to textWidth, returning nothing for
// an empty string. Word wrapping is deliberate (see WrapToWidthWords): a
// description is prose, and the rune-based wrapper would split "read-only" across
// lines.
func wrapBlock(s string, textWidth int) []string {
	if s == "" {
		return nil
	}
	return strings.Split(WrapToWidthWords(s, textWidth), "\n")
}

// menuRowWidth is the number of columns one popup row may occupy: the overlay
// reserves a two-column gutter for the caret / indent.
func menuRowWidth(width int) int {
	if w := width - 2; w >= 1 {
		return w
	}
	return width
}

// menuRow is one display row of the command popup: either a category header or
// a candidate command. cmdIdx indexes into mn.filtered; -1 for a header.
type menuRow struct {
	header string
	cmd    runtime.SlashCommand
	cmdIdx int
}

// commandLayout expands the filtered candidates into display rows, inserting
// one category header before each group. The candidates are pre-sorted by
// category (see refresh), so each category appears as one contiguous block.
func (mn slashMenu) commandLayout() []menuRow {
	rows := make([]menuRow, 0, len(mn.filtered)+len(runtime.CategoryOrder))
	last := ""
	for i, c := range mn.filtered {
		cat := runtime.CategoryOf(c)
		if cat != last {
			rows = append(rows, menuRow{header: cat, cmdIdx: -1})
			last = cat
		}
		rows = append(rows, menuRow{cmd: c, cmdIdx: i})
	}
	return rows
}

// commandWindow returns the [start,end) slice of layout rows to render,
// scrolled so the selected command stays visible. Header rows count toward the
// maxMenuRows height but are not selectable.
func (mn slashMenu) commandWindow() (int, int) {
	rows := mn.commandLayout()
	n := len(rows)
	if n <= maxMenuRows {
		return 0, n
	}
	sel := 0
	for i, r := range rows {
		if r.cmdIdx == mn.selected {
			sel = i
			break
		}
	}
	start := sel - maxMenuRows + 1
	if start < 0 {
		start = 0
	}
	if start > n-maxMenuRows {
		start = n - maxMenuRows
	}
	return start, start + maxMenuRows
}

// rows reports how many terminal rows the popup occupies at the given width, so
// the model can reserve them out of the transcript. It counts the lines view
// actually draws — including the continuation lines a wrapped description adds,
// and the highlighted picker row's Info block — which is what keeps the reserved
// height and the drawn height from disagreeing.
func (mn slashMenu) rows(width int) int {
	if !mn.active || mn.count() == 0 {
		return 0
	}
	if mn.picking() {
		n := len(mn.pick)
		if n > maxMenuRows {
			n = maxMenuRows
		}
		if mn.selected >= 0 && mn.selected < len(mn.pick) {
			n += len(mn.pick[mn.selected].Info)
		}
		if mn.pickHint != "" {
			n++
		}
		return n
	}
	rows := mn.commandLayout()
	start, end := mn.commandWindow()
	rowWidth := menuRowWidth(width)
	n := 0
	for i := start; i < end; i++ {
		n += len(mn.commandLines(rows[i], rowWidth))
	}
	return n
}

// commandLines renders one command layout row to the styled display lines the
// popup shows: a category header, or a candidate behind the caret gutter ("› "
// on the highlighted row, two spaces otherwise). rows() counts these same lines,
// so the reserved height and the drawn height cannot drift apart.
func (mn slashMenu) commandLines(r menuRow, rowWidth int) []string {
	if r.header != "" {
		return []string{mn.theme.MenuHeader.Render("  " + TruncateToWidth(r.header, rowWidth))}
	}
	body := commandRowLines(r.cmd, r.cmdIdx == mn.selected, rowWidth)
	out := make([]string, 0, len(body))
	for i, ln := range body {
		// The first line of the highlighted row carries the caret and the accent;
		// its wrapped continuation lines stay dim under the same gutter, so the
		// caret marks the row rather than the paragraph it grew into.
		if i == 0 && r.cmdIdx == mn.selected {
			out = append(out, mn.theme.Accent.Render("› "+ln.text))
			continue
		}
		if ln.hint {
			// A usage hint reads as code, not as prose: it borrows the style the
			// transcript uses for command text, so "how do I call it" is visually
			// distinct from the sentence above it without another color.
			out = append(out, mn.theme.ToolCmd.Render("  "+ln.text))
			continue
		}
		out = append(out, mn.theme.System.Render("  "+ln.text))
	}
	return out
}

// close deactivates the menu and drops its candidates.
func (mn *slashMenu) close() {
	mn.active = false
	mn.filtered = nil
	mn.selected = 0
	mn.pick = nil
	mn.pickMark = ""
	mn.pickKind = ""
	mn.pickModel = ""
	mn.pickHint = ""
	mn.pickDefault = false
}

// openPicker shows the menu as an item picker (arrow keys + Enter, Esc
// cancels) for plain string items confirmed as /model. mark annotates the
// matching row, e.g. the current model.
func (mn *slashMenu) openPicker(items []string, mark string) {
	its := make([]pickItem, 0, len(items))
	for _, it := range items {
		its = append(its, pickItem{Title: it, Value: it})
	}
	mn.openPickerDetailed(its, mark, "model")
}

// openPickerDetailed shows the menu as an item picker with titled rows,
// confirmed through the kind path ("model" or "resume").
func (mn *slashMenu) openPickerDetailed(items []pickItem, mark, kind string) {
	mn.pick = items
	mn.pickMark = mark
	mn.pickKind = kind
	mn.pickModel = ""
	mn.pickHint = ""
	mn.pickDefault = false
	mn.active = len(items) > 0
	mn.filtered = nil
	mn.selected = mn.firstSelectable(0, 1)
}

// firstSelectable returns the index of the first selectable row at or after
// from, scanning in the direction of step (1 forward, -1 backward). A picker
// whose first row is a heading must open with a real row highlighted, or Enter
// would do nothing on a menu that looks ready.
func (mn slashMenu) firstSelectable(from, step int) int {
	for i := from; i >= 0 && i < len(mn.pick); i += step {
		if !mn.pick[i].Heading {
			return i
		}
	}
	return from
}

// openLevelPicker shows the second stage of the /model flow: the reasoning
// levels model advertises, with the current level marked and the level saved
// for this provider/model pair tagged as the default. model stays pending
// until the level is confirmed, so the confirm can run both steps as a single
// /model <id> <level>. Esc closes the menu (close clears the pending model)
// and cancels the switch entirely.
func (mn *slashMenu) openLevelPicker(model string, levels []agentcore.ThinkingLevel, mark, defaultLevel agentcore.ThinkingLevel) {
	items := make([]pickItem, 0, len(levels))
	for _, lvl := range levels {
		items = append(items, pickItem{
			Title:   string(lvl),
			Value:   string(lvl),
			Default: lvl == defaultLevel,
		})
	}
	mn.openPickerDetailed(items, string(mark), "model-level")
	mn.pickModel = model
	mn.pickHint = "↑↓ select · Enter apply · d set default for this model · Esc cancel"
}

// openDefaultLevelPicker is openLevelPicker for the model picker's `d` key:
// the chosen level is saved with the model instead of switching the session.
func (mn *slashMenu) openDefaultLevelPicker(model string, levels []agentcore.ThinkingLevel, mark, defaultLevel agentcore.ThinkingLevel) {
	mn.openLevelPicker(model, levels, mark, defaultLevel)
	mn.pickDefault = true
	mn.pickHint = "↑↓ select · Enter save as default · Esc cancel"
}

// picking reports whether the menu is in picker mode.
func (mn slashMenu) picking() bool { return mn.pick != nil }

// pickCurrent returns the highlighted pick item's Value.
func (mn slashMenu) pickCurrent() (string, bool) {
	if !mn.picking() || mn.selected < 0 || mn.selected >= len(mn.pick) {
		return "", false
	}
	if mn.pick[mn.selected].Heading {
		return "", false
	}
	v := mn.pick[mn.selected].Value
	if v == "" {
		v = mn.pick[mn.selected].Title
	}
	return v, true
}

// pickMove moves the picker highlight, wrapping at the ends.
func (mn *slashMenu) pickMove(d int) {
	if len(mn.pick) == 0 {
		return
	}
	// Step over headings, so the highlight never lands on a row that cannot be
	// chosen. The loop terminates because a picker of nothing but headings would
	// have had no selectable start either (see firstSelectable).
	for i := 0; i < len(mn.pick); i++ {
		mn.selected = (mn.selected + d + len(mn.pick)) % len(mn.pick)
		if !mn.pick[mn.selected].Heading {
			return
		}
	}
}

// moveUp / moveDown cycle the highlighted candidate, wrapping at the ends so
// arrow navigation is continuous. Both work in picker mode via count().
func (mn *slashMenu) moveUp() {
	if mn.picking() {
		mn.pickMove(-1)
		return
	}
	if n := mn.count(); n > 0 {
		mn.selected--
		if mn.selected < 0 {
			mn.selected = n - 1
		}
	}
}

func (mn *slashMenu) moveDown() {
	if mn.picking() {
		mn.pickMove(1)
		return
	}
	if n := mn.count(); n > 0 {
		mn.selected++
		if mn.selected >= n {
			mn.selected = 0
		}
	}
}

// current returns the highlighted candidate, or ok=false when the menu is
// inactive / empty.
func (mn slashMenu) current() (runtime.SlashCommand, bool) {
	if !mn.active || mn.selected < 0 || mn.selected >= len(mn.filtered) {
		return runtime.SlashCommand{}, false
	}
	return mn.filtered[mn.selected], true
}

// view renders the popup as a block of up to maxMenuRows lines, the highlighted
// row marked with a "›" caret and accented. Command rows carry a category
// header (Session, Model, …); picker rows render as plain items. Rows are
// truncated to the width so they never wrap. Returns "" when inactive so the
// model omits the overlay entirely (and its row) while idle.
func (mn slashMenu) view(width int) string {
	if !mn.active || mn.rows(width) == 0 {
		return ""
	}
	rowWidth := menuRowWidth(width)
	var lines []string
	if mn.picking() {
		start, end := mn.window()
		for i := start; i < end; i++ {
			lines = append(lines, mn.pickRowLine(i, rowWidth))
			// The detail block follows the highlighted row so the explanation
			// stays visually attached to what it explains.
			if i == mn.selected {
				lines = append(lines, mn.pickInfoLines(rowWidth)...)
			}
		}
		if mn.pickHint != "" {
			lines = append(lines, ansi.Truncate(mn.theme.System.Render(mn.pickHint), rowWidth, "…"))
		}
		return strings.Join(lines, "\n")
	}
	rows := mn.commandLayout()
	start, end := mn.commandWindow()
	for i := start; i < end; i++ {
		lines = append(lines, mn.commandLines(rows[i], rowWidth)...)
	}
	return strings.Join(lines, "\n")
}

// window returns the [start,end) slice of filtered candidates to display,
// scrolled to keep the selection visible when the list is taller than
// maxMenuRows.
// count is the candidate count in either mode.
func (mn slashMenu) count() int {
	if mn.picking() {
		return len(mn.pick)
	}
	return len(mn.filtered)
}

func (mn slashMenu) window() (int, int) {
	n := mn.count()
	if n <= maxMenuRows {
		return 0, n
	}
	start := mn.selected - maxMenuRows + 1
	if start < 0 {
		start = 0
	}
	if start > n-maxMenuRows {
		start = n - maxMenuRows
	}
	return start, start + maxMenuRows
}
