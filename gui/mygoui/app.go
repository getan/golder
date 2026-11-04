// Package mygoui is the Golder desktop window, drawn with MyGo's native UI.
// It consumes appsession.Controller and holds no agent logic of its own: the
// view renders a snapshot, applies ordered events, and calls typed commands.
package mygoui

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/getan/golder/gui/appsession"
)

// App is the window's state. Everything here is read and written on MyGo's
// main thread, except the controller event pump, which enters through
// Window.Update.
type App struct {
	ctrl *appsession.Controller
	win  *mygo.Window

	md markdown.Options

	items []appsession.Item
	index map[string]int
	list  ui.ListState

	thinkingOpen map[string]bool
	toolOpen     map[string]bool
	approvalOpen map[string]bool

	draft       string
	attachments []string

	status          appsession.Status
	project         string
	selectedProject string
	projects        []appsession.ProjectFolder
	sessions        []appsession.SessionSummary
	pending         []appsession.ApprovalView
	showArchived    bool
	renameOpen      bool
	renameID        string
	renameDraft     string
	deleteOpen      bool
	deleteID        string
	deleteTitle     string

	banner    string
	bannerErr bool

	modelOpen    bool
	modelQuery   string
	models       []appsession.ModelOption
	modelsErr    string
	modelsReady  bool
	providerOpen bool
	providers    []string

	permOpen bool

	settingsOpen bool
	settingsTab  int
	settings     appsession.ProviderSettings
	settingsEdit appsession.ProviderSettingsEdit
	settingsErr  string
	settingsNote string
	settingsBusy bool
	ttySessions  []appsession.TTYSummary
	display      appsession.DisplaySettings
	displayErr   string
	displayNote  string

	extension              appsession.LarkExtensionSettings
	extensionSources       []appsession.ExtensionSourceView
	extensionEdit          appsession.LarkExtensionEdit
	extensionDomains       map[string]bool
	extensionBusy          bool
	extensionErr           string
	extensionNote          string
	extensionAuthURL       string
	extensionConfirm       string
	extensionSourceConfirm string
	extensionCancel        context.CancelFunc

	paletteOpen  bool
	paletteQuery string
	paletteSel   int
	actions      []appsession.Action

	changesOpen bool
	changesView appsession.ChangeView
	changes     appsession.Changes
	changesErr  string
	fileDiff    appsession.FileDiff
	diffLoading bool
	diffRequest uint64
	diffCancel  context.CancelFunc
	diffSel     int
	diff        *editor.DiffState

	filesOpen       bool
	fileEntries     map[string][]appsession.ProjectFileEntry
	fileExpanded    map[string]bool
	fileLoading     map[string]bool
	fileSelected    string
	filePreview     appsession.ProjectFilePreview
	filePreviewBusy bool
	filesErr        string

	browsers          []appsession.BrowserPreview
	browserOpen       bool
	browserPanelOpen  bool
	browserAutoOpen   bool
	browserAutoSocket string
	browserAutoUntil  time.Time
	browserRetryArmed bool
	browserState      *browserWindowState
	browserCancel     context.CancelFunc
}

// New builds the app around an opened controller.
func New(ctrl *appsession.Controller) *App {
	display, displayErr := appsession.LoadDisplaySettings()
	a := &App{
		ctrl:         ctrl,
		index:        map[string]int{},
		thinkingOpen: map[string]bool{},
		toolOpen:     map[string]bool{},
		approvalOpen: map[string]bool{},
		fileEntries:  map[string][]appsession.ProjectFileEntry{},
		fileExpanded: map[string]bool{},
		fileLoading:  map[string]bool{},
		display:      display,
		md:           markdown.Options{},
	}
	if displayErr != nil {
		a.displayErr = displayErr.Error()
	}
	a.applySnapshot(ctrl.Snapshot())
	return a
}

// Run opens the window and runs the app until the last window closes.
func (a *App) Run() error {
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleWindowMenu},
	}))
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Golder",
			Width:     1180,
			Height:    780,
			MinWidth:  860,
			MinHeight: 560,
			StateKey:  "golder-gui-main",
			Content:   ui.View(a.view),
		})
		a.win.OnClosed(func() {
			if a.diffCancel != nil {
				a.diffCancel()
				a.diffCancel = nil
			}
			a.closeBrowserPanel()
			_ = a.ctrl.Close()
		})
		go a.pump()
		a.refreshProjects()
		a.refreshSessions()
		a.refreshChanges()
		go a.refreshModels()
		go a.refreshBrowsers()
	})
	return mygo.App.Run()
}

