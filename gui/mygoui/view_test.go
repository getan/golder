package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/getan/golder/gui/appsession"
)

func testApp() *App {
	a := &App{
		index:        map[string]int{},
		thinkingOpen: map[string]bool{},
		toolOpen:     map[string]bool{},
		approvalOpen: map[string]bool{},
		fileEntries:  map[string][]appsession.ProjectFileEntry{},
		fileLoading:  map[string]bool{},
		display:      appsession.DisplaySettings{Theme: "system", FontScale: 1},
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
	for _, want := range []string{"project", "Projects", "Chats", "Settings", "↑", "test-provider · test-model · off", "Changes"} {
		if !tt.HasText(want) {
			t.Fatalf("view is missing %q; texts: %q", want, tt.Texts())
		}
	}
	for _, gone := range []string{"Workspace", "New session", "+  New session", "▤", "Model ·", "Think ·", "Mode ·", "Permissions: test-model", "✋", "🌐"} {
		if tt.HasText(gone) {
			t.Fatalf("view still contains retired top-level action %q", gone)
		}
	}
	if err := tt.Click("Approval mode"); err != nil || !a.permOpen {
		t.Fatalf("monochrome approval hand is not accessible: open=%t err=%v", a.permOpen, err)
	}
}

func TestSidebarToggleKeepsNavigationAccessible(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Toggle sidebar"); err != nil || !a.sidebarClosed {
		t.Fatalf("collapse sidebar: closed=%t err=%v", a.sidebarClosed, err)
	}
	tt.Frame()
	if tt.HasText("Chats") || !tt.HasText("Settings") || !tt.HasText("Update") {
		t.Fatalf("unexpected collapsed navigation: %q", tt.Texts())
	}
	if err := tt.Click("Archived sessions"); err != nil || a.sidebarClosed || !a.showArchived {
		t.Fatalf("archive should reopen sidebar: closed=%t archived=%t err=%v", a.sidebarClosed, a.showArchived, err)
	}
}

func TestSidebarDividerResizes(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	divider, ok := tt.Find("Divider")
	if !ok {
		t.Fatal("sidebar divider not found")
	}
	x, y := divider.X+divider.W/2, divider.Y+divider.H/2
	tt.Press(x, y)
	tt.Move(x+60, y)
	tt.Release(x+60, y)
	if a.sidebarSize <= sidebarWidth {
		t.Fatalf("drag did not enlarge sidebar: %v", a.sidebarSize)
	}
}

func TestTitleBarIconButtonsShareSizeAndChangesCloses(t *testing.T) {
	a := testApp()
	a.changesOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	for _, label := range []string{"Files", "Changes", "Computer Use", "Browser", "Terminal"} {
		box, ok := tt.Find(label)
		if !ok || box.W != 30 || box.H != 30 {
			t.Fatalf("%s button has box %+v, found=%t; want 30×30", label, box, ok)
		}
	}
	if err := tt.Click("Changes"); err != nil || a.changesOpen {
		t.Fatalf("changes action did not close the drawer: open=%t err=%v", a.changesOpen, err)
	}
}

func TestTitleBarIconButtonsHaveNoDefaultTile(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := testApp()
		tt := ui.NewTester(a.view, 1200, 800)
		tt.SetDark(dark)
		for _, label := range []string{"Files", "Changes", "Computer Use", "Browser", "Terminal"} {
			tt.Move(2, 2)
			box, ok := tt.Find(label)
			if !ok {
				t.Fatalf("button %q not found", label)
			}
			x, y := int(box.X+4), int(box.Y+box.H/2)
			before := tt.Image().RGBAAt(x, y)
			outside := tt.Image().RGBAAt(int(box.X-3), y)
			if before != outside {
				t.Fatalf("%s has a default tile in dark=%t: %v vs %v", label, dark, before, outside)
			}
			tt.Move(box.X+box.W/2, box.Y+box.H/2)
			if after := tt.Image().RGBAAt(x, y); after == before {
				t.Fatalf("%s has no hover feedback in dark=%t", label, dark)
			}
		}
	}
}

