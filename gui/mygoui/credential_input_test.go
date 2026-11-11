package mygoui

import (
	"bytes"
	"context"
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/session"
)

func credentialInputPixels(t *testing.T, tt *ui.Tester, label string) []byte {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("input %q not found: %q", label, tt.Texts())
	}
	img := tt.Image()
	box := image.Rect(int(math.Floor(float64(r.X))), int(math.Floor(float64(r.Y))), int(math.Ceil(float64(r.X+r.W))), int(math.Ceil(float64(r.Y+r.H)))).Intersect(img.Bounds())
	if box.Empty() {
		t.Fatalf("input %q is outside the frame: %+v", label, r)
	}
	var pixels []byte
	for y := box.Min.Y; y < box.Max.Y; y++ {
		start := img.PixOffset(box.Min.X, y)
		pixels = append(pixels, img.Pix[start:start+box.Dx()*4]...)
	}
	return pixels
}

func TestCredentialPlaceholdersFollowAvailableIdentity(t *testing.T) {
	saved := appsession.ProviderSettings{Provider: "openai", Credential: appsession.CredentialView{Available: true, Source: "named", Name: "openai-default"}}
	edit := appsession.ProviderSettingsEdit{Provider: "openai", CredentialName: "openai-default"}
	if !providerCredentialAvailable(saved, edit) || credentialPlaceholder(true, "empty") != credentialMaskPlaceholder {
		t.Fatal("available provider credential must show a fixed mask")
	}
	for _, changed := range []appsession.ProviderSettingsEdit{
		{Provider: "anthropic", CredentialName: "openai-default"},
		{Provider: "openai", CredentialName: "other"},
		{Provider: "openai", CredentialName: "openai-default", RemoveCredential: true},
	} {
		if providerCredentialAvailable(saved, changed) {
			t.Fatalf("old credential status applied to a different selection: %+v", changed)
		}
	}
	saved.Credential.Available = false
	if providerCredentialAvailable(saved, edit) || credentialPlaceholder(false, "empty") != "empty" {
		t.Fatal("missing provider credential must not show a mask")
	}
	for _, source := range []string{"environment", "explicit"} {
		inherited := appsession.ProviderSettings{Provider: "openai", Credential: appsession.CredentialView{Available: true, Source: source}}
		if !providerCredentialAvailable(inherited, appsession.ProviderSettingsEdit{Provider: "openai"}) {
			t.Errorf("%s credential should show as available", source)
		}
	}
	lark := appsession.LarkExtensionSettings{CredentialName: "lark-app-secret", CredentialAvailable: true}
	if !larkCredentialAvailable(lark, appsession.LarkExtensionEdit{CredentialName: " lark-app-secret "}) ||
		larkCredentialAvailable(lark, appsession.LarkExtensionEdit{CredentialName: "different"}) ||
		larkCredentialAvailable(appsession.LarkExtensionSettings{CredentialAvailable: true}, appsession.LarkExtensionEdit{}) {
		t.Fatal("Lark mask must describe only the available saved reference")
	}
}

