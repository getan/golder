package explore

import (
	"reflect"
	"testing"
)

func TestIsToolMatchesTUIExplorationPolicy(t *testing.T) {
	for _, name := range []string{"read", "ls", "grep", "find", "view_image", "  View_Image  "} {
		if !IsTool(name) {
			t.Errorf("%q should be grouped", name)
		}
	}
	for _, name := range []string{"bash", "apply_patch", "task", "webfetch", "websearch", "memory_search", ""} {
		if IsTool(name) {
			t.Errorf("%q must keep its own card", name)
		}
	}
}

func TestBuilderMergesReadsAndKeepsDifferentEnvelopes(t *testing.T) {
	var b Builder
	b.Add("read", map[string]any{"path": " a.go "}, "sandbox")
	b.Add("read", map[string]any{"path": "b.go"}, "sandbox")
	b.Add("read", map[string]any{"path": "a.go"}, "sandbox")
	b.Add("read", map[string]any{"path": "c.go"}, "bare")
	b.Add("grep", map[string]any{"pattern": "TODO", "path": "internal", "glob": "*.go"}, "")
	b.Add("find", map[string]any{"glob": "*.go", "path": "docs"}, "")
	b.Add("ls", nil, "")
	b.Add("view_image", map[string]any{"path": "graph.png"}, "")
	want := []Row{
		{Text: "Read a.go, b.go", Envelope: "sandbox"},
		{Text: "Read c.go", Envelope: "bare"},
		{Text: "Search TODO in internal"},
		{Text: "Find *.go in docs"},
		{Text: "List ."},
		{Text: "View graph.png"},
	}
	if got := b.Finish(); !reflect.DeepEqual(got, want) {
		t.Fatalf("summary=%+v, want %+v", got, want)
	}
}
