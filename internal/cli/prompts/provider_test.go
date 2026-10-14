package prompts

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// TestProviderCommandListsEnvVarsAndAvailability verifies the bare /provider
// listing shows every provider with the environment variables it reads and a
// value-free set/not-set marker, grouping ready providers first. The test
// pins the env layer with fake values only (never a real secret).
func TestProviderCommandListsEnvVarsAndAvailability(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_API_KEY", "") // force "not set" regardless of the host env
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	t.Setenv("CLAUDE_API_KEY", "")

	live := &cli.LiveConfig{Model: "deepseek-v4.1-flash", ProviderName: "opencode-go"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/provider")
	if err != nil {
		t.Fatalf("ResolveOutcome /provider: %v", err)
	}
	msg := out.Message
	for _, want := range []string{
		"ready to use (credential found):",
		"needs a key (set one of these):",
		"OPENCODE_API_KEY",
		"DEEPSEEK_API_KEY",
		"ANTHROPIC_OAUTH_TOKEN / ANTHROPIC_API_KEY / CLAUDE_API_KEY",
		"(current)",
		"ollama",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("listing missing %q:\n%s", want, msg)
		}
	}
	// A configured provider must not appear under the missing-key heading.
	readyIdx := strings.Index(msg, "ready to use")
	missingIdx := strings.Index(msg, "needs a key")
	// Locate the opencode-go listing ROW and check its credential marker is
	// the bare "set" (the variable is already shown in its own column).
	// Rows start with two spaces, which skips the "provider: opencode-go"
	// title line above the listing.
	openIdx := strings.Index(msg, "\n  opencode-go")
	openRow := ""
	if openIdx >= 0 {
		rest := msg[openIdx+1:]
		if end := strings.Index(rest, "\n"); end >= 0 {
			openRow = rest[:end]
		}
	}
	if readyIdx < 0 || missingIdx < 0 || openIdx < readyIdx || openIdx > missingIdx ||
		!strings.Contains(openRow, " set") || strings.Contains(openRow, "not set") {
		t.Errorf("opencode-go should be listed in the ready group:\n%s", msg)
	}
}

// TestProviderCommandSwitch verifies /provider <name> switches the live
// provider to that provider's DefaultModel, clears the previous gateway's
// fetched model catalog, and warns when no credential is configured for the
// target.
func TestProviderCommandSwitch(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	live := &cli.LiveConfig{
		Model:         "muse-spark-1.3-contributor",
		ProviderName:  "opencode-go",
		FetchedModels: []string{"muse-spark-1.3-contributor"},
	}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/provider deepseek")
	if err != nil {
		t.Fatalf("ResolveOutcome /provider deepseek: %v", err)
	}
	if live.ProviderName != "deepseek" {
		t.Errorf("live.ProviderName = %q, want deepseek", live.ProviderName)
	}
	if live.Model != "deepseek-v4-flash" {
		t.Errorf("live.Model = %q, want deepseek-v4-flash (the provider default)", live.Model)
	}
	if live.FetchedModels != nil {
		t.Errorf("live.FetchedModels = %v, want nil after a provider switch", live.FetchedModels)
	}
	if !strings.Contains(out.Message, "provider switched to deepseek") {
		t.Errorf("message = %q, want the switch confirmation", out.Message)
	}
	if !strings.Contains(out.Message, "protocol: openai/resp_api") {
		t.Errorf("message = %q, want the effective protocol named", out.Message)
	}
	if !strings.Contains(out.Message, "warning: no credential found") || !strings.Contains(out.Message, "DEEPSEEK_API_KEY") {
		t.Errorf("message = %q, want a missing-credential warning naming the env var", out.Message)
	}
}

// TestProviderCommandClearsEndpointOverride verifies a switch drops the
// previous provider's --base-url/--protocol override instead of leaking it
// into the new provider's driver.
func TestProviderCommandClearsEndpointOverride(t *testing.T) {
	live := &cli.LiveConfig{
		Model:        "m",
		ProviderName: "openai",
		BaseURL:      "https://custom.example.com/v1",
		Protocol:     "openai",
	}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/provider deepseek")
	if err != nil {
		t.Fatalf("ResolveOutcome /provider deepseek: %v", err)
	}
	if live.BaseURL != "" || live.Protocol != "" {
		t.Errorf("live endpoint override = (%q, %q), want cleared", live.BaseURL, live.Protocol)
	}
	if !strings.Contains(out.Message, "override cleared") {
		t.Errorf("message = %q, want the override-cleared note", out.Message)
	}
}

