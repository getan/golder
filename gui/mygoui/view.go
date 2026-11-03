package mygoui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

const (
	sidebarWidth = 248
	changesWidth = 460
	titleHeight  = 46
)

// view is the window's single build function.
func (a *App) view(c *ui.Context) {
	t := c.Theme()
	a.list.Key = func(i int) any { return a.items[i].ID }
	a.list.FollowEnd = true
	a.list.FollowAnimation = &ui.Smooth

	c.Root().Background(t.Background)
	ui.Column(c).Fill().Children(func() {
		a.titleBar(c)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c)
			a.center(c)
			if a.changesOpen {
				a.changesPanel(c)
			}
		})
		a.bannerView(c)
	})
	a.paletteModal(c)
	a.modelModal(c)
	a.permissionModal(c)
	a.thinkingModal(c)
	a.workspacesModal(c)
	a.browsersModal(c)
}

// titleBar shows the project, the session, and the page-level actions.
func (a *App) titleBar(c *ui.Context) {
	t := c.Theme()
	changes := len(a.changes.Files)
	ui.Row(c).Height(titleHeight).Padding(0, 14).Gap(10).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, "Golder").FontSize(15).Bold()
		ui.Text(c, "·").TextColor(t.TextMuted)
		name := strings.TrimSpace(a.status.Title)
		if name == "" {
			name = "New session"
		}
		ui.Text(c, name).FontSize(13).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(320)
		ui.Spacer(c)
		if ui.Button(c, "Session").Tooltip("Session actions").Clicked() {
			a.paletteQuery = ""
			a.paletteOpen = true
			a.loadActions()
		}
		label := "Changes"
		if changes > 0 {
			label = fmt.Sprintf("Changes %d  +%d −%d", changes, a.changes.Added, a.changes.Deleted)
		}
		if ui.Button(c, label).Tooltip("Review uncommitted changes").Clicked() {
			a.changesOpen = !a.changesOpen
			if a.changesOpen {
				a.refreshChanges()
			}
		}
		if ui.Button(c, "Workspace").Tooltip("Terminal workspaces").Clicked() {
			a.wsOpen = true
			go a.refreshWorkspaces()
		}
		if ui.Button(c, "Browser").Tooltip("Browser panes").Clicked() {
			a.browserOpen = true
			go a.refreshBrowsers()
		}
	})
}

// sidebar lists the sessions of the store.
func (a *App) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(sidebarWidth).Shrink(0).Background(t.Surface).
		BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Padding(10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "New session").Grow(1).Clicked() {
				a.newSession()
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 8, 10, 8).Gap(2).Children(func() {
				if len(a.sessions) == 0 {
					ui.Text(c, "No sessions yet.").FontSize(12).TextColor(t.TextMuted).Padding(8)
				}
				for _, s := range a.sessions {
					a.sessionRow(c, s)
				}
			})
		})
	})
}

func (a *App) sessionRow(c *ui.Context, s appsession.SessionSummary) {
	t := c.Theme()
	row := ui.Column(c.Key(s.ID)).Padding(7, 8).Gap(2).Radius(6).Cursor(ui.CursorPointer)
	if s.Current {
		row.Background(t.SurfacePressed)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.Title).FontSize(13).FontWeight(500).SingleLine().Ellipsis("…").Grow(1)
			ui.Text(c, s.UpdatedAt.Local().Format("01-02")).FontSize(11).TextColor(t.TextMuted).Shrink(0)
		})
		detail := s.Model
		if s.Provider != "" && s.Model != "" {
			detail = s.Provider + "/" + s.Model
		}
		if detail != "" {
			ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		}
	})
	if row.Clicked() && !s.Current {
		a.resume(s.ID)
	}
}

// center is the transcript, the approval queue and the composer.
func (a *App) center(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
		ui.List(c, &a.list, len(a.items), func(i int) {
			if i >= 0 && i < len(a.items) {
				a.itemView(c, a.items[i])
			}
		}).Grow(1).MinHeight(0).Padding(18, 24).Gap(14).Justify(ui.End).Children(func() {
			if len(a.items) == 0 {
				a.emptyState(c)
			}
		})
		for i := range a.pending {
			a.approvalCard(c, a.pending[i])
		}
		a.composer(c)
	})
}