// pump delivers controller events onto the main thread.
func (a *App) pump() {
	for ev := range a.ctrl.Events() {
		ev := ev
		win := a.win
		if win == nil {
			continue
		}
		win.Update(func() { a.applyEvent(ev) })
	}
}

func (a *App) applySnapshot(snap appsession.Snapshot) {
	previousProject := a.project
	a.status = snap.Status
	a.project = snap.Project
	if previousProject != "" && previousProject != a.project {
		a.resetFileExplorer()
	}
	if a.selectedProject == "" {
		a.selectedProject = snap.Project
	}
	a.items = append(a.items[:0], snap.Items...)
	a.index = make(map[string]int, len(a.items))
	for i, it := range a.items {
		a.index[it.ID] = i
	}
	a.pending = append(a.pending[:0], snap.Pending...)
}

func (a *App) applyEvent(ev appsession.Event) {
	switch ev.Kind {
	case appsession.EventItemAdded:
		if ev.Item == nil {
			return
		}
		if _, ok := a.index[ev.Item.ID]; ok {
			a.replaceItem(*ev.Item)
			a.followBrowserTool(*ev.Item)
			return
		}
		a.index[ev.Item.ID] = len(a.items)
		a.items = append(a.items, *ev.Item)
		a.followBrowserTool(*ev.Item)
	case appsession.EventItemUpdated:
		if ev.Item != nil {
			a.replaceItem(*ev.Item)
			a.followBrowserTool(*ev.Item)
		}
	case appsession.EventApproval:
		if ev.Approval != nil {
			a.pending = append(a.pending, *ev.Approval)
			a.approvalOpen[ev.Approval.ID] = true
		}
	case appsession.EventApprovalDone:
		a.removeApproval(ev.ID)
	case appsession.EventStatus:
		if ev.Status != nil {
			a.status = *ev.Status
			a.updateTitle()
		}
	case appsession.EventRunEnd:
		a.banner, a.bannerErr = "", false
		if ev.Error != "" {
			a.banner, a.bannerErr = ev.Error, true
		}
		a.refreshSessions()
		a.refreshChanges()
		go a.refreshBrowsers()
	}
}

func (a *App) replaceItem(it appsession.Item) {
	if i, ok := a.index[it.ID]; ok {
		a.items[i] = it
		return
	}
	a.index[it.ID] = len(a.items)
	a.items = append(a.items, it)
}

func (a *App) removeApproval(id string) {
	for i, p := range a.pending {
		if p.ID == id {
			a.pending = append(a.pending[:i], a.pending[i+1:]...)
			delete(a.approvalOpen, id)
			return
		}
	}
}

func (a *App) updateTitle() {
	if a.win == nil {
		return
	}
	name := strings.TrimSpace(a.status.Title)
	if name == "" || name == "New session" {
		name = "Golder"
	}
	if a.status.Running {
		name += " — working…"
	}
	a.win.SetTitle(name)
}

// submit sends one prompt and its operator-selected local attachments.
func (a *App) submit(text string) bool {
	if strings.TrimSpace(text) == "" && len(a.attachments) == 0 {
		return false
	}
	attachments := make([]appsession.UserAttachment, 0, len(a.attachments))
	for _, path := range a.attachments {
		attachments = append(attachments, appsession.UserAttachment{Path: path})
	}
	if err := a.ctrl.SubmitAttachments(context.Background(), text, attachments); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return false
	}
	a.attachments = nil
	a.banner, a.bannerErr = "", false
	return true
}

func (a *App) respond(id string, answer appsession.Answer) {
	if err := a.ctrl.RespondApproval(id, answer); err != nil {
		a.banner, a.bannerErr = err.Error(), true
	}
}

func (a *App) refreshSessions() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions, err := a.ctrl.ListSessions(ctx)
	if err != nil {
		log.Printf("golder-gui: list sessions: %v", err)
		return
	}
	a.sessions = sessions
}

func (a *App) refreshProjects() {
	projects, selected, err := a.ctrl.ListProjects()
	if err != nil {
		log.Printf("golder-gui: list projects: %v", err)
		return
	}
	a.projects = projects
	if selected != "" {
		a.selectedProject = selected
	} else {
		a.selectedProject = ""
	}
}