// TestProviderCommandUnknown verifies an unknown name lists the supported set.
func TestProviderCommandUnknown(t *testing.T) {
	live := &cli.LiveConfig{Model: "m", ProviderName: "openai"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/provider nope")
	if err != nil {
		t.Fatalf("ResolveOutcome /provider nope: %v", err)
	}
	if !strings.Contains(out.Message, "unknown provider") || !strings.Contains(out.Message, "opencode-go") {
		t.Errorf("message = %q, want unknown-provider + supported list", out.Message)
	}
	if live.ProviderName != "openai" {
		t.Errorf("live.ProviderName = %q, want unchanged openai", live.ProviderName)
	}
}

// TestProviderCommandListsDecisionModelApart pins the split the user asked for:
// the reviewer's decision model is shown in its own section — it has a
// credential and a proxy route worth seeing — and switching to it is refused
// with the reason, because the wire it speaks is not chat.
func TestProviderCommandListsDecisionModelApart(t *testing.T) {
	live := &cli.LiveConfig{Model: "m", ProviderName: "openai"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, provider.NewCredentialStore(nil))

	out, err := reg.ResolveOutcome("/provider")
	if err != nil {
		t.Fatalf("ResolveOutcome /provider: %v", err)
	}
	msg := out.Message
	section := strings.Index(msg, "decision model (answers the reviewer")
	if section < 0 {
		t.Fatalf("/provider must list the decision model in its own section:\n%s", msg)
	}
	if !strings.Contains(msg[section:], "typesafe") {
		t.Errorf("the decision section should name typesafe:\n%s", msg[section:])
	}
	// It belongs to that section alone: any chat group naming it is the bug this
	// replaces. (Within its own row it may appear twice — the name, and the
	// endpoint summary's proxy note.)
	for _, group := range strings.Split(msg, "\n\n") {
		if strings.Contains(group, "typesafe") && !strings.HasPrefix(group, "decision model (") {
			t.Errorf("typesafe is listed outside its section, in group:\n%s", group)
		}
	}

	// And it cannot be selected as the conversation model.
	switchOut, err := reg.ResolveOutcome("/provider typesafe")
	if err != nil {
		t.Fatalf("ResolveOutcome /provider typesafe: %v", err)
	}
	if !strings.Contains(switchOut.Message, "decision model") || !strings.Contains(switchOut.Message, "/proxy") {
		t.Errorf("switch message = %q, want it refused with the /proxy route named", switchOut.Message)
	}
	if live.ProviderName != "openai" {
		t.Errorf("live.ProviderName = %q, want unchanged openai", live.ProviderName)
	}
}

// TestProvidersAvailableFirstOrdersReadyFirst verifies the shared display
// order: providers with a credential found come first (registry order kept
// inside each group), so both the text listing and the TUI picker put the
// ready-to-use providers up top — and that the list holds every CONVERSATION
// provider and no other. The decision-only endpoint is deliberately absent: it
// is not a model anyone can switch to, and a picker that offered it would offer
// a switch that cannot work.
func TestProvidersAvailableFirstOrdersReadyFirst(t *testing.T) {
	// Clear every plausible key, then set exactly one provider's key.
	for _, spec := range provider.ProviderSpecs() {
		for _, env := range spec.EnvVars {
			t.Setenv(env, "")
		}
	}
	t.Setenv("DEEPSEEK_API_KEY", "test-key")

	specs := ProvidersAvailableFirst(nil)
	want := 0
	for _, spec := range provider.ProviderSpecs() {
		if spec.DecisionOnly {
			continue
		}
		want++
	}
	if len(specs) != want {
		t.Fatalf("specs = %d, want %d conversation providers", len(specs), want)
	}
	for _, spec := range specs {
		if spec.DecisionOnly {
			t.Errorf("picker offers the decision model %q, which cannot carry a conversation", spec.Name)
		}
	}
	if specs[0].Name != "deepseek" {
		t.Errorf("first spec = %q, want the configured provider deepseek", specs[0].Name)
	}
	if specs[len(specs)-1].Name == "deepseek" {
		t.Error("configured provider sorted last, want first")
	}
}
