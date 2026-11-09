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

	"github.com/getan/golder/internal/explore"
)

// isExploreTool reports whether a tool call is an exploration call.
func isExploreTool(name string) bool {
	return explore.IsTool(name)
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
	rows := g.summaryRows()
	groupMarker := g.collapseMarker()
	if len(rows) == 0 {
		return g.header(theme) + groupMarker + g.notes(theme, width)
	}
	var b strings.Builder
	b.WriteString(g.header(theme) + groupMarker)
	indent := "    "
	for i, row := range rows {
		line := row.Text
		if groupMarker == "" {
			// A mixed group says nothing on the header, so each row carries its
			// own answer rather than the group's: one marker that covered both
			// a sandboxed read and an unconfined one would be a lie about one of
			// them.
			if m := envMarkerFor(row.Envelope); m != "" {
				line += " " + m
			}
		}
		gutter := indent
		if i == 0 {
			gutter = "  └ "
		}
		wrapped := strings.Split(WrapToWidth(line, max(1, width-len([]rune(gutter)))), "\n")
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

// collapseMarker is the one word the collapsed header carries when every member
// call ran under the same envelope — the answer to "was this exploration
// isolated". It includes the leading space, and it is empty for a mixed group
// (the rows say it instead) or for a group whose calls have not reported yet.
func (g exploreGroup) collapseMarker() string {
	env := ""
	for _, c := range g.cards {
		if c.envelope == "" {
			continue
		}
		if env == "" {
			env = c.envelope
			continue
		}
		if env != c.envelope {
			return ""
		}
	}
	if m := envMarkerFor(env); m != "" {
		return " " + m
	}
	return ""
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
	rows := g.summaryRows()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Text)
	}
	return out
}

// summaryRows is summaryLines with each row's envelope attached. A run of reads
// merges only while the reads share an envelope: one marker cannot describe two
// different answers.
func (g exploreGroup) summaryRows() []explore.Row {
	var summary explore.Builder
	for _, c := range g.cards {
		summary.Add(c.name, c.input, c.envelope)
	}
	return summary.Finish()
}