func TestProviderCredentialMaskIsVisualAndWriteOnly(t *testing.T) {
	a := testApp()
	a.settings = appsession.ProviderSettings{Provider: "openai", Providers: []string{"openai"}, Credential: appsession.CredentialView{Available: true, Source: "named", Name: "openai-default"}}
	a.settingsEdit = appsession.ProviderSettingsEdit{Provider: "openai", CredentialName: "openai-default"}
	tt := ui.NewTester(a.providerSettingsView, 1000, 1100)
	masked := credentialInputPixels(t, tt, "Provider API key")
	if a.settingsEdit.NewCredential != "" || !tt.HasText("Credential available · type a new key") {
		t.Fatal("the mask must not enter the write-only key field")
	}
	a.settings.Credential.Available = false
	tt.Frame()
	if bytes.Equal(masked, credentialInputPixels(t, tt, "Provider API key")) {
		t.Fatal("configured and unconfigured inputs must render differently")
	}
	a.settings.Credential.Available = true
	tt.Frame()
	if err := tt.Click("Provider API key"); err != nil {
		t.Fatal(err)
	}
	tt.Type("provider-replacement")
	if a.settingsEdit.NewCredential != "provider-replacement" || tt.HasText("provider-replacement") {
		t.Fatal("typing must replace the visual placeholder without exposing the new key")
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	if a.settingsEdit.NewCredential != "" {
		t.Fatal("clearing the entry should retain the existing credential")
	}
}

func TestLarkCredentialMaskIsVisualAndWriteOnly(t *testing.T) {
	a := testApp()
	a.extension = appsession.LarkExtensionSettings{ExtensionPackageView: appsession.ExtensionPackageView{Installed: true, Configured: true}, CredentialName: "lark-app-secret", CredentialAvailable: true, AppID: "cli_test"}
	a.extensionEdit = appsession.LarkExtensionEdit{CredentialName: "lark-app-secret", AppID: "cli_test"}
	tt := ui.NewTester(a.larkExtensionCard, 1000, 1500)
	masked := credentialInputPixels(t, tt, "Lark App Secret")
	if a.extensionEdit.NewAppSecret != "" || !tt.HasText("Credential available · type a new secret") {
		t.Fatal("the mask must not enter the write-only App Secret field")
	}
	a.extension.CredentialAvailable = false
	tt.Frame()
	if bytes.Equal(masked, credentialInputPixels(t, tt, "Lark App Secret")) {
		t.Fatal("configured and unconfigured App Secret inputs must render differently")
	}
	a.extension.CredentialAvailable = true
	tt.Frame()
	if err := tt.Click("Lark App Secret"); err != nil {
		t.Fatal(err)
	}
	tt.Type("lark-replacement")
	if a.extensionEdit.NewAppSecret != "lark-replacement" || tt.HasText("lark-replacement") {
		t.Fatal("typing must replace the visual placeholder without exposing the new App Secret")
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	if a.extensionEdit.NewAppSecret != "" {
		t.Fatal("clearing the entry should retain the existing App Secret")
	}
}

func TestProviderCredentialSaveLeavesExistingValueAndAcceptsReplacement(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	path := provider.CredentialFilePath()
	if err := provider.SaveCredential(path, "openai-default", "original-key"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetProviderDefaults(config.ProviderDefaults{Provider: "openai", Model: "gpt-test", Credential: "openai-default"}); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := appsession.New(appsession.Options{ProviderName: "openai", Model: "gpt-test", Cwd: t.TempDir(), Store: store})
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
	ui.NewTester(a.providerSettingsView, 1000, 1100)
	if a.settingsEdit.NewCredential != "" || !providerCredentialAvailable(a.settings, a.settingsEdit) {
		t.Fatal("loaded credential should only appear as a mask")
	}
	a.saveSettings(false)
	if a.settingsErr != "" {
		t.Fatalf("saving unchanged settings: %s", a.settingsErr)
	}
	if got, err := provider.ResolveCredentialReference(path, "openai-default"); err != nil || got != "original-key" {
		t.Fatalf("unchanged save did not preserve the original credential: %v", err)
	}
	a.settingsEdit.NewCredential = "replacement-key"
	a.saveSettings(false)
	if a.settingsErr != "" {
		t.Fatalf("saving replacement: %s", a.settingsErr)
	}
	if got, err := provider.ResolveCredentialReference(path, "openai-default"); err != nil || got != "replacement-key" || a.settingsEdit.NewCredential != "" {
		t.Fatalf("replacement was not saved or cleared from the form: %v", err)
	}
}

func TestProviderSettingsSwitchDoesNotCarryCredentialReference(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := appsession.New(appsession.Options{ProviderName: "openai", Model: "gpt-test", Cwd: t.TempDir(), Store: store})
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	settings, err := c.SaveProviderSettings(context.Background(), appsession.ProviderSettingsEdit{
		Provider: "openai", Model: "gpt-test", BaseURL: "https://openai.example/v1", NewCredential: "fixture-key",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.ctrl = c
	a.installSettings(settings)
	a.settings.Providers = []string{"openai", "opencode-go"}
	a.settingsEdit.NewCredential = "unsaved-key"
	a.settingsEdit.RemoveCredential = true
	tt := ui.NewTester(a.providerSettingsView, 1000, 1100)
	for _, item := range []string{"openai", "opencode-go"} {
		if err := tt.Click(item); err != nil {
			t.Fatal(err)
		}
	}
	e := a.settingsEdit
	if e.Provider != "opencode-go" || e.CredentialName != "" || e.BaseURL != "" || e.NewCredential != "" || e.RemoveCredential {
		t.Fatal("provider selection retained fields or credentials from the previous form")
	}
	for _, item := range []string{"opencode-go", "openai"} {
		if err := tt.Click(item); err != nil {
			t.Fatal(err)
		}
	}
	if a.settingsEdit.CredentialName != "openai-default" || a.settingsEdit.BaseURL != "https://openai.example/v1" {
		t.Fatal("switching back did not restore the saved connection settings")
	}
}
