package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/runtime"
)

func extensionModel(t *testing.T) Model {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if e := extensions.WriteSource(path, extensions.SourceRef{Kind: "mcp", ID: "remote"}, &extensions.SourceConfig{Transport: "http", URL: "https://example.invalid/mcp"}); e != nil {
		t.Fatal(e)
	}
	program, _ := os.Executable()
	if e := extensions.WriteSource(path, extensions.SourceRef{Kind: "plugin", ID: "old"}, &extensions.SourceConfig{Runtime: "rpc", Command: program}); e != nil {
		t.Fatal(e)
	}
	manifestDir := filepath.Join(home, "cli-plugin")
	if e := os.MkdirAll(manifestDir, 0o700); e != nil {
		t.Fatal(e)
	}
	manifest := filepath.Join(manifestDir, "golder-plugin.toml")
	if e := os.WriteFile(manifest, []byte("[plugin]\nid='local'\nversion='1'\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := extensions.WriteSource(path, extensions.SourceRef{Kind: "plugin", ID: "local"}, &extensions.SourceConfig{Runtime: "cli", Manifest: manifest}); e != nil {
		t.Fatal(e)
	}
	m, e := extensions.New(extensions.Options{ConfigPath: path})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = m.Close() })
	s, e := runtime.ParseSkill("/skills/demo/SKILL.md", []byte("---\nname: demo\ndescription: Demo workflow\ndisable-model-invocation: true\n---\nInspect $ARGUMENTS"))
	if e != nil {
		t.Fatal(e)
	}
	m.SetSkills([]*runtime.Skill{s}, false)
	model := NewModel(Options{Extensions: m, Skills: []*runtime.Skill{s}, NoAnimations: true})
	res, _ := model.Update(tea.WindowSizeMsg{Width: 68, Height: 28})
	return res.(Model)
}

func TestExtensionPickerLeavesSkillsUnderSkills(t *testing.T) {
	m := extensionModel(t)
	res, cmd := m.runSlash("/extensions")
	m = res.(Model)
	if cmd != nil {
		t.Fatal("opening list scheduled external I/O")
	}
	var text strings.Builder
	for _, r := range m.menu.pick {
		text.WriteString(r.Title + " " + r.Status + "\n")
	}
	for _, want := range []string{"MCP servers", "MCP · http", "CLI plugins", "local [CLI]", "Legacy RPC plugins", "RPC · legacy", "Available packages", "Lark + Project [MCP package]"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("missing %s: %s", want, text.String())
		}
	}
	for _, duplicate := range []string{"Skills · instructions only", "demo [Skill]"} {
		if strings.Contains(text.String(), duplicate) {
			t.Fatalf("/extensions duplicated %q from /skills: %s", duplicate, text.String())
		}
	}
	res, _ = m.chooseExtension(extensionValue("package-detail", "lark"))
	m = res.(Model)
	if len(m.menu.pick) != 1 || !strings.Contains(m.menu.pick[0].Info[0].Value, "Lark") {
		t.Fatalf("Lark package details missing: %+v", m.menu.pick)
	}
	res, _ = m.chooseExtension(extensionValue("back"))
	m = res.(Model)
	if !strings.Contains(m.View().Content, "MCP") {
		t.Fatal("narrow view lost type label")
	}
	for _, tc := range []struct {
		command string
		want    []string
		reject  []string
	}{
		{command: "/mcp", want: []string{"MCP servers", "remote"}, reject: []string{"CLI plugins", "Legacy RPC plugins", "Skills"}},
		{command: "/plugin", want: []string{"CLI plugins", "local", "Legacy RPC plugins", "old"}, reject: []string{"MCP servers", "Skills"}},
		{command: "/skills", want: []string{"Skills · instructions only", "demo"}, reject: []string{"MCP servers", "CLI plugins", "Legacy RPC plugins", "remote", "local", "old"}},
	} {
		res, _ = m.runSlash(tc.command)
		m = res.(Model)
		text.Reset()
		for _, r := range m.menu.pick {
			text.WriteString(r.Title + "\n")
		}
		for _, want := range tc.want {
			if !strings.Contains(text.String(), want) {
				t.Errorf("%s missing %q: %s", tc.command, want, text.String())
			}
		}
		for _, reject := range tc.reject {
			if strings.Contains(text.String(), reject) {
				t.Errorf("%s contains %q: %s", tc.command, reject, text.String())
			}
		}
	}
	res, _ = m.runSlash("/skills")
	m = res.(Model)
	res, _ = m.chooseExtension(extensionValue("skill-detail", "demo"))
	m = res.(Model)
	res, _ = m.chooseExtension(extensionValue("back", "skills"))
	m = res.(Model)
	text.Reset()
	for _, r := range m.menu.pick {
		text.WriteString(r.Title + "\n")
	}
	if !strings.Contains(text.String(), "demo [Skill]") || strings.Contains(text.String(), "MCP servers") {
		t.Fatalf("Skill Back left the /skills catalog: %s", text.String())
	}
}

func TestSkillDetailsHaveNoConnectionAndDoNotGrantTools(t *testing.T) {
	m := extensionModel(t)
	res, _ := m.chooseExtension(extensionValue("skill-detail", "demo"))
	m = res.(Model)
	for _, r := range m.menu.pick {
		if strings.Contains(strings.ToLower(r.Title), "connect") || strings.Contains(strings.ToLower(r.Title), "restart") {
			t.Fatal("skill treated as process")
		}
	}
	before := len(m.opts.Extensions.Tools())
	res, _ = m.runExtension("skills", []string{"disable", "demo"})
	m = res.(Model)
	if m.opts.Extensions.Skill("demo") != nil || len(m.opts.Extensions.Tools()) != before {
		t.Fatal("skill disabled incorrectly")
	}
	if _, ok := m.slash.Lookup("demo"); ok {
		t.Fatal("disabled skill remains a slash command")
	}
}

func TestExtensionReviewDoesNotConnectOrEnable(t *testing.T) {
	m := extensionModel(t)
	res, cmd := m.chooseExtension(extensionValue("review", "mcp", "remote"))
	m = res.(Model)
	if cmd != nil {
		t.Fatal("review ran I/O")
	}
	for _, s := range m.opts.Extensions.List() {
		if s.Enabled || s.Trust == "approved" {
			t.Fatal("review granted authority")
		}
	}
	if m.mcpWizard == nil || !m.mcpWizard.approvalOnly || !strings.Contains(m.View().Content, "Enter approve") {
		t.Fatal("missing explicit confirmation")
	}
}
