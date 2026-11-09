package mygoui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func browserBindingApp(t *testing.T) (*App, *browserSessionView) {
	t.Helper()
	a, _, _ := sessionNavigationApp(t)
	a.startBrowserSession()
	t.Cleanup(a.resetBrowserSession)
	return a, a.browserSession
}

func TestBindingOpensExactBrowserWithoutTranscript(t *testing.T) {
	a, view := browserBindingApp(t)
	preview := appsession.BrowserPreview{Socket: "/tmp/selected.sock", Browser: "selected", Title: "Selected"}
	other := appsession.BrowserPreview{Socket: "/tmp/other.sock", Browser: "other"}
	view.binding.Select(preview.Endpoint())
	state := view.binding.Snapshot()
	a.applyBrowserSnapshot(view, state, []appsession.BrowserPreview{other})
	if a.browserPanelOpen {
		t.Fatal("opened a different browser while selected view was starting")
	}
	a.applyBrowserSnapshot(view, state, []appsession.BrowserPreview{other, preview})
	if a.browserState == nil || a.browserState.browserID != "selected" {
		t.Fatal("binding did not open its exact surface")
	}
	a.hideBrowserPanel()
	a.applyBrowserSnapshot(view, state, []appsession.BrowserPreview{preview})
	if a.browserPanelOpen {
		t.Fatal("periodic discovery reopened a dismissed drawer")
	}
	view.binding.Present(view.binding.Snapshot())
	a.applyBrowserSnapshot(view, view.binding.Snapshot(), []appsession.BrowserPreview{preview})
	if !a.browserPanelOpen {
		t.Fatal("new foreground operation failed to reveal the browser")
	}
	view.binding.Clear(view.binding.Snapshot())
	a.applyBrowserSnapshot(view, view.binding.Snapshot(), nil)
	if a.browserPanelOpen {
		t.Fatal("closed binding retained the surface")
	}
}

func TestSessionSwitchClearsBrowserAndRejectsLateABAResults(t *testing.T) {
	a, old := browserBindingApp(t)
	first := a.ctrl
	preview := appsession.BrowserPreview{Socket: "/tmp/old.sock", Browser: "old", Title: "Google"}
	old.binding.Select(preview.Endpoint())
	state := old.binding.Snapshot()
	a.applyBrowserSnapshot(old, state, []appsession.BrowserPreview{preview})
	second, err := a.hub.NewSession(context.Background(), a.project)
	if err != nil {
		t.Fatal(err)
	}
	a.activateSession(second)
	if len(a.browsers) != 0 || a.browserPanelOpen || a.browserOpen {
		t.Fatal("new conversation retained old browser state")
	}
	a.applyBrowserSnapshot(old, state, []appsession.BrowserPreview{preview})
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Browser"); err != nil {
		t.Fatal(err)
	}
	if a.browserPanelOpen {
		t.Fatal("Browser click opened a previous conversation")
	}
	a.activateSession(first)
	a.applyBrowserSnapshot(old, state, []appsession.BrowserPreview{preview})
	if len(a.browsers) != 0 || a.browserPanelOpen {
		t.Fatal("late callback from an earlier visit survived A-B-A")
	}
	a.applyBrowserSnapshot(a.browserSession, state, []appsession.BrowserPreview{preview})
	if !a.browserPanelOpen {
		t.Fatal("current subscription could not restore its own browser")
	}
}

func TestStatusAndSnapshotChangeResetBrowser(t *testing.T) {
	for _, kind := range []string{"status", "snapshot"} {
		t.Run(kind, func(t *testing.T) {
			a, view := browserBindingApp(t)
			preview := appsession.BrowserPreview{Socket: "/tmp/old.sock", Browser: "old"}
			view.binding.Select(preview.Endpoint())
			a.applyBrowserSnapshot(view, view.binding.Snapshot(), []appsession.BrowserPreview{preview})
			a.browserOpen = true
			if kind == "status" {
				a.applyEvent(appsession.Event{Kind: appsession.EventStatus, Status: &appsession.Status{SessionID: "new"}})
			} else {
				a.applySnapshot(appsession.Snapshot{Status: appsession.Status{SessionID: "new"}})
			}
			if a.browserPanelOpen || a.browserState != nil || len(a.browsers) != 0 || a.browserOpen || view.ctx.Err() == nil {
				t.Fatal("session identity change retained browser state or subscription")
			}
		})
	}
}

func TestTranscriptCannotSelectOrCloseBrowser(t *testing.T) {
	a, view := browserBindingApp(t)
	preview := appsession.BrowserPreview{Socket: "/tmp/current.sock", Browser: "current"}
	view.binding.Select(preview.Endpoint())
	a.applyBrowserSnapshot(view, view.binding.Snapshot(), []appsession.BrowserPreview{preview})
	before := a.browserState
	for _, action := range []string{"open", "attach", "tab_select", "quit"} {
		item := appsession.Item{ID: action, Tool: &appsession.ToolState{Name: "browser", Input: map[string]any{"action": action}, Details: map[string]any{"socket": "/tmp/foreign.sock", "browser": "foreign"}}}
		a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	}
	if a.browserState != before || !a.browserPanelOpen || view.binding.Snapshot().Endpoint != preview.Endpoint() {
		t.Fatal("transcript drove browser control state")
	}
}

func TestStaleBindingSnapshotDoesNotOverrideNewSelection(t *testing.T) {
	a, view := browserBindingApp(t)
	one := appsession.BrowserPreview{Socket: "/tmp/one.sock", Browser: "one"}
	two := appsession.BrowserPreview{Socket: "/tmp/two.sock", Browser: "two"}
	view.binding.Select(one.Endpoint())
	old := view.binding.Snapshot()
	view.binding.Select(two.Endpoint())
	a.applyBrowserSnapshot(view, old, []appsession.BrowserPreview{one})
	if a.browserPanelOpen || len(a.browsers) != 0 {
		t.Fatal("obsolete discovery was applied")
	}
	a.applyBrowserSnapshot(view, view.binding.Snapshot(), []appsession.BrowserPreview{two})
	if a.browserState == nil || a.browserState.browserID != "two" {
		t.Fatal("new selection was lost")
	}
}
