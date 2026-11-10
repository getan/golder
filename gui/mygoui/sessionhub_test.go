package mygoui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
)

type guiBlockingProvider struct {
	started chan struct{}
	release chan struct{}
}

func (*guiBlockingProvider) Name() string { return "fake" }
func (*guiBlockingProvider) Models() []provider.Model {
	return []provider.Model{{Provider: "fake", ID: "test"}}
}
func (p *guiBlockingProvider) StreamCompletion(ctx context.Context, _ provider.CompletionRequest) (*provider.AssistantMessageEventStream, error) {
	stream := provider.NewAssistantMessageEventStream(1)
	go func() {
		defer stream.Close()
		close(p.started)
		select {
		case <-ctx.Done():
			return
		case <-p.release:
		}
		_ = stream.Emit(ctx, provider.StreamDoneEvent{Message: agentcore.AssistantMessage{
			RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn,
			Content: agentcore.ContentList{agentcore.NewTextContent("finished")},
		}})
	}()
	return stream, nil
}

func TestSessionSidebarShowsBackgroundStates(t *testing.T) {
	a := testApp()
	now := time.Now()
	a.sessions = []appsession.SessionSummary{
		{ID: "running", Title: "Running task", Cwd: a.project, UpdatedAt: now, Running: true},
		{ID: "approval", Title: "Approval task", Cwd: a.project, UpdatedAt: now, Running: true, NeedsApproval: true},
		{ID: "done", Title: "Finished task", Cwd: a.project, UpdatedAt: now, Unread: true},
		{ID: "failed", Title: "Failed task", Cwd: a.project, UpdatedAt: now, Unread: true, Failed: true},
	}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"◌ Working", "● Needs attention", "● Done", "● Failed"} {
		if !tt.HasText(want) {
			t.Fatalf("sidebar missing %q: %q", want, tt.Texts())
		}
	}
}

func TestSessionEventNeverUpdatesAnotherTranscript(t *testing.T) {
	a := testApp()
	current, other := &appsession.Controller{}, &appsession.Controller{}
	a.ctrl = current
	a.status.SessionID = "current"
	a.snapshotSeq = 5
	foreign := appsession.Item{ID: "foreign", Kind: appsession.ItemAssistant, Text: "other reply"}
	a.applySessionEvent(appsession.SessionEvent{Controller: other, Event: appsession.Event{Session: "other", Seq: 8, Kind: appsession.EventItemAdded, Item: &foreign}})
	stale := appsession.Item{ID: "stale", Kind: appsession.ItemAssistant, Text: "already in snapshot"}
	a.applySessionEvent(appsession.SessionEvent{Controller: current, Event: appsession.Event{Session: "current", Seq: 5, Kind: appsession.EventItemAdded, Item: &stale}})
	if len(a.items) != 0 {
		t.Fatalf("foreign or stale event entered active transcript: %+v", a.items)
	}
	currentItem := appsession.Item{ID: "local", Kind: appsession.ItemAssistant, Text: "current reply"}
	a.applySessionEvent(appsession.SessionEvent{Controller: current, Event: appsession.Event{Session: "current", Seq: 6, Kind: appsession.EventItemAdded, Item: &currentItem}})
	if len(a.items) != 1 || a.items[0].Text != "current reply" {
		t.Fatalf("current session event was lost: %+v", a.items)
	}
}

func TestSwitchingSessionsRestoresDrafts(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	project := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &guiBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	factory := func(_ context.Context, cwd string, setup appsession.ProjectSetup) (run.Env, error) {
		return run.Env{Cwd: cwd, ProviderName: setup.ProviderName, Provider: p, Credentials: provider.NewCredentialStore(nil),
			SysPrompt: "test", Prompt: runtime.PromptInputs{WorkingDir: cwd}, Permissions: permissions.New(permissions.Auto),
			ReviewNotes: run.NewReviewNotes(), Budget: contextbudget.New()}, nil
	}
	first := appsession.New(appsession.Options{Model: "test", ProviderName: "fake", Provider: p, Cwd: project, Store: store, ProjectFactory: factory,
		Tools: []agentcore.AgentTool{&agenttool.TodoTool{Store: agenttool.NewTodoStore()}}})
	if err := first.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := New(first)
	t.Cleanup(func() { _ = a.hub.Close() })
	desktop, _ := newComputerNavigationSession(t)
	if err := first.AttachComputer(first.SessionID(), desktop); err != nil {
		t.Fatal(err)
	}
	a.computerSession, a.computerOpen = desktop, true
	if err := first.Submit(context.Background(), "first prompt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first session did not start")
	}
	a.applySnapshot(first.Snapshot())
	a.draft, a.attachments = "draft one", []appsession.UserAttachment{{Path: "one.txt"}}
	image, err := appsession.NewClipboardImage("Pasted image 1.png", clipboardTestPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	a.attachments = append(a.attachments, image)
	a.historyNav = promptHistoryCursor{index: 1, draft: "original draft", browsing: true}
	firstList := &transcriptWindow{}
	a.list = firstList
	second, err := a.hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	a.activateSession(second)
	if a.draft != "" || len(a.attachments) != 0 || a.list != nil || a.historyNav.browsing || !first.IsRunning() {
		t.Fatal("draft leaked into a new conversation")
	}
	if first.ComputerSession() != desktop || a.computerSession != nil || second.ComputerSession() != nil || !desktop.Status().Active || desktop.Status().Paused {
		t.Fatal("switching sessions paused, revoked, or transferred desktop control")
	}
	a.draft = "draft two"
	a.list = &transcriptWindow{}
	a.resume(first.SessionID())
	if a.ctrl != first || !a.status.Running || a.draft != "draft one" || a.list != firstList || len(a.attachments) != 2 || a.attachments[0].Path != "one.txt" || a.attachments[1].Name != image.Name || len(a.attachments[1].ImagePNG) == 0 {
		t.Fatalf("draft was not restored: %q %+v", a.draft, a.attachments)
	}
	if a.historyNav != (promptHistoryCursor{index: 1, draft: "original draft", browsing: true}) {
		t.Fatalf("history navigation was not restored: %+v", a.historyNav)
	}
	if a.computerSession != desktop || desktop.Status().Paused {
		t.Fatal("returning to the owning conversation did not preserve desktop control")
	}
	close(p.release)
}
