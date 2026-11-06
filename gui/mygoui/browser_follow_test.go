package mygoui

import (
	"context"
	"testing"

	"github.com/getan/golder/gui/appsession"
)

func selectedBrowserItem(id, socket, browser, tab, target string) appsession.Item {
	return appsession.Item{ID: id, Tool: &appsession.ToolState{
		Name:  "browser",
		Input: map[string]any{"action": "tab_select", "tab": tab},
		Details: map[string]any{
			"action": "tab.activate", "socket": socket, "browser": browser,
			"tab": tab, "target_id": target, "selection_revision": "7",
		},
	}}
}

func TestBrowserToolSelectionRequiresSuccessfulExplicitIdentity(t *testing.T) {
	item := selectedBrowserItem("selection", "/tmp/browser.sock", "browser", "tab", "target")
	selection, socket, ok := browserToolSelection(item)
	if !ok || socket != "/tmp/browser.sock" || selection.BrowserID != "browser" || selection.TabID != "tab" || selection.TargetID != "target" || selection.Revision != 7 {
		t.Fatalf("selection = %+v, %q, %v", selection, socket, ok)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*appsession.ToolState)
	}{
		{"background snapshot", func(s *appsession.ToolState) { s.Input["action"] = "snapshot" }},
		{"failed selection", func(s *appsession.ToolState) { s.Error = true }},
		{"running selection", func(s *appsession.ToolState) { s.Running = true }},
		{"missing browser", func(s *appsession.ToolState) { delete(s.Details, "browser") }},
		{"missing target", func(s *appsession.ToolState) { delete(s.Details, "target_id") }},
		{"invalid revision", func(s *appsession.ToolState) { s.Details["selection_revision"] = "invalid" }},
		{"malformed action", func(s *appsession.ToolState) { s.Input["action"] = []string{"tab_select"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := selectedBrowserItem("selection", "/tmp/browser.sock", "browser", "tab", "target")
			tc.mutate(item.Tool)
			if _, _, ok := browserToolSelection(item); ok {
				t.Fatal("accepted an unconfirmed or incomplete selection")
			}
		})
	}
}

func TestCompletedTabSelectionQueuesMatchingSurfaceOnce(t *testing.T) {
	a := testApp()
	a.browserPanelOpen = true
	a.browserState = &browserWindowState{socket: browserSocketKey("/tmp/browser.sock"), browserID: "browser"}
	item := selectedBrowserItem("selection-1", "/tmp/browser.sock", "browser", "a", "ta")
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	if pending := a.browserState.pendingFollow; pending == nil || pending.TabID != "a" || pending.TargetID != "ta" {
		t.Fatalf("matching follow was not queued: %+v", pending)
	}
	a.browserState.pendingFollow = nil // simulates a later manual choice
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	if a.browserState.pendingFollow != nil {
		t.Fatal("a repeated transcript event replayed an old selection")
	}
}

func TestTabSelectionWaitsForExactBrowserDiscovery(t *testing.T) {
	a := testApp()
	item := selectedBrowserItem("selection-2", "/tmp/selected.sock", "selected-browser", "a", "ta")
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	if !a.browserAutoOpen || a.browserAutoSelection == nil {
		t.Fatal("selection did not wait for browser discovery")
	}
	a.applyBrowserDiscovery([]appsession.BrowserPreview{{Socket: "/tmp/other.sock", Browser: "other"}})
	if a.browserPanelOpen {
		t.Fatal("opened a different browser while waiting")
	}
	a.applyBrowserDiscovery([]appsession.BrowserPreview{{Socket: "/tmp/selected.sock", Browser: "selected-browser"}})
	if !a.browserPanelOpen || a.browserState == nil || a.browserState.pendingFollow == nil || a.browserState.pendingFollow.TabID != "a" {
		t.Fatalf("exact browser did not receive selection: %+v", a.browserState)
	}
}

func TestFollowRejectsAnotherBrowserAndManualChoiceClearsPending(t *testing.T) {
	state := &browserWindowState{browserID: "browser"}
	queueBrowserFollow(state, appsession.BrowserSelection{BrowserID: "other", TabID: "a", TargetID: "ta"})
	if state.pendingFollow != nil {
		t.Fatal("queued a selection for another browser")
	}
	queueBrowserFollow(state, appsession.BrowserSelection{BrowserID: "browser", TabID: "a", TargetID: "ta"})
	if state.pendingFollow == nil {
		t.Fatal("matching selection was not queued")
	}
	runBrowserSelection(state, nil)
	if state.pendingFollow != nil {
		t.Fatal("manual choice did not cancel the earlier pending follow")
	}
}

func TestManualChoiceQueuesBehindInFlightFollow(t *testing.T) {
	state := &browserWindowState{browserID: "browser"}
	state.switching.Store(true)
	state.pendingFollow = &appsession.BrowserSelection{BrowserID: "browser", TabID: "a", TargetID: "ta"}
	runBrowserSelection(state, func(context.Context) error { return nil })
	if state.pendingFollow != nil || state.pendingManual == nil {
		t.Fatal("manual selection was lost or failed to supersede the earlier follow")
	}
}

func TestClosingPanelCancelsPendingFollow(t *testing.T) {
	a := testApp()
	state := &browserWindowState{browserID: "browser", pendingFollow: &appsession.BrowserSelection{BrowserID: "browser", TabID: "a", TargetID: "ta"}}
	a.browserState, a.browserPanelOpen, a.browserAutoOpen = state, true, true
	a.closeBrowserPanel()
	if a.browserPanelOpen || a.browserAutoOpen || state.pendingFollow != nil || state.viewSequence.Load() == 0 {
		t.Fatal("closing the panel left a pending selection or callback")
	}
}

func TestSessionChangeCancelsPendingFollow(t *testing.T) {
	a := testApp()
	a.status.SessionID = "old"
	state := &browserWindowState{browserID: "browser", pendingFollow: &appsession.BrowserSelection{BrowserID: "browser", TabID: "a", TargetID: "ta"}}
	state.pendingManual = func(context.Context) error { return nil }
	a.browserState, a.browserAutoOpen = state, true
	a.browserAutoSelection = state.pendingFollow
	a.browserFollowed = map[string]bool{"selection": true}
	a.applyEvent(appsession.Event{Kind: appsession.EventStatus, Status: &appsession.Status{SessionID: "new"}})
	if a.browserAutoOpen || a.browserAutoSelection != nil || len(a.browserFollowed) != 0 || state.pendingFollow != nil || state.pendingManual != nil {
		t.Fatal("session change retained a previous session's pending selections")
	}
}