func TestBrowserUsesRightDrawer(t *testing.T) {
	a := testApp()
	a.browserPanelOpen = true
	a.browserState = &browserWindowState{title: "Example", address: "https://example.com"}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if a.browserState.viewW < 550 || a.browserState.viewW > 650 || a.browserState.viewH < 600 {
		t.Fatalf("browser viewport did not occupy half the window: %.0fx%.0f", a.browserState.viewW, a.browserState.viewH)
	}
	for _, want := range []string{"Example", "Connecting to Chromium…", "×"} {
		if !tt.HasText(want) {
			t.Fatalf("browser drawer is missing %q; texts: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Browser"); err != nil || a.browserPanelOpen {
		t.Fatalf("monochrome browser action did not close the drawer: open=%t err=%v", a.browserPanelOpen, err)
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
	for _, want := range []string{"Changes", "project  ·  feature/ui", "Unstaged", "Staged", "Stage all", "Stage", "ui.go", "+3", "−1"} {
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
	if a.diff.Layout != editor.DiffUnified || !a.diff.Wrap {
		t.Fatalf("narrow review drawer must use a wrapped unified diff: layout=%v wrap=%t", a.diff.Layout, a.diff.Wrap)
	}
}

func TestChangesDiffUsesWrappedUnifiedLayoutForEachFileKind(t *testing.T) {
	longLine := strings.Repeat("long text 中文 ", 30)
	for _, tc := range []struct {
		name, patch, oldText, newText string
	}{
		{"modified", "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-old\n+" + longLine + "\n", "old", longLine},
		{"added", "diff --git a/f.txt b/f.txt\nnew file mode 100644\n--- /dev/null\n+++ b/f.txt\n@@ -0,0 +1 @@\n+" + longLine + "\n", "", longLine},
		{"deleted", "diff --git a/f.txt b/f.txt\ndeleted file mode 100644\n--- a/f.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-" + longLine + "\n", longLine, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.changesOpen = true
			a.changes = appsession.Changes{Files: []appsession.FileStatus{{Path: "f.txt"}}}
			a.diffRequest = 1
			a.applyFileDiff(1, 0, appsession.FileDiff{Path: "f.txt", Patch: tc.patch}, nil)
			if a.changesErr != "" || a.diff == nil {
				t.Fatalf("diff not loaded: %s", a.changesErr)
			}
			if a.diff.Layout != editor.DiffUnified || !a.diff.Wrap {
				t.Fatalf("layout=%v wrap=%t", a.diff.Layout, a.diff.Wrap)
			}
			if !strings.Contains(a.diff.Old.String(), tc.oldText) || !strings.Contains(a.diff.New.String(), tc.newText) {
				t.Fatalf("patch content lost: old=%q new=%q", a.diff.Old.String(), a.diff.New.String())
			}
			if image := ui.NewTester(a.view, 860, 560).Image(); image == nil {
				t.Fatal("narrow review view did not render")
			}
		})
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
	for _, want := range []string{"!  Approval required · bash", "git push origin main", "Allow once", "Deny"} {
		if !tt.HasText(want) {
			t.Fatalf("approval card is missing %q; texts: %q", want, tt.Texts())
		}
	}
	if !a.approvalOpen["approval-1"] {
		t.Fatal("a new approval card must open so its decision buttons are visible")
	}
}

func TestTerminalApprovalDisplaysExactCallWithoutSessionGrant(t *testing.T) {
	a := testApp()
	summary := `{"action":"send","pane":"terminal-42","text":"echo reviewed"}`
	a.pending = []appsession.ApprovalView{{ID: "terminal-approval", Tool: "terminal", Summary: summary}}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText(summary) || !tt.HasText("Allow once") {
		t.Fatalf("terminal approval omitted the exact call: %q", tt.Texts())
	}
	if tt.HasText("Always this session") {
		t.Fatal("terminal approval allowed future calls without review")
	}
}

func TestWaitingForFirstOutputIndicatorLifecycle(t *testing.T) {
	a := testApp()
	a.status.Running, a.status.RunID = true, "run-1"
	a.items = []appsession.Item{{ID: "run-1-user", Kind: appsession.ItemUser, Text: "hello", Done: true}}
	a.index["run-1-user"] = 0
	if !a.awaitingFirstOutput() {
		t.Fatal("run with no model output should be waiting")
	}
	if tt := ui.NewTester(a.view, 1200, 800); !tt.HasText("正在思考…") {
		t.Fatalf("waiting indicator missing: %q", tt.Texts())
	}
	a.items = append(a.items, appsession.Item{ID: "run-1-a-0", Kind: appsession.ItemAssistant, Text: "answer"})
	if a.awaitingFirstOutput() {
		t.Fatal("first assistant output must remove the waiting indicator")
	}
	a.items = a.items[:1]
	a.pending = []appsession.ApprovalView{{ID: "approval", Tool: "bash"}}
	if a.awaitingFirstOutput() {
		t.Fatal("pending approval card replaces the waiting indicator")
	}
}
