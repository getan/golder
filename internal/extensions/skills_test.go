package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	legacy "github.com/getan/golder/internal/plugin"
	"github.com/getan/golder/internal/runtime"
)

func TestSkillDisableAndManualInvocationAreIndependentOfTools(t *testing.T) {
	m := testManager(t, SourceRef{Kind: "mcp", ID: "unused"}, SourceConfig{Transport: "http", URL: "https://example.invalid/mcp"}, nil)
	s, e := runtime.ParseSkill("/demo/SKILL.md", []byte("---\nname: demo\ndescription: Workflow\ndisable-model-invocation: true\n---\nRead the data."))
	if e != nil {
		t.Fatal(e)
	}
	m.SetSkills([]*runtime.Skill{s}, false)
	if m.SkillViews()[0].State != "manual only" {
		t.Fatal(m.SkillViews())
	}
	if !m.SetSkillEnabled("demo", false) || m.Skill("demo") != nil || len(m.Tools()) != 0 {
		t.Fatal("skill changed tool authority")
	}
	if !m.SetSkillEnabled("demo", true) || m.Skill("demo") == nil {
		t.Fatal("skill did not restore")
	}
	m.SetSkills([]*runtime.Skill{s}, true)
	if len(m.Skills()) != 0 || len(m.SkillViews()) != 0 {
		t.Fatal("--no-skills ignored")
	}
}

func TestEventMetadataNeverContainsConversationText(t *testing.T) {
	m := &Manager{events: make(chan legacy.EventParams, 1)}
	m.Observe(agentcore.TurnEndEvent{Message: agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent("private conversation text")}}})
	ev := <-m.events
	if strings.Contains(string(ev.Data), "private") {
		t.Fatal("event body leaked", string(ev.Data))
	}
}

func TestInvalidSourceIsIsolatedAndNoToolsCannotEnable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if e := os.WriteFile(path, []byte("[mcp.servers.bad]\nenabled=true\ntransport='wrong'\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	m, e := New(Options{ConfigPath: path, NoTools: true})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	if len(m.List()) != 1 || m.List()[0].LastError == "" {
		t.Fatal("invalid source disappeared")
	}
	if e = m.Enable(SourceRef{Kind: "mcp", ID: "bad"}); e == nil {
		t.Fatal("--no-tools permits enabling")
	}
}
