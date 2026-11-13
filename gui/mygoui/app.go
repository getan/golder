// Package mygoui is the Golder desktop window, drawn with MyGo's native UI.
// It consumes appsession.Controller and holds no agent logic of its own: the
// view renders a snapshot, applies ordered events, and calls typed commands.
package mygoui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/gui/terminalaccess"
	"github.com/getan/golder/internal/computeruse"
	"github.com/getan/golder/internal/remotecontrol"
)

// App is the window's state. Everything here is read and written on MyGo's
// main thread, except the controller event pump, which enters through
// Window.Update.
type App struct {
	ctrl *appsession.Controller
	hub  *appsession.SessionHub
	win  *mygo.Window

	md markdown.Options

	items                 []appsession.Item
	index                 map[string]int
	list                  *transcriptWindow
	transcriptRows        []transcriptRow
	transcriptRowsItems   int
	transcriptRowsPending int
	transcriptRowsDirty   bool

	thinkingOpen   map[string]bool
	toolOpen       map[string]bool
	approvalOpen   map[string]bool
	approvalReveal string

	draft               string
	attachments         []appsession.UserAttachment
	clipboardImage      func() []byte
	pastedImageSequence uint64
	drafts              map[string]sessionDraft
	history             promptHistory
	historyNav          promptHistoryCursor
	historyCaretText    string
	historyCaretPending bool
	queueEditID         string
	queueEditText       string
	snapshotSeq         uint64

	status            appsession.Status
	project           string
	selectedProject   string
	projects          []appsession.ProjectFolder
	sessions          []appsession.SessionSummary
	pending           []appsession.ApprovalView
	showArchived      bool
	sidebarClosed     bool
	sidebarSize       float32
	rightSplit        float32
	rightStackSplit   float32
	rightPreferred    rightPanel
	updateBusy        bool
	updatePhase       string
	updateTag         string
	updateError       string
	updatePackage     guiDownload
	updatePath        string
	updateProgress    int
	updateReceived    int64
	updateDialogOpen  bool
	updateQuitConfirm bool
	updateAfterCheck  bool
	updateCancel      context.CancelFunc
	updateSeq         uint64
	renameOpen        bool
	renameID          string
	renameDraft       string
	deleteOpen        bool
	deleteID          string
	deleteTitle       string

	banner    string
	bannerErr bool

	remoteOpen        bool
	remoteBusy        bool
	remoteErr         string
	remoteServer      *remotecontrol.SecureServer
	remoteCtrl        *appsession.Controller
	remoteQR          *ui.Bitmap
	remotePair        *remotecontrol.PairRequest
	remoteRelease     func()
	remoteStartCancel context.CancelFunc
	remoteStartSeq    uint64
	browserResumeBusy string
	browserResumeNote string
	browserResumeSeq  uint64

	modelOpen         bool
	modelQuery        string
	models            []appsession.ModelOption
	modelsErr         string
	modelsReady       bool
	modelRequest      uint64
	providerSelection string
	providers         []string

	permOpen bool

	settingsOpen   bool
	settingsTab    int
	settings       appsession.ProviderSettings
	settingsEdit   appsession.ProviderSettingsEdit
	settingsModels []string
	settingsErr    string
	settingsNote   string
	settingsBusy   bool
	proxySettings  appsession.ProxySettings
	proxyErr       string
	proxyNote      string
	ttySessions    []appsession.TTYSummary
	display        appsession.DisplaySettings
	displayErr     string
	displayNote    string

	extension                  appsession.LarkExtensionSettings
	extensionSources           []appsession.ExtensionSourceView
	extensionEdit              appsession.LarkExtensionEdit
	extensionDomains           map[string]bool
	extensionExtendedOpen      bool
	extensionBusy              bool
	extensionCLIInstall        *officialCLIInstallState
	extensionErr               string
	extensionNote              string
	extensionAuthURL           string
	extensionAuthCode          string
	extensionAuthQR            *ui.Bitmap
	extensionLarkVerified      bool
	extensionProjectVerified   bool
	extensionLarkStatus        string
	extensionLarkMissingScopes []string
	extensionProjectStatus     string
	extensionStatusStarted     bool
	extensionStatusCancel      context.CancelFunc
	extensionStatusSeq         uint64
	extensionConfirm           string
	extensionSourceConfirm     string
	extensionCancel            context.CancelFunc
	mcpAdd                     *mcpAddForm // transient operator-only input
	mcpOAuth                   *guiMCPAuthorization
	mcpRemove                  *mcpRemoveState
	mcpApproval                *mcpRemoveState
	mcpToolsID                 string

	paletteOpen  bool
	paletteQuery string
	paletteSel   int
	actions      []appsession.Action

	terminalOpen         bool
	terminalSeq          uint64
	terminals            map[terminalScope]*terminalProject
	terminalAccess       *terminalaccess.Registry
	terminalAgentSeq     uint64
	terminalAgents       map[*appsession.Controller]*terminalAgent
	terminalGrantPane    *terminalPane
	terminalGrantAgent   *terminalAgent
	terminalGrantRead    bool
	terminalGrantOpen    bool
	terminalClosePane    *terminalPane
	terminalCloseOpen    bool
	terminalRenamePane   *terminalPane
	terminalRenameDraft  string
	terminalRenameOpen   bool
	terminalExitOpen     bool
	terminalExitApproved bool
	terminalShuttingDown bool
	terminalShutdownDone bool
	fileDialog           fileDialogState

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
	gitStates   map[string]*gitPanelState

	filesOpen        bool
	fileEntries      map[string][]appsession.ProjectFileEntry
	fileLoading      map[string]bool
	fileDirectory    string
	fileRoot         string
	fileStates       map[string]fileExplorerSnapshot
	fileShowHidden   bool
	fileGeneration   uint64
	fileSelected     string
	filePreview      appsession.ProjectFilePreview
	filePretty       bool
	filePreviewBusy  bool
	fileEditor       *editor.State
	fileSavedVersion uint64
	fileSaving       bool
	filesErr         string

	browsers         []appsession.BrowserPreview
	browserOpen      bool
	browserPanelOpen bool
	browserSession   *browserSessionView
	browserState     *browserWindowState
	browserCancel    context.CancelFunc

	computerOpen             bool
	computerPickerOpen       bool
	computerBusy             bool
	computerError            string
	computerPermission       computeruse.PermissionState
	computerPermissionNote   string
	computerBackend          computeruse.PlatformBackend
	computerSession          *computeruse.Session
	computerTargets          []computeruse.Target
	computerApplications     []computeruse.Application
	computerSelected         int
	computerMatched          bool
	computerSuggested        string
	computerRequestPicker    bool
	computerFrame            *ui.Bitmap
	computerFrameInfo        computeruse.Frame // metadata only; PNG lives in the local preview
	computerTextDraft        string
	computerInputBusy        bool
	computerInputStop        context.CancelFunc
	computerInputSeq         uint64
	computerPreviewSeq       uint64
	computerCandidate        *ui.Bitmap
	computerCandidateID      string
	computerCandidateTarget  string
	computerCandidateSession string
	computerCandidateControl bool
	computerPreviewFailed    bool
	computerSkipPreview      bool
	computerPreviewStop      context.CancelFunc
	computerSetupStop        context.CancelFunc
	computerSetupSeq         uint64
}

