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

func TestSetDefaultModelForScopesThinkingLevelPerProviderModel(t *testing.T) {
	path := writeConfig(t, "")
	if err := SetDefaultModelFor("commandcode", "cc-model", "high"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultModelFor("opencode-go", "oc-model", "max"); err != nil {
		t.Fatal(err)
	}
	if level, _ := DefaultThinkingLevelFor("commandcode", "cc-model"); level != "high" {
		t.Errorf("commandcode/cc-model level = %q, want high", level)
	}
	if level, _ := DefaultThinkingLevelFor("opencode-go", "oc-model"); level != "max" {
		t.Errorf("opencode-go/oc-model level = %q, want max", level)
	}
	if level, _ := DefaultThinkingLevelFor("commandcode", "oc-model"); level != "" {
		t.Errorf("a different model inherited a level: %q", level)
	}

	// Changing one pair must leave every other pair alone.
	if err := SetDefaultThinkingLevelFor("commandcode", "cc-model", "low"); err != nil {
		t.Fatal(err)
	}
	if level, _ := DefaultThinkingLevelFor("opencode-go", "oc-model"); level != "max" {
		t.Errorf("changing one pair moved another: %q", level)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "thinking_level") {
		t.Errorf("per-model save wrote the shared thinking_level:\n%s", text)
	}
	for _, want := range []string{
		"[thinking_defaults]",
		`"commandcode/cc-model" = "low"`,
		`"opencode-go/oc-model" = "max"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
}

func TestSetDefaultModelForMigratesLegacyGlobalThinkingLevel(t *testing.T) {
	path := writeConfig(t, `provider = "commandcode"
model = "cc-model"
thinking_level = "high"

[model_defaults]
commandcode = "cc-model"
opencode-go = "oc-model"
`)
	if err := SetDefaultModelFor("commandcode", "cc-model", "low"); err != nil {
		t.Fatal(err)
	}
	if level, _ := DefaultThinkingLevelFor("commandcode", "cc-model"); level != "low" {
		t.Errorf("commandcode level = %q, want low", level)
	}
	if level, _ := DefaultThinkingLevelFor("opencode-go", "oc-model"); level != "high" {
		t.Errorf("opencode level = %q, want the migrated high", level)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "thinking_level") {
		t.Errorf("legacy thinking_level was not migrated:\n%s", got)
	}
}

func TestSetDefaultThinkingLevelForClearsOnlyOnePair(t *testing.T) {
	writeConfig(t, "")
	if err := SetDefaultThinkingLevelFor("commandcode", "cc", "high"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultThinkingLevelFor("opencode-go", "oc", "max"); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultThinkingLevelFor("commandcode", "cc", ""); err != nil {
		t.Fatal(err)
	}
	if level, _ := DefaultThinkingLevelFor("commandcode", "cc"); level != "" {
		t.Errorf("cleared pair still has %q", level)
	}
	if level, _ := DefaultThinkingLevelFor("opencode-go", "oc"); level != "max" {
		t.Errorf("clearing one pair dropped another: %q", level)
	}
}
