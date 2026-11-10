package mygoui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

func (a *App) beginGUIDownload() (context.Context, context.CancelFunc, uint64) {
	ctx, cancel, seq := a.beginGUIUpdateOperation("downloading", 15*time.Minute)
	a.updateTag = a.updatePackage.Tag
	a.updateProgress, a.updateReceived = 0, 0
	a.updateError, a.updatePath = "", ""
	a.updateDialogOpen, a.updateQuitConfirm = true, false
	// Progress belongs to the download panel, not a repeatedly recreated banner.
	// Dismissing another notification must stay dismissed as bytes arrive.
	return ctx, cancel, seq
}

func (a *App) applyGUIDownloadProgress(seq uint64, done, total int64) {
	if seq != a.updateSeq || !a.updateBusy || a.updatePhase != "downloading" || total <= 0 || total != a.updatePackage.Size {
		return
	}
	a.updateReceived = max(a.updateReceived, min(max(done, 0), total))
	a.updateProgress = int(a.updateReceived * 100 / total)
}

func (a *App) finishGUIDownload(seq uint64, path string, err error) {
	if !a.finishGUIUpdateOperation(seq) {
		return
	}
	if err == nil && path == "" {
		err = errors.New("the verified download is unavailable")
	}
	a.updateDialogOpen, a.updateQuitConfirm = true, false
	if err != nil {
		a.updateError, a.updatePath = err.Error(), ""
		a.banner, a.bannerErr = "GUI download failed: "+err.Error()+". Click Update to retry.", true
		return
	}
	a.updateError, a.updatePath = "", path
	a.updateProgress, a.updateReceived = 100, a.updatePackage.Size
	a.banner, a.bannerErr = "GUI "+a.updateTag+" downloaded. Open the package to install it.", false
}

func (a *App) guiDownloadStatus() string {
	if a.updateReceived == 0 {
		return "Connecting to download server…"
	}
	if a.updateProgress >= 100 {
		return "Verifying and saving the package…"
	}
	return "Downloading the GUI package…"
}

func guiDownloadProgressRect(r ui.Rect, percent int) ui.Rect {
	r.W *= float32(min(max(percent, 0), 100)) / 100
	return r
}

func (a *App) guiDownloadProgressView(c *ui.Context) {
	t := c.Theme()
	running := a.updateBusy && a.updatePhase == "downloading"
	ui.Column(c).Width(500).Gap(10).Children(func() {
		ui.Text(c, "Download GUI "+a.updateTag).FontSize(16).Bold()
		if running {
			ui.Text(c, a.guiDownloadStatus()).FontSize(12).Wrap()
		} else if a.updateError != "" {
			ui.Text(c, "Download failed: "+a.updateError).FontSize(12).TextColor(t.Danger).Wrap()
		}
		percent := a.updateProgress
		ui.Box(c.Key("gui-download-progress")).Height(8).FillWidth().Label("GUI download progress").Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(r, t.Border, 4)
			if percent > 0 {
				p.Fill(guiDownloadProgressRect(r, percent), t.Accent, 4)
			}
		})
		ui.Text(c, fmt.Sprintf("%d%% · %s / %s", percent, officialCLIBytes(a.updateReceived), officialCLIBytes(a.updatePackage.Size))).FontSize(12)
		ui.Text(c, "Downloads use the configured Golder proxy when available. Nothing is installed or replaced automatically.").FontSize(11).TextColor(t.TextMuted).Wrap()
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if running {
				if subtleButton(c, "Cancel download").Clicked() {
					a.cancelGUIUpdate()
					a.updateDialogOpen = false
					a.banner, a.bannerErr = "GUI download cancelled.", false
				}
				if subtleButton(c, "Continue in background").Clicked() {
					a.updateDialogOpen = false
				}
			} else {
				if subtleButton(c, "Close").Clicked() {
					a.updateDialogOpen = false
				}
				if subtlePrimaryButton(c, "Retry download").Clicked() {
					a.startGUIDownload()
				}
			}
		})
	})
}