type sessionDraft struct {
	text        string
	attachments []appsession.UserAttachment
	list        *transcriptWindow
	historyNav  promptHistoryCursor
	tail        appsession.Item
}

// New builds the app around an opened controller.
func New(ctrl *appsession.Controller) *App {
	display, displayErr := appsession.LoadDisplaySettings()
	a := &App{
		ctrl:         ctrl,
		hub:          appsession.NewSessionHub(ctrl),
		drafts:       make(map[string]sessionDraft),
		index:        map[string]int{},
		thinkingOpen: map[string]bool{},
		toolOpen:     map[string]bool{},
		approvalOpen: map[string]bool{},
		fileEntries:  map[string][]appsession.ProjectFileEntry{},
		fileLoading:  map[string]bool{},
		display:      display,
		sidebarSize:  sidebarWidth,
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
	mygo.App.SetMenu(mygo.NewMenu(desktopMenuItems(runtime.GOOS, func() {
		if a.win != nil {
			a.win.Close()
		}
	})))
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
		a.win.OnClose(func(event *mygo.CloseEvent) {
			if a.deferCloseForFileDialog() {
				event.PreventDefault()
				return
			}
			if !a.terminalExitApproved && a.hasRunningTerminals() {
				event.PreventDefault()
				a.terminalExitOpen = true
				a.win.Invalidate()
				return
			}
			if !a.terminalShutdownDone {
				event.PreventDefault()
				a.cancelGUIUpdate()
				a.beginTerminalShutdown()
			}
		})
		a.win.OnClosed(func() {
			a.stopRemoteControl()
			authBusy := a.mcpOAuth != nil && a.mcpOAuth.cancel != nil || a.extensionCancel != nil
			if a.mcpOAuth != nil && a.mcpOAuth.cancel != nil {
				a.mcpOAuth.cancel()
			}
			if a.extensionCancel != nil {
				a.extensionCancel()
			}
			a.cancelOfficialCLIInstall()
			if a.diffCancel != nil {
				a.diffCancel()
				a.diffCancel = nil
			}
			a.resetBrowserSession()
			a.stopComputer()
			a.closeTerminals()
			if authBusy {
				// Authorization teardown may need the UI thread to destroy its
				// private web view. Do not block that thread waiting for the hub.
				go func() { _ = a.hub.Close() }()
			} else {
				_ = a.hub.Close()
			}
		})
		a.registerTerminalAgent(a.ctrl)
		go a.pump()
		a.refreshProjects()
		a.refreshSessions()
		a.refreshChanges()
		a.startModelRefresh()
		a.startBrowserSession()
		a.checkGUIUpdate(false)
	})
	return mygo.App.Run()
}

