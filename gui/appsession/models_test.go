package appsession

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

func clearProviderEnvironment(t *testing.T) {
	t.Helper()
	seen := map[string]bool{}
	for _, spec := range provider.ProviderSpecs() {
		for _, name := range append(append([]string(nil), spec.EnvVars...), provider.BaseURLEnvVars(spec)...) {
			if name != "" && !seen[name] {
				t.Setenv(name, "")
				seen[name] = true
			}
		}
	}
}

func TestProvidersOnlyIncludesConfigured(t *testing.T) {
	clearProviderEnvironment(t)
	c, _, _ := newTestController(t)
	t.Setenv("OPENAI_API_KEY", "configured-openai")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic.example")
	if err := provider.SaveCredential(provider.CredentialFilePath(), "deepseek-default", "configured-deepseek"); err != nil {
		t.Fatal(err)
	}
	providers := c.Providers()
	for _, name := range []string{"test-provider", "openai", "deepseek", "anthropic"} {
		if !slices.Contains(providers, name) {
			t.Errorf("configured provider %s missing from %v", name, providers)
		}
	}
	if slices.Contains(providers, "groq") {
		t.Errorf("unconfigured provider included: %v", providers)
	}
}

func TestSelectProviderClearsPreviousEndpointAndUsesNamedCredential(t *testing.T) {
	clearProviderEnvironment(t)
	c, _, _ := newTestController(t)
	if err := provider.SaveCredential(provider.CredentialFilePath(), "deepseek-default", "deepseek-key"); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.live.BaseURL = "https://old-provider.example/v1"
	c.live.Protocol = "anthropic"
	c.live.FetchedModels = []string{"old-model"}
	c.live.FetchedKey = "old"
	c.mu.Unlock()
	if err := c.SelectProvider(context.Background(), "deepseek"); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	if snap.Provider != "deepseek" || snap.Model != ModelForProvider("deepseek") {
		t.Fatalf("provider switch = %+v", snap.Status)
	}
	c.mu.Lock()
	baseURL, protocol, fetched := c.live.BaseURL, c.live.Protocol, c.live.FetchedModels
	key := c.creds.GetAPIKey(context.Background(), "deepseek")
	c.mu.Unlock()
	if baseURL != "" || protocol != "" || len(fetched) != 0 {
		t.Fatalf("previous provider overrides leaked: base=%q protocol=%q fetched=%v", baseURL, protocol, fetched)
	}
	if key != "deepseek-key" {
		t.Fatal("the selected provider's named credential was not installed")
	}
	if err := c.SelectProvider(context.Background(), "anthropic"); err == nil {
		t.Fatal("unconfigured provider was accepted")
	}
	if c.Snapshot().Provider != "deepseek" {
		t.Fatal("failed selection changed provider")
	}
}

func TestSelectProviderRejectsRunningSession(t *testing.T) {
	clearProviderEnvironment(t)
	c, _, _ := newTestController(t)
	t.Setenv("OPENAI_API_KEY", "configured-openai")
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
	err := c.SelectProvider(context.Background(), "openai")
	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("switch during run = %v, want ErrBusy", err)
	}
}

func TestSelectProviderRestoresConfiguredEndpointAndCredential(t *testing.T) {
	clearProviderEnvironment(t)
	c, _, _ := newTestController(t)
	if err := provider.SaveCredential(provider.CredentialFilePath(), "custom-openai", "openai-key"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetProviderDefaults(config.ProviderDefaults{
		Provider: "openai", Model: "gpt-test", BaseURL: "https://openai.example/v1",
		Protocol: "openai/resp_api", Credential: "custom-openai",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SelectProvider(context.Background(), "openai"); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	baseURL, protocol, model := c.live.BaseURL, c.live.Protocol, c.live.Model
	key := c.creds.GetAPIKey(context.Background(), "openai")
	c.mu.Unlock()
	if baseURL != "https://openai.example/v1" || protocol != "openai/resp_api" || model != "gpt-test" || key != "openai-key" {
		t.Fatalf("configured provider was not restored: base=%q protocol=%q model=%q credentialPresent=%t", baseURL, protocol, model, key == "openai-key")
	}
}
