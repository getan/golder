package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetDefaultProviderEditsOnlyTheKey(t *testing.T) {
	const body = `# my settings
model = "deepseek-v4.1-flash"   # pinned on purpose
thinking_level = "high"

[dream]
provider = "opencode-go"
`
	path := writeConfig(t, body)

	rewrote, err := SetDefaultProvider("commandcode", "deepseek/deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !rewrote {
		t.Error("the model line should have been rewritten to match the new provider")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, `provider = "commandcode"`) {
		t.Errorf("provider line missing:\n%s", text)
	}
	if !strings.Contains(text, `model = "deepseek/deepseek-v4.1-flash"`) {
		t.Errorf("model line not updated:\n%s", text)
	}
	if !strings.Contains(text, "# my settings") || !strings.Contains(text, "thinking_level = \"high\"") {
		t.Errorf("unrelated lines were dropped:\n%s", text)
	}
	// The table body's own provider key must survive as it was.
	if !strings.Contains(text, "[dream]\nprovider = \"opencode-go\"") {
		t.Errorf("a table's provider key was rewritten:\n%s", text)
	}

	name, err := DefaultProvider()
	if err != nil || name != "commandcode" {
		t.Fatalf("DefaultProvider = %q, %v", name, err)
	}
}

func TestSetDefaultProviderAddsAndClears(t *testing.T) {
	path := writeConfig(t, "thinking_level = \"high\"\n\n[dream]\nenabled = true\n")
	if _, err := SetDefaultProvider("commandcode", ""); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `provider = "commandcode"`) {
		t.Fatalf("provider line missing:\n%s", got)
	}
	// Appended to the top-level block, above the table.
	if strings.Index(string(got), "provider") > strings.Index(string(got), "[dream]") {
		t.Errorf("provider was appended after the table:\n%s", got)
	}

	if _, err := SetDefaultProvider("", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "provider") {
		t.Errorf("clear left the key behind:\n%s", got)
	}
	if !strings.Contains(string(got), "thinking_level") || !strings.Contains(string(got), "[dream]") {
		t.Errorf("clear dropped other settings:\n%s", got)
	}
	if name, _ := DefaultProvider(); name != "" {
		t.Errorf("DefaultProvider after clear = %q", name)
	}
}

func TestSetDefaultProviderCreatesTheFile(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if _, err := SetDefaultProvider("commandcode", "deepseek/deepseek-v4.1-flash"); err != nil {
		t.Fatalf("set on a missing file: %v", err)
	}
	name, err := DefaultProvider()
	if err != nil || name != "commandcode" {
		t.Fatalf("DefaultProvider = %q, %v", name, err)
	}
}
