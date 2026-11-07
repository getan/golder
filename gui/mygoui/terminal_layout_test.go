package mygoui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestTerminalLayoutSplitAndRemove(t *testing.T) {
	a := &terminalPane{id: "a"}
	b := &terminalPane{id: "b"}
	c := &terminalPane{id: "c"}
	root := &terminalLayout{pane: a}
	if !root.split("a", b, false) || !root.split("b", c, true) || root.split("missing", c, false) {
		t.Fatal("unexpected split result")
	}
	if root.vertical || !root.second.vertical || root.percent != 50 || root.second.percent != 50 {
		t.Fatalf("split orientation or proportion is wrong: %+v", root)
	}
	var ids []string
	root.each(func(p *terminalPane) { ids = append(ids, p.id) })
	if !reflect.DeepEqual(ids, []string{"a", "b", "c"}) {
		t.Fatalf("panes: %v", ids)
	}
	root, removed := root.remove("b")
	if removed != b || root.second.pane != c {
		t.Fatal("removing a leaf did not collapse its split")
	}
	root, removed = root.remove("a")
	if removed != a || root.pane != c {
		t.Fatal("removing a branch did not retain its sibling")
	}
	root, removed = root.remove("c")
	if removed != c || root != nil {
		t.Fatal("removing the final pane did not empty the layout")
	}
}

func TestTerminalPanelsFollowSelectedProject(t *testing.T) {
	a := testApp()
	a.terminalOpen = true
	first := &terminalPane{id: "a", name: "first", project: "/tmp/project"}
	a.terminalProject(first.project).root = &terminalLayout{pane: first}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("first") || !tt.HasText("Terminal") {
		t.Fatalf("first project terminal not shown: %q", tt.Texts())
	}
	if !tt.HasText("Right-click a pane title to split it or manage Agent access.") {
		t.Fatal("terminal pane menu hint is missing")
	}
	if err := tt.RightClick("Terminal pane first"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Menu(); !reflect.DeepEqual(got, []string{"Split right", "Split down", "-", "Allow Agent to view", "Assign Agent control", "Revoke Agent access", "Take back control", "-", "Rename…", "Close terminal"}) {
		t.Fatalf("terminal title menu: %q", got)
	}
	tt.CloseMenu()
	a.selectedProject = "/tmp/other-project"
	tt.Frame()
	if tt.HasText("first") {
		t.Fatal("another project's pane was shown")
	}
	a.selectedProject = first.project
	tt.Frame()
	if !tt.HasText("first") {
		t.Fatal("returning to the project lost its pane")
	}
}
