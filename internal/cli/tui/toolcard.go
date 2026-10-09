package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/patch"
)

// This file implements the rich tool-call card component (US-006, SPEC 3.2,
// FR-6/7/8). A toolCard is a bordered inline block in the transcript that shows
// a single tool invocation: a header with the tool name and a status icon
// (running / success / warn), the decoded call arguments, the tool's response
// rendered as an indented tree, and - for tools that report a diff (edit,
// #560) - a colored Diff section. Cards are created on toolStartMsg,
// completed on toolEndMsg, and toggled between a collapsed preview and the full
// response view with Ctrl+T or a mouse click on the card (see model.go). All
// width math goes through ui.Width /
// WrapToWidth / TruncateToWidth so CJK and emoji (two columns) never split.

// cardState is the lifecycle of a tool card: running while the tool executes,
// then success or warn once it finishes (warn covers a reported tool error).
type cardState int

const (
	cardRunning cardState = iota
	cardSuccess
	cardWarn
)

// respNode is one line of a tool's response, with depth giving the tree indent
// level (each level is rendered as two leading spaces).
type respNode struct {
	text  string
	depth int
}

// toolCard is a single tool invocation rendered as a bordered card. input holds
// the decoded call arguments (nil when the args were not a JSON object);
// response is the parsed result tree, populated on completion; diff holds a
// unified diff the tool reported in its result metadata (edit today), rendered
// as its own colored section instead of plain response text. expanded flips
// the response between a capped preview and the full tree.
type toolCard struct {
	id       string
	name     string
	input    map[string]any
	response []respNode
	diff     string
	state    cardState
	expanded bool

	// notes holds the permission-gate verdicts this call received. A verdict
	// is published by the gate before the call executes, i.e. after the card
	// was announced (the model streamed the call first), so it is attached
	// here and rendered between the headline and the body: the note stays
	// directly below the command no matter how long the output or diff grows,
	// instead of being pushed away by it.
	notes []judge.Note
}

// setInput fills the decoded call arguments when the announce/start event
// delivers them after the card was created. Nil never clears existing input.
func (c *toolCard) setInput(input map[string]any) {
	if input == nil {
		return
	}
	c.input = input
}

// toggleExpanded flips the card between its collapsed summary and full detail.
func (c *toolCard) toggleExpanded() {
	c.expanded = !c.expanded
}

// renderForced draws the card as if its expand flag were expanded, without
// mutating it. The explore group uses this to render every member in full when
// the group is expanded while each card keeps its own (collapsed) state for a
// later group collapse.
func (c toolCard) renderForced(theme Theme, width int, expanded bool) string {
	c.expanded = expanded
	return c.render(theme, width)
}

// addNote attaches a permission-gate verdict to the card. The appended slice
// is ordered by arrival; in practice a call receives one verdict.
func (c *toolCard) addNote(n judge.Note) {
	c.notes = append(c.notes, n)
}

// abort closes a card that never received its end event (the run was
// interrupted or ended with the tool pending) as a warn, so a forever
// "Running" row does not promise output that will never arrive. A finished
// card is left untouched.
func (c *toolCard) abort() {
	if c.state == cardRunning {
		c.state = cardWarn
	}
}

// expandHint names the key that toggles a card between its collapsed summary
// and the full detail (codex parity: ctrl+t). A mouse click on the card does
// the same thing (see Model.pendingCardClick), which is how a card further up
// the transcript is expanded once newer calls have scrolled past it.
const expandHint = "ctrl+t"

// collapsedPreviewLines is how many response lines a collapsed card shows
// before the "… +N lines" hint. Keeping a few rows visible means a short
// command's output is readable without expanding, matching codex.
const collapsedPreviewLines = 3

// statusBullet renders the codex-style bullet in the state's theme color:
// dim gray while running, bold green on success, bold red on failure. The old
// ✓/! glyphs were retired here on purpose: codex reserves ✓ for the
// approval affordance (goal complete, trust prompts), while execution rows use
// only the colored bullet so success/failure reads at a glance.
func (c toolCard) statusBullet(theme Theme) string {
	switch c.state {
	case cardSuccess:
		return theme.Success.Render("•")
	case cardWarn:
		return theme.Error.Render("•")
	default:
		return theme.System.Render("•")
	}
}

