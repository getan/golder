package prompts

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/runtime"
)

// TestModelThinkSwitchesLevel verifies /model think <level> mutates the live
// thinking level so the next turn picks it up, and that the bare form reports
// the current level. This is the only spelling: the standalone /think that used
// to share this action was removed as a duplicate of /model.
func TestModelThinkSwitchesLevel(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingMedium}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	out, err := reg.ResolveOutcome("/model think high")
	if err != nil {
		t.Fatalf("ResolveOutcome /model think high: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("message = %q, want it to mention high", out.Message)
	}

	// The bare form reports the current level without changing it.
	out, err = reg.ResolveOutcome("/model think")
	if err != nil {
		t.Fatalf("ResolveOutcome /model think: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("bare /model think mutated level to %q", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("bare /model think message = %q, want current level high", out.Message)
	}
	// The hint it prints must name the surviving spelling.
	if !strings.Contains(out.Message, "/model think") {
		t.Errorf("message = %q, want it to name /model think", out.Message)
	}
}

// TestModelThinkRejectsInvalid verifies an unknown level is rejected and the
// live level is left unchanged.
func TestModelThinkRejectsInvalid(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingLow}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	out, err := reg.ResolveOutcome("/model think bogus")
	if err != nil {
		t.Fatalf("ResolveOutcome /model think bogus: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("invalid level changed ThinkingLevel to %q", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "invalid") {
		t.Errorf("message = %q, want an invalid-level notice", out.Message)
	}
}

// TestThinkCommandRemoved verifies the standalone command is gone: it is not in
// the registry (so it never shows in /help or autocomplete) and resolving it is
// an unknown command, exactly like any other typo.
func TestThinkCommandRemoved(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test"}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	for _, c := range reg.List() {
		if c.Name == "think" {
			t.Fatalf("/think must not be registered any more; got %+v", c)
		}
	}
	if _, err := reg.ResolveOutcome("/think high"); err == nil {
		t.Error("/think should no longer resolve")
	}
}