// pump delivers events from all live session controllers onto the main thread.
func (a *App) pump() {
	for {
		select {
		case <-a.hub.Done():
			return
		case ev := <-a.hub.Events():
			win := a.win
			if win != nil {
				win.Update(func() { a.applySessionEvent(ev) })
			}
		}
	}
}

func (a *App) applySessionEvent(message appsession.SessionEvent) {
	ev := message.Event
	if message.Controller != a.ctrl || ev.Session != a.status.SessionID {
		if ev.Kind == appsession.EventStatus || ev.Kind == appsession.EventApproval || ev.Kind == appsession.EventApprovalDone || ev.Kind == appsession.EventRunEnd || ev.Kind == appsession.EventComputerRequest {
			a.refreshSessions()
		}
		return
	}
	if ev.Seq != 0 && ev.Seq <= a.snapshotSeq {
		if ev.Kind == appsession.EventRunEnd {
			if request := a.hub.TakeComputerRequest(message.Controller); request != "" {
				a.computerSuggested = request
				a.applyEvent(appsession.Event{Kind: appsession.EventRunEnd})
			}
		}
		return
	}
	if ev.Kind == appsession.EventRunEnd && a.computerSuggested == "" {
		a.computerSuggested = a.hub.TakeComputerRequest(message.Controller)
	}
	a.applyEvent(ev)
	if ev.Kind == appsession.EventRunEnd {
		a.hub.ClearComputerRequest(message.Controller)
	}
	if ev.Kind == appsession.EventStatus || ev.Kind == appsession.EventApproval || ev.Kind == appsession.EventApprovalDone {
		a.refreshSessions()
	}
}

func (a *App) applySnapshot(snap appsession.Snapshot) {
	previousProject := a.project
	previousSession := a.status.SessionID
	var tail appsession.Item
	if previousSession != "" && previousSession == snap.SessionID {
		tail = transcriptTail(a.items)
	}
	fileScopeChanged := previousSession != snap.Status.SessionID || previousProject != snap.Project
	if fileScopeChanged && previousSession != "" {
		a.rememberFileExplorer(previousSession, previousProject)
		a.rememberGitView(previousSession)
	}
	a.status = snap.Status
	a.project = snap.Project
	a.snapshotSeq = snap.Seq
	if previousSession != "" && previousSession != snap.SessionID {
		a.resetBrowserSession()
		a.resetTerminalDialogs()
	}
	if fileScopeChanged {
		a.restoreFileExplorer(a.status.SessionID, a.project)
		a.restoreGitView(a.status.SessionID)
	}
	if a.selectedProject == "" {
		a.selectedProject = snap.Project
	}
	a.items = append(a.items[:0], snap.Items...)
	a.index = make(map[string]int, len(a.items))
	for i, it := range a.items {
		a.index[it.ID] = i
	}
	a.list.noteRestoredItems(tail, a.items, a.index)
	a.pending = append(a.pending[:0], snap.Pending...)
	a.transcriptRowsDirty = true
	if len(a.pending) > 0 {
		a.approvalReveal = a.pending[len(a.pending)-1].ID
	} else {
		a.approvalReveal = ""
	}
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
		a.list.noteAddedItem(*ev.Item)
		a.index[ev.Item.ID] = len(a.items)
		a.items = append(a.items, *ev.Item)
	case appsession.EventItemUpdated:
		if ev.Item != nil {
			a.replaceItem(*ev.Item)
		}
	case appsession.EventApproval:
		if ev.Approval != nil {
			a.transcriptRowsDirty = true
			for i := range a.pending {
				if a.pending[i].ID == ev.Approval.ID {
					a.pending[i] = *ev.Approval
					return
				}
			}
			a.pending = append(a.pending, *ev.Approval)
			a.approvalOpen[ev.Approval.ID] = true
			a.approvalReveal = ev.Approval.ID
			if key := toolPresentationKey(ev.Approval.RunID, ev.Approval.ToolCallID, ev.Approval.Origin); key != "" {
				a.toolOpen[key] = true
			}
		}
	case appsession.EventApprovalDone:
		a.removeApproval(ev.ID)
	case appsession.EventComputerRequest:
		if ev.Session == a.status.SessionID {
			a.computerSuggested = ev.ID
		}
	case appsession.EventStatus:
		if ev.Status != nil {
			if a.status.SessionID != "" && a.status.SessionID != ev.Status.SessionID {
				a.stopComputer()
				a.resetBrowserSession()
				a.resetTerminalDialogs()
			}
			a.status = *ev.Status
			if a.browserSession == nil {
				a.startBrowserSession()
			}
			a.updateTitle()
		}
	case appsession.EventRunEnd:
		a.banner, a.bannerErr = "", false
		if ev.Error != "" {
			a.banner, a.bannerErr = ev.Error, true
			a.computerSuggested = ""
			if a.ctrl != nil {
				a.ctrl.ResetComputerRequest()
				a.resumeQueued()
			}
		} else if a.computerSuggested != "" {
			if a.computerSession != nil && a.computerSession.Status().Active {
				a.banner = "Computer Use is already active; stop it before selecting another window."
				a.computerSuggested = ""
				if a.ctrl != nil {
					a.ctrl.ResetComputerRequest()
					a.resumeQueued()
				}
			} else {
				a.computerRequestPicker = true
				a.computerPickerOpen = true
				a.loadComputerTargets()
			}
		}
		a.refreshSessions()
		a.refreshChanges()
	}
}

