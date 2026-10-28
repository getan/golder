package extensioncmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/extensions"
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
