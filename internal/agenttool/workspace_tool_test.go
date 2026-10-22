package agenttool

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// The full-stack preset arranges a backend, a frontend and a browser pane, and
// the browser carries its own control socket under the workspace.
func TestWorkspacePresetFullStack(t *testing.T) {
	if _, err := exec.LookPath("kitty-browse"); err != nil {
		t.Skip("kitty-browse is not installed")
	}
	root := t.TempDir()
	tool := NewWorkspaceTool(root, true)
	specs, err := tool.presetPlan(workspaceArgs{Preset: PresetFullStack, URL: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("panes = %d (%+v), want backend, frontend, browser", len(specs), specs)
	}
	if specs[0].Name != "backend" || specs[1].Name != "frontend" || specs[2].Kind != "browser" {
		t.Errorf("plan = %+v, want backend, frontend, browser", specs)
	}
	if !strings.Contains(specs[2].Socket, ".golder/browser/") || specs[2].URL != "http://localhost:5173" {
		t.Errorf("browser pane = %+v, want a workspace socket and the url", specs[2])
	}
}

// A preset that does not exist is a clear error, not an empty plan.
func TestWorkspacePresetUnknown(t *testing.T) {
	tool := NewWorkspaceTool(t.TempDir(), true)
	if _, err := tool.presetPlan(workspaceArgs{Preset: "nope"}); err == nil {
		t.Fatal("an unknown preset must fail")
	}
	if _, err := tool.presetPlan(workspaceArgs{}); err == nil {
		t.Fatal("create without a preset must fail")
	}
}

// A snapshot merges a terminal tail and a browser state into one result, and
// names each pane.
func TestWorkspaceSnapshotMergesPanes(t *testing.T) {
	tool := NewWorkspaceTool(t.TempDir(), true)
	tool.panes["backend"] = workspacePane{Name: "backend", Kind: "terminal", WindowID: "w-back"}
	tool.panes["browser"] = workspacePane{Name: "browser", Kind: "browser", WindowID: "w-browse", Socket: "/tmp/x.sock"}
	tool.capture = func(context.Context, string, int) (string, error) { return "backend log line", nil }
	tool.browserSnapshot = func(context.Context, string) (map[string]any, error) {
		return map[string]any{"url": "http://localhost:3000", "title": "App"}, nil
	}
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"snapshot","lines":20}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("snapshot failed: %s", agentcore.ContentToText(res.Content))
	}
	text := agentcore.ContentToText(res.Content)
	for _, want := range []string{"backend log line", "http://localhost:3000", "w-browse"} {
		if !strings.Contains(text, want) {
			t.Errorf("snapshot = %s, want %q", text, want)
		}
	}
}

// A sub-agent may read the arranged panes and nothing else.
func TestWorkspaceSubAgentCannotCreateOrClose(t *testing.T) {
	tool := NewWorkspaceTool(t.TempDir(), false)
	if strings.Contains(string(tool.Schema()), `"create"`) {
		t.Error("the read schema must not advertise create")
	}
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"create","preset":"review"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a sub-agent create must be refused")
	}
	res, err = tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"close","name":"backend"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a sub-agent close must be refused")
	}
}

// snapshot with no arranged panes says so instead of returning an empty array.
func TestWorkspaceSnapshotWithoutPanes(t *testing.T) {
	tool := NewWorkspaceTool(t.TempDir(), true)
	res, err := tool.Execute(context.Background(), "1", json.RawMessage(`{"action":"snapshot"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("snapshot with no panes must be an error")
	}
}
