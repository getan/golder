// Package mygoui is the Golder desktop window, drawn with MyGo's native UI.
// It consumes appsession.Controller and holds no agent logic of its own: the
// view renders a snapshot, applies ordered events, and calls typed commands.
package mygoui

import (
	"context"
	"fmt"
	"log"
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

	draft string

	status   appsession.Status
	project  string
	sessions []appsession.SessionSummary
	pending  []appsession.ApprovalView

	banner    string
	bannerErr bool

	modelOpen    bool
	modelQuery   string
	models       []appsession.ModelOption
	modelsErr    string
	modelsReady  bool
	providerOpen bool
	providers    []string

	permOpen  bool
	thinkOpen bool

	paletteOpen  bool
	paletteQuery string
	paletteSel   int
	actions      []appsession.Action

	changesOpen bool
	changes     appsession.Changes
	changesErr  string
	diffFiles   []editor.FilePatch
	diffSel     int
	diff        *editor.DiffState

	workspaces  []appsession.WorkspaceInfo
	wsOpen      bool
	browsers    []appsession.BrowserPreview
	browserOpen bool
}

// New builds the app around an opened controller.
func New(ctrl *appsession.Controller) *App {
	a := &App{
		ctrl:         ctrl,
		index:        map[string]int{},
		thinkingOpen: map[string]bool{},
		toolOpen:     map[string]bool{},
		md:           markdown.Options{},
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
			_ = a.ctrl.Close()
		})
		go a.pump()
		a.refreshSessions()
		a.refreshChanges()
		go a.refreshModels()
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
	a.status = snap.Status
	a.project = snap.Project
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
			return
		}
		a.index[ev.Item.ID] = len(a.items)
		a.items = append(a.items, *ev.Item)
	case appsession.EventItemUpdated:
		if ev.Item != nil {
			a.replaceItem(*ev.Item)
		}
	case appsession.EventApproval:
		if ev.Approval != nil {
			a.pending = append(a.pending, *ev.Approval)
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

// submit sends one prompt and clears the draft.
func (a *App) submit(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if err := a.ctrl.Submit(context.Background(), text); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.banner, a.bannerErr = "", false
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

func (a *App) refreshChanges() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ch, err := a.ctrl.Changes(ctx)
	if err != nil {
		a.changesErr = err.Error()
		return
	}
	a.changesErr = ""
	changed := len(ch.Files) != len(a.changes.Files) || ch.Added != a.changes.Added || ch.Deleted != a.changes.Deleted
	a.changes = ch
	if changed {
		a.rebuildDiff()
	}
}

func (a *App) rebuildDiff() {
	a.diffFiles = nil
	a.diff = nil
	if a.changes.Patch == "" {
		return
	}
	files, err := editor.ParsePatch(a.changes.Patch)
	if err != nil {
		a.changesErr = err.Error()
		return
	}
	a.diffFiles = files
	if a.diffSel >= len(files) {
		a.diffSel = 0
	}
	a.selectDiff(a.diffSel)
}

func (a *App) selectDiff(i int) {
	if i < 0 || i >= len(a.diffFiles) {
		return
	}
	f := a.diffFiles[i]
	d := &editor.DiffState{}
	d.SetPatch(f)
	a.diff = d
	a.diffSel = i
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

func (a *App) refreshWorkspaces() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, err := a.ctrl.Workspaces(ctx)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.workspaces = ws
	win := a.win
	if win != nil {
		win.Update(func() {})
	}
}

func (a *App) refreshBrowsers() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.browsers = a.ctrl.Browsers(ctx)
	if a.win != nil {
		a.win.Update(func() {})
	}
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
	a.modelOpen = false
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
	a.thinkOpen = false
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.ctrl.NewSession(ctx); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.items = nil
	a.index = map[string]int{}
	a.status = a.ctrl.Snapshot().Status
	a.refreshSessions()
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
