package appsession

import (
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

func proxyTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
}

func setProxyProvider(t *testing.T, settings *ProxySettings, name string, enabled bool) {
	t.Helper()
	for i := range settings.Providers {
		if settings.Providers[i].Name == name {
			settings.Providers[i].Enabled = enabled
			return
		}
	}
	t.Fatalf("provider %q is missing", name)
}

func TestProxySettingsDefaultDoesNotEnableRoutes(t *testing.T) {
	proxyTestHome(t)
	settings, err := LoadProxySettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.URL != "http://127.0.0.1:7897" || settings.Browser {
		t.Fatalf("unexpected defaults: %+v", settings)
	}
	if _, err := SaveProxySettings(settings); err != nil {
		t.Fatal(err)
	}
	for _, spec := range provider.ProviderSpecs() {
		if provider.NeedsProxy(spec.Name, spec.DefaultBaseURL) {
			t.Fatalf("saving the suggested URL unexpectedly enabled %s", spec.Name)
		}
	}
	if route, err := provider.BrowserProxyServer(); err != nil || route != "" {
		t.Fatalf("browser should remain direct: %q %v", route, err)
	}
}

func TestProxySettingsRoutesAreIndependentAndSharedWithCLI(t *testing.T) {
	proxyTestHome(t)
	settings, err := LoadProxySettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.URL, settings.Browser = "http://127.0.0.1:7897", true
	setProxyProvider(t, &settings, "openai", true)
	settings, err = SaveProxySettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if route, err := provider.BrowserProxyServer(); err != nil || route != settings.URL {
		t.Fatalf("browser route = %q, %v", route, err)
	}
	transport := provider.HTTPClientFor("openai", "https://api.openai.com").Transport.(*http.Transport)
	request, _ := http.NewRequest("GET", "https://api.openai.com/", nil)
	proxy, err := transport.Proxy(request)
	if err != nil || proxy == nil || proxy.String() != settings.URL {
		t.Fatalf("provider route = %v, %v", proxy, err)
	}
	settings.Browser = false
	settings, err = SaveProxySettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.LoadProxyConfig()
	if err != nil || raw.Browser || !slices.Contains(raw.Providers, "openai") {
		t.Fatalf("browser toggle changed provider selection: %+v, %v", raw, err)
	}
	info, err := os.Stat(config.ProxyConfigPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("proxy settings are not owner-only: %v, %v", info, err)
	}
}

func TestProxySettingsPreserveUnchangedLegacyProviderSelection(t *testing.T) {
	proxyTestHome(t)
	t.Setenv("GOLDER_PROXY", "http://127.0.0.1:7897")
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: "http://localhost:7890"}); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadProxySettings()
	if err != nil || !settings.EnvOverride || settings.EffectiveURL != "http://127.0.0.1:7897" {
		t.Fatalf("environment override: %+v, %v", settings, err)
	}
	settings.Browser = true
	if _, err := SaveProxySettings(settings); err != nil {
		t.Fatal(err)
	}
	raw, err := config.LoadProxyConfig()
	if err != nil || !raw.Browser || raw.Providers != nil {
		t.Fatalf("browser-only change altered legacy provider defaults: %+v %v", raw, err)
	}
}

func TestProxySettingsPreserveUnknownSelectedProvider(t *testing.T) {
	proxyTestHome(t)
	want := []string{"legacy-provider", "openai"}
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: "http://localhost:7890", Providers: want}); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadProxySettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.Browser = true
	settings, err = SaveProxySettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.LoadProxyConfig()
	if err != nil || !slices.Equal(raw.Providers, want) {
		t.Fatalf("untouched provider order or unknown selection changed: %+v, %v", raw, err)
	}
	setProxyProvider(t, &settings, "openai", false)
	if _, err := SaveProxySettings(settings); err != nil {
		t.Fatal(err)
	}
	raw, err = config.LoadProxyConfig()
	if err != nil || !slices.Contains(raw.Providers, "legacy-provider") || slices.Contains(raw.Providers, "openai") {
		t.Fatalf("unknown provider was not preserved: %+v, %v", raw, err)
	}
}

func TestProxySettingsFirstSaveFreezesDisplayedLegacySelection(t *testing.T) {
	proxyTestHome(t)
	t.Setenv("GOLDER_PROXY", "http://localhost:7897")
	settings, err := LoadProxySettings()
	if err != nil {
		t.Fatal(err)
	}
	var displayed []string
	for _, option := range settings.Providers {
		if option.Enabled {
			displayed = append(displayed, option.Name)
		}
	}
	settings.Browser = true
	if _, err := SaveProxySettings(settings); err != nil {
		t.Fatal(err)
	}
	raw, err := config.LoadProxyConfig()
	if err != nil || raw.Providers == nil {
		t.Fatalf("first saved URL left provider routing implicit: %+v, %v", raw, err)
	}
	for _, name := range displayed {
		if !slices.Contains(raw.Providers, name) {
			t.Fatalf("displayed provider %s lost during save", name)
		}
	}
}

func TestProxySettingsRejectInvalidUnsafeAndConcurrentChanges(t *testing.T) {
	proxyTestHome(t)
	settings, err := LoadProxySettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"not-a-url", "http://user:secret@127.0.0.1:7890", "http://127.0.0.1:7890/path"} {
		edit := settings
		edit.URL, edit.Browser = address, true
		if _, err := SaveProxySettings(edit); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe URL %q accepted or leaked: %v", address, err)
		}
	}
	if err := config.SaveProxyConfig(config.ProxyConfig{URL: "http://localhost:8888", Providers: []string{"openai"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProxySettings(settings); err == nil || !strings.Contains(err.Error(), "changed elsewhere") {
		t.Fatalf("concurrent edit was not rejected: %v", err)
	}
}

func TestProxySettingsDoNotExposeEnvironmentCredentials(t *testing.T) {
	proxyTestHome(t)
	t.Setenv("GOLDER_PROXY", "http://user:secret@127.0.0.1:7890")
	settings, err := LoadProxySettings()
	if err != nil || strings.Contains(settings.EffectiveURL, "secret") {
		t.Fatalf("unsafe environment display: %+v, %v", settings, err)
	}
	settings.Browser = true
	if _, err := SaveProxySettings(settings); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("browser accepted credential-bearing override: %v", err)
	}
}

func TestProxySettingsHideSensitiveEnvironmentPath(t *testing.T) {
	proxyTestHome(t)
	t.Setenv("GOLDER_PROXY", "http://user:secret@127.0.0.1:7890/private-token")
	settings, err := LoadProxySettings()
	if err != nil || settings.EffectiveURL != "http://127.0.0.1:7890" {
		t.Fatalf("environment display was not redacted: %q, %v", settings.EffectiveURL, err)
	}
}

func TestProxySettingsInvalidOverrideCannotEnableProvider(t *testing.T) {
	proxyTestHome(t)
	t.Setenv("GOLDER_PROXY", "bad-proxy")
	settings, err := LoadProxySettings()
	if err != nil || settings.EffectiveURL != "(invalid URL)" {
		t.Fatalf("invalid override display: %q, %v", settings.EffectiveURL, err)
	}
	setProxyProvider(t, &settings, "openai", true)
	if _, err := SaveProxySettings(settings); err == nil || !strings.Contains(err.Error(), "GOLDER_PROXY") {
		t.Fatalf("invalid override was accepted: %v", err)
	}
}
