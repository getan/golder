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

func TestNewSessionPlusWithExistingTranscript(t *testing.T) {
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
	item := appsession.Item{ID: "old-message", Kind: appsession.ItemUser, Text: "old conversation"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	approval := appsession.ApprovalView{ID: "old-approval", Tool: "bash", Summary: "old approval"}
	a.applyEvent(appsession.Event{Kind: appsession.EventApproval, Approval: &approval})
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("old conversation") || !tt.HasText("old approval") {
		t.Fatalf("old session is not visible: %q", tt.Texts())
	}
	if err := tt.Click("Create session in project"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.ctrl == first || len(a.items) != 0 || len(a.pending) != 0 || tt.HasText("old conversation") || tt.HasText("old approval") {
		t.Fatalf("new conversation was not activated: items=%+v pending=%+v texts=%q", a.items, a.pending, tt.Texts())
	}
}
