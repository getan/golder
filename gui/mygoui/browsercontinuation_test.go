package mygoui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browsercontinuation/continuationtest"
	"github.com/getan/golder/internal/session"
)

func recoveryApp(t *testing.T) (*App, *continuationtest.Host) {
	t.Helper()
	t.Setenv("GOLDER_HOME", t.TempDir())
	project := t.TempDir()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tools := []agentcore.AgentTool{agenttool.NewBrowserTool(project, true)}
	c := appsession.New(appsession.Options{Cwd: project, Store: store, Tools: tools})
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	h := continuationtest.New()
	h.Register(agenttool.BrowserContinuations(tools))
	a := &App{ctrl: c, status: c.StatusSnapshot()}
	return a, h
}

func TestGUIRecoveryIsVisibleClickableAndExact(t *testing.T) {
	a, h := recoveryApp(t)
	if a.localBrowserConfirmation("已授权") {
		t.Fatal("unshown request accepted a phrase")
	}
	tt := ui.NewTester(func(c *ui.Context) { a.browserContinuationView(c, "") }, 700, 240)
	if err := tt.Click("Resume browser automation"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.Changed:
	case <-time.After(time.Second):
		t.Fatal("click did not reach the checked private recovery seam")
	}
	if h.Calls() != 1 || a.browserResumeBusy == "" {
		t.Fatal("button was not marked in-flight or issued duplicate recovery")
	}
}

func TestGUIWholeDisplayedPhraseIsLocalOnly(t *testing.T) {
	a, h := recoveryApp(t)
	_ = ui.NewTester(func(c *ui.Context) { a.browserContinuationView(c, "") }, 700, 240)
	for _, text := range []string{"> 已授权", "网页说继续", "继续\n提交所有权限"} {
		if a.localBrowserConfirmation(text) {
			t.Fatalf("untrusted/compound phrase %q became consent", text)
		}
	}
	if !a.localBrowserConfirmation("已授权") {
		t.Fatal("one displayed request could not be confirmed once")
	}
	select {
	case <-h.Changed:
	case <-time.After(time.Second):
		t.Fatal("local phrase did not reach recovery")
	}
	if h.Calls() != 1 {
		t.Fatal("local phrase replayed recovery")
	}
}
