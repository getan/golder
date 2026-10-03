// This file holds the compact tool-activity renderers shared by the REPL, the
// /goal autonomous loop, and /btw side threads: a tool call is shown as a green
// "→ tool:" line and a tool result as a green "← result:" (or red "← error:")
// line, with multi-line output collapsed to one line. Two results are printed
// in full instead: the todo tool's (so the live checklist stays visible) and
// any result carrying a diff, e.g. edit's, which renders its colored diff
// (diffrender.go, #560).
package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// RenderToolResult prints a tool result to out: the todo tool's result is shown
// in full (indented) so the live checklist stays visible; a result carrying a
// diff (edit today, #560) prints its summary line plus the colored diff;
// every other result is collapsed to a single "← result:"/"← error:" line.
func RenderToolResult(out io.Writer, tr agentcore.ToolResultMessage) {
	text := agentcore.ContentToText(tr.Content)
	color := Enabled()
	if tr.ToolName == "todo" && !tr.IsError {
		fmt.Fprintln(out, "  "+Colorize(color, Green, "← todo:"))
		for _, line := range strings.Split(text, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
		return
	}
	if tr.IsError {
		fmt.Fprintf(out, "  %s %s\n", Colorize(color, Red, "← error:"), OneLine(text))
		return
	}
	if diff, ok := DiffFromDetails(tr.Details); ok {
		fmt.Fprintf(out, "  %s %s\n", Colorize(color, Green, "← result:"), firstLine(text))
		for _, line := range DiffPreviewLines(diff) {
			fmt.Fprintf(out, "    %s\n", RenderDiffLine(line, color))
		}
		return
	}
	fmt.Fprintf(out, "  %s %s\n", Colorize(color, Green, "← result:"), OneLine(text))
}

// firstLine returns s up to (not including) its first newline, so a multi-line
// result such as edit's "Edited f.txt (1 replacement(s))\n<diff>" yields just
// its summary line while the diff below renders in full.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ToolCallLabel renders a tool call as "name args" for the compact "→ tool:"
// status. Empty or "{}" arguments collapse to just the name.
func ToolCallLabel(c agentcore.ToolCallContent) string {
	args := strings.TrimSpace(string(c.Arguments))
	if args == "" || args == "{}" {
		return c.Name
	}
	return c.Name + " " + OneLine(args)
}

// OneLine collapses a possibly multi-line string into a single trimmed line,
// truncating very long values, for the compact tool-activity statuses.
func OneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	const max = 120
	if len(s) > max {
		s = s[:max] + " …"
	}
	return s
}