// title is the one-line summary shared by the live collapsed card and the
// resume replay: codex-style `Running <name arg>` while the tool executes and
// `Ran <name arg>` once it finishes, so the transcript reads as a verb.
// Search-family cards are the exception and render as a sentence of their own
// (see webSearchLead).
func (c toolCard) title() string {
	if prefix, detail, ok := c.webSearchLead(); ok {
		return prefix + " " + detail
	}
	if cmd, ok := c.userShellCommand(); ok {
		if c.state == cardRunning {
			return "Running " + cmd
		}
		return "You ran " + cmd
	}
	header := c.headline()
	if c.state == cardRunning {
		return "Running " + header
	}
	return "Ran " + header
}

// render draws the card codex-style: the headline wraps a long command onto
// `  │ ` continuation lines (never a mid-line ellipsis), the collapsed card
// previews the first few response lines and then hints `… +N lines`, and the
// expanded card shows the input, response, and diff sections as
// gutter-prefixed lines (` │ ` argument continuations, ` └ ` output start)
// with no border anywhere.
//
// A todo card is the exception: its payload is the checklist the call
// submitted, so every row renders in both states and the tool's echo response
// (the same list re-rendered under a "Todos:" header) is skipped as pure
// duplication. The user reads the actual task list, not just "N tasks".
func (c toolCard) render(theme Theme, width int) string {
	out := c.renderHeadline(theme, width)
	if notes := c.renderNotes(theme, width); notes != "" {
		out += "\n" + notes
	}
	if items := c.todoItems(); len(items) > 0 && c.todoResponseIsEcho() {
		return out + c.renderChecklist(theme, width, items)
	}
	// A hosted search's "response" is the driver's display-only placeholder
	// ("hosted: web_search(query)"): the headline already carries the query, so
	// a body would only repeat it. Codex renders search activity as a single
	// line; match that.
	if c.isHostedSearch() {
		return out
	}
	if c.expanded {
		return out + c.renderDetail(theme, width)
	}
	preview, hidden := c.preview(theme, width, collapsedPreviewLines)
	out += preview
	if hidden > 0 {
		out += "\n" + theme.System.Render(
			WrapToWidth(fmt.Sprintf("… +%d lines (%s or click to expand)", hidden, expandHint), max(1, width)))
	}
	return out
}

// renderNotes paints the gate verdicts attached to this card, one line each,
// directly under the headline and above the body. The note text wraps to the
// transcript width exactly like a standalone review block would.
func (c toolCard) renderNotes(theme Theme, width int) string {
	if len(c.notes) == 0 {
		return ""
	}
	lines := make([]string, 0, len(c.notes))
	for _, n := range c.notes {
		lines = append(lines, renderNoteLine(theme, width, n))
	}
	return strings.Join(lines, "\n")
}

