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

func TestSetDefaultModelForKeepsPerProviderEntries(t *testing.T) {
	path := writeConfig(t, "# my settings\n\n[dream]\nenabled = true\n")
	if err := SetDefaultModelFor("commandcode", "deepseek/deepseek-v4.1-flash", "high"); err != nil {
		t.Fatalf("set commandcode: %v", err)
	}
	if err := SetDefaultModelFor("opencode-go", "deepseek-v4.1-flash", "max"); err != nil {
		t.Fatalf("set opencode: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		"[model_defaults]",
		`commandcode = "deepseek/deepseek-v4.1-flash"`,
		`opencode-go = "deepseek-v4.1-flash"`,
		"# my settings",
		"[dream]\nenabled = true",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if model, _ := DefaultModelFor("commandcode"); model != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("commandcode default = %q", model)
	}
	if model, _ := DefaultModelFor("opencode-go"); model != "deepseek-v4.1-flash" {
		t.Errorf("opencode default = %q", model)
	}
	if model, _ := DefaultModelFor("deepseek"); model != "" {
		t.Errorf("unset provider default = %q", model)
	}
	// The top-level pair follows the last set, so a new session starts there.
	if model, _ := DefaultModel(); model != "deepseek-v4.1-flash" {
		t.Errorf("top-level model = %q", model)
	}
	if provider, _ := DefaultProvider(); provider != "opencode-go" {
		t.Errorf("top-level provider = %q", provider)
	}
}

func TestDefaultModelForFallsBackToTheTopLevelPair(t *testing.T) {
	writeConfig(t, "provider = \"commandcode\"\nmodel = \"legacy-model\"\n")
	if model, _ := DefaultModelFor("commandcode"); model != "legacy-model" {
		t.Errorf("legacy fallback = %q", model)
	}
	if model, _ := DefaultModelFor("opencode-go"); model != "" {
		t.Errorf("another provider must not inherit the legacy default: %q", model)
	}
}

func TestSetDefaultModelForClearsOnlyOneProvider(t *testing.T) {
	writeConfig(t, "")
	if err := SetDefaultModelFor("commandcode", "cc", "high"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultModelFor("opencode-go", "oc", "high"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultModelFor("commandcode", "", ""); err != nil {
		t.Fatal(err)
	}
	if model, _ := DefaultModelFor("commandcode"); model != "" {
		t.Errorf("commandcode still has %q", model)
	}
	if model, _ := DefaultModelFor("opencode-go"); model != "oc" {
		t.Errorf("clearing commandcode dropped opencode: %q", model)
	}
	if model, _ := DefaultModel(); model != "oc" {
		t.Errorf("top-level model changed while clearing another provider: %q", model)
	}
}
