package prompts

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/runtime"
)

// TestThinkCommandSwitchesLevel verifies /think mutates the live thinking level
// so the next turn picks it up, and that a bare /think reports the current level.
func TestThinkCommandSwitchesLevel(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingMedium}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	out, err := reg.ResolveOutcome("/think high")
	if err != nil {
		t.Fatalf("ResolveOutcome /think high: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("message = %q, want it to mention high", out.Message)
	}

	// Bare /think reports the current level without changing it.
	out, err = reg.ResolveOutcome("/think")
	if err != nil {
		t.Fatalf("ResolveOutcome /think: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("bare /think mutated level to %q", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("bare /think message = %q, want current level high", out.Message)
	}
}

// TestThinkCommandRejectsInvalid verifies an unknown level is rejected and the
// live level is left unchanged.
func TestThinkCommandRejectsInvalid(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingLow}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	out, err := reg.ResolveOutcome("/think bogus")
	if err != nil {
		t.Fatalf("ResolveOutcome /think bogus: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingLow {
		t.Errorf("invalid level changed ThinkingLevel to %q", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "invalid") {
		t.Errorf("message = %q, want an invalid-level notice", out.Message)
	}
}

// TestEffectAliasesThink verifies /effect behaves identically to /think: it
// switches the live thinking level and a bare /effect reports the current level.
func TestEffectAliasesThink(t *testing.T) {
	live := &cli.LiveConfig{Model: "test", ProviderName: "test", ThinkingLevel: agentcore.ThinkingMedium}
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, nil)

	out, err := reg.ResolveOutcome("/effect high")
	if err != nil {
		t.Fatalf("ResolveOutcome /effect high: %v", err)
	}
	if live.ThinkingLevel != agentcore.ThinkingHigh {
		t.Errorf("ThinkingLevel = %q, want high", live.ThinkingLevel)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("message = %q, want it to mention high", out.Message)
	}

	out, err = reg.ResolveOutcome("/effect")
	if err != nil {
		t.Fatalf("ResolveOutcome /effect: %v", err)
	}
	if !strings.Contains(out.Message, "high") {
		t.Errorf("bare /effect message = %q, want current level high", out.Message)
	}
}
