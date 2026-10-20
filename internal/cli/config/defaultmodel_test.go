package config

import (
	"os"
	"strings"
	"testing"
)

func TestSetDefaultModelEditsOnlyItsKeys(t *testing.T) {
	const body = `# my settings
provider = "opencode-go"
model = "old-model"   # pinned
thinking_level = "low"

[dream]
enabled = true
`
	path := writeConfig(t, body)
	if err := SetDefaultModel("new-model", "high", "commandcode"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		`provider = "commandcode"`,
		`model = "new-model"`,
		`thinking_level = "high"`,
		"# my settings",
		"[dream]\nenabled = true",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if model, _ := DefaultModel(); model != "new-model" {
		t.Errorf("DefaultModel = %q", model)
	}
	if level, _ := DefaultThinkingLevel(); level != "high" {
		t.Errorf("DefaultThinkingLevel = %q", level)
	}
}

func TestSetDefaultModelAddsMissingKeys(t *testing.T) {
	path := writeConfig(t, "approve = true\n\n[memory]\nenabled = true\n")
	if err := SetDefaultModel("deepseek-v4.1-flash", "max", "commandcode"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	if !strings.Contains(text, `model = "deepseek-v4.1-flash"`) ||
		!strings.Contains(text, `thinking_level = "max"`) ||
		!strings.Contains(text, `provider = "commandcode"`) {
		t.Fatalf("keys missing:\n%s", text)
	}
	if strings.Index(text, "model =") > strings.Index(text, "[memory]") {
		t.Errorf("keys were appended after a table:\n%s", text)
	}
}

func TestSetDefaultModelClearsOnlyModelAndLevel(t *testing.T) {
	path := writeConfig(t, "provider = \"commandcode\"\nmodel = \"m\"\nthinking_level = \"high\"\n")
	if err := SetDefaultModel("", "", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	if strings.Contains(text, "model =") || strings.Contains(text, "thinking_level") {
		t.Errorf("model/level were not cleared:\n%s", text)
	}
	if !strings.Contains(text, `provider = "commandcode"`) {
		t.Errorf("clearing the model dropped the provider:\n%s", text)
	}
}

func TestSetDefaultModelCreatesTheFile(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := SetDefaultModel("m", "high", "commandcode"); err != nil {
		t.Fatalf("set on missing file: %v", err)
	}
	if model, _ := DefaultModel(); model != "m" {
		t.Errorf("DefaultModel = %q", model)
	}
}

func TestSetDefaultThinkingLevelLeavesTheModelAlone(t *testing.T) {
	path := writeConfig(t, "provider = \"commandcode\"\nmodel = \"m\"\n")
	if err := SetDefaultThinkingLevel("xhigh"); err != nil {
		t.Fatalf("set level: %v", err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	if !strings.Contains(text, `thinking_level = "xhigh"`) {
		t.Errorf("level missing:\n%s", text)
	}
	if !strings.Contains(text, `model = "m"`) || !strings.Contains(text, `provider = "commandcode"`) {
		t.Errorf("setting the level touched the model/provider:\n%s", text)
	}
}
