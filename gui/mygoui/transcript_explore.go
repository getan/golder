package mygoui

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/explore"
)

type explorePresentation struct {
	running, failed bool
	pending         int
	envelope        string
	mixed           bool
	summary         []explore.Row
	moreCalls       int
}

func exploreEnvelope(st *appsession.ToolState) string {
	if st == nil {
		return ""
	}
	env, _ := st.Details["envelope"].(string)
	if env == "sandbox" || env == "bare" {
		return env
	}
	return ""
}

func exploreEnvelopeMarker(env string) string {
	switch env {
	case "sandbox":
		return "·sandbox"
	case "bare":
		return "·unconfined"
	default:
		return ""
	}
}

func (a *App) explorePresentation(row transcriptRow) explorePresentation {
	var result explorePresentation
	var summary explore.Builder
	shown := 0
	for _, member := range row.members {
		item, ok := a.transcriptRowItem(member)
		if !ok || item.Tool == nil {
			continue
		}
		st := item.Tool
		result.running = result.running || st.Running
		result.failed = result.failed || st.Error || item.Failed
		result.pending += len(member.approvals)
		env := exploreEnvelope(st)
		if env != "" {
			if result.envelope == "" {
				result.envelope = env
			} else if result.envelope != env {
				result.mixed = true
			}
		}
		// A long run is one list row. Bound the text assembled for its preview
		// while still scanning every call for status, approvals and envelopes.
		if shown < 128 {
			summary.Add(st.Name, st.Input, env)
			shown++
		} else {
			result.moreCalls++
		}
	}
	result.summary = summary.Finish()
	return result
}

// exploreGroupView folds a run into one list row. A pending decision and every
// non-routine verdict stay visible even while the individual calls are folded.
func (a *App) exploreGroupView(c *ui.Context, row transcriptRow) {
	t := c.Theme()
	group := a.explorePresentation(row)
	head := "Explored"
	if group.running {
		head = "Exploring"
	}
	if len(row.members) > 1 {
		head += fmt.Sprintf(" · %d calls", len(row.members))
	}
	if group.failed {
		head += " · failed"
	}
	if group.pending > 0 {
		head += " · approval required"
	}
	if group.mixed {
		head += " · mixed sandbox/unconfined"
	} else {
		if marker := exploreEnvelopeMarker(group.envelope); marker != "" {
			head += " " + marker
		}
	}
	key := "explore:" + row.key
	open := a.toolOpen[key]
	ui.Column(c.Key(key)).Gap(4).Children(func() {
		disclosure := ui.Collapsible(c.Key("header"), head, &open, func() {
			if !open {
				return
			}
			for _, member := range row.members {
				item, ok := a.transcriptRowItem(member)
				if ok {
					a.toolCardDetailsWithDefault(c, item, member.notes, member.approvals, true)
				}
			}
		})
		if group.failed {
			disclosure.TextColor(t.Danger)
		}
		a.toolOpen[key] = open
		if open {
			return
		}
		// Bound the collapsed preview: the full, selectable calls and outputs
		// are available by expanding, without making a long search burst tall.
		ui.Column(c.Key("summary")).MarginStart(20).Gap(3).Children(func() {
			const maxRows = 4
			for _, line := range group.summary[:min(len(group.summary), maxRows)] {
				ui.Row(c).Gap(5).MinWidth(0).Children(func() {
					ui.Text(c, oneLine(line.Text, 320)).Grow(1).MinWidth(0).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					if group.mixed {
						if marker := exploreEnvelopeMarker(line.Envelope); marker != "" {
							ui.Text(c, marker).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
						}
					}
				})
			}
			if more := len(group.summary) - maxRows; more > 0 {
				ui.Text(c, fmt.Sprintf("… %d more steps", more)).FontSize(11).TextColor(t.TextMuted)
			}
			if group.moreCalls > 0 {
				ui.Text(c, fmt.Sprintf("… %d more calls", group.moreCalls)).FontSize(11).TextColor(t.TextMuted)
			}
			for _, member := range row.members {
				for _, index := range member.notes {
					if index >= 0 && index < len(a.items) {
						a.noteLine(c, a.items[index])
					}
				}
				for _, index := range member.approvals {
					if index >= 0 && index < len(a.pending) {
						a.approvalCard(c, a.pending[index])
					}
				}
			}
		})
	})
}
