package mygoui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
)

func sessionNavigationApp(t *testing.T) (*App, *appsession.Controller, string) {
	t.Helper()
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
	first := appsession.New(appsession.Options{Model: "test", ProviderName: "fake", Provider: p, Cwd: project, Store: store, ProjectFactory: factory})
	if err := first.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := New(first)
	t.Cleanup(func() { _ = a.hub.Close() })
	a.projects = []appsession.ProjectFolder{{Name: "project", Path: project}}
	a.selectedProject = project
	return a, first, project
}

func addNavigationTranscript(a *App, id string) {
	item := appsession.Item{ID: id + "-message", Kind: appsession.ItemUser, Text: id + " conversation"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	approval := appsession.ApprovalView{ID: id + "-approval", Tool: "bash", Summary: id + " approval"}
	a.applyEvent(appsession.Event{Kind: appsession.EventApproval, Approval: &approval})
}

func TestSidebarSwitchSessionWithVisibleTranscript(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	if err := first.Submit(context.Background(), "first saved message"); err != nil {
		t.Fatal(err)
	}
	second, err := a.hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	a.activateSession(second)
	addNavigationTranscript(a, "second")
	a.sessions = []appsession.SessionSummary{{ID: first.SessionID(), Title: "First conversation", Cwd: project}}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("second conversation") || !tt.HasText("second approval") {
		t.Fatalf("initial transcript not visible: %q", tt.Texts())
	}
	if err := tt.Click("First conversation"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ctrl != first || len(a.items) == 0 || len(a.pending) != 0 || !tt.HasText("first saved message") || tt.HasText("second conversation") || tt.HasText("second approval") {
		t.Fatalf("switch kept old session state: items=%+v pending=%+v texts=%q", a.items, a.pending, tt.Texts())
	}
	addNavigationTranscript(a, "first")
	a.sessions = []appsession.SessionSummary{{ID: second.SessionID(), Title: "Second conversation", Cwd: project}}
	tt.Frame()
	if err := tt.Click("Second conversation"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ctrl != second || len(a.items) != 0 || len(a.pending) != 0 || tt.HasText("first conversation") || tt.HasText("first approval") {
		t.Fatalf("switch back kept old session state: items=%+v pending=%+v texts=%q", a.items, a.pending, tt.Texts())
	}
}

func TestPaletteNewSessionWithVisibleTranscript(t *testing.T) {
	a, first, _ := sessionNavigationApp(t)
	addNavigationTranscript(a, "old")
	a.actions = []appsession.Action{{ID: appsession.ActionNewSession, Label: "New Session", Category: "Session"}}
	a.paletteOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("New Session"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ctrl == first || len(a.items) != 0 || len(a.pending) != 0 || tt.HasText("old conversation") || tt.HasText("old approval") {
		t.Fatalf("palette new session kept old state: items=%+v pending=%+v texts=%q", a.items, a.pending, tt.Texts())
	}
}

func TestOtherProjectNewSessionWithVisibleTranscript(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	other := t.TempDir()
	a.projects = []appsession.ProjectFolder{{Name: "project", Path: project}, {Name: "other-project", Path: other}}
	addNavigationTranscript(a, "old")
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Create session in other-project"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ctrl == first || !sameProjectPath(a.project, other) || len(a.items) != 0 || len(a.pending) != 0 || tt.HasText("old conversation") {
		t.Fatalf("cross-project new session: project=%q items=%+v pending=%+v texts=%q", a.project, a.items, a.pending, tt.Texts())
	}
}

func TestSelectingOtherProjectKeepsActiveTranscript(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	other := t.TempDir()
	if _, err := first.AddProject(other); err != nil {
		t.Fatal(err)
	}
	a.projects = []appsession.ProjectFolder{{Name: "project", Path: project}, {Name: "other-project", Path: other}}
	addNavigationTranscript(a, "active")
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("other-project"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.bannerErr || a.ctrl != first || !sameProjectPath(a.selectedProject, other) || !sameProjectPath(a.project, project) || !tt.HasText("active conversation") || !tt.HasText("active approval") {
		t.Fatalf("project selection changed active transcript: selected=%q project=%q banner=%q texts=%q", a.selectedProject, a.project, a.banner, tt.Texts())
	}
}

func TestProjectContextMenuNewSessionWithVisibleTranscript(t *testing.T) {
	a, first, _ := sessionNavigationApp(t)
	addNavigationTranscript(a, "old")
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.RightClick("project"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("New session"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ctrl == first || len(a.items) != 0 || len(a.pending) != 0 || tt.HasText("old conversation") {
		t.Fatalf("context menu new session kept old state: items=%+v pending=%+v texts=%q", a.items, a.pending, tt.Texts())
	}
}

func TestDeleteOtherSessionKeepsActiveTranscript(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	second, err := a.hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.hub.Resume(context.Background(), first.SessionID()); err != nil {
		t.Fatal(err)
	}
	addNavigationTranscript(a, "active")
	a.sessions = []appsession.SessionSummary{{ID: second.SessionID(), Title: "Session to delete", Cwd: project}}
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.RightClick("Session to delete"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.bannerErr || a.ctrl != first || !tt.HasText("active conversation") || !tt.HasText("active approval") {
		t.Fatalf("delete changed active transcript: banner=%q texts=%q", a.banner, tt.Texts())
	}
}

func TestArchiveOtherSessionKeepsActiveTranscript(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	second, err := a.hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.hub.Resume(context.Background(), first.SessionID()); err != nil {
		t.Fatal(err)
	}
	addNavigationTranscript(a, "active")
	a.sessions = []appsession.SessionSummary{{ID: second.SessionID(), Title: "Session to archive", Cwd: project}}
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.RightClick("Session to archive"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Archive"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.bannerErr || a.ctrl != first || !tt.HasText("active conversation") || !tt.HasText("active approval") {
		t.Fatalf("archive changed active transcript: banner=%q texts=%q", a.banner, tt.Texts())
	}
}

func TestComposerRemovesSeveralAttachmentsSafely(t *testing.T) {
	a := testApp()
	a.attachments = []appsession.UserAttachment{{Path: "/tmp/one.txt"}, {Path: "/tmp/two.txt"}, {Path: "/tmp/three.txt"}}
	tt := ui.NewTester(a.composer, 900, 450)
	for _, name := range []string{"two.txt", "one.txt", "three.txt"} {
		if err := tt.Click("Remove " + name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if tt.HasText("Remove " + name) {
			t.Fatalf("removed attachment is still displayed: %q", tt.Texts())
		}
	}
	if len(a.attachments) != 0 {
		t.Fatalf("attachments remain after removal: %+v", a.attachments)
	}
}
