package mygoui

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/session"
)

func clearSettingsEndpointEnv(t *testing.T, name string) provider.ProviderSpec {
	t.Helper()
	spec, ok := provider.LookupProviderSpec(name)
	if !ok {
		t.Fatalf("missing provider %q", name)
	}
	for _, env := range provider.BaseURLEnvVars(spec) {
		t.Setenv(env, "")
	}
	return spec
}

func TestProviderSettingsShowDefaultURLWithoutSavingOverride(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	openai := clearSettingsEndpointEnv(t, "openai")
	anthropic := clearSettingsEndpointEnv(t, "anthropic")
	a := testApp()
	a.installSettings(appsession.ProviderSettings{Provider: "openai", Model: "custom-model", Providers: []string{"openai", "anthropic"}})
	tt := ui.NewTester(a.providerSettingsView, 1000, 1100)
	if a.settingsEdit.BaseURL != "" || !tt.HasText("Provider default: "+openai.DefaultBaseURL+" (environment and protocol may override it).") {
		t.Fatalf("default endpoint should be visible but not saved: edit=%+v texts=%q", a.settingsEdit, tt.Texts())
	}
	if err := tt.Click("Provider Base URL"); err != nil {
		t.Fatal(err)
	}
	tt.Type("https://custom.example/v1")
	if a.settingsEdit.BaseURL != "https://custom.example/v1" {
		t.Fatalf("custom endpoint was not editable: %q", a.settingsEdit.BaseURL)
	}
	a.installSettings(appsession.ProviderSettings{Provider: "anthropic", Model: anthropic.DefaultModel, Providers: []string{"openai", "anthropic"}})
	tt.Frame()
	if a.settingsEdit.BaseURL != "" || !tt.HasText("Provider default: "+anthropic.DefaultBaseURL+" (environment and protocol may override it).") {
		t.Fatalf("provider switch did not update default endpoint: edit=%+v texts=%q", a.settingsEdit, tt.Texts())
	}
}

