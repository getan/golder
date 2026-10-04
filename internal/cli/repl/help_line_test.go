package repl

// Tests for /help's template listing (US-011, #341): the /help Action groups
// built-ins by category and lists prompt templates under Extensions with their
// hint and source tag.

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/runtime"
)

// TestHelpActionIncludesTemplateLabelAndTier verifies the /help Action lists a
// prompt template under Extensions with its argument-hint, description, and
// source tag.
func TestHelpActionIncludesTemplateLabelAndTier(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	prompts.RegisterLiveCommands(reg, &cli.LiveConfig{Model: "test", ProviderName: "test"}, nil)
	reg.AddUser(runtime.SlashCommand{
		Name:         "review",
		ArgumentHint: "<PR-URL>",
		Description:  "Review PRs",
		Tier:         runtime.TierGlobal,
		Expand:       func(string) string { return "" },
	})

	out, err := reg.ResolveOutcome("/help")
	if err != nil {
		t.Fatalf("ResolveOutcome /help: %v", err)
	}
	if !out.Handled || out.Kind != runtime.SlashAction {
		t.Fatalf("/help should be a handled action, got handled=%v kind=%v", out.Handled, out.Kind)
	}
	for _, want := range []string{"Extensions", "/review", "<PR-URL>", "Review PRs", "(source: global)"} {
		if !strings.Contains(out.Message, want) {
			t.Errorf("/help output missing %q:\n%s", want, out.Message)
		}
	}
}
