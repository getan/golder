package mygoui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/extensions"
)

type officialCLIInstallState struct {
	project       bool
	initialLark   string
	initialMeegle string
	progress      extensions.OfficialCLIInstallProgress
	cancel        context.CancelFunc
	finished      bool
	message       string
	failed        bool
}

func (s *officialCLIInstallState) name() string {
	if s.project {
		return "Meegle (Project CLI)"
	}
	return "lark-cli"
}

func (a *App) beginOfficialCLIInstall(project bool) (*officialCLIInstallState, context.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	s := &officialCLIInstallState{
		project: project, initialLark: a.extensionEdit.LarkCLI, initialMeegle: a.extensionEdit.ProjectCLI,
		progress: extensions.OfficialCLIInstallProgress{Phase: "checking"}, cancel: cancel,
	}
	a.extensionCLIInstall, a.extensionBusy, a.extensionErr, a.extensionNote = s, true, "", ""
	return s, ctx
}

func (a *App) installOfficialLarkCLI(project bool) {
	if a.extensionBusy {
		return
	}
	if a.ctrl == nil || a.win == nil {
		a.extensionErr = "The official CLI installer is unavailable."
		return
	}
	s, ctx := a.beginOfficialCLIInstall(project)
	win, ctrl := a.win, a.ctrl
	go func() {
		defer s.cancel()
		var lastPhase string
		var lastUpdate time.Time
		path, err := ctrl.InstallOfficialLarkCLIWithProgress(ctx, project, func(p extensions.OfficialCLIInstallProgress) {
			// Do not queue a UI task for every network chunk. Always publish phase
			// changes; subsequent chunks update at most ten times per second.
			now := time.Now()
			if p.Phase == lastPhase && now.Sub(lastUpdate) < 100*time.Millisecond {
				return
			}
			lastPhase, lastUpdate = p.Phase, now
			win.Update(func() {
				if a.ctrl == ctrl {
					a.applyOfficialCLIProgress(s, p)
				}
			})
		})
		var lark, meegle string
		if err == nil {
			// Discovery is local only and stays off the UI thread. Prefer the
			// newly verified executable for the tool the person just installed.
			lark, meegle = s.initialLark, s.initialMeegle
			if project {
				meegle = path
			} else {
				lark = path
			}
			lark, meegle = ctrl.DiscoverLarkCLIs(lark, meegle)
		}
		win.Update(func() {
			if a.extensionCLIInstall != s {
				return
			}
			if a.ctrl != ctrl {
				a.extensionCLIInstall, a.extensionBusy = nil, false
				return
			}
			a.finishOfficialCLIInstall(s, path, lark, meegle, err)
		})
	}()
}

func (a *App) applyOfficialCLIProgress(s *officialCLIInstallState, p extensions.OfficialCLIInstallProgress) {
	if a.extensionCLIInstall == s && !s.finished {
		s.progress = p
	}
}

func (a *App) finishOfficialCLIInstall(s *officialCLIInstallState, path, lark, meegle string, err error) {
	if a.extensionCLIInstall != s || s.finished {
		return
	}
	s.finished, a.extensionBusy = true, false
	s.cancel()
	if err != nil {
		s.failed = true
		s.message = s.name() + " installation failed: " + err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			s.message = s.name() + " download timed out. Check access to the official download server and try again."
		} else if errors.Is(err, context.Canceled) {
			s.failed = false
			s.message = s.name() + " download cancelled. Existing CLI paths were kept."
		}
		if s.failed {
			a.extensionErr = s.message
		}
		return
	}
	// Fill both paths automatically, without saving/enabling the source or
	// overwriting another tool's custom path or edits made during the download.
	if a.extensionEdit.LarkCLI == s.initialLark {
		if !s.project {
			a.extensionEdit.LarkCLI = path
		} else if strings.TrimSpace(a.extensionEdit.LarkCLI) == "" {
			a.extensionEdit.LarkCLI = lark
		}
	}
	if a.extensionEdit.ProjectCLI == s.initialMeegle {
		if s.project {
			a.extensionEdit.ProjectCLI = path
		} else if strings.TrimSpace(a.extensionEdit.ProjectCLI) == "" {
			a.extensionEdit.ProjectCLI = meegle
		}
	}
	s.message = s.name() + " installed. CLI paths were refreshed; review them and save configuration to apply."
	a.extensionNote = s.message
}

func (a *App) cancelOfficialCLIInstall() {
	if s := a.extensionCLIInstall; s != nil && !s.finished {
		s.cancel()
	}
}

func (a *App) officialCLIInstallLabel(project bool) string {
	if s := a.extensionCLIInstall; s != nil && !s.finished && s.project == project {
		return "Installing " + s.name() + "…"
	}
	if project {
		return "Install Project CLI"
	}
	return "Install lark-cli"
}

func officialCLIBytes(n int64) string {
	if n < 1<<10 {
		return fmt.Sprintf("%d B", max(n, 0))
	}
	if n < 1<<20 {
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

func (s *officialCLIInstallState) text() string {
	if s.finished {
		return s.message
	}
	p := s.progress
	phase := map[string]string{
		"checking": "Checking installed binary…", "connecting": "Connecting to download server…",
		"verifying": "Verifying checksum…", "extracting": "Extracting native executable…",
		"installing": "Installing verified binary…", "complete": "Discovering installed CLI paths…",
	}[p.Phase]
	if p.Phase == "downloading" {
		if p.Total > 0 {
			return fmt.Sprintf("%s: Downloading… %d%% · %s / %s", s.name(), min(p.Downloaded*100/p.Total, 100), officialCLIBytes(p.Downloaded), officialCLIBytes(p.Total))
		}
		return fmt.Sprintf("%s: Downloading… %s received (total size unknown)", s.name(), officialCLIBytes(p.Downloaded))
	}
	return s.name() + ": " + phase
}

func (a *App) officialCLIInstallStatus(c *ui.Context) {
	s := a.extensionCLIInstall
	if s == nil {
		return
	}
	theme := c.Theme()
	ui.Column(c).Gap(4).Padding(8).Radius(6).Border(1, theme.Border).Children(func() {
		color := theme.TextMuted
		if s.failed {
			color = theme.Danger
		}
		ui.Text(c, s.text()).FontSize(12).TextColor(color).Wrap()
		if !s.finished {
			ui.Text(c, "Waiting for the server can be slow; a download request times out after 3 minutes.").FontSize(10.5).TextColor(theme.TextMuted).Wrap()
			if subtleButton(c, "Cancel download").Clicked() {
				a.cancelOfficialCLIInstall()
			}
		}
	})
}