func TestSavingDisplayedDefaultPreservesEnvironmentPrecedence(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_WIRE_PROBE", "off")
	spec := clearSettingsEndpointEnv(t, "openai")
	t.Setenv("OPENAI_BASE_URL", "https://env-gateway.example/v1")
	if err := config.SetProviderDefaults(config.ProviderDefaults{Provider: spec.Name, Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := appsession.New(appsession.Options{ProviderName: spec.Name, Model: "gpt-test", Cwd: t.TempDir(), Store: store})
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	settings, err := c.LoadProviderSettings()
	if err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.ctrl = c
	a.installSettings(settings)
	a.saveSettings(false)
	if a.settingsErr != "" {
		t.Fatal(a.settingsErr)
	}
	cfg, err := config.LoadFileConfig(config.FileConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved := cfg.ProviderProfiles[spec.Name].BaseURL; saved != "" || provider.ResolveBaseURL(spec, saved, os.Getenv) != "https://env-gateway.example/v1" {
		t.Fatalf("visual default changed endpoint precedence: saved=%q", saved)
	}
}

func TestProviderSettingsSelectModelAndKeepCustomEntry(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	spec := clearSettingsEndpointEnv(t, "openai")
	provider.StoreModelCatalog(provider.ModelCatalogCacheKey(spec.Name, spec.DefaultBaseURL), []string{"catalog-one", "catalog-two", "catalog-two", "systemone-private"})
	a := testApp()
	a.installSettings(appsession.ProviderSettings{Provider: "openai", Model: "existing-custom", Protocol: "openai", Providers: []string{"openai"}})
	if !slices.Contains(a.settingsModels, "catalog-one") || !slices.Contains(a.settingsModels, "catalog-two") || slices.Contains(a.settingsModels, "systemone-private") {
		t.Fatalf("unexpected model options: %v", a.settingsModels)
	}
	tt := ui.NewTester(a.providerSettingsView, 1000, 1100)
	if err := tt.Click("Choose default model"); err != nil {
		t.Fatalf("open model dropdown: %v; texts=%q", err, tt.Texts())
	}
	if err := tt.Click("catalog-two"); err != nil {
		t.Fatalf("select model: %v; texts=%q", err, tt.Texts())
	}
	if a.settingsEdit.Model != "catalog-two" || a.settingsEdit.Protocol != "" {
		t.Fatalf("selected model or protocol override is wrong: %+v", a.settingsEdit)
	}
	if err := tt.Click("Default model ID"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("vendor/custom-model")
	if a.settingsEdit.Model != "vendor/custom-model" {
		t.Fatalf("custom model entry was lost: %q", a.settingsEdit.Model)
	}
}

func TestProviderModelDropdownWorksInsideSettingsModal(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	spec := clearSettingsEndpointEnv(t, "openai")
	provider.StoreModelCatalog(provider.ModelCatalogCacheKey(spec.Name, spec.DefaultBaseURL), []string{"modal-model-one", "modal-model-two"})
	a := testApp()
	a.installSettings(appsession.ProviderSettings{Provider: spec.Name, Model: "initial-model", Providers: []string{spec.Name}})
	a.settingsOpen = true
	tt := ui.NewTester(a.view, 1200, 800)
	if err := tt.Click("Choose default model"); err != nil {
		t.Fatalf("open dropdown in settings modal: %v", err)
	}
	if err := tt.Click("modal-model-two"); err != nil || a.settingsEdit.Model != "modal-model-two" {
		t.Fatalf("select model in settings modal: model=%q err=%v", a.settingsEdit.Model, err)
	}
}

func TestProviderSettingsModelSuggestionsFollowEndpoint(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	spec := clearSettingsEndpointEnv(t, "openai")
	defaultKey := provider.ModelCatalogCacheKey(spec.Name, spec.DefaultBaseURL)
	customURL := "https://gateway.example/v1"
	provider.StoreModelCatalog(defaultKey, []string{"default-endpoint-model"})
	provider.StoreModelCatalog(provider.ModelCatalogCacheKey(spec.Name, customURL), []string{"custom-endpoint-model"})
	defaults := settingsModelSuggestions(spec.Name, "")
	custom := settingsModelSuggestions(spec.Name, customURL)
	if !slices.Contains(defaults, "default-endpoint-model") || slices.Contains(defaults, "custom-endpoint-model") ||
		!slices.Contains(custom, "custom-endpoint-model") || slices.Contains(custom, "default-endpoint-model") {
		t.Fatalf("catalogs crossed endpoints: default=%v custom=%v", defaults, custom)
	}
	unknown := settingsModelSuggestions(spec.Name, "https://unseen.example/v1")
	if slices.Contains(unknown, "default-endpoint-model") || slices.Contains(unknown, "custom-endpoint-model") || !slices.Contains(unknown, spec.DefaultModel) {
		t.Fatalf("unseen gateway inherited another endpoint's catalog: %v", unknown)
	}
	if got := settingsDefaultBaseURL("openai"); got != spec.DefaultBaseURL || !strings.HasPrefix(got, "https://") {
		t.Fatalf("provider default URL = %q", got)
	}
}

func TestProviderSettingsBundledModelsAreLimitedToDefaultEndpoint(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	spec := clearSettingsEndpointEnv(t, "opencode-go")
	bundled := provider.FallbackModelCatalog(spec.Name)
	if len(bundled) < 2 {
		t.Fatal("expected multiple bundled models for the fixture")
	}
	defaults := settingsModelSuggestions(spec.Name, "")
	for _, id := range bundled {
		if !slices.Contains(defaults, id) {
			t.Errorf("bundled model %q missing", id)
		}
	}
	custom := settingsModelSuggestions(spec.Name, "https://custom.example/v1")
	for _, id := range bundled {
		if id != spec.DefaultModel && slices.Contains(custom, id) {
			t.Fatalf("bundled model %q leaked into custom gateway suggestions", id)
		}
	}
}