func (a *App) refreshChanges() {
	if a.diffCancel != nil {
		a.diffCancel()
		a.diffCancel = nil
	}
	a.diffRequest++
	a.diffLoading = false
	a.fileDiff = appsession.FileDiff{}
	a.diff = nil
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	view := a.changesView
	if view != appsession.ChangesStaged {
		view = appsession.ChangesUnstaged
	}
	ch, err := a.ctrl.ChangesFor(ctx, view)
	if err != nil {
		a.changesErr = err.Error()
		return
	}
	a.changesErr = ""
	a.changes = ch
	if a.diffSel >= len(a.changes.Files) {
		a.diffSel = 0
	}
	if a.changesOpen && len(a.changes.Files) > 0 {
		a.selectDiffRow(a.diffSel)
	}
}

func (a *App) selectDiffRow(row int) {
	if row < 0 || row >= len(a.changes.Files) {
		return
	}
	a.diffSel = row
	a.diff = nil
	a.fileDiff = appsession.FileDiff{}
	a.changesErr = ""
	ctrl := a.ctrl
	if ctrl == nil {
		a.diffLoading = false
		return
	}
	a.diffLoading = true
	if a.diffCancel != nil {
		a.diffCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a.diffCancel = cancel
	a.diffRequest++
	request := a.diffRequest
	path := a.changes.Files[row].Path
	view := a.currentChangesView()
	load := func() (appsession.FileDiff, error) { return ctrl.FileDiff(ctx, view, path) }
	win := a.win
	if win == nil {
		result, err := load()
		cancel()
		a.applyFileDiff(request, row, result, err)
		return
	}
	go func() {
		defer cancel()
		result, err := load()
		win.Update(func() { a.applyFileDiff(request, row, result, err) })
	}()
}

func (a *App) applyFileDiff(request uint64, row int, result appsession.FileDiff, loadErr error) {
	if request != a.diffRequest || row < 0 || row >= len(a.changes.Files) ||
		(loadErr == nil && a.changes.Files[row].Path != result.Path) {
		return
	}
	a.diffCancel = nil
	a.diffLoading = false
	if loadErr != nil {
		a.changesErr = loadErr.Error()
		return
	}
	a.fileDiff = result
	old := a.changes.Files[row]
	a.changes.Files[row].Added, a.changes.Files[row].Deleted = result.Added, result.Deleted
	a.changes.Added += result.Added - old.Added
	a.changes.Deleted += result.Deleted - old.Deleted
	if result.Patch == "" {
		return
	}
	files, err := editor.ParsePatch(result.Patch)
	if err != nil {
		a.changesErr = err.Error()
		return
	}
	if len(files) == 0 {
		return
	}
	// The review drawer is narrow: split view wastes half its width for a new
	// or deleted file, and horizontal scrolling obscures long changed lines.
	// Unified, wrapped rendering keeps both sides visible in one column.
	diff := &editor.DiffState{Layout: editor.DiffUnified, Wrap: true}
	diff.SetPatch(files[0])
	a.diff = diff
}

func (a *App) refreshModels() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := a.ctrl.ListModels(ctx)
	win := a.win
	if win == nil {
		return
	}
	win.Update(func() {
		a.modelsReady = true
		if err != nil {
			a.modelsErr = err.Error()
			return
		}
		a.modelsErr = ""
		a.models = models
	})
}

func (a *App) refreshBrowsers() {
	a.refreshBrowsersForPanel(false, "")
}

func (a *App) refreshBrowsersForPanel(open bool, preferredSocket string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	browsers := a.ctrl.Browsers(ctx)
	if a.win == nil {
		a.browsers = browsers
		return
	}
	a.win.Update(func() {
		a.browsers = browsers
		if open {
			a.browserAutoOpen = true
			a.browserAutoUntil = time.Now().Add(8 * time.Second)
			if preferredSocket = browserSocketKey(preferredSocket); preferredSocket != "" {
				a.browserAutoSocket = preferredSocket
			}
		}
		decision, index := browserAutoOpenDecision(a.browserAutoOpen, a.browserPanelOpen, a.browserAutoSocket, browsers)
		switch decision {
		case browserOpenSingle:
			a.clearBrowserAutoOpen()
			a.openBrowserPreview(browsers[index])
		case browserChooseInstance:
			a.clearBrowserAutoOpen()
			a.browserOpen = true
		case browserOpenAlready:
			a.clearBrowserAutoOpen()
		case browserOpenNone:
			if browserAutoOpenShouldRetry(a.browserAutoOpen, a.browserAutoUntil, time.Now()) {
				a.scheduleBrowserAutoOpenRetry()
			} else if a.browserAutoOpen && !a.browserAutoUntil.IsZero() {
				a.clearBrowserAutoOpen()
			}
		}
	})
}

