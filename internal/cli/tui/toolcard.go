package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
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
func (c toolCard) title() string {
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
	if items := c.todoItems(); len(items) > 0 && c.todoResponseIsEcho() {
		return out + c.renderChecklist(theme, width, items)
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

// renderHeadline renders the bullet plus `Running/Ran name arg`, wrapping the
// text onto `  │ ` continuation lines so a long command is fully readable
// instead of being cut with an ellipsis. It wraps at width-4 (the widest
// gutter) so both the bullet line and the continuations fit; the two columns
// the bullet leaves unused on the first line are harmless slack.
func (c toolCard) renderHeadline(theme Theme, width int) string {
	lines := strings.Split(WrapToWidth(c.title(), max(1, width-4)), "\n")
	var b strings.Builder
	b.WriteString(c.statusBullet(theme) + " " + theme.ToolHeader.Render(lines[0]))
	for _, ln := range lines[1:] {
		b.WriteString("\n" + theme.ToolHeader.Render("  │ "+ln))
	}
	return b.String()
}

// preview renders the first n response lines of a collapsed card, the first
// carrying the `└ ` output gutter, and reports how many body lines remain
// hidden (further response lines plus any diff the card carries).
func (c toolCard) preview(theme Theme, width, n int) (string, int) {
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

// headline is the tool name plus its most salient argument.
func (c toolCard) headline() string {
	header := c.name
	if arg := c.primaryArg(); arg != "" {
		header = c.name + " " + oneLine(arg)
	}
	return header
}

// totalBodyLines counts every body line a card can show: its response lines
// plus the diff's lines (the edit-family tools move the diff out of the
// response into its own section).
func (c toolCard) totalBodyLines() int {
	n := len(c.response)
	if c.diff != "" {
		n += len(strings.Split(strings.TrimRight(c.diff, "\n"), "\n"))
	}
	return n
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
				if k == "command" && strings.EqualFold(c.name, "bash") {
					// The full command already wraps across the headline;
					// repeating it here would be pure noise.
					continue
				}
				kv := "  │ " + k + ": " + fmt.Sprintf("%v", c.input[k])
				b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth(kv, inner)))
			}
		}
	}
	wroteOutput := false
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
	if c.diff != "" {
		for i, ln := range strings.Split(strings.TrimRight(c.diff, "\n"), "\n") {
			prefix := "    "
			if !wroteOutput && i == 0 {
				prefix = "  └ "
			}
			b.WriteString("\n" + diffLineStyle(theme, ln).Render(WrapToWidth(prefix+ln, inner)))
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
	case "bash":
		keyPrefs = []string{"command"}
	case "web_search":
		// Hosted search args carry query (search) or url (open_page): show
		// what was searched, never the bare action tag.
		keyPrefs = []string{"query", "url"}
	case "read", "write", "edit", "multiedit":
		// The file tools emit "path"; accept "file_path" as a fallback for
		// callers that use the Claude-style key.
		keyPrefs = []string{"path", "file_path"}
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

// diffLineStyle picks the theme style for one unified-diff line, mirroring
// ui.RenderDiffLine's compact-REPL coloring: dim file headers and context,
// cyan @@ hunk markers, red removals, green additions.
func diffLineStyle(theme Theme, line string) lipgloss.Style {
	switch {
	case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
		return theme.DiffCtx
	case strings.HasPrefix(line, "@@"):
		return theme.DiffHunk
	case strings.HasPrefix(line, "-"):
		return theme.DiffDel
	case strings.HasPrefix(line, "+"):
		return theme.DiffAdd
	default:
		return theme.DiffCtx
	}
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
func parseToolResult(result string) []respNode {
	lines := strings.Split(result, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	nodes := make([]respNode, 0, len(lines))
	for _, ln := range lines {
		leading := len(ln) - len(strings.TrimLeft(ln, " "))
		nodes = append(nodes, respNode{text: ln[leading:], depth: leading / 2})
	}
	return nodes
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
func (c toolCard) renderChecklist(theme Theme, width int, items []todoInputItem) string {
	inner := max(1, width-2)
	var b strings.Builder
	for _, it := range items {
		line := fmt.Sprintf("  │ [%s] %s", todoMark(it.status), oneLine(it.content))
		b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth(line, inner)))
	}
	return b.String()
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