func (a *App) replaceItem(it appsession.Item) {
	if i, ok := a.index[it.ID]; ok {
		previous := a.items[i]
		a.list.noteUpdatedItem(previous, it)
		a.noteExpandedTranscriptUpdate(previous, it)
		if previous.Kind != it.Kind || it.Kind == appsession.ItemNote || (previous.Tool == nil) != (it.Tool == nil) || previous.Tool != nil && it.Tool != nil && (previous.Tool.ID != it.Tool.ID || previous.Tool.Name != it.Tool.Name) {
			a.transcriptRowsDirty = true
		}
		if a.list != nil {
			a.list.remeasure = true
		}
		a.items[i] = it
		return
	}
	a.list.noteAddedItem(it)
	a.index[it.ID] = len(a.items)
	a.items = append(a.items, it)
}

func (a *App) removeApproval(id string) {
	for i, p := range a.pending {
		if p.ID == id {
			a.pending = append(a.pending[:i], a.pending[i+1:]...)
			a.transcriptRowsDirty = true
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
	if a.localBrowserConfirmation(strings.TrimSpace(text)) {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		if len(a.attachments) != 0 {
			a.banner, a.bannerErr = "Remove attachments before running a slash command.", true
			return false
		}
		if a.ctrl.IsRunning() {
			a.banner, a.bannerErr = "Stop the current run before running a slash command.", true
			return false
		}
		line := strings.TrimSpace(text)
		name, args := line, ""
		if i := strings.IndexAny(line, " \t\n"); i >= 0 {
			name, args = line[:i], strings.TrimSpace(line[i+1:])
		}
		result, err := a.ctrl.RunAction(context.Background(), name, args)
		if err != nil {
			a.banner, a.bannerErr = err.Error(), true
			return false
		}
		a.history.record(&a.historyNav, text, a.status.SessionID, a.project)
		a.banner, a.bannerErr = result.Message, false
		return true
	}
	if err := a.ctrl.SubmitAttachments(context.Background(), text, a.attachments); err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return false
	}
	if a.list != nil {
		a.list.jumpToBottom()
	}
	a.history.record(&a.historyNav, text, a.status.SessionID, a.project)
	a.attachments = nil
	a.banner, a.bannerErr = "", false
	return true
}

func (a *App) resumeQueued() {
	if a.ctrl == nil {
		return
	}
	if len(a.ctrl.StatusSnapshot().QueuedMessages) == 0 {
		return
	}
	if err := a.ctrl.StartQueued(context.Background()); err != nil && !errors.Is(err, appsession.ErrBusy) {
		a.banner, a.bannerErr = err.Error(), true
	}
}

func (a *App) respond(id string, answer appsession.Answer) {
	if err := a.ctrl.RespondApproval(id, answer); err != nil {
		a.banner, a.bannerErr = err.Error(), true
	}
}

func (a *App) refreshSessions() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sessions []appsession.SessionSummary
	var err error
	if a.hub != nil {
		sessions, err = a.hub.ListSessions(ctx)
	} else if a.ctrl != nil {
		sessions, err = a.ctrl.ListSessions(ctx)
	} else {
		return
	}
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

func (a *App) startModelRefresh() {
	ctrl, win := a.ctrl, a.win
	if ctrl == nil {
		return
	}
	a.modelRequest++
	a.models, a.modelsErr, a.modelsReady = nil, "", false
	if win == nil {
		return
	}
	request, providerName := a.modelRequest, ctrl.StatusSnapshot().Provider
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		models, err := ctrl.ListModels(ctx)
		win.Update(func() { a.applyModelResults(ctrl, request, providerName, models, err) })
	}()
}

