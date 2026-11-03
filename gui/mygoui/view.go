package mygoui

import (
	"fmt"
	"path/filepath"
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
	if !a.settingsOpen {
		// The password field is write-only. Dismissing Settings must not retain a
		// value in long-lived view state.
		a.settingsEdit.NewCredential = ""
		a.settingsEdit.RemoveCredential = false
	}
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		a.paletteQuery = ""
		a.paletteOpen = true
		a.loadActions()
	}
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
	a.settingsModal(c)
	a.sessionRenameModal(c)
	a.sessionDeleteModal(c)
	a.browsersModal(c)
}

// titleBar shows the project, the session, and the page-level actions.
func (a *App) titleBar(c *ui.Context) {
	t := c.Theme()
	changes := len(a.changes.Files)
	ui.Row(c).Height(titleHeight).Padding(0, 14).Gap(10).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		project := filepath.Base(strings.TrimSpace(a.project))
		if project == "." || project == "" {
			project = "Golder"
		}
		ui.Text(c, project).FontSize(15).Bold().MaxWidth(220).SingleLine().Ellipsis("…").Tooltip(a.project)
		ui.Text(c, "·").TextColor(t.TextMuted)
		name := strings.TrimSpace(a.status.Title)
		if name == "" {
			name = "New session"
		}
		ui.Text(c, name).FontSize(13).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(320)
		ui.Spacer(c)
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
		if len(a.browsers) > 0 {
			browserLabel := "Browser"
			if len(a.browsers) > 1 {
				browserLabel = fmt.Sprintf("Browser %d", len(a.browsers))
			}
			if ui.Button(c, browserLabel).Tooltip("Open the active interactive browser").Clicked() {
				if len(a.browsers) == 1 {
					a.openBrowserPreview(a.browsers[0])
				} else {
					a.browserOpen = true
				}
			}
		}
		settings := ui.ButtonBase(c).Size(32, 32).Radius(16).Label("Settings").Tooltip("Settings").
			Background(t.Surface).Border(1, t.Border)
		if settings.Hovered() {
			settings.Background(t.SurfaceHover)
		}
		settings.Children(func() {
			ui.Text(c, "⚙").FontSize(16).TextAlign(ui.Center)
		})
		if settings.Clicked() {
			a.openSettings()
		}
	})
}

// sidebar lists the sessions of the store.
func (a *App) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(sidebarWidth).Shrink(0).Background(t.Surface).
		BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Padding(10, 12).Gap(8).Children(func() {
			options := a.projectOptions()
			if a.selectedProject == "" {
				a.selectedProject = a.project
			}
			ui.Select(c, &a.selectedProject, options).FillWidth().Tooltip("Filter sessions by project")
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if ui.PrimaryButton(c, "New session").Grow(1).Clicked() {
					a.newSession()
					a.selectedProject = a.project
				}
				archiveLabel := "Archived"
				if a.showArchived {
					archiveLabel = "Hide archived"
				}
				if ui.Button(c, archiveLabel).Tooltip("Show or hide archived sessions").Clicked() {
					a.showArchived = !a.showArchived
				}
			})
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 8, 10, 8).Gap(4).Children(func() {
				groups := a.visibleSessionGroups()
				if len(groups) == 0 {
					ui.Text(c, "No sessions yet.").FontSize(12).TextColor(t.TextMuted).Padding(8)
				}
				for _, group := range groups {
					name := filepath.Base(group.Cwd)
					if group.Cwd == "" || name == "." {
						name = "Unattributed"
					}
					ui.Column(c).Padding(8, 8, 3, 8).Gap(1).Children(func() {
						ui.Text(c, name).FontSize(11).FontWeight(600).SingleLine().Ellipsis("…")
						ui.Text(c, sessionPathDisplay(group.Cwd)).FontSize(10).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(group.Cwd)
					})
					for _, s := range group.Sessions {
						a.sessionRow(c, s)
					}
				}
			})
		})
	})
}

func (a *App) sessionRow(c *ui.Context, s appsession.SessionSummary) {
	t := c.Theme()
	row := ui.Column(c.Key(s.ID)).Padding(7, 8).Gap(2).Radius(6).Cursor(ui.CursorPointer)
	a.attachSessionMenu(row, s)
	if s.Current {
		row.Background(t.SurfacePressed)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, s.Title).FontSize(13).FontWeight(500).SingleLine().Ellipsis("…").Grow(1)
			if s.Archived {
				ui.Text(c, "archived").FontSize(9).TextColor(t.TextMuted)
			}
			ui.Text(c, s.UpdatedAt.Local().Format("01-02")).FontSize(11).TextColor(t.TextMuted).Shrink(0)
		})
		detail := sessionPathDisplay(s.Cwd)
		model := s.Model
		if s.Provider != "" && s.Model != "" {
			model = s.Provider + "/" + s.Model
		}
		if model != "" {
			detail += " · " + model
		}
		ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(s.Cwd)
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

// browsersModal lists live controlled browsers and opens a native surface.
func (a *App) browsersModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.browserOpen, func() {
		ui.Column(c).Gap(8).Width(560).Children(func() {
			ui.Text(c, "Browsers").FontSize(16).Bold()
			ui.Text(c, "The native surface draws Chromium frames and forwards mouse, keyboard, scroll and composed text through the owner-only control socket.").
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
					if ui.Button(c, "Open").Clicked() {
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
