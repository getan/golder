package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func testApp() *App {
	a := &App{
		index:        map[string]int{},
		thinkingOpen: map[string]bool{},
		toolOpen:     map[string]bool{},
		status: appsession.Status{
			Model:      "test-model",
			Provider:   "test-provider",
			Permission: "auto",
			Title:      "A session",
		},
		project: "/tmp/project",
	}
	return a
}

func TestViewRendersChrome(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Golder", "New session", "Send", "test-model", "auto"} {
		if !tt.HasText(want) {
			t.Fatalf("view is missing %q; texts: %q", want, tt.Texts())
		}
	}
}

func TestViewRendersTranscript(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "u1", Kind: appsession.ItemUser, Text: "hello there", Done: true},
		{ID: "a1", Kind: appsession.ItemAssistant, Text: "**hi**", Done: true},
		{ID: "t1", Kind: appsession.ItemTool, Text: "bash", Done: true, Tool: &appsession.ToolState{
			ID: "c1", Name: "bash", Input: map[string]any{"command": "go test ./..."},
		}},
		{ID: "n1", Kind: appsession.ItemNotice, Text: "Context compacted.", Done: true},
	}
	for i, it := range a.items {
		a.index[it.ID] = i
	}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"hello there", "bash", "Context compacted."} {
		if !tt.HasText(want) {
			t.Fatalf("transcript is missing %q; texts: %q", want, tt.Texts())
		}
	}
}

func TestPaletteListsActions(t *testing.T) {
	a := testApp()
	a.paletteOpen = true
	a.actions = []appsession.Action{
		{ID: "model", Label: "/model", Category: "Model", Description: "Switch the model"},
		{ID: "help", Label: "/help", Category: "General", Description: "List commands"},
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("/model") || !tt.HasText("/help") {
		t.Fatalf("palette is missing actions; texts: %q", tt.Texts())
	}
}

func TestApprovalCardRendersDecision(t *testing.T) {
	a := testApp()
	a.pending = []appsession.ApprovalView{{
		ID:        "approval-1",
		Kind:      "consent",
		Tool:      "bash",
		Summary:   "git push origin main",
		Rationale: "network access to github.com",
		Source:    "local",
		Level:     "confirm",
		Mode:      "ask",
		Needs:     []string{"network github.com"},
	}}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Approval required", "git push origin main", "Allow once", "Deny"} {
		if !tt.HasText(want) {
			t.Fatalf("approval card is missing %q; texts: %q", want, tt.Texts())
		}
	}
}