func (a *App) applyModelResults(ctrl *appsession.Controller, request uint64, providerName string, models []appsession.ModelOption, err error) {
	if a.ctrl != ctrl || a.modelRequest != request || ctrl.StatusSnapshot().Provider != providerName {
		return
	}
	a.modelsReady = true
	if err != nil {
		a.modelsErr = err.Error()
		return
	}
	a.modelsErr = ""
	a.models = models
}

func (a *App) runAction(id, args string) {
	a.paletteOpen = false
	if id == appsession.ActionNewSession {
		a.newSession()
		return
	}
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
	a.status = a.ctrl.StatusSnapshot()
	a.startModelRefresh()
}

func (a *App) setProvider(name string) {
	if a.ctrl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.ctrl.SelectProvider(ctx, name); err != nil {
		a.providerSelection = a.status.Provider
		a.modelsErr = err.Error()
		return
	}
	a.status = a.ctrl.StatusSnapshot()
	a.providerSelection = a.status.Provider
	a.modelQuery = ""
	a.banner, a.bannerErr = "", false
	a.startModelRefresh()
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
	if a.fileSaving {
		a.banner, a.bannerErr = "Wait for the file save to finish before switching sessions.", true
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	peer, err := a.hub.Resume(ctx, id)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.activateSession(peer)
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
	if a.fileSaving {
		a.banner, a.bannerErr = "Wait for the file save to finish before switching sessions.", true
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	peer, err := a.hub.NewSession(ctx, project)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.activateSession(peer)
	a.selectedProject = a.project
	a.refreshProjects()
	a.refreshSessions()
	a.refreshChanges()
}

func (a *App) activateSession(peer *appsession.Controller) {
	if peer == nil || peer == a.ctrl {
		return
	}
	if a.drafts == nil {
		a.drafts = make(map[string]sessionDraft)
	}
	if a.status.SessionID != "" {
		draft := sessionDraft{text: a.draft, attachments: append([]appsession.UserAttachment(nil), a.attachments...), list: a.list, historyNav: a.historyNav}
		draft.tail = transcriptTail(a.items)
		a.drafts[a.status.SessionID] = draft
	}
	if a.computerRequestPicker && a.computerSuggested != "" {
		a.hub.ParkComputerRequest(a.ctrl, a.computerSuggested)
	}
	a.parkComputerPanel()
	a.resetBrowserSession()
	a.ctrl = peer
	a.registerTerminalAgent(peer)
	a.thinkingOpen, a.toolOpen, a.approvalOpen = map[string]bool{}, map[string]bool{}, map[string]bool{}
	a.modelRequest++
	a.models, a.modelsReady, a.modelsErr = nil, false, ""
	a.applySnapshot(peer.Snapshot())
	draft := a.drafts[a.status.SessionID]
	delete(a.drafts, a.status.SessionID)
	a.draft, a.attachments = draft.text, draft.attachments
	a.historyNav = draft.historyNav
	a.historyCaretPending = false
	a.queueEditID, a.queueEditText = "", ""
	a.list = draft.list
	a.list.noteRestoredItems(draft.tail, a.items, a.index)
	a.computerSession = peer.ComputerSession()
	a.selectedProject = a.project
	_ = peer.SelectProject(a.project)
	a.banner, a.bannerErr = "", false
	a.updateTitle()
	a.startBrowserSession()
	if request := a.hub.TakeComputerRequest(peer); request != "" {
		a.computerSuggested = request
		a.applyEvent(appsession.Event{Kind: appsession.EventRunEnd})
	}
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