// renderHeadline renders the bullet plus `Running/Ran name arg`, wrapping the
// text onto `  │ ` continuation lines so a long command is fully readable
// instead of being cut with an ellipsis. It wraps at width-4 (the widest
// gutter) so both the bullet line and the continuations fit; the two columns
// the bullet leaves unused on the first line are harmless slack.
//
// Coloring follows codex's exec cell: the bullet carries the state color, the
// verb is bold in the terminal's default color, the tool name is cyan, and the
// command text is near-white. The old all-blue bold headline made every command
// read like a hyperlink.
func (c toolCard) renderHeadline(theme Theme, width int) string {
	// The search sentence is its own headline (no separate "Ran <name>" verb),
	// so render it before the generic path splits verb / name / argument.
	if prefix, detail, ok := c.webSearchLead(); ok {
		return c.renderSearchHeadline(theme, width, prefix, detail)
	}
	verb := "Ran"
	if c.state == cardRunning {
		verb = "Running"
	}
	if cmd, ok := c.shellCommand(); ok {
		if c.isUserShell() {
			// codex renders a user passthrough as `• You ran ls`: the command is
			// the subject, with no tool name in between.
			if verb == "Ran" {
				verb = "You ran"
			}
			return c.renderUserShellHeadline(theme, width, verb, cmd)
		}
		return c.renderShellHeadline(theme, width, verb, cmd)
	}
	// The verb is prepended at render time (not part of the wrapped text), so
	// reserve its columns in the wrap budget: bullet(2) + verb + space.
	avail := max(1, width-len(verb)-4)
	lines := strings.Split(WrapToWidth(c.headline(), avail), "\n")
	first := lines[0]
	head := theme.ToolVerb.Render(verb) + " "
	if rest, ok := strings.CutPrefix(first, c.name); ok {
		head += theme.ToolName.Render(c.name) + theme.ToolCmd.Render(rest)
	} else {
		// The name itself was wrapped (a very long tool name); style the whole
		// segment as the name so nothing is dropped.
		head += theme.ToolName.Render(first)
	}
	var b strings.Builder
	b.WriteString(c.statusBullet(theme) + " " + head)
	for _, ln := range lines[1:] {
		b.WriteString("\n" + theme.ToolCmd.Render("  │ "+ln))
	}
	return b.String()
}

// renderSearchHeadline renders "Searching the web for <query>" style search
// sentences: the leading phrase takes the verb style and the detail the
// command style, with long text wrapping onto the same `  │ ` gutter the
// generic headline uses.
func (c toolCard) renderSearchHeadline(theme Theme, width int, prefix, detail string) string {
	avail := max(1, width-4)
	lines := strings.Split(WrapToWidth(prefix+" "+detail, avail), "\n")
	var b strings.Builder
	b.WriteString(c.statusBullet(theme) + " ")
	first := lines[0]
	if rest, ok := strings.CutPrefix(first, prefix); ok {
		b.WriteString(theme.ToolVerb.Render(prefix))
		if rest != "" {
			b.WriteString(theme.ToolCmd.Render(rest))
		}
	} else {
		b.WriteString(theme.ToolVerb.Render(first))
	}
	for _, ln := range lines[1:] {
		b.WriteString("\n" + theme.ToolCmd.Render("  │ "+ln))
	}
	return b.String()
}

// isUserShell reports whether the card renders a user `!` passthrough command.
// Such a card is created by the TUI itself (never announced by a tool), named
// "shell", and rendered codex-style as `You ran <command>` — no tool name.
func (c toolCard) isUserShell() bool {
	return strings.EqualFold(c.name, "shell")
}

// shellCommand returns the command line of a shell-family card (the agent's
// bash tool, or a user `!` passthrough) when there is one to syntax-highlight,
// collapsing embedded newlines so the headline stays one logical line
// (wrapHLSpans still honors hard breaks if one survives).
func (c toolCard) shellCommand() (string, bool) {
	if !strings.EqualFold(c.name, "bash") && !c.isUserShell() {
		return "", false
	}
	raw, _ := c.input["command"].(string)
	if cmd := oneLine(raw); cmd != "" {
		return cmd, true
	}
	return "", false
}

// userShellCommand returns the command of a `!` passthrough card, or ok=false
// for every other card.
func (c toolCard) userShellCommand() (string, bool) {
	if !c.isUserShell() {
		return "", false
	}
	return c.shellCommand()
}

