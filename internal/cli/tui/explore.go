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
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/judge"
)

// exploreTools names the read-only exploration tools whose cards coalesce.
// Deliberately a small, closed set: bash/apply_patch/task calls — anything with
// side effects or its own rich rendering — always keep their own card, so the
// group never hides a call the user must see.
//
// Parallelism is not the criterion, and neither is the path: view_image reads
// one local file like read does (it folds in), while webfetch, websearch and
// memory_search are parallel reads whose RESULT is the thing the user came to
// see — folding those would replace a page of fetched text with a summary line.
// A read of a file outside the workspace folds in with everything else, since
// grouping describes what the model is doing, not where the bytes came from;
// the summary still prints the absolute path.
var exploreTools = map[string]bool{
	"read":       true,
	"ls":         true,
	"grep":       true,
	"find":       true,
	"view_image": true,
}

// isExploreTool reports whether a tool call is an exploration call.
func isExploreTool(name string) bool {
	return exploreTools[strings.ToLower(strings.TrimSpace(name))]
}

// isExploreCall reports whether one tool call coalesces into an exploration
// cell. A native read tool always does; a bash call does when its command is
// provably read-only, which is the same recognizer the permission gate uses to
// skip the reviewer (judge.ReadOnlyShellSummary, which answers the containment
// question and the summary question from one decision). The two must agree: a
// shell read that the gate let through untested is exactly the call whose output
// is routine enough to summarize, and a call the gate reviewed keeps its own
// card so the verdict note and the command stay visible.
//
// A bash call whose arguments have not arrived yet does not fold — the fold is
// decided when the block is appended, and a card announced before its command
// streamed in has nothing to summarize. It keeps its own card, which is the
// honest rendering: the command is still unknown.
func isExploreCall(c *toolCard) bool {
	if c == nil {
		return false
	}
	if isExploreTool(c.name) {
		return true
	}
	_, _, ok := shellReadStep(c)
	return ok
}

// shellReadStep reports a bash card's read-only summary, if it has one. The
// whole call's arguments go to the recognizer rather than the command text
// alone: the answer belongs to the gate, and a call carrying an escalation
// request is not one the gate let through untested — folding it would hide a
// call the reviewer in fact judged.
func shellReadStep(c *toolCard) (verb, detail string, ok bool) {
	if !strings.EqualFold(strings.TrimSpace(c.name), "bash") {
		return "", "", false
	}
	if oneLine(argString(c.input["command"])) == "" {
		return "", "", false
	}
	raw, err := json.Marshal(c.input)
	if err != nil {
		return "", "", false
	}
	return judge.ReadOnlyShellSummary(raw)
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
		return g.header(theme) + g.notes(theme, width)
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
	return b.String() + g.notes(theme, width)
}

// notes renders the gate verdicts attached to the group's member calls. A
// collapsed cell would otherwise swallow them, and a verdict is the one thing
// about a folded call that must not take an expansion to see: the summary says
// what was read, while the note says the read was denied, contained, or a
// reviewed call in an otherwise routine-looking cell. An allow carries no note
// at all, so an ordinary read contributes nothing here. Expanded, each card
// paints its own notes in place, which is why this is the collapsed form only.
func (g exploreGroup) notes(theme Theme, width int) string {
	var out []string
	for _, c := range g.cards {
		for _, n := range c.notes {
			out = append(out, renderNoteLine(theme, width, n))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "\n" + strings.Join(out, "\n")
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
	case "view_image":
		if p := arg("path"); p != "" {
			return "View", p
		}
		return "View", ""
	case "bash":
		// A shell call folds only when its command is provably read-only (the
		// same recognizer the gate uses); the summary then names the read, and a
		// command it declines falls through to "" so the card keeps its own line.
		if verb, detail, ok := shellReadStep(&c); ok {
			return verb, detail
		}
	}
	return "", ""
}
