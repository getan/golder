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

func TestExtensionPickerSeparatesSourcesAndSkills(t *testing.T) {
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
	for _, want := range []string{"Tool sources", "MCP · http", "RPC · legacy", "Skills", "demo [Skill]", "manual only"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("missing %s: %s", want, text.String())
		}
	}
	if !strings.Contains(m.View().Content, "MCP") {
		t.Fatal("narrow view lost type label")
	}
	res, _ = m.runSlash("/skills")
	m = res.(Model)
	for _, r := range m.menu.pick {
		if strings.Contains(r.Title, "remote") {
			t.Fatal("source in Skills filter")
		}
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
	if len(m.menu.pick) < 2 || !strings.Contains(m.menu.pick[1].Title, "Approve") {
		t.Fatal("missing explicit confirmation")
	}
}