func (a *App) emptyState(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(8).Padding(40).Children(func() {
		ui.Text(c, "Ask Golder to build something.").FontSize(22).FontWeight(600)
		ui.Text(c, "The agent works in this project. Tool calls, approvals and changes appear here.").
			FontSize(13).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(420)
		ui.Row(c).Gap(8).Justify(ui.Center).Children(func() {
			if ui.Button(c, "Model: "+a.status.Model).Clicked() {
				a.modelOpen = true
			}
			if ui.Button(c, "Permissions: "+a.status.Permission).Clicked() {
				a.permOpen = true
			}
		})
	})
}

// bannerView shows the last controller message.
func (a *App) bannerView(c *ui.Context) {
	if a.banner == "" && a.changesErr == "" {
		return
	}
	t := c.Theme()
	msg := a.banner
	color := t.TextMuted
	if a.bannerErr {
		color = t.Danger
	}
	if a.changesErr != "" {
		msg, color = "Changes: "+a.changesErr, t.Danger
	}
	ui.Row(c).Padding(8, 16).Gap(8).AlignItems(ui.Center).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, msg).FontSize(12).TextColor(color).Grow(1).SingleLine().Ellipsis("…")
		if ui.Button(c, "Dismiss").Clicked() {
			a.banner, a.bannerErr, a.changesErr = "", false, ""
		}
	})
}

// workspacesModal lists terminal workspaces and opens one in a terminal window.
func (a *App) workspacesModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.wsOpen, func() {
		ui.Column(c).Gap(8).Width(560).Children(func() {
			ui.Text(c, "Terminal workspaces").FontSize(16).Bold()
			ui.Text(c, "golder workspace keeps PTYs in a daemon. Opening one runs `golder workspace attach` in a MyGo terminal window; the GUI never runs a second workspace.").
				FontSize(12).TextColor(t.TextMuted)
			if ui.Button(c, "Refresh").Clicked() {
				go a.refreshWorkspaces()
			}
			if len(a.workspaces) == 0 {
				ui.Text(c, "No named workspace was found.").FontSize(12).TextColor(t.TextMuted)
			}
			ui.Scroll(c).MaxHeight(320).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					for _, w := range a.workspaces {
						a.workspaceRow(c, w)
					}
				})
			})
		})
	})
}

func (a *App) workspaceRow(c *ui.Context, w appsession.WorkspaceInfo) {
	t := c.Theme()
	state := "stopped"
	if w.Running {
		state = fmt.Sprintf("running · %d panes", w.Panes)
	}
	if w.Error != "" {
		state = w.Error
	}
	ui.Row(c).Gap(8).Padding(6, 8).Radius(6).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Text(c, w.Session).FontWeight(600)
			ui.Text(c, state).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})
		if !w.Running {
			ui.Button(c, "Open").Disabled(true).Tooltip("Start it with `golder workspace` first")
		} else if ui.Button(c, "Open").Clicked() {
			a.wsOpen = false
			a.openWorkspaceTerminal(w.Session)
		}
	})
}

// browsersModal lists live browser panes and opens a read-only preview.
func (a *App) browsersModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.browserOpen, func() {
		ui.Column(c).Gap(8).Width(560).Children(func() {
			ui.Text(c, "Browser panes").FontSize(16).Bold()
			ui.Text(c, "Previews are PNG screenshots from the browser's control socket; the GUI does not load the page itself.").
				FontSize(12).TextColor(t.TextMuted)
			if ui.Button(c, "Refresh").Clicked() {
				go a.refreshBrowsers()
			}
			if len(a.browsers) == 0 {
				ui.Text(c, "No browser control socket was found under .golder/browser.").FontSize(12).TextColor(t.TextMuted)
			}
			for _, b := range a.browsers {
				row := ui.Row(c).Gap(8).Padding(6, 8).Radius(6).AlignItems(ui.Center).Background(t.Surface)
				row.Children(func() {
					ui.Column(c).Grow(1).Gap(1).Children(func() {
						title := b.Title
						if title == "" {
							title = b.URL
						}
						ui.Text(c, title).FontWeight(600).SingleLine().Ellipsis("…")
						ui.Text(c, b.URL).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					})
					if ui.Button(c, "Preview").Clicked() {
						a.browserOpen = false
						a.openBrowserPreview(b)
					}
				})
			}
		})
	})
}

// relativeTime formats a session timestamp for the sidebar.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return t.Local().Format("Jan 2")
	}
}
