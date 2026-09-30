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
// completed on toolEndMsg, and toggled between a capped and a full response view
// with Ctrl+T (see model.go). All width math goes through ui.Width /
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

// expandHint names the key that toggles a card between its one-line summary
// and the full detail (codex parity: ctrl+t).
const expandHint = "ctrl+t"


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

// render draws the card codex-style: collapsed is a single bullet line
// (`• Running/Ran name(args)`), optionally followed by a middle-truncation
// hint (`… +N lines`) when output is hidden; expanded shows the input,
// response, and diff sections as gutter-prefixed lines (` │ ` command
// continuations, ` └ ` output start) with no border anywhere.
func (c toolCard) render(theme Theme, width int) string {
	title := c.title()
	line := c.statusBullet(theme) + " " + theme.ToolHeader.Render(TruncateToWidth(title, max(1, width-4)))
	if c.expanded {
		return line + c.renderDetail(theme, width)
	}
	if hidden := c.hiddenLines(); hidden > 0 {
		line += "\n" + theme.System.Render(
			fmt.Sprintf("… +%d lines (%s to view transcript)", hidden, expandHint))
	}
	return line
}

// headline is the tool name plus its most salient argument.
func (c toolCard) headline() string {
	header := c.name
	if arg := c.primaryArg(); arg != "" {
		header = c.name + " " + oneLine(arg)
	}
	return header
}

// hiddenLines counts response + diff lines hidden while collapsed.
func (c toolCard) hiddenLines() int {
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
		for _, k := range sortedKeys(c.input) {
			kv := "  │ " + k + ": " + fmt.Sprintf("%v", c.input[k])
			b.WriteString("\n" + theme.ToolBody.Render(WrapToWidth(kv, inner)))
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
	case "read", "write", "edit", "multiedit":
		// The file tools emit "path"; accept "file_path" as a fallback for
		// callers that use the Claude-style key.
		keyPrefs = []string{"path", "file_path"}
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
