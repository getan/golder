package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/cli/config"
)

func TestDownloadProxySettingsShowSavedRouteNotUnsavedSuggestion(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	a := testApp()
	a.loadProxySettings()
	if a.proxyErr != "" || a.proxySettings.URL != appsession.DefaultProxyURL {
		t.Fatalf("cannot load suggested address: %s", a.proxyErr)
	}
	tt := ui.NewTester(a.downloadProxySettingsView, 800, 400)
	if !tt.HasText("Route: Default/environment network") || !tt.HasText("Source: No Golder proxy configured") || tt.HasText("Route: Proxy") {
		t.Fatalf("unsaved default was shown as active: %q", tt.Texts())
	}
	a.saveProxySettings()
	tt.Frame()
	if a.proxyErr != "" || !tt.HasText("Route: Proxy · "+appsession.DefaultProxyURL) || !tt.HasText("Source: Saved in Golder Settings") || a.proxySettings.Browser {
		t.Fatalf("saved GUI address not reflected independently of browser switches: err=%s texts=%q", a.proxyErr, tt.Texts())
	}
	a.proxySettings.URL = "http://127.0.0.1:9999"
	tt.Frame()
	if !tt.HasText("Route: Proxy · "+appsession.DefaultProxyURL) || tt.HasText("Route: Proxy · http://127.0.0.1:9999") {
		t.Fatal("editing the field changed the displayed effective route without saving")
	}
	a.saveProxySettings()
	tt.Frame()
	if a.proxyErr != "" || !tt.HasText("Route: Proxy · http://127.0.0.1:9999") {
		t.Fatalf("new saved address did not update the route: %s %q", a.proxyErr, tt.Texts())
	}
}

func TestDownloadProxySettingsIdentifySanitizedEnvironmentOverride(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "http://operator:private-value@127.0.0.1:8888/hidden-path")
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: "http://127.0.0.1:7897", Providers: []string{}}); err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.loadProxySettings()
	tt := ui.NewTester(a.downloadProxySettingsView, 800, 400)
	if a.proxyErr != "" || !tt.HasText("Route: Proxy · http://127.0.0.1:8888") || !tt.HasText("Source: Environment override (GOLDER_PROXY)") {
		t.Fatalf("environment source/address not explained: err=%s texts=%q", a.proxyErr, tt.Texts())
	}
	text := strings.Join(tt.Texts(), "\n")
	if strings.Contains(text, "private-value") || strings.Contains(text, "hidden-path") || strings.Contains(text, "operator:") {
		t.Fatal("environment credentials or private URL path leaked into the GUI")
	}
	// Saving the field must not claim that an environment override went away.
	a.proxySettings.URL = "http://127.0.0.1:9999"
	a.saveProxySettings()
	tt.Frame()
	if a.proxyErr != "" || !tt.HasText("Route: Proxy · http://127.0.0.1:8888") || !tt.HasText("Source: Environment override") {
		t.Fatalf("save incorrectly hid the environment override: %s %q", a.proxyErr, tt.Texts())
	}
}

func TestDownloadProxySettingsExplainInvalidOverride(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "bad-proxy")
	a := testApp()
	a.loadProxySettings()
	tt := ui.NewTester(a.downloadProxySettingsView, 800, 400)
	if a.proxyErr != "" || !tt.HasText("Route: Blocked by invalid proxy settings") || !tt.HasText("Source: Environment override") || tt.HasText("Route: Proxy ·") {
		t.Fatalf("invalid environment override was shown as a working route: %s %q", a.proxyErr, tt.Texts())
	}
}
