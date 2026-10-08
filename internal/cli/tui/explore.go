package tui

// The exploring cell: a run of consecutive read-only tool calls (file reads,
// directory listings, content/name searches) rendered as ONE transcript block —
// a `• Explored` header with an indented summary of what was looked at —
// mirroring codex's exec cell grouping.
//
// Without it, "familiarize yourself with this repo" prints a dozen near-empty
// cards and buries the answer. Grouping is display-only: every card keeps its
// own state, output and notes, and expanding the group (click it, or Ctrl+T
// while its newest call is the last card) reveals the full per-call detail.
// Consecutive reads additionally merge onto one line (`Read a.go, b.go`), the
// codex shorthand for a burst of file reads.

import (
	"strings"
)

// exploreTools names the read-only exploration tools whose cards coalesce.
// Deliberately a small, closed set: bash/apply_patch/task calls — anything with
// side effects or its own rich rendering — always keep their own card, so the
// group never hides a call the user must see.
var exploreTools = map[string]bool{
	"read": true,
	"ls":   true,
	"grep": true,
	"find": true,
}

// isExploreTool reports whether a tool call is an exploration call.
func isExploreTool(name string) bool {
	return exploreTools[strings.ToLower(strings.TrimSpace(name))]
}

// exploreGroup is the state of one coalesced run: the cards in call order and
// the group's own expand flag.
type exploreGroup struct {
	cards    []*toolCard
	expanded bool
}

// running reports whether any member call is still executing (the header then
// reads "Exploring" and takes the running bullet).
func (g exploreGroup) running() bool {
	for _, c := range g.cards {
		if c.state == cardRunning {
			return true
		}
	}
	return false
}

// failed reports whether any member call finished in the warn state, so the
// collapsed header can carry the failure color instead of hiding it.
func (g exploreGroup) failed() bool {
	for _, c := range g.cards {
		if c.state == cardWarn {
			return true
		}
	}
	return false
}

// stable reports whether the group's collapsed render is final: every member
// call finished. It is the transcript's commit signal (stableBlock).
func (g exploreGroup) stable() bool {
	for _, c := range g.cards {
		if c.state == cardRunning {
			return false
		}
	}
	return true
}

// toggleExpanded flips the group between its summary and the per-call detail.
func (g *exploreGroup) toggleExpanded() { g.expanded = !g.expanded }

// render draws the group: a status bullet with `Exploring`/`Explored`, then the
// indented summary lines (`  └ Read a.go, b.go` / `    Search ROUTER in cli`).
// Expanded, each member card renders in full — the group is a lens, not a
// replacement, and no output is ever unreachable.
func (g exploreGroup) render(theme Theme, width int) string {
	if g.expanded {
		parts := make([]string, 0, len(g.cards))
		for _, c := range g.cards {
			parts = append(parts, c.renderForced(theme, width, true))
		}
		return g.header(theme) + "\n" + strings.Join(parts, "\n")
	}
	lines := g.summaryLines()
	if len(lines) == 0 {
		return g.header(theme)
	}
	var b strings.Builder
	b.WriteString(g.header(theme))
	indent := "    "
	for i, ln := range lines {
		gutter := indent
		if i == 0 {
			gutter = "  └ "
		}
		wrapped := strings.Split(WrapToWidth(ln, max(1, width-len([]rune(gutter)))), "\n")
		for j, w := range wrapped {
			if j == 0 {
				b.WriteString("\n" + theme.System.Render(gutter) + theme.ToolCmd.Render(w))
				continue
			}
			b.WriteString("\n" + theme.ToolCmd.Render(indent+w))
		}
	}
	return b.String()
}

// header renders the `• Exploring` / `• Explored` line. The bullet follows the
// group's aggregate state: the running color while any call executes, the
// failure color when one failed, success otherwise.
func (g exploreGroup) header(theme Theme) string {
	bullet := theme.Success.Render("•")
	verb := "Explored"
	switch {
	case g.running():
		bullet = theme.System.Render("•")
		verb = "Exploring"
	case g.failed():
		bullet = theme.Error.Render("•")
	}
	return bullet + " " + theme.ToolVerb.Render(verb)
}

// summaryLines renders one line per distinct step, merging a run of
// consecutive reads into a single `Read a.go, b.go` line (codex's shorthand,
// names deduped). Steps that name nothing (a call whose args never arrived)
// are skipped rather than rendered as an empty line.
func (g exploreGroup) summaryLines() []string {
	var (
		out      []string
		reads    []string
		seenRead = map[string]bool{}
	)
	flushReads := func() {
		if len(reads) > 0 {
			out = append(out, "Read "+strings.Join(reads, ", "))
			reads = nil
			seenRead = map[string]bool{}
		}
	}
	for _, c := range g.cards {
		verb, detail := c.exploreStep()
		if verb == "" {
			continue
		}
		if verb == "Read" {
			if !seenRead[detail] {
				seenRead[detail] = true
				reads = append(reads, detail)
			}
			continue
		}
		flushReads()
		if detail == "" {
			out = append(out, verb)
			continue
		}
		out = append(out, verb+" "+detail)
	}
	flushReads()
	return out
}

// exploreStep names one exploration call for the group summary: the verb to
// lead with and the detail to show. Each tool contributes what the user would
// otherwise scan the card for — the file read, the directory listed, the
// pattern searched (and where). Returns "" when the call has no usable
// argument yet (the summary then simply omits it).
func (c toolCard) exploreStep() (verb, detail string) {
	arg := func(key string) string { return oneLine(argString(c.input[key])) }
	switch strings.ToLower(strings.TrimSpace(c.name)) {
	case "read":
		if p := arg("path"); p != "" {
			return "Read", p
		}
		return "Read", ""
	case "ls":
		if p := arg("path"); p != "" {
			return "List", p
		}
		return "List", "."
	case "grep":
		pattern := arg("pattern")
		if pattern == "" {
			return "Search", ""
		}
		if scope := arg("path"); scope != "" {
			return "Search", pattern + " in " + scope
		}
		if glob := arg("glob"); glob != "" {
			return "Search", pattern + " in " + glob
		}
		return "Search", pattern
	case "find":
		glob := arg("glob")
		if glob == "" {
			return "Find", ""
		}
		if scope := arg("path"); scope != "" {
			return "Find", glob + " in " + scope
		}
		return "Find", glob
	}
	return "", ""
}