// renderShellHeadline renders a bash headline with the command syntax-
// highlighted (chroma + Catppuccin, matching codex): the tool name is cyan,
// every token carries its theme color, and continuation lines hang under the
// same `  │ ` gutter — with the gutter itself dim so only the command text
// carries color. The command is never truncated: it wraps to as many `  │ `
// lines as it needs (codex caps this at two and adds an ellipsis; a command is
// the one thing the user must always be able to read in full).
func (c toolCard) renderShellHeadline(theme Theme, width int, verb, cmd string) string {
	spans := []hlSpan{{text: c.name + " ", color: colorToolName, bold: true}}
	spans = append(spans, highlightShellCommand(cmd, syntaxDark())...)
	firstLimit := max(1, width-2-len(verb)-1)
	lines := wrapHLSpans(spans, firstLimit, max(1, width-4))
	var b strings.Builder
	b.WriteString(c.statusBullet(theme) + " " + theme.ToolVerb.Render(verb) + " ")
	b.WriteString(renderHLSpans(lines[0], theme.ToolCmd))
	for _, ln := range lines[1:] {
		b.WriteString("\n" + theme.ToolBody.Render("  │ ") + renderHLSpans(ln, theme.ToolCmd))
	}
	return b.String()
}

// renderUserShellHeadline renders a `!` passthrough headline: the same
// syntax-highlighted command and `  │ ` continuation gutter as a bash card,
// but without the tool-name token — codex's `• You ran ls` shape.
func (c toolCard) renderUserShellHeadline(theme Theme, width int, verb, cmd string) string {
	spans := highlightShellCommand(cmd, syntaxDark())
	firstLimit := max(1, width-2-len(verb)-1)
	lines := wrapHLSpans(spans, firstLimit, max(1, width-4))
	var b strings.Builder
	b.WriteString(c.statusBullet(theme) + " " + theme.ToolVerb.Render(verb) + " ")
	b.WriteString(renderHLSpans(lines[0], theme.ToolCmd))
	for _, ln := range lines[1:] {
		b.WriteString("\n" + theme.ToolBody.Render("  │ ") + renderHLSpans(ln, theme.ToolCmd))
	}
	return b.String()
}

// preview renders the first n response lines of a collapsed card, the first
// carrying the `└ ` output gutter, and reports how many body lines remain
// hidden (further response lines plus any diff the card carries).
func (c toolCard) preview(theme Theme, width, n int) (string, int) {
	if c.diff != "" {
		// A diff card's body is the diff: collapsed, it previews nothing and
		// reports the whole thing as hidden, so the hint's count matches what
		// expanding reveals. (Patch cards open expanded by default.)
		return "", c.totalBodyLines()
	}
	inner := max(1, width-2)
	var b strings.Builder
	shown := 0
	for i, node := range c.response {
		if shown >= n {
			break
		}
		indent := strings.Repeat("  ", node.depth)
		text := indent + node.text
		if i == 0 {
			b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth("  └ "+text, inner)))
		} else {
			b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth("    "+text, inner)))
		}
		shown++
	}
	return b.String(), c.totalBodyLines() - shown
}

// headline is the tool name plus its most salient argument. A card carrying a
// diff appends the change counts, codex-style (`apply_patch src/x.go (+37 -0)`),
// so the headline summarises the edit the way codex's "Edited path" line does
// and the diff body needs no separate header for the single-file case.
func (c toolCard) headline() string {
	header := c.name
	if arg := c.primaryArg(); arg != "" {
		header = c.name + " " + oneLine(arg)
	}
	if c.diff != "" {
		added, removed := diffTotals(c.diff)
		header += fmt.Sprintf(" (+%d -%d)", added, removed)
	}
	return header
}

// totalBodyLines counts every body line a card can show: its response lines
// plus the diff's rendered lines (the edit-family tools move the diff out of
// the response into its own section). Per file the renderer folds the raw
// ---/+++ header pair into one counts line, so count that instead of two.
// A card with a diff suppresses its response: the tool's text summary said the
// same thing the headline and the diff headers say.
func (c toolCard) totalBodyLines() int {
	if c.diff == "" {
		return len(c.response)
	}
	sections := parseUnifiedDiff(c.diff)
	n := 0
	for i, sec := range sections {
		n += len(sec.lines)
		if len(sections) > 1 {
			n++
			if i > 0 {
				n++ // the blank line between file chunks
			}
		}
	}
	return n
}

