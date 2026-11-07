package mygoui

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestComposerModelButtonOpensCombinedPicker(t *testing.T) {
	a := testApp()
	a.providers = []string{"test-provider", "configured-provider"}
	a.modelsReady = true
	a.models = []appsession.ModelOption{{ID: "test-model", Provider: "test-provider", Current: true}}
	tt := ui.NewTester(a.view, 1200, 800)
	label := "test-provider · test-model · off"
	if err := tt.Click(label); err != nil || !a.modelOpen {
		t.Fatalf("combined model button did not open: open=%t err=%v", a.modelOpen, err)
	}
	for _, want := range []string{"Provider, model & reasoning", "Provider", "Reasoning effort", "test-model"} {
		if !tt.HasText(want) {
			t.Fatalf("picker missing %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("test-provider"); err != nil {
		t.Fatalf("open provider selector: %v", err)
	}
	if !tt.HasText("configured-provider") {
		t.Fatalf("configured provider missing from selector: %q", tt.Texts())
	}
}

func TestComposerPickerSwitchesConfiguredProvider(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "configured-deepseek")
	c := appsession.New(appsession.Options{Model: "gpt-test", ProviderName: "openai", Cwd: t.TempDir()})
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	a := testApp()
	a.ctrl, a.status = c, c.Snapshot().Status
	a.modelsReady = true
	a.models = []appsession.ModelOption{{ID: "gpt-test", Provider: "openai", Current: true}}
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("openai · gpt-test · off"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("openai"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("deepseek"); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Status; got.Provider != "deepseek" || got.Model != appsession.ModelForProvider("deepseek") {
		t.Fatalf("provider selection did not update session: %+v", got)
	}
	if a.status.Provider != "deepseek" || a.modelsReady || len(a.models) != 0 {
		t.Fatalf("provider selection did not update view and clear old models: status=%+v models=%+v", a.status, a.models)
	}
}

func TestModelResultsIgnoreOlderRequestsAndProviders(t *testing.T) {
	c := appsession.New(appsession.Options{Model: "test-model", ProviderName: "openai", Cwd: t.TempDir()})
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	a := testApp()
	a.ctrl, a.modelRequest = c, 2
	stale := []appsession.ModelOption{{ID: "stale"}}
	a.applyModelResults(c, 1, "openai", stale, nil)
	if len(a.models) != 0 || a.modelsReady {
		t.Fatal("older model request overwrote the current list")
	}
	a.applyModelResults(c, 2, "deepseek", stale, nil)
	if len(a.models) != 0 || a.modelsReady {
		t.Fatal("another provider's models overwrote the current list")
	}
	want := []appsession.ModelOption{{ID: "current"}}
	a.applyModelResults(c, 2, "openai", want, nil)
	if !a.modelsReady || len(a.models) != 1 || a.models[0].ID != "current" {
		t.Fatalf("current model result was not applied: %+v", a.models)
	}
}