func (a *App) followBrowserTool(item appsession.Item) {
	refresh, open, socket := browserToolPanelRefresh(item)
	if !refresh {
		return
	}
	if open {
		a.browserAutoOpen = true
		a.browserAutoUntil = time.Now().Add(8 * time.Second)
		if socket = browserSocketKey(socket); socket != "" {
			a.browserAutoSocket = socket
		}
	}
	go a.refreshBrowsersForPanel(open, socket)
}

func browserToolPanelRefresh(item appsession.Item) (refresh, open bool, socket string) {
	if item.Tool == nil || item.Tool.Name != "browser" || item.Tool.Running || item.Tool.Error {
		return false, false, ""
	}
	action, _ := item.Tool.Input["action"].(string)
	if action == "open" || action == "attach" {
		socket, _ = item.Tool.Details["socket"].(string)
	}
	switch action {
	case "open", "attach", "navigate", "tab_new", "tab_select":
		open = true
	}
	return true, open, socket
}

func (a *App) clearBrowserAutoOpen() {
	a.browserAutoOpen, a.browserAutoSocket = false, ""
	a.browserAutoUntil = time.Time{}
}

func (a *App) scheduleBrowserAutoOpenRetry() {
	if a.browserRetryArmed || a.win == nil {
		return
	}
	a.browserRetryArmed = true
	win := a.win
	go func() {
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		win.Update(func() {
			a.browserRetryArmed = false
			if browserAutoOpenShouldRetry(a.browserAutoOpen, a.browserAutoUntil, time.Now()) {
				go a.refreshBrowsersForPanel(false, "")
			}
		})
	}()
}

func browserAutoOpenShouldRetry(pending bool, until, now time.Time) bool {
	return pending && !until.IsZero() && now.Before(until)
}

type browserOpenDecision uint8

const (
	browserOpenNone browserOpenDecision = iota
	browserOpenSingle
	browserChooseInstance
	browserOpenAlready
)

func browserAutoOpenDecision(pending, panelOpen bool, preferredSocket string, browsers []appsession.BrowserPreview) (browserOpenDecision, int) {
	if !pending {
		return browserOpenNone, -1
	}
	if panelOpen {
		return browserOpenAlready, -1
	}
	if preferredSocket = browserSocketKey(preferredSocket); preferredSocket != "" {
		for i, browser := range browsers {
			if browserSocketKey(browser.Socket) == preferredSocket {
				return browserOpenSingle, i
			}
		}
		return browserOpenNone, -1
	}
	if len(browsers) == 1 {
		return browserOpenSingle, 0
	}
	if len(browsers) > 1 {
		return browserChooseInstance, -1
	}
	return browserOpenNone, -1
}

func browserSocketKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func (a *App) runAction(id, args string) {
	a.paletteOpen = false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := a.ctrl.RunAction(ctx, id, args)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	if res.Message != "" {
		a.banner, a.bannerErr = res.Message, false
	}
	if id == appsession.ActionNewSession {
		a.items = nil
		a.index = map[string]int{}
		a.status = a.ctrl.Snapshot().Status
		a.refreshSessions()
	}
}

func (a *App) setModel(providerName, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	thinking := a.status.Thinking
	if err := a.ctrl.SelectModel(ctx, providerName, id, thinking); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.banner, a.bannerErr = "", false
	go a.refreshModels()
}

func (a *App) setPermission(mode string) {
	a.permOpen = false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.ctrl.SetPermissionMode(ctx, mode); err != nil {
		a.banner, a.bannerErr = err.Error(), true
	}
}

func (a *App) setThinking(level string) {
	if err := a.ctrl.SetThinking(agentThinkingLevel(level)); err != nil {
		a.banner, a.bannerErr = err.Error(), true
	}
}

func (a *App) resume(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.ctrl.Resume(ctx, id); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.applySnapshot(a.ctrl.Snapshot())
	a.refreshSessions()
	a.refreshChanges()
}

func (a *App) newSession() {
	project := strings.TrimSpace(a.selectedProject)
	if project == "" {
		project = a.project
	}
	a.newSessionInProject(project)
}

func (a *App) newSessionInProject(project string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.ctrl.NewSessionInProject(ctx, project); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.applySnapshot(a.ctrl.Snapshot())
	a.selectedProject = a.project
	a.refreshProjects()
	a.refreshSessions()
	a.refreshChanges()
}

// contextPercent renders the context gauge, 0 when the window is unknown.
func (a *App) contextPercent() int {
	if a.status.ContextLimit <= 0 {
		return 0
	}
	p := a.status.ContextUsed * 100 / a.status.ContextLimit
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func (a *App) notify(format string, args ...any) {
	a.banner = fmt.Sprintf(format, args...)
	a.bannerErr = false
}
