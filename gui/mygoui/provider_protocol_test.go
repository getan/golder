package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestProviderSettingsProtocolSelector(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	a := testApp()
	a.installSettings(appsession.ProviderSettings{
		Provider: "opencode-go", Model: "gemini-4-argon-contributor", Protocol: "openai",
		Providers: []string{"opencode-go"},
	})
	tt := ui.NewTester(a.providerSettingsView, 1000, 1100)
	if !tt.HasText("Default model protocol") || !tt.HasText("Other models use Auto.") || !tt.HasText("openai/chat") {
		t.Fatalf("legacy Chat selection or scope hint missing: %q", tt.Texts())
	}
	if err := tt.Click("openai/chat"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Auto"); err != nil {
		t.Fatal(err)
	}
	if a.settingsEdit.Protocol != "" || !tt.HasText("Expected protocol: gemini") {
		t.Fatalf("Auto did not clear the override or preview Gemini: protocol=%q, texts=%q", a.settingsEdit.Protocol, tt.Texts())
	}
	if err := tt.Click("Auto"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("openai/chat"); err != nil {
		t.Fatal(err)
	}
	if a.settingsEdit.Protocol != "openai/chat" {
		t.Fatalf("manual choice not recorded: %q", a.settingsEdit.Protocol)
	}
}
