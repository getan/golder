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
	a.applyDisplayPreferences(c)
	t := c.Theme()
	if !a.computerPickerOpen && a.computerCandidate != nil {
		a.clearComputerCandidate()
	}
	if !a.computerPickerOpen && a.computerRequestPicker {
		a.computerRequestPicker = false
		a.computerSuggested = ""
		if a.ctrl != nil {
			a.ctrl.ResetComputerRequest()
		}
	}
	if !a.computerPickerOpen && a.computerSetupStop != nil {
		a.computerSetupStop()
		a.computerSetupStop = nil
		a.computerSetupSeq++
		a.computerBusy = false
		if a.computerBackend != nil {
			backend := a.computerBackend
			a.computerBackend = nil
			go backend.Close()
		}
	}
	if !a.computerPickerOpen && a.computerBackend != nil && a.computerSession == nil {
		backend := a.computerBackend
		a.computerBackend = nil
		a.computerTargets = nil
		go backend.Close()
	}
	if !a.settingsOpen {
		// The password field is write-only. Dismissing Settings must not retain a
		// value in long-lived view state.
		a.settingsEdit.NewCredential = ""
		a.settingsEdit.RemoveCredential = false
		a.extensionEdit.NewAppSecret = ""
		a.extensionAuthURL = ""
		a.extensionAuthCode = ""
		a.extensionConfirm = ""
		a.extensionSourceConfirm = ""
		if a.extensionCancel != nil {
			a.extensionCancel()
			a.extensionCancel = nil
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyK) {
		a.paletteQuery = ""
		a.paletteOpen = true
		a.loadActions()
	}
	a.list.Key = func(i int) any {
		if i < len(a.items) {
			return a.items[i].ID
		}
		i -= len(a.items)
		if i < len(a.pending) {
			return a.pending[i].ID
		}
		return "awaiting-first-output"
	}
	a.list.FollowEnd = true
	a.list.FollowAnimation = &ui.Smooth

	c.Root().Background(t.Background)
	ui.Column(c).Fill().Children(func() {
		a.titleBar(c)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if a.browserPanelOpen {
				ui.Row(c).Grow(1).MinWidth(0).AlignItems(ui.Stretch).Children(func() {
					a.sidebar(c)
					a.center(c)
				})
				a.browserPanel(c)
				return
			}
			a.sidebar(c)
			a.center(c)
			if a.filesOpen {
				a.fileExplorerPanel(c)
			}
			if a.changesOpen {
				a.changesPanel(c)
			}
			if a.computerOpen {
				a.computerPanel(c)
			}
		})
		a.bannerView(c)
	})
	a.paletteModal(c)
	a.modelModal(c)
	a.permissionModal(c)
	a.settingsModal(c)
	a.sessionRenameModal(c)
	a.sessionDeleteModal(c)
	a.browsersModal(c)
	a.computerPicker(c)
}

// titleBar shows the project, the session, and the page-level actions.
func (a *App) titleBar(c *ui.Context) {
	t := c.Theme()
	changes := a.changes.TotalFiles
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
		if compactIconButton(c, "files", filesMonochromeIcon, "Files", "Browse project files").Clicked() {
			if a.filesOpen {
				a.filesOpen = false
			} else {
				a.openFileExplorer()
			}
		}
		changesTip := "Review uncommitted changes"
		if changes > 0 {
			changesTip = fmt.Sprintf("Changes · %d files · +%d −%d", changes, a.changes.Added, a.changes.Deleted)
		}
		if compactIconButton(c, "changes", changesMonochromeIcon, "Changes", changesTip).Clicked() {
			a.changesOpen = !a.changesOpen
			if a.changesOpen {
				a.filesOpen = false
				a.hideComputerPanel()
				a.closeBrowserPanel()
				a.refreshChanges()
			}
		}
		if compactIconButton(c, "computer", computerMonochromeIcon, "Computer Use", "Select or view a controlled application window").Clicked() {
			a.toggleComputerPanel()
		}
		browserTip := "Browser · no active page"
		if len(a.browsers) > 0 {
			browserTip = fmt.Sprintf("Browser · %d active", len(a.browsers))
		}
		if compactIconButton(c, "browser", browserMonochromeIcon, "Browser", browserTip).Clicked() {
			a.filesOpen = false
			a.hideComputerPanel()
			if a.browserPanelOpen {
				a.closeBrowserPanel()
			} else if len(a.browsers) == 1 {
				a.openBrowserPreview(a.browsers[0])
			} else {
				a.browserOpen = true
				go a.refreshBrowsers()
			}
		}
	})
}

