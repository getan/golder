package mygoui

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/browsercontinuation"
)

// This method is invoked only by real local composer/button events. The
// controller's remote-input and Agent-tool paths never interpret chat as consent.
func (a *App) localBrowserConfirmation(text string) bool {
	if a.ctrl == nil || !browsercontinuation.ConfirmationText(text) || len(a.attachments) > 0 {
		return false
	}
	views := a.ctrl.BrowserContinuations()
	if len(views) != 1 || !views[0].Displayed || views[0].KeptPaused || views[0].Busy || views[0].NeedsRefresh || views[0].OutcomeUnknown {
		for _, v := range views {
			if v.Displayed {
				a.browserResumeNote = "Choose the exact recovery button, or refresh its expired request. No browser was resumed."
				return true
			}
		}
		return false
	}
	a.resumeBrowser(views[0].ID, false)
	return true
}

func (a *App) resumeBrowser(id string, keep bool) {
	if a.ctrl == nil || a.browserResumeBusy != "" {
		return
	}
	ctrl, session, win := a.ctrl, a.status.SessionID, a.win
	refresh := false
	for _, v := range ctrl.BrowserContinuations() {
		if v.ID == id {
			refresh = v.NeedsRefresh && !keep
		}
	}
	a.browserResumeSeq++
	seq := a.browserResumeSeq
	a.browserResumeBusy, a.browserResumeNote = id, "Resuming automation…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var err error
		if refresh {
			err = ctrl.RefreshBrowserContinuation(ctx, session, id)
		} else {
			err = ctrl.ResumeBrowser(ctx, session, id, keep)
		}
		if win == nil {
			return
		}
		win.Update(func() {
			if a.ctrl != ctrl || a.status.SessionID != session || a.browserResumeSeq != seq {
				return
			}
			a.browserResumeBusy = ""
			if err != nil {
				a.browserResumeNote = "Resume not confirmed: " + err.Error()
				return
			}
			a.browserResumeNote = "Automation resumed. The previous task continues only if it is still waiting."
			if refresh {
				a.browserResumeNote = "Confirmation refreshed; click Resume automation to continue."
			}
			if keep {
				a.browserResumeNote = "Automation remains paused."
			}
		})
	}()
}

func (a *App) browserContinuationView(c *ui.Context, browser string) {
	if a.ctrl == nil {
		return
	}
	views := a.ctrl.BrowserContinuations()
	for _, pending := range views {
		pending := pending
		if browser != "" && pending.BrowserID != browser {
			continue
		}
		ui.Column(c.Key("browser-resume:"+pending.ID)).Padding(8, 12).Gap(5).Children(func() {
			label := "Browser paused · " + pending.Origin + " · " + pending.Reason
			ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted).Wrap()
			if pending.Error != "" {
				ui.Text(c, pending.Error).FontSize(11).Wrap()
			}
			ui.Text(c, "完成必要操作后点击恢复，或在本会话回复“继续／已授权”。").FontSize(11).Wrap()
			a.ctrl.DisplayBrowserContinuation(pending.ID)
			if !pending.Supported {
				ui.Text(c, "Upgrade the browser host to enable checked recovery.").FontSize(11).Wrap()
			}
			if pending.RetryAfterMS > 0 {
				ui.Text(c, "Server cooldown active; wait for Retry-After before resuming.").FontSize(11).Wrap()
			}
			disabled := a.status.ReadOnly || a.status.Permission == "read-only" || a.browserResumeBusy != "" || pending.Busy || !pending.Supported || pending.OutcomeUnknown || pending.RetryAfterMS > 0 || strings.TrimSpace(pending.Error) != "" && !pending.NeedsRefresh
			buttonLabel := "恢复自动化"
			if pending.NeedsRefresh {
				buttonLabel = "更新恢复请求"
			}
			ui.Row(c).Gap(8).Children(func() {
				if subtleButton(c, buttonLabel, disabled).Label("Resume browser automation").Clicked() {
					a.resumeBrowser(pending.ID, false)
				}
				if subtleButton(c, "保持暂停", a.browserResumeBusy != "").Label("Keep browser paused").Clicked() {
					a.resumeBrowser(pending.ID, true)
				}
			})
		})
	}
	if a.browserResumeNote != "" {
		ui.Text(c, a.browserResumeNote).FontSize(11).Padding(4, 12).Wrap()
	}
}
