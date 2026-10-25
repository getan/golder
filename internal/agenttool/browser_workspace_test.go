package agenttool

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/getan/golder/internal/tuiosctl"
)

func TestBrowserHelperFollowsAgentWorkspaceNotFocus(t *testing.T) {
	f, socket := startTuiosFake(t)
	var owner, focused atomic.Int32
	owner.Store(1)
	focused.Store(7)
	f.answer = func(verb string, p map[string]any) any {
		switch verb {
		case "list-windows":
			// The user changes focus again after the list response. Creation
			// still has to use the agent pane's explicit workspace.
			current := focused.Swap(focused.Load() + 1)
			return map[string]any{"current_workspace": current, "focused_window_id": "user", "windows": []map[string]any{
				{"window_id": "agent", "workspace": owner.Load()},
				{"window_id": "user", "workspace": current, "focused": true},
			}}
		case "new-window":
			if got := int32(p["workspace"].(float64)); got != owner.Load() {
				t.Errorf("created in workspace %d, want agent workspace %d", got, owner.Load())
			}
			if p["focus"] != false {
				t.Error("helper creation stole focus")
			}
			return map[string]any{"window_id": "browser"}
		}
		return map[string]any{"type": "ok"}
	}
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.Options{Socket: socket, Session: "work", PaneID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, workspace := range []int32{1, 1, 4} {
		owner.Store(workspace) // deliberately moving golder itself is respected
		if _, err := c.NewWindowInCallerWorkspace(context.Background(), tuiosctl.NewWindowParams{Name: "browser", Focus: true, Workspace: 99}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowserHelperNeverFallsBackWhenAgentLocationIsUnknown(t *testing.T) {
	f, socket := startTuiosFake(t)
	c, err := tuiosctl.Dial(context.Background(), tuiosctl.Options{Socket: socket, Session: "work", PaneID: "missing-agent"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.NewWindowInCallerWorkspace(context.Background(), tuiosctl.NewWindowParams{Name: "browser"}); err == nil {
		t.Fatal("unknown agent pane silently used the focused workspace")
	}
	if f.seen("new-window") {
		t.Fatal("created a pane without locating the agent")
	}
}

func TestBrowserOpenReusesLivePaneWithoutConsultingFocus(t *testing.T) {
	root := shortRoot(t)
	withBrowserSocket(t, "")
	path := filepath.Join(root, ".golder", "existing.sock")
	posts := serveDiscoveredBrowser(t, path, "existing")
	tool := NewBrowserTool(root, true)
	trustFakeControl(tool.Control)
	tool.newTuios = func(context.Context) (*tuiosctl.Client, error) {
		t.Error("reusing a live browser must not create a pane or follow focus")
		return nil, errors.New("unexpected pane creation")
	}
	for range 2 {
		res := runBrowserCall(t, tool, `{"action":"open"}`)
		if res.IsError {
			t.Fatalf("existing browser not reused: %+v", res)
		}
	}
	if posts.Load() != 0 || canonicalBrowserSocket(tool.socketPath()) != canonicalBrowserSocket(path) {
		t.Fatal("open changed the existing page or selected a different browser")
	}
}