// complete attaches a finished tool call's outcome to its card: the terminal
// state, the response tree, and — when the tool reported one — the diff. It is
// the single definition of what a finished card looks like, shared by the live
// tool-end path and session replay so the two cannot drift.
func (c *toolCard) complete(ok bool, result string, details any) {
	if ok {
		c.state = cardSuccess
	} else {
		c.state = cardWarn
	}
	// A tool that reported a diff gets the dedicated colored Diff section; the
	// diff is also embedded in the result text, so strip it there to keep the
	// card from showing the change twice (#560).
	if diff, ok := ui.DiffFromDetails(details); ok {
		c.diff = diff
		result = stripDiffTail(result)
	}
	c.response = parseToolResult(result)
}

// defaultCardExpanded reports whether a tool's cards start expanded. Patch and
// edit cards do: the diff is the whole point of the card, and folding it hides
// the change behind a keypress — reading tools stay folded so a long read
// cannot bury the transcript.
func defaultCardExpanded(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "apply_patch", "edit", "write":
		return true
	default:
		return false
	}
}

// renderDetail renders the expanded sections under the header line with
// codex gutters: input args as `  │ k: v` continuations, the first output
// line as `  └ ...` and the rest as `    ...`, diff lines with the same
// output gutter plus per-line diff colors. An empty result renders
// `  └ (no output)` so a silent tool does not look truncated.
func (c toolCard) renderDetail(theme Theme, width int) string {
	if width < 4 {
		width = 4
	}
	inner := width - 2
	var b strings.Builder
	if len(c.input) > 0 {
		rendered := false
		if items := c.todoItems(); len(items) > 0 {
			// The todo call submits the whole checklist; render it as the
			// same checkbox rows the tool's result uses instead of dumping
			// the decoded array's Go map syntax (`map[content:... status:...]`).
			rendered = true
			b.WriteString(c.renderChecklist(theme, width, items))
		}
		if !rendered {
			for _, k := range sortedKeys(c.input) {
				if k == "command" && (strings.EqualFold(c.name, "bash") || c.isUserShell()) {
					// The full command already wraps across the headline;
					// repeating it here would be pure noise.
					continue
				}
				if k == "patch" && strings.EqualFold(c.name, "apply_patch") {
					// The patch body is noise in a transcript: the response
					// carries the per-file summary and the colored diff, and
					// the headline names the targets.
					continue
				}
				kv := "  │ " + k + ": " + fmt.Sprintf("%v", c.input[k])
				b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth(kv, inner)))
			}
		}
	}
	wroteOutput := false
	if c.diff != "" {
		// The diff replaces the response body: the tool's own text was the
		// per-file summary, which the headline and the diff headers already
		// carry (codex does the same — its header line IS the summary).
		b.WriteString(renderDiff(theme, c.diff, inner))
		wroteOutput = true
	} else {
		for i, n := range c.response {
			indent := strings.Repeat("  ", n.depth)
			text := indent + n.text
			if i == 0 {
				b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth("  └ "+text, inner)))
			} else {
				b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth("    "+text, inner)))
			}
			wroteOutput = true
		}
	}
	if !wroteOutput {
		b.WriteString("\n" + theme.System.Render(WrapToWidth("  └ (no output)", inner)))
	}
	return b.String()
}

// oneLine collapses s to a single line for headlines.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// argString renders a decoded argument value as trimmed text ("" for anything
// that is not a non-empty string), so callers can test an argument without a
// type assertion dance.
func argString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// isHostedSearch reports whether the card is a provider-executed web search
// (name web_search; the local tool is websearch). Its recorded response is a
// synthetic "hosted: …" placeholder, so the card renders as the headline
// alone.
func (c toolCard) isHostedSearch() bool {
	return strings.EqualFold(strings.TrimSpace(c.name), "web_search")
}

