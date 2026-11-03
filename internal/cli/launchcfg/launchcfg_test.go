package launchcfg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

func TestOverlayUsesSharedFlagFilePrecedence(t *testing.T) {
	file := config.FileConfig{
		Model: "file-model", Provider: "openai", BaseURL: "https://file.invalid",
		Protocol: "responses", APIKey: "test-key", Credential: "ignored", ThinkingLevel: "high",
	}
	got := Overlay(Values{Model: "default", Provider: "openrouter"}, file, func(name string) bool {
		return name == "model"
	})
	if got.Model != "default" || got.Provider != "openai" || got.BaseURL != "https://file.invalid" ||
		got.Protocol != "responses" || got.APIKey != "test-key" || got.CredentialRef != "" || got.ThinkingLevel != "high" {
		t.Fatalf("overlay = %+v", got)
	}
}

func TestResolveCredentialNamedThenEnvironmentFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	t.Setenv("OPENAI_API_KEY", "env-secret")
	if err := provider.SaveCredential(provider.CredentialFilePath(), "openai-default", "named-secret"); err != nil {
		t.Fatal(err)
	}

	got := ResolveCredential("openai", "", "openai-default")
	if got.apiKey != "named-secret" || !got.Status.Available || got.Status.Source != CredentialNamed || got.Status.Name != "openai-default" {
		t.Fatalf("named status = %+v warnings=%v", got.Status, got.Warnings)
	}

	if err := os.Remove(filepath.Join(home, ".credentials.yaml")); err != nil {
		t.Fatal(err)
	}
	got = ResolveCredential("openai", "", "missing")
	if got.apiKey != "" || got.Status.Source != CredentialEnvironment || got.Status.EnvVar != "OPENAI_API_KEY" || len(got.Warnings) != 1 {
		t.Fatalf("fallback status = %+v warnings=%v", got.Status, got.Warnings)
	}
}

func TestResolveCredentialDoesNotExposeEnvironmentValue(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "env-secret-must-not-return")
	got := ResolveCredential("openai", "", "")
	if got.apiKey != "" || got.Status.Source != CredentialEnvironment || got.Status.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("status = %+v warnings=%v", got.Status, got.Warnings)
	}
}
