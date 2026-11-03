package mygoui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

// approvalCard shows one pending decision above the composer. It stays in the
// window, not in a modal, so the transcript behind it remains readable while
// the tool call waits.
func (a *App) approvalCard(c *ui.Context, p appsession.ApprovalView) {
	t := c.Theme()
	ui.Column(c.Key(p.ID)).Gap(6).Padding(11, 14).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Approval required").FontSize(13).Bold().TextColor(t.Danger)
			if p.Origin != "" {
				ui.Text(c, "from "+p.Origin).FontSize(11).TextColor(t.TextMuted)
			}
			ui.Spacer(c)
			ui.Text(c, p.Source+"/"+p.Level).FontSize(11).TextColor(t.TextMuted)
		})
		if p.TrustDir != "" {
			ui.Text(c, "Trust "+p.TrustDir+"?").FontSize(12)
		}
		if p.Tool != "" {
			ui.Text(c, p.Tool).FontSize(12).FontWeight(600)
		}
		if p.Summary != "" {
			ui.Text(c, p.Summary).FontSize(12).Selectable()
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
			if ui.PrimaryButton(c, "Always this session").Clicked() {
				a.respond(p.ID, appsession.AnswerSession)
			}
		})
	})
}

// pendingCount is used by the title bar when a decision is waiting.
func (a *App) pendingLabel() string {
	if len(a.pending) == 0 {
		return ""
	}
	return fmt.Sprintf("  ·  %d waiting", len(a.pending))
}