// webSearchLead builds the lead phrase and detail for the search-family cards —
// the local `websearch` tool and the provider-executed `web_search` call — so
// they render as a codex-style verb sentence ("Searched the web for <query>")
// instead of "Ran <name> <arg>". The generic form glued the raw query onto the
// tool name, and the local tool's argument fallback could even surface its
// count field instead of the query.
//
// ok=false lets the generic headline take over when the call carries no
// renderable detail (an action shape without a query/url/pattern).
func (c toolCard) webSearchLead() (prefix, detail string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(c.name)) {
	case "websearch", "web_search":
	default:
		return "", "", false
	}
	running := c.state == cardRunning
	// open_page is an extraction, not a search (codex reads the same).
	if strings.EqualFold(argString(c.input["action"]), "open_page") {
		if url := oneLine(argString(c.input["url"])); url != "" {
			if running {
				return "Extracting", url, true
			}
			return "Extracted", url, true
		}
	}
	detail = oneLine(argString(c.input["query"]))
	if detail == "" {
		detail = oneLine(argString(c.input["pattern"]))
	}
	if detail == "" {
		return "", "", false
	}
	if running {
		return "Searching the web for", detail, true
	}
	return "Searched the web for", detail, true
}

// primaryArg returns the most salient call argument to inline in the card header
// so the user can see what the tool is operating on at a glance (FR-6), e.g.
// Bash(cd /x && git add -A). It picks the command for bash and the file path for
// the file tools, otherwise the first argument in sorted-key order. Returns ""
// when the call carried no arguments.
func (c toolCard) primaryArg() string {
	if len(c.input) == 0 {
		return ""
	}
	var keyPrefs []string
	switch strings.ToLower(c.name) {
	case "bash", "shell":
		// "shell" is the user `!` passthrough card; like bash, its command is
		// the salient argument (and already rendered in the headline).
		keyPrefs = []string{"command"}
	case "websearch", "web_search":
		// Search args carry query (search) or url (open_page): show what was
		// searched, never the bare action tag or the count field.
		keyPrefs = []string{"query", "url"}
	case "read", "write", "edit", "multiedit":
		// The file tools emit "path"; accept "file_path" as a fallback for
		// callers that use the Claude-style key.
		keyPrefs = []string{"path", "file_path"}
	case "apply_patch":
		// The patch text itself is too bulky for a headline; summarize it as
		// the touched paths (one file named, several counted).
		if text, ok := c.input["patch"].(string); ok {
			switch paths := patch.Paths(text); len(paths) {
			case 0:
				return ""
			case 1:
				return paths[0]
			default:
				return fmt.Sprintf("%d files", len(paths))
			}
		}
		return ""
	case "todo":
		// The checklist has no single salient argument; a count keeps the
		// header one line (the expanded body renders the checkbox rows).
		if items := todoItemsFromInput(c.input["todos"]); len(items) > 0 {
			return fmt.Sprintf("%d tasks", len(items))
		}
		return ""
	}
	for _, key := range keyPrefs {
		if v, ok := c.input[key]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	keys := sortedKeys(c.input)
	return fmt.Sprintf("%v", c.input[keys[0]])
}

// sortedKeys returns the map keys in a stable (sorted) order so the input
// section renders deterministically instead of in Go's random map order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stripDiffTail removes the trailing unified diff from a tool result text so
// the card's Response section keeps only the summary line; the diff itself
// renders in the colored Diff section. It cuts at the diff's "--- a/" header
// rather than matching the exact suffix, so a result clipped mid-diff still
// splits cleanly. The cut is only applied when the tool reported a diff
// (card.diff != ""), so e.g. bash output of git diff is never mangled.
func stripDiffTail(text string) string {
	if i := strings.Index(text, "\n--- a/"); i >= 0 {
		return text[:i+1]
	}
	return text
}

// parseToolResult splits a tool's textual result into response tree nodes,
// inferring depth from leading whitespace (every two leading spaces is one
// level). Trailing empty lines are trimmed so the card does not render blank
// tail rows.
//
// Carriage returns are normalized away before splitting. A PTY-backed command
// (bash with tty=true) yields the raw terminal byte stream, whose lines end in
// CRLF, and this transcript is rendered inside a fixed-width viewport: a bare
// CR reaching the terminal is executed as a carriage return, rewinding the
// cursor to column 0 so the next write overwrites the row's start — the text
// visibly shifts and loses its prefix (issue seen on `python3 -i` output).
// A CR not paired with LF is a progress-bar rewrite, where a terminal leaves
// only the segment after the last CR visible; a trailing CR alone leaves the
// line before it intact.
func parseToolResult(result string) []respNode {
	result = strings.ReplaceAll(result, "\r\n", "\n")
	lines := strings.Split(result, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	nodes := make([]respNode, 0, len(lines))
	for _, ln := range lines {
		ln = normalizeCR(ln)
		leading := len(ln) - len(strings.TrimLeft(ln, " "))
		nodes = append(nodes, respNode{text: ln[leading:], depth: leading / 2})
	}
	return nodes
}

// normalizeCR resolves the carriage returns within one line the way a terminal
// would: the segment after the last CR is what remains visible, except that a
// CR only at the end of the line (nothing written after it) leaves the text
// before it intact.
func normalizeCR(line string) string {
	if !strings.ContainsRune(line, '\r') {
		return line
	}
	segs := strings.Split(line, "\r")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			return segs[i]
		}
	}
	return ""
}

