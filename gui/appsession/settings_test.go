package appsession

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

func TestSaveProviderSettingsUsesNamedCredentialAndCanApply(t *testing.T) {
	c, _, _ := newTestController(t)
	if err := provider.SaveCredential(provider.CredentialFilePath(), "openai-default", "test-key"); err != nil {
		t.Fatal(err)
	}

	got, err := c.SaveProviderSettings(context.Background(), ProviderSettingsEdit{
		Provider:       "openai",
		Model:          "gpt-test",
		Protocol:       "openai/resp_api",
		CredentialName: "openai-default",
		ThinkingLevel:  "high",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai" || got.Model != "gpt-test" || !got.Credential.Available ||
		got.Credential.Source != "named" || got.Credential.Name != "openai-default" {
		t.Fatalf("settings = %+v", got)
	}
	snap := c.Snapshot()
	if snap.Provider != "openai" || snap.Model != "gpt-test" || snap.Thinking != "high" {
		t.Fatalf("snapshot = %+v", snap.Status)
	}
	cfg, err := config.LoadFileConfig(config.FileConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Credential != "openai-default" || cfg.APIKey != "" || cfg.Protocol != "openai/resp_api" {
		t.Fatalf("file config = %+v", cfg)
	}
}

func TestSaveProviderSettingsMigratesLegacyLiteralKey(t *testing.T) {
	c, _, _ := newTestController(t)
	configPath := config.FileConfigPath()
	if err := os.WriteFile(configPath, []byte("provider = \"openai\"\nmodel = \"gpt-test\"\napi_key = \"legacy-plain-key\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.SaveProviderSettings(context.Background(), ProviderSettingsEdit{
		Provider: "openai", Model: "gpt-test", ThinkingLevel: "high",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Credential.Name != "openai-default" || !got.Credential.Available {
		t.Fatalf("credential = %+v", got.Credential)
	}
	cfg, err := config.LoadFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "" || cfg.Credential != "openai-default" {
		t.Fatalf("migrated config = %+v", cfg)
	}
	raw, err := os.ReadFile(provider.CredentialFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("legacy-plain-key")) {
		t.Fatalf("credential remained plaintext: %q", raw)
	}
	if value, err := provider.ResolveCredentialReference(provider.CredentialFilePath(), "openai-default"); err != nil || value != "legacy-plain-key" {
		t.Fatalf("resolved migrated credential = %q, %v", value, err)
	}
}

func TestSaveProviderSettingsRejectsRemoteHTTPAndUnknownCredential(t *testing.T) {
	c, _, _ := newTestController(t)
	_, err := c.SaveProviderSettings(context.Background(), ProviderSettingsEdit{
		Provider: "openai", Model: "gpt-test", BaseURL: "http://example.com/v1",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("remote HTTP error = %v", err)
	}

	_, err = c.SaveProviderSettings(context.Background(), ProviderSettingsEdit{
		Provider: "openai", Model: "gpt-test", CredentialName: "not-stored",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "not stored") {
		t.Fatalf("missing credential error = %v", err)
	}
}

func TestProviderSettingsAllowsLoopbackHTTP(t *testing.T) {
	edit, _, _, err := normalizeProviderEdit(ProviderSettingsEdit{
		Provider: "openai", Model: "gpt-test", BaseURL: "http://127.0.0.1:8080/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if edit.BaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("base URL = %q", edit.BaseURL)
	}
}
