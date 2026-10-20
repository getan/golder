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
	if DefaultThinkingLevelName() != "high" {
		t.Errorf("DefaultThinkingLevelName = %q", DefaultThinkingLevelName())
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
	got := SetDefaultThinkingText(agentcore.ThinkingMax)
	if !strings.Contains(got, "max") {
		t.Errorf("report = %q", got)
	}
	if DefaultThinkingLevelName() != "max" {
		t.Errorf("level = %q", DefaultThinkingLevelName())
	}
	if DefaultModelName() != "m" || DefaultProviderName() != "commandcode" {
		t.Errorf("setting the level changed the model/provider: %q / %q",
			DefaultModelName(), DefaultProviderName())
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