// todoInputItem is one checklist entry decoded from a todo tool call's
// untyped arguments (the shape argsToMap produces for a JSON array:
// []any of map[string]any).
type todoInputItem struct {
	content string
	status  string
}

// todoItems returns the checklist this card's call submitted, or nil when the
// card is not a todo call or its arguments did not decode to items.
func (c toolCard) todoItems() []todoInputItem {
	if !strings.EqualFold(c.name, "todo") {
		return nil
	}
	return todoItemsFromInput(c.input["todos"])
}

// todoResponseIsEcho reports whether the card's response carries no
// information beyond the checklist it already renders: true while the call is
// still running (no response yet) and when the response is the todo tool's own
// render of the same list, which always opens with the "Todos:" header. A
// validation error or any other text is not an echo and still renders.
func (c toolCard) todoResponseIsEcho() bool {
	return len(c.response) == 0 || c.response[0].text == "Todos:"
}

// renderChecklist renders the checklist as checkbox rows with the `  │ `
// gutter, using the same marks as the tool's own RenderTodoList ([ ] pending,
// [~] in progress, [x] completed) so the call and its result read alike.
// Rows are styled by status: the in-progress step takes the card's tool-name
// cyan, done rows recede to dim gray, and pending rows stay plain muted gray.
// The `  │ ` gutter stays out of the row style — only the checkbox text is
// colored, so the gutter reads as card chrome like every other continuation
// line.
func (c toolCard) renderChecklist(theme Theme, width int, items []todoInputItem) string {
	inner := max(1, width-2)
	textWidth := max(1, inner-4)
	gutter := theme.ToolBody.Render("  │ ")
	var b strings.Builder
	for _, it := range items {
		row := fmt.Sprintf("[%s] %s", todoMark(it.status), oneLine(it.content))
		b.WriteString("\n" + gutter + todoStatusStyle(theme, it.status).Render(WrapToWidth(row, textWidth)))
	}
	return b.String()
}

// todoStatusStyle picks the row style for a todo status.
func todoStatusStyle(theme Theme, status string) lipgloss.Style {
	switch status {
	case "completed":
		return theme.TodoDone
	case "in_progress":
		return theme.TodoActive
	default:
		return theme.TodoPending
	}
}

// todoItemsFromInput extracts the checklist from a todo call's "todos"
// argument. It returns nil when the value is not a JSON array of
// {content,status} objects, so callers fall back to the generic renderer
// rather than losing the argument.
func todoItemsFromInput(v any) []todoInputItem {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	items := make([]todoInputItem, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return nil
		}
		content, _ := m["content"].(string)
		status, _ := m["status"].(string)
		items = append(items, todoInputItem{content: content, status: status})
	}
	return items
}

// todoMark mirrors agenttool.RenderTodoList's checkbox marks so the call's
// arguments read exactly like the tool's result block: [ ] pending,
// [~] in progress, [x] completed.
func todoMark(status string) string {
	switch status {
	case "completed":
		return "x"
	case "in_progress":
		return "~"
	default:
		return " "
	}
}
