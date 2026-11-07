package mygoui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/extensions"
)

func TestMCPClipboardReviewDoesNotRenderSecrets(t *testing.T) {
	f := newMCPAddForm()
	secret := "fixture-" + "confidential-value"
	f.parseClipboard(`{"mcpServers":{"demo":{"url":"https://example.com/mcp","headers":{"Authorization":"` + secret + `"}}}}`)
	if f.phase != "review" || f.current() == nil || f.current().Headers["Authorization"] != secret {
		t.Fatalf("clipboard import failed: %s", f.err)
	}
	a := testApp()
	a.settingsOpen, a.settingsTab, a.mcpAdd = true, 2, f
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Add MCP server") || !tt.HasText("Source name") {
		t.Fatalf("missing add/review controls: %q", tt.Texts())
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, secret) {
			t.Fatal("secret was rendered in the settings view")
		}
	}
	a.settingsOpen = false
	tt.Frame()
	if a.mcpAdd != nil {
		t.Fatal("closing settings retained the private form")
	}
}

func TestMCPManualFormParsesFixedArgv(t *testing.T) {
	f := newMCPAddForm()
	f.mode, f.name, f.target = "Local command", "demo", `echo "hello world" '; touch /tmp/not-run'`
	f.prepareManual()
	d := f.current()
	if f.err != "" || d == nil || d.Command != "echo" || len(d.Args) != 2 || d.Args[1] != "; touch /tmp/not-run" || d.Network {
		t.Fatalf("manual MCP did not preserve fixed argv: %s", f.err)
	}
}

func TestMCPManualBearerTokenIsHiddenInReview(t *testing.T) {
	f := newMCPAddForm()
	secret := "fixture-" + "bearer-value"
	f.mode, f.name, f.target, f.authToken = "Remote URL", "demo", "https://example.invalid/mcp", secret
	f.prepareManual()
	if f.err != "" || f.current() == nil || f.authToken != "" || f.current().Headers["Authorization"] != "Bearer "+secret {
		t.Fatalf("manual Bearer configuration was not prepared safely: %s", f.err)
	}
	a := testApp()
	a.settingsOpen, a.settingsTab, a.mcpAdd = true, 2, f
	tt := ui.NewTester(a.view, 1200, 800)
	for _, text := range tt.Texts() {
		if strings.Contains(text, secret) {
			t.Fatal("manual Bearer token was rendered")
		}
	}
}

func TestMCPManageControlsPreserveHiddenValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	ref := extensions.SourceRef{Kind: "mcp", ID: "demo"}
	secret := "fixture-" + "legacy-secret-value"
	if err := extensions.WriteSource(path, ref, &extensions.SourceConfig{Transport: "http", URL: "https://example.invalid/mcp", Headers: map[string]string{"Authorization": secret}}); err != nil {
		t.Fatal(err)
	}
	manager, err := extensions.New(extensions.Options{ConfigPath: path, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	a := testApp()
	a.ctrl = appsession.New(appsession.Options{Extensions: manager})
	a.settingsOpen, a.settingsTab = true, 2
	a.extensionSources = a.ctrl.ExtensionSources()
	a.beginMCPEdit("demo")
	if a.mcpAdd == nil || a.mcpAdd.edit == nil {
		t.Fatalf("edit did not open: %s", a.extensionErr)
	}
	a.mcpAdd.target = "https://new.example.invalid/mcp"
	a.mcpAdd.prepareManual()
	if a.mcpAdd.current() == nil || a.mcpAdd.current().Headers["Authorization"] != secret {
		t.Fatal("edit lost the existing hidden header")
	}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, text := range tt.Texts() {
		if strings.Contains(text, secret) {
			t.Fatal("legacy secret appeared in the GUI")
		}
	}
	a.mcpAdd = nil
	a.setGenericExtension(a.extensionSources[0], true)
	if a.mcpApproval == nil || a.mcpApproval.edit.ID != "demo" {
		t.Fatal("enabling did not open a detailed review")
	}
	tt.Frame()
	for _, text := range tt.Texts() {
		if strings.Contains(text, secret) {
			t.Fatal("approval review rendered a hidden credential")
		}
	}
	a.confirmMCPRemove("demo")
	if a.mcpRemove == nil || !manager.HasSource(ref) {
		t.Fatal("removal was not explicitly confirmed")
	}
}
