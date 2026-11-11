package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/provider"
)

func TestProviderProfilesRestoreConnectionAndScopeProtocol(t *testing.T) {
	clearProviderEnvironment(t)
	t.Setenv("GOLDER_WIRE_PROBE", "off")
	c, _, _ := newTestController(t)
	ctx := context.Background()
	const (
		defaultModel = "deepseek-v4.1-flash"
		geminiModel  = "gemini-4-argon-contributor"
		goURL        = "https://opencode.example/v1"
	)
	for _, edit := range []ProviderSettingsEdit{
		{Provider: "opencode-go", Model: defaultModel, BaseURL: goURL, Protocol: "openai/chat", NewCredential: "fixture-go-key"},
		{Provider: "commandcode", Model: "deepseek/deepseek-v4.1-flash", BaseURL: "https://commandcode.example/v1", NewCredential: "fixture-cc-key"},
	} {
		got, err := c.SaveProviderSettings(ctx, edit, false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Credential.Name != edit.Provider+"-default" || !got.Credential.Available {
			t.Fatalf("credential reference for %s: %+v", edit.Provider, got.Credential)
		}
	}
	got, err := c.LoadProviderSettingsFor("opencode-go")
	if err != nil || got.BaseURL != goURL || got.Model != defaultModel || got.Protocol != provider.ProtocolOpenAI || got.Credential.Name != "opencode-go-default" {
		t.Fatalf("saved OpenCode settings not restored: %+v, %v", got, err)
	}
	if err := c.SelectProvider(ctx, "opencode-go"); err != nil {
		t.Fatal(err)
	}
	assertLive := func(model, override, effective string) {
		t.Helper()
		c.mu.Lock()
		live := *c.live
		key := c.creds.GetAPIKey(ctx, "opencode-go")
		c.mu.Unlock()
		if live.Model != model || live.BaseURL != goURL || live.Protocol != override || key != "fixture-go-key" ||
			provider.EffectiveProtocol(live.ProviderName, live.Model, live.Protocol) != effective {
			t.Fatalf("unexpected live model/endpoint/protocol: %q %q %q", live.Model, live.BaseURL, live.Protocol)
		}
	}
	assertLive(defaultModel, provider.ProtocolOpenAI, provider.ProtocolOpenAI)
	if err := c.SelectModel(ctx, "opencode-go", geminiModel, ""); err != nil {
		t.Fatal(err)
	}
	assertLive(geminiModel, "", provider.ProtocolGemini)
	if err := c.SelectModel(ctx, "opencode-go", defaultModel, ""); err != nil {
		t.Fatal(err)
	}
	assertLive(defaultModel, provider.ProtocolOpenAI, provider.ProtocolOpenAI)

	// Save without applying: the old live override must not reappear in Settings.
	got, err = c.SaveProviderSettings(ctx, ProviderSettingsEdit{
		Provider: "opencode-go", Model: defaultModel, BaseURL: goURL, CredentialName: "opencode-go-default",
	}, false)
	if err != nil || got.Protocol != "" {
		t.Fatalf("saving Auto did not clear the default override: %q, %v", got.Protocol, err)
	}
}
