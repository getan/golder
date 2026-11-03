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
	for _, want := range []string{"project", "New session", "↑", "test-model", "auto"} {
		if !tt.HasText(want) {
			t.Fatalf("view is missing %q; texts: %q", want, tt.Texts())
		}
	}
	for _, gone := range []string{"Workspace", "Browser", "Session", "Permissions: test-model"} {
		if tt.HasText(gone) {
			t.Fatalf("view still contains retired top-level action %q", gone)
		}
	}
}

func TestComposerReusesCircularControlForStop(t *testing.T) {
	a := testApp()
	a.status.Running = true
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("■") || tt.HasText("↑") {
		t.Fatalf("running composer controls = %q", tt.Texts())
	}
}

func TestSettingsRendersProviderAndPasswordControls(t *testing.T) {
	a := testApp()
	a.settingsOpen = true
	a.settings = appsession.ProviderSettings{
		Provider: "openai", Model: "gpt-test", Providers: []string{"openai", "anthropic"},
		Credential: appsession.CredentialView{Available: true, Source: "named", Name: "openai-default"},
	}
	a.settingsEdit = appsession.ProviderSettingsEdit{Provider: "openai", Model: "gpt-test", CredentialName: "openai-default"}
	a.settingsEdit.NewCredential = "private-test-value"
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Settings", "Provider defaults", "New API key", "Configured · openai-default", "Save & apply"} {
		if !tt.HasText(want) {
			t.Fatalf("settings is missing %q; texts: %q", want, tt.Texts())
		}
	}
	if tt.HasText("private-test-value") {
		t.Fatal("password value was rendered as ordinary text")
	}
}

func TestExtensionsSettingsRendersManagedLarkWizard(t *testing.T) {
	a := testApp()
	a.settingsOpen = true
	a.settingsTab = 2
	a.extension = appsession.LarkExtensionSettings{
		ExtensionPackageView: appsession.ExtensionPackageView{
			ID: "lark", Name: "Lark + Project", Description: "Lark services",
			Installed: true, Configured: true, Version: "1.2.3", State: "disabled",
		},
		Domains: []string{"docs", "im"}, ProjectHost: "project.feishu.cn",
	}
	a.extensionEdit = appsession.LarkExtensionEdit{
		AppID: "cli_test", CredentialName: "lark-app-secret", NewAppSecret: "private-extension-secret",
		ProjectHost: "project.feishu.cn",
	}
	a.extensionDomains = map[string]bool{"docs": true, "im": true}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Extensions", "Lark + Project", "Official lark-cli", "Save configuration", "Approve & enable", "Authorize Lark"} {
		if !tt.HasText(want) {
			t.Fatalf("extensions wizard is missing %q; texts: %q", want, tt.Texts())
		}
	}
	if tt.HasText("private-extension-secret") {
		t.Fatal("extension secret was rendered as ordinary text")
	}
}

func TestSessionGroupsFilterProjectAndArchived(t *testing.T) {
	a := testApp()
	a.sessions = []appsession.SessionSummary{
		{ID: "one", Title: "Current", Cwd: "/tmp/project", Current: true},
		{ID: "two", Title: "Other", Cwd: "/tmp/other"},
		{ID: "three", Title: "Old", Cwd: "/tmp/project", Archived: true},
	}
	a.selectedProject = allProjectsLabel
	groups := a.visibleSessionGroups()
	if len(groups) != 2 || groups[0].Cwd != "/tmp/project" || len(groups[0].Sessions) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	a.showArchived = true
	groups = a.visibleSessionGroups()
	if len(groups[0].Sessions) != 2 {
		t.Fatalf("archived group = %+v", groups[0])
	}
	a.selectedProject = "/tmp/other"
	groups = a.visibleSessionGroups()
	if len(groups) != 1 || groups[0].Cwd != "/tmp/other" {
		t.Fatalf("filtered groups = %+v", groups)
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

func TestTTYToolCardOffersTerminalTakeover(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{{
		ID: "tty", Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{
			ID: "call", Name: "bash", Input: map[string]any{"command": "top", "tty": true},
			Details: map[string]any{"bash_id": "bash_3"}, Output: "running",
		},
	}}
	a.index["tty"] = 0
	a.toolOpen["tty"] = true
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Open Terminal") {
		t.Fatalf("TTY card controls = %q", tt.Texts())
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
