package mygoui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

// approvalCard renders pending consent as a transcript card, using the same
// folded detail pattern as tool calls rather than a separate fixed panel.
func (a *App) approvalCard(c *ui.Context, p appsession.ApprovalView) {
	t := c.Theme()
	open, ok := a.approvalOpen[p.ID]
	if !ok {
		open = true
	}
	head := "!  Approval required"
	if p.Tool != "" {
		head += " · " + p.Tool
	}
	el := ui.Collapsible(c.Key(p.ID), head, &open, func() {
		if p.Origin != "" {
			ui.Text(c, "from "+p.Origin).FontSize(11).TextColor(t.TextMuted)
		}
		if p.Source != "" || p.Level != "" {
			ui.Text(c, p.Source+"/"+p.Level).FontSize(11).TextColor(t.TextMuted)
		}
		if p.TrustDir != "" {
			ui.Text(c, "Trust "+p.TrustDir+"?").FontSize(12)
		}
		if p.Summary != "" {
			ui.Text(c, p.Summary).FontSize(12).Selectable().Wrap()
		}
		if len(p.Needs) > 0 {
			ui.Text(c, "Needs: "+strings.Join(p.Needs, ", ")).FontSize(11).TextColor(t.TextMuted)
		}
		if p.Rationale != "" {
			ui.Text(c, p.Rationale).FontSize(11.5).TextColor(t.TextMuted).Selectable()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Deny").Clicked() {
				a.respond(p.ID, appsession.AnswerDeny)
			}
			if ui.Button(c, "Allow once").Clicked() {
				a.respond(p.ID, appsession.AnswerOnce)
			}
			if p.Tool != "terminal" {
				if ui.PrimaryButton(c, "Always this session").Clicked() {
					a.respond(p.ID, appsession.AnswerSession)
				}
			}
		})
	})
	el.TextColor(t.Danger)
	a.approvalOpen[p.ID] = open
}

// pendingCount is used by the title bar when a decision is waiting.
func (a *App) pendingLabel() string {
	if len(a.pending) == 0 {
		return ""
	}
	return fmt.Sprintf("  ·  %d waiting", len(a.pending))
}
