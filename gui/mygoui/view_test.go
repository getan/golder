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
		project:         "/tmp/project",
		selectedProject: "/tmp/project",
		projects:        []appsession.ProjectFolder{{Path: "/tmp/project", Name: "project"}},
	}
	return a
}

func TestViewRendersChrome(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"project", "Projects", "Chats", "New session", "Settings", "↑", "test-model · off", "☝"} {
		if !tt.HasText(want) {
			t.Fatalf("view is missing %q; texts: %q", want, tt.Texts())
		}
	}
	for _, gone := range []string{"Workspace", "Browser", "Model ·", "Think ·", "Mode ·", "Permissions: test-model"} {
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
	for _, want := range []string{"Settings", "Provider defaults", "New API key", "Configured · openai-default", "AES-256-GCM encrypted", "Save & apply"} {
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
	for _, want := range []string{"Extensions", "Lark + Project", "Official lark-cli", "AES-256-GCM encrypted", "Save configuration", "Approve & enable", "Authorize Lark"} {
		if !tt.HasText(want) {
			t.Fatalf("extensions wizard is missing %q; texts: %q", want, tt.Texts())
		}
	}
	if tt.HasText("private-extension-secret") {
		t.Fatal("extension secret was rendered as ordinary text")
	}
}

func TestVisibleSessionsFilterProjectAndArchived(t *testing.T) {
	a := testApp()
	a.sessions = []appsession.SessionSummary{
		{ID: "one", Title: "Current", Cwd: "/tmp/project", Current: true},
		{ID: "two", Title: "Other", Cwd: "/tmp/other"},
		{ID: "three", Title: "Old", Cwd: "/tmp/project", Archived: true},
	}
	a.selectedProject = "/tmp/project"
	sessions := a.visibleSessions()
	if len(sessions) != 1 || sessions[0].ID != "one" {
		t.Fatalf("sessions = %+v", sessions)
	}
	a.showArchived = true
	sessions = a.visibleSessions()
	if len(sessions) != 2 {
		t.Fatalf("archived sessions = %+v", sessions)
	}
	a.selectedProject = "/tmp/other"
	sessions = a.visibleSessions()
	if len(sessions) != 1 || sessions[0].ID != "two" {
		t.Fatalf("filtered sessions = %+v", sessions)
	}
}

func TestChangesPanelRendersGitReviewControls(t *testing.T) {
	a := testApp()
	a.changesOpen = true
	a.changes = appsession.Changes{
		View: appsession.ChangesUnstaged, RepoRoot: "/tmp/project", Branch: "feature/ui", TotalFiles: 1,
		Files: []appsession.FileStatus{{Path: "ui.go", Status: "M", Added: 3, Deleted: 1}},
	}
	tt := ui.NewTester(a.view, 1400, 800)
	for _, want := range []string{"▤  Changes", "project  ·  feature/ui", "Unstaged", "Staged", "Stage all", "Stage", "ui.go", "+3", "−1"} {
		if !tt.HasText(want) {
			t.Fatalf("changes panel is missing %q; texts: %q", want, tt.Texts())
		}
	}
}

func TestLazyDiffDiscardsStaleResult(t *testing.T) {
	a := testApp()
	a.changes = appsession.Changes{Files: []appsession.FileStatus{{Path: "a.go", Status: "M"}}}
	a.diffRequest = 2
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	a.applyFileDiff(1, 0, appsession.FileDiff{Path: "a.go", Patch: patch, Added: 1, Deleted: 1}, nil)
	if a.diff != nil || a.fileDiff.Path != "" {
		t.Fatal("stale file diff was applied")
	}
	a.applyFileDiff(2, 0, appsession.FileDiff{Path: "a.go", Patch: patch, Added: 1, Deleted: 1}, nil)
	if a.diff == nil || a.fileDiff.Path != "a.go" {
		t.Fatal("current file diff was not applied")
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
