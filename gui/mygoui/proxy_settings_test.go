package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/provider"
)

func TestProxySettingsViewSeparatesBrowserAndProviders(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	a := testApp()
	a.loadProxySettings()
	a.settingsOpen = true
	tt := ui.NewTester(a.view, 1200, 900)
	if err := tt.Click("Proxy"); err != nil || a.settingsTab != 5 {
		t.Fatalf("open Proxy tab: tab=%d err=%v", a.settingsTab, err)
	}
	for _, want := range []string{"Proxy URL", "Browser", "Use proxy for Browser", "Providers", "openai", "Save proxy settings"} {
		if !tt.HasText(want) {
			t.Fatalf("proxy settings missing %q: %q", want, tt.Texts())
		}
	}
	if a.proxySettings.URL != appsession.DefaultProxyURL {
		t.Fatalf("default proxy URL = %q", a.proxySettings.URL)
	}
	if err := tt.Click("Proxy address"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("http://127.0.0.1:7890")
	if a.proxySettings.URL != "http://127.0.0.1:7890" {
		t.Fatalf("edited proxy URL = %q", a.proxySettings.URL)
	}
	if err := tt.Click("Use proxy for Browser"); err != nil || !a.proxySettings.Browser {
		t.Fatalf("browser switch: enabled=%t err=%v", a.proxySettings.Browser, err)
	}
	// The effective Downloads route card adds content above Providers. Scroll
	// within Settings, as a person does, before clicking the provider checkbox.
	box, ok := tt.Find("Use proxy for Browser")
	if !ok {
		t.Fatal("browser route checkbox is missing")
	}
	tt.Scroll(box.X+5, box.Y+5, 0, 200)
	if err := tt.Click("openai"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Save proxy settings"); err != nil {
		t.Fatal(err)
	}
	if a.proxyErr != "" {
		t.Fatalf("save failed: %s", a.proxyErr)
	}
	if route, err := provider.BrowserProxyServer(); err != nil || route != "http://127.0.0.1:7890" {
		t.Fatalf("browser route = %q, %v", route, err)
	}
	if !provider.NeedsProxy("openai", "https://api.openai.com") || provider.NeedsProxy("anthropic", "https://api.anthropic.com") {
		t.Fatal("provider switches were not independent")
	}
}
