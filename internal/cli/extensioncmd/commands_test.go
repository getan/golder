package extensioncmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/runtime"
)

func TestRegisterListEnableDisableNeverRunsProgram(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	var out, errs bytes.Buffer
	if code := Run("mcp", []string{"add", "demo", "--transport", "stdio", "--", "/definitely/missing/program", "--serve"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatal(code, errs.String())
	}
	c, e := extensions.LoadConfig(extensions.ConfigPath())
	if e != nil || c.MCP.Servers["demo"].Enabled || c.MCP.Servers["demo"].Args[0] != "--serve" {
		t.Fatalf("bad config %+v %v", c, e)
	}
	out.Reset()
	errs.Reset()
	if code := Run("mcp", []string{"list", "--json"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	var list []extensions.Snapshot
	if json.Unmarshal(out.Bytes(), &list) != nil || len(list) != 1 || list[0].Trust != "needs-review" {
		t.Fatal(out.String())
	}
	out.Reset()
	errs.Reset()
	if code := Run("mcp", []string{"enable", "demo", "--yes"}, strings.NewReader(""), &out, &errs); code == 0 {
		t.Fatal("missing program approved")
	}
	if strings.Contains(errs.String(), "panic") {
		t.Fatal(errs.String())
	}
	info, _ := os.Stat(filepath.Join(home, "config.toml"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
}

func TestHTTPConfigRequiresNoConnectionAndJSONIsClean(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var out, errs bytes.Buffer
	for _, args := range [][]string{{"add", "remote", "--transport", "http", "--url", "https://example.invalid/mcp", "--json"}, {"enable", "remote", "--yes", "--json"}, {"inspect", "remote", "--json"}, {"disable", "remote", "--json"}} {
		out.Reset()
		errs.Reset()
		if code := Run("mcp", args, strings.NewReader(""), &out, &errs); code != 0 {
			t.Fatal(args, code, errs.String())
		}
		if !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Fatalf("not JSON for %v: %s", args, out.String())
		}
	}
}

func TestExtensionsListLeavesSkillsToSkills(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	m, err := extensions.New(extensions.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	skill, err := runtime.ParseSkill("/skills/deploy/SKILL.md", []byte("---\nname: deploy\ndescription: Deploy workflow\n---\nDeploy"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetSkills([]*runtime.Skill{skill}, false)
	if got := RenderList(m, "extensions"); strings.Contains(got, "Deploy") || strings.Contains(got, "Skills") || !strings.Contains(got, "Lark + Project") {
		t.Fatalf("extensions list duplicated Skills:\n%s", got)
	}
	if got := RenderList(m, "skills"); !strings.Contains(got, "deploy") || !strings.Contains(got, "Skills") {
		t.Fatalf("skills list lost its catalog:\n%s", got)
	}
	var out, errs bytes.Buffer
	if code := Run("extensions", []string{"list", "--json"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("extensions JSON failed: %s", errs.String())
	}
	var inventory map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &inventory); err != nil || inventory["packages"] == nil || inventory["skills"] != nil {
		t.Fatalf("extensions JSON mixed packages and Skills: %s (%v)", out.String(), err)
	}
}
