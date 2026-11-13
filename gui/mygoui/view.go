package mygoui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

const (
	sidebarWidth     = 248
	navigationWidth  = 50
	minComposerWidth = 320
	minRightWidth    = 180
	titleHeight      = 46
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
			a.resumeQueued()
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
		a.extensionAuthQR = nil
		a.extensionConfirm = ""
		a.extensionSourceConfirm = ""
		a.mcpAdd = nil
		a.mcpRemove = nil
		a.mcpApproval = nil
		a.mcpToolsID = ""
		if a.mcpOAuth != nil && a.mcpOAuth.cancel != nil {
			a.mcpOAuth.cancel()
		}
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
	// Scrolling with the mouse need not focus a widget. In that case provide
	// the same jump shortcut without intercepting focused editors or panes.
	if a.list != nil && !a.list.follow && !c.Root().FocusWithin() && c.Shortcut(ui.Ctrl, ui.KeyE) {
		a.list.jumpToBottom()
	}
	if !c.Root().FocusWithin() && c.Shortcut(ui.Ctrl, ui.KeyT) {
		a.toggleLatestToolCard()
	}
	if !a.remoteOpen && a.remoteRelease != nil {
		a.stopRemoteControl()
	}

	c.Root().Background(t.Background)
	ui.Column(c).Fill().Children(func() {
		a.titleBar(c)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.navigationRail(c)
			a.clampWorkspaceWidths(c.Root().LayoutBounds().W - navigationWidth)
			conversation := func() {
				if a.sidebarClosed {
					a.center(c)
				} else {
					if a.sidebarSize <= 0 {
						a.sidebarSize = sidebarWidth
					}
					ui.Split(c, &a.sidebarSize, func() { a.sidebar(c) }, func() { a.center(c) }).Fill().MinWidth(0)
				}
			}
			if a.hasRightPanel() {
				ui.SplitPercent(c, &a.rightSplit, conversation, func() { a.rightPanelView(c) }).Grow(1).MinWidth(0)
			} else {
				conversation()
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
	a.terminalModals(c)
	a.guiUpdateModal(c)
	a.remoteControlModal(c)
}

// Reserve space for the composer after accounting for the navigation rail,
// optional sidebar and right panel. The layout bounds are from the previous
// frame and are in the same unzoomed coordinates as the split sizes.
func (a *App) clampWorkspaceWidths(width float32) {
	if a.sidebarSize <= 0 {
		a.sidebarSize = sidebarWidth
	}
	if a.rightSplit <= 0 {
		a.rightSplit = 50
	}
	if width <= 1 {
		return
	}
	rightMin := float32(0)
	if a.hasRightPanel() {
		rightMin = minRightWidth + 1
	}
	sidebar := float32(0)
	if !a.sidebarClosed {
		maxSidebar := max(width-rightMin-minComposerWidth-1, 0)
		a.sidebarSize = min(a.sidebarSize, maxSidebar)
		sidebar = a.sidebarSize + 1
	}
	if !a.hasRightPanel() {
		return
	}
	available := width - 1 // the right workspace divider
	minPercent := min(max((sidebar+minComposerWidth)*100/available, 30), 100)
	maxPercent := max(min((available-minRightWidth)*100/available, 70), minPercent)
	a.rightSplit = min(max(a.rightSplit, minPercent), maxPercent)
}

// Keep both sides usable while remembering each panel's last in-session split.
func clampPanelSplit(split *float32, initial float32) {
	if *split <= 0 {
		*split = initial
	}
	*split = min(max(*split, 30), 70)
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
		if remoteTitleIconButton(c, a.remoteServer != nil).Clicked() {
			a.remoteOpen = true
		}
		if titleIconButton(c, "files", filesMonochromeIcon, "Files", "Browse project files", a.filesOpen, false).Clicked() {
			if a.filesOpen {
				a.filesOpen = false
			} else {
				a.openFileExplorer()
			}
		}
		changesTip := "Git status, commit, push and fast-forward pull"
		if changes > 0 {
			changesTip = fmt.Sprintf("Git · %d files · +%d −%d", changes, a.changes.Added, a.changes.Deleted)
		}
		if titleIconButton(c, "changes", changesMonochromeIcon, "Git", changesTip, a.changesOpen, false).Clicked() {
			if a.changesOpen {
				a.closeChangesPanel()
			} else {
				a.filesOpen = false
				a.changesOpen = true
				a.refreshChanges()
			}
		}
		computerPresent := a.computerOpen || a.computerSession != nil && a.computerSession.Status().Active
		if titleIconButton(c, "computer", computerMonochromeIcon, "Computer Use", "Select or view a controlled application window", a.rightPanelVisible(rightPanelComputer), computerPresent).Clicked() {
			if !a.revealRequestedPanel(rightPanelComputer) {
				a.filesOpen = false
				a.closeChangesPanel()
				a.toggleComputerPanel()
				if a.computerOpen {
					a.rightPreferred = rightPanelComputer
				}
			}
		}
		browserTip := "Browser · no active page"
		if len(a.browsers) > 0 {
			browserTip = fmt.Sprintf("Browser · %d active", len(a.browsers))
		}
		if titleIconButton(c, "browser", browserMonochromeIcon, "Browser", browserTip, a.rightPanelVisible(rightPanelBrowser), a.browserPanelOpen || len(a.browsers) > 0).Clicked() {
			if !a.revealRequestedPanel(rightPanelBrowser) {
				a.filesOpen = false
				a.closeChangesPanel()
				if a.browserPanelOpen {
					a.hideBrowserPanel()
				} else if len(a.browsers) == 1 {
					a.requestBrowserPreview(a.browsers[0])
					a.rightPreferred = rightPanelBrowser
				} else {
					a.browserOpen = true
					if a.ctrl != nil {
						a.refreshBrowsers()
					}
				}
			}
		}
		scope := a.currentTerminalScope()
		terminalExists := a.terminals != nil && a.terminals[scope] != nil && a.terminals[scope].root != nil
		if titleIconButton(c, "terminal", terminalMonochromeIcon, "Terminal", "Show or hide conversation terminals", a.rightPanelVisible(rightPanelTerminal), a.terminalOpen || terminalExists).Clicked() {
			if !a.revealRequestedPanel(rightPanelTerminal) {
				a.filesOpen = false
				a.closeChangesPanel()
				a.toggleTerminal()
				if a.terminalOpen {
					a.rightPreferred = rightPanelTerminal
				}
			}
		}
	})
}

// sidebar mirrors the Codex project model: folders are explicit top-level
// objects, while chats are filtered under the selected folder.
func (a *App) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Surface).
		BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Padding(10, 10, 8, 10).Gap(10).Children(func() {
			header := ui.Row(c).Padding(0, 4).AlignItems(ui.Center)
			header.Children(func() {
				ui.Text(c, "Projects").FontSize(11).FontWeight(600).TextColor(t.TextMuted).Grow(1)
				add := subtleButton(c, "+").Label("Add project folder").Tooltip("Add project folder")
				if !header.Hovered() && !add.FocusVisible() {
					add.Opacity(0)
				}
				if add.Clicked() {
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
						add := subtleButton(c, "+").Label("Create session in " + project.Name).Tooltip("New session in " + project.Name)
						if !row.Hovered() && !add.FocusVisible() {
							add.Opacity(0)
						}
						if add.Clicked() {
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
	})
}

func (a *App) navigationRail(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(navigationWidth).Shrink(0).Padding(8, 10).Gap(8).AlignItems(ui.Center).
		Background(t.Surface).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
		if compactIconButton(c, "toggle-sidebar", sidebarIcon, "Toggle sidebar", "Show or hide projects and chats").Clicked() {
			a.sidebarClosed = !a.sidebarClosed
		}
		ui.Spacer(c)
		if compactIconButton(c, "archive", archiveIcon, "Archived sessions", "Show or hide archived sessions").Clicked() {
			a.showArchived = !a.showArchived
			a.sidebarClosed = false
		}
		if a.updateButton(c).Clicked() {
			a.activateGUIUpdate()
		}
		if compactIconButton(c, "settings", settingsIcon, "Settings", "Settings").Clicked() {
			a.openSettings()
		}
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
			switch {
			case s.NeedsApproval:
				ui.Text(c, "● Needs attention").FontSize(10).TextColor(t.Warning).Tooltip("This session is waiting for you")
			case s.Running:
				ui.Text(c, "◌ Working").FontSize(10).TextColor(t.Accent).Tooltip("This session is running")
			case s.Unread && s.Failed:
				ui.Text(c, "● Failed").FontSize(10).TextColor(t.Danger).Tooltip("Background run failed")
			case s.Unread:
				ui.Text(c, "● Done").FontSize(10).TextColor(t.Success).Tooltip("Background run finished")
			}
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
	// Sidebar actions can switch sessions during this frame. Resolve both the
	// list and its rows after the sidebar has processed those actions, so their
	// indices always refer to the same active session.
	if a.list == nil {
		a.list = &transcriptWindow{}
	}
	rows := a.projectedTranscriptRows()
	waiting := a.awaitingFirstOutput()
	a.list.sync(rows, waiting)
	if a.list.remeasure {
		a.list.remeasure = false
		c.Invalidate()
	}
	if a.approvalReveal != "" {
		if index := approvalRowIndex(rows, a.pending, a.approvalReveal); index >= 0 {
			a.list.ScrollIntoView(index)
		}
		a.approvalReveal = ""
	}
	t := c.Theme()
	window := a.list
	center := ui.Column(c).Grow(1).MinWidth(0).Background(t.Background)
	center.OnShortcut(ui.Ctrl, ui.KeyE, func() {
		window.jumpToBottom()
		c.Services().Invalidate()
	})
	center.OnShortcut(ui.Ctrl, ui.KeyT, func() {
		a.toggleLatestToolCard()
		c.Services().Invalidate()
	})
	center.Children(func() {
		scroll := ui.Scroll(c.Key("transcript:"+a.status.SessionID)).Selectable().TrackScroll(&window.scroll).Grow(1).MinHeight(0).
			Padding(18, 24).Gap(14).Justify(ui.End).CopyData(func(parts []ui.SelectedText) transfer.Data {
			if !window.copyReady(parts) {
				a.banner, a.bannerErr = "The selection is still loading. Copy again when it finishes.", true
				c.Services().Invalidate()
				return transfer.TextData("[Selection is still loading; copy again.]")
			}
			text, hasObjects := window.copyText(parts)
			if hasObjects {
				a.banner, a.bannerErr = "Inline formulas are represented as [formula] in copied text.", false
				c.Services().Invalidate()
			}
			return transfer.TextData(text)
		})
		scroll.HandleInput(window.handleScrollInput)
		scroll.ContextMenu(func(m *ui.Menu) {
			m.EditItems()
			m.Separator()
			item := m.Item("Copy response as Markdown")
			if item.Chosen() {
				if window.menuValid {
					source := []byte(window.menuSource)
					c.Services().WriteClipboardData(transfer.New(transfer.NewItem(
						transfer.Bytes(transfer.Text, source), transfer.Bytes(transfer.Markdown, source),
					)))
				}
				window.menuSource, window.menuValid = "", false
			} else {
				x, y, over := scroll.PointerPosition()
				bounds := scroll.Bounds()
				window.menuSource, window.menuValid = window.markdownAt(bounds.X+x, bounds.Y+y)
				window.menuValid = over && window.menuValid
				item.Disabled(!window.menuValid)
			}
		})
		if !scroll.FocusWithin() {
			window.focusKey = ""
		}
		ranges := window.plan(scroll.LayoutBounds().H, scroll.SelectedTexts())
		window.ranges = append(window.ranges[:0], ranges...)
		scroll.Children(func() {
			window.startMarker = ui.Text(c.Key("transcript-select-start"), "").Absolute().Width(0).Height(0).Selectable().ID()
			defer func() {
				window.endMarker = ui.Text(c.Key("transcript-select-end"), "").Absolute().Width(0).Height(0).Selectable().ID()
			}()
			if len(window.keys) == 0 {
				window.menuHits = nil
				a.emptyState(c)
				return
			}
			previous := 0
			rendered := make(map[string]bool)
			hits := make([]transcriptMenuHit, 0)
			for _, visible := range ranges {
				if visible.lo > previous {
					space := window.prefix(visible.lo) - window.prefix(previous) - transcriptRowGap
					ui.Box(c.Key(fmt.Sprintf("transcript-space:%d", previous))).Height(float32(max(space, 0))).Shrink(0)
				}
				for i := visible.lo; i < visible.hi; i++ {
					i, key := i, window.keys[i]
					rendered[key] = true
					row := ui.Column(c.Key("transcript-row:" + key)).FillWidth().Shrink(0)
					hit := transcriptMenuHit{box: row.Bounds()}
					if i < len(rows) {
						if item, ok := a.transcriptRowItem(rows[i]); ok && item.Kind == appsession.ItemAssistant {
							hit.source, hit.assistant = item.Text, true
						}
					}
					hits = append(hits, hit)
					if row.FocusWithin() {
						window.focusKey = key
					}
					height := row.LayoutBounds().H
					if height > 0 {
						delete(window.measuring, key)
						if window.measure(i, height) {
							c.Invalidate()
						}
					} else if !window.measuring[key] {
						window.measuring[key] = true
						c.Invalidate()
					}
					row.Children(func() {
						if i < len(rows) {
							a.transcriptRowView(c, rows[i])
						} else if waiting {
							ui.Text(c.Key("awaiting-first-output"), "正在思考…").FontSize(12).TextColor(t.TextMuted)
						}
					})
				}
				previous = visible.hi
			}
			window.menuHits = hits
			for key := range window.measuring {
				if !rendered[key] {
					delete(window.measuring, key)
				}
			}
			if previous < len(window.keys) {
				space := window.prefix(len(window.keys)) - window.prefix(previous) - transcriptRowGap
				ui.Box(c.Key("transcript-trailing-space")).Height(float32(max(space, 0))).Shrink(0)
			}
		})
		if !window.follow && window.unseen > 0 && scroll.LayoutBounds().H > 0 {
			word := "lines"
			if window.unseen == 1 {
				word = "line"
			}
			label := fmt.Sprintf("↓ %d new %s · Ctrl+E to jump", window.unseen, word)
			bounds, parent := scroll.LayoutBounds(), center.LayoutBounds()
			bottom := max(parent.Y+parent.H-(bounds.Y+bounds.H), 0) + 8
			accessible := fmt.Sprintf("Jump to latest (%d new %s)", window.unseen, word)
			if subtleButton(c, label).Absolute().Right(24).Bottom(bottom).Label(accessible).
				Tooltip("New logical text lines; wrapped lines are not counted").Clicked() {
				window.jumpToBottom()
				c.Invalidate()
			}
		}
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
		msg, color = "Git: "+a.changesErr, t.Danger
	}
	ui.Row(c).Padding(8, 16).Gap(8).AlignItems(ui.Center).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, msg).Selectable().FontSize(12).TextColor(color).Grow(1).SingleLine().Ellipsis("…")
		if subtleButton(c, "Copy message").Clicked() {
			c.Services().WriteClipboard(msg)
		}
		if subtleButton(c, "Dismiss").Clicked() {
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
			if subtleButton(c, "Refresh").Clicked() {
				a.refreshBrowsers()
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
					if subtleButton(c, "Open").Clicked() {
						a.browserOpen = false
						a.filesOpen = false
						a.closeChangesPanel()
						a.requestBrowserPreview(b)
						a.rightPreferred = rightPanelBrowser
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
