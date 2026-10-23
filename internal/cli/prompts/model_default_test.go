package prompts

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
)

func TestSetDefaultModelTextPersistsModelLevelAndProvider(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	live := &cli.LiveConfig{
		Model:         "old-model",
		ProviderName:  "commandcode",
		ThinkingLevel: agentcore.ThinkingHigh,
		FetchedModels: []string{"new-model"},
	}
	got := SetDefaultModelText("new-model", agentcore.ThinkingHigh, live)
	for _, want := range []string{"default model set to new-model", "provider: commandcode", "think: high"} {
		if !strings.Contains(got, want) {
			t.Errorf("report %q missing %q", got, want)
		}
	}
	if DefaultModelName() != "new-model" {
		t.Errorf("DefaultModelName = %q", DefaultModelName())
	}
	if level, _ := config.DefaultThinkingLevelFor("commandcode", "new-model"); level != "high" {
		t.Errorf("scoped level = %q, want high", level)
	}
	if DefaultProviderName() != "commandcode" {
		t.Errorf("DefaultProviderName = %q", DefaultProviderName())
	}
}

func TestSetDefaultThinkingTextLeavesTheModel(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := config.SetDefaultModel("m", "low", "commandcode"); err != nil {
		t.Fatal(err)
	}
	got := SetDefaultThinkingText("commandcode", "m", agentcore.ThinkingMax)
	if !strings.Contains(got, "max") {
		t.Errorf("report = %q", got)
	}
	if level, _ := config.DefaultThinkingLevelFor("commandcode", "m"); level != "max" {
		t.Errorf("scoped level = %q, want max", level)
	}
	if level, _ := config.DefaultThinkingLevelFor("commandcode", "other"); level != "" {
		t.Errorf("other model inherited level %q", level)
	}
	if DefaultModelName() != "m" || DefaultProviderName() != "commandcode" {
		t.Errorf("setting the level changed the model/provider: %q / %q",
			DefaultModelName(), DefaultProviderName())
	}
}

func TestDefaultThinkingLevelForProviderModelIsScoped(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := config.SetDefaultThinkingLevelFor("openai", "m-a", "high"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetDefaultThinkingLevelFor("commandcode", "m-a", "low"); err != nil {
		t.Fatal(err)
	}
	if got := DefaultThinkingLevelForProviderModel("openai", "m-a"); got != agentcore.ThinkingHigh {
		t.Errorf("openai/m-a = %q, want high", got)
	}
	if got := DefaultThinkingLevelForProviderModel("openai", "m-b"); got != "" {
		t.Errorf("openai/m-b inherited %q", got)
	}
	if got := DefaultThinkingLevelForProviderModel("commandcode", "m-b"); got != "" {
		t.Errorf("commandcode/m-b inherited %q", got)
	}
}

func TestSetDefaultProviderKeepsItsModel(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := config.SetDefaultModel("m", "high", "commandcode"); err != nil {
		t.Fatal(err)
	}
	SetDefaultProviderText("commandcode", nil)
	if DefaultModelName() != "m" {
		t.Errorf("re-setting the same provider dropped its default model: %q", DefaultModelName())
	}
}

func TestSetDefaultModelTextKeepsPerProviderEntries(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	cc := &cli.LiveConfig{ProviderName: "commandcode", FetchedModels: []string{"cc-model"}}
	oc := &cli.LiveConfig{ProviderName: "opencode-go", FetchedModels: []string{"oc-model"}}
	if got := SetDefaultModelText("cc-model", agentcore.ThinkingHigh, cc); !strings.Contains(got, "commandcode") {
		t.Fatalf("cc report = %q", got)
	}
	if got := SetDefaultModelText("oc-model", agentcore.ThinkingLow, oc); !strings.Contains(got, "opencode-go") {
		t.Fatalf("oc report = %q", got)
	}
	if got := DefaultModelForProvider("commandcode"); got != "cc-model" {
		t.Errorf("commandcode default = %q", got)
	}
	if got := DefaultModelForProvider("opencode-go"); got != "oc-model" {
		t.Errorf("opencode default = %q", got)
	}
	if got := DefaultModelName(); got != "oc-model" {
		t.Errorf("top-level model = %q, want the last set", got)
	}
	if got := DefaultProviderName(); got != "opencode-go" {
		t.Errorf("top-level provider = %q, want the last set", got)
	}
}

func TestDefaultModelForProviderFallsBackToTheBuiltIn(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	if got := DefaultModelForProvider("opencode-go"); got != "deepseek-v4.1-flash" {
		t.Errorf("opencode built-in default = %q", got)
	}
	if got := DefaultModelForProvider("commandcode"); got != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("commandcode built-in default = %q", got)
	}
	if got := DefaultModelForProvider("not-a-provider"); got != "" {
		t.Errorf("unknown provider = %q", got)
	}
}
