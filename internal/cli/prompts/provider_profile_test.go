package prompts

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

func TestProviderProfileSlashSwitchKeepsConnectionAndUsesAuto(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_WIRE_PROBE", "off")
	const baseURL = "https://go.example/v1"
	if err := config.SetProviderDefaults(config.ProviderDefaults{
		Provider: "opencode-go", Model: "deepseek-v4.1-flash", Protocol: "openai", BaseURL: baseURL, Credential: "go-test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveCredential(provider.CredentialFilePath(), "go-test", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	live := &cli.LiveConfig{ProviderName: "openai", Model: "gpt-test"}
	creds := provider.NewCredentialStore(nil)
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)
	for _, tc := range []struct{ command, protocol, label string }{
		{"/provider opencode-go", "openai", "openai/chat"},
		{"/model gemini-4-argon-contributor", "", "gemini"},
		{"/model deepseek-v4.1-flash", "openai", "openai/chat"},
	} {
		out, err := reg.ResolveOutcome(tc.command)
		if err != nil || !strings.Contains(out.Message, "protocol: "+tc.label) || live.Protocol != tc.protocol || live.BaseURL != baseURL || creds.GetAPIKey(context.Background(), "opencode-go") != "fixture-key" {
			t.Fatalf("%s did not preserve connection and select %s: %q, %v", tc.command, tc.label, out.Message, err)
		}
	}
}
