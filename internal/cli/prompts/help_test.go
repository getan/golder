package prompts

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/runtime"
)

// TestHelpGroupsByCategory verifies the /help listing renders the categories in
// CategoryOrder, puts /model under Model, and collects user commands under
// Extensions with their source tag.
func TestHelpGroupsByCategory(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, &cli.LiveConfig{Model: "m", ProviderName: "p"}, nil)
	RegisterPermissionCommand(reg, nil, nil)
	reg.AddUser(runtime.SlashCommand{
		Name:        "review",
		Description: "Review PRs",
		Expand:      func(string) string { return "" },
	})

	out, err := reg.ResolveOutcome("/help")
	if err != nil {
		t.Fatalf("ResolveOutcome /help: %v", err)
	}
	if !out.Handled || out.Kind != runtime.SlashAction {
		t.Fatalf("/help should be a handled action, got handled=%v kind=%v", out.Handled, out.Kind)
	}
	msg := out.Message
	index := func(s string) int { return strings.Index(msg, s) }
	for _, pair := range [][2]string{
		{"General", "Session"},
		{"Session", "Model"},
		{"Model", "Memory"},
		{"Memory", "Permissions"},
		{"Permissions", "Modes"},
		{"Modes", "Extensions"},
	} {
		a, b := index(pair[0]), index(pair[1])
		if a < 0 || b < 0 || a >= b {
			t.Errorf("category %q should render before %q:\n%s", pair[0], pair[1], msg)
		}
	}
	if modelHeader, modelCmd := index("Model"), index("/model"); modelHeader < 0 || modelCmd < modelHeader {
		t.Errorf("/model should appear under the Model header:\n%s", msg)
	}
	if !strings.Contains(msg, "/review") || !strings.Contains(msg, "(source: global)") {
		t.Errorf("user command should be listed under Extensions with its source:\n%s", msg)
	}
}