// sidebar mirrors the Codex project model: folders are explicit top-level
// objects, while chats are filtered under the selected folder.
func (a *App) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(sidebarWidth).Shrink(0).Background(t.Surface).
		BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Padding(10, 10, 8, 10).Gap(10).Children(func() {
			ui.Row(c).Padding(0, 4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Projects").FontSize(11).FontWeight(600).TextColor(t.TextMuted).Grow(1)
				if ui.Button(c, "+").Tooltip("Add project folder").Clicked() {
					a.addProject()
				}
			})
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 8, 12, 8).Gap(3).Children(func() {
				projects := a.projects
				if len(projects) == 0 {
					ui.Text(c, "Add a folder to start a project.").FontSize(12).TextColor(t.TextMuted).Padding(8)
				}
				for _, project := range projects {
					project := project
					selected := sameProjectPath(project.Path, a.selectedProject)
					row := ui.Row(c.Key("project:"+project.Path)).Padding(6, 7).Gap(7).Radius(6).
						AlignItems(ui.Center).Cursor(ui.CursorPointer).Tooltip(project.Path)
					a.attachProjectMenu(row, project)
					if selected {
						row.Background(t.SurfacePressed)
					} else if row.Hovered() {
						row.Background(t.SurfaceHover)
					}
					row.Children(func() {
						marker := "›"
						if selected {
							marker = "⌄"
						}
						ui.Text(c, marker).FontSize(13).TextColor(t.TextMuted).Width(12)
						ui.Text(c, project.Name).FontSize(13).FontWeight(500).SingleLine().Ellipsis("…").Grow(1)
						if sameProjectPath(project.Path, a.project) {
							ui.Text(c, "●").FontSize(7).TextColor(t.Accent).Tooltip("Current session project")
						}
						if ui.Button(c, "+").Tooltip("New session in " + project.Name).Clicked() {
							a.selectedProject = project.Path
							a.newSessionInProject(project.Path)
						}
					})
					if row.Clicked() && !selected {
						a.selectProject(project.Path)
					}
				}
				ui.Text(c, "Chats").FontSize(11).FontWeight(600).TextColor(t.TextMuted).Padding(12, 8, 4, 8)
				sessions := a.visibleSessions()
				if len(sessions) == 0 {
					ui.Text(c, "No sessions in this project.").FontSize(12).TextColor(t.TextMuted).Padding(8)
				}
				for _, session := range sessions {
					a.sessionRow(c, session)
				}
			})
		})
		ui.Column(c).Padding(8, 10, 10, 10).Gap(4).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			archiveLabel := "Archived sessions"
			if a.showArchived {
				archiveLabel = "Hide archived"
			}
			if ui.Button(c, archiveLabel).FillWidth().Tooltip("Show or hide archived sessions").Clicked() {
				a.showArchived = !a.showArchived
			}
			if ui.Button(c, "⚙  Settings").FillWidth().Clicked() {
				a.openSettings()
			}
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
	})
	if row.Clicked() && !s.Current {
		a.resume(s.ID)
	}
}

// center is the transcript, the approval queue and the composer.
func (a *App) center(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
		waiting := a.awaitingFirstOutput()
		total := len(a.items) + len(a.pending)
		if waiting {
			total++
		}
		ui.List(c, &a.list, total, func(i int) {
			switch {
			case i >= 0 && i < len(a.items):
				a.itemView(c, a.items[i])
			case i >= len(a.items) && i < len(a.items)+len(a.pending):
				a.approvalCard(c, a.pending[i-len(a.items)])
			case waiting:
				ui.Text(c.Key("awaiting-first-output"), "正在思考…").FontSize(12).TextColor(t.TextMuted)
			}
		}).Grow(1).MinHeight(0).Padding(18, 24).Gap(14).Justify(ui.End).Children(func() {
			if total == 0 {
				a.emptyState(c)
			}
		})
		a.composer(c)
	})
}

func (a *App) awaitingFirstOutput() bool {
	if !a.status.Running || a.status.RunID == "" || len(a.pending) > 0 {
		return false
	}
	prefix := a.status.RunID + "-"
	for _, item := range a.items {
		if !strings.HasPrefix(item.ID, prefix) {
			continue
		}
		switch item.Kind {
		case appsession.ItemAssistant, appsession.ItemThinking, appsession.ItemTool:
			return false
		}
	}
	return true
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
