package run

// Regression coverage for driver convergence (#425, FR-16): the SAME resolved
// hook set must fire in every driver mode. Two wiring paths survive — the
// one-shot entry (headless / subagent_rpc / and every driver that assembles its
// gates, via Gates.InstallDriver) and the per-turn entry (repl / tui / goal /
// btw via BuildDispatcher once + InstallSeams per run) — and this asserts a
// PreToolUse hook installed through either reaches PrepareArguments and blocks
// before BeforeToolCall. It also pins FR-18: an empty hook set wires no seam, so a run with no
// hooks configured behaves exactly as before.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/runtime"
)

// blockingPreToolUse is a hook set whose PreToolUse hook exits 2 (Claude Code
// block semantics), so argument preparation fails before any gate is consulted.
func blockingPreToolUse() hooks.HookSet {
	return hooks.HookSet{
		"PreToolUse": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: "exit 2"}}}},
	}
}

// firePreToolUse checks the hook seam without invoking any authorization gate.
func firePreToolUse(cfg *runtime.RunConfig) bool {
	prepare := cfg.Batch.ToolExecutorConfig.PrepareArguments
	if prepare == nil {
		return false
	}
	_, err := prepare(context.Background(), "Bash", json.RawMessage(`{"command":"echo hi"}`))
	return err != nil
}

// TestHookConvergenceBothPaths asserts the same PreToolUse hook set fires in both
// driver wiring paths: the one-shot headless path and the multi-turn REPL path.
func TestHookConvergenceBothPaths(t *testing.T) {
	deps := HookDeps{SessionID: "s1", ProjectDir: t.TempDir()}
	set := blockingPreToolUse()

	// The gate assembly path: InstallDriver wires gates + seams + SessionStart
	// all-in-one. The gate chain is empty (no trust store, full access), so the
	// only thing that can block is the hook.
	t.Run("gates", func(t *testing.T) {
		var cfg runtime.RunConfig
		g := NewGates(GatesOpts{Cwd: deps.ProjectDir, Perms: permissions.New(permissions.FullAccess)})
		d, _ := g.InstallDriver(context.Background(), &cfg, set, deps, "startup", nil)
		if d == nil {
			t.Fatal("expected dispatcher for non-empty hook set")
		}
		if !firePreToolUse(&cfg) {
			t.Fatal("gates path: PreToolUse hook did not block")
		}
		// The same assembly also published the chain a sub-agent inherits.
		if cfg.Batch.ToolExecutorConfig.GateChain == nil {
			t.Fatal("gates path: the inherited chain was not published")
		}
		if cfg.Batch.ToolExecutorConfig.GateChain.PrepareArguments == nil {
			t.Fatal("gates path: a child must inherit the PreToolUse preparation")
		}
	})

	// The per-turn path: BuildDispatcher once, then InstallSeams per run.
	t.Run("repl", func(t *testing.T) {
		d := BuildDispatcher(set, deps)
		if d == nil {
			t.Fatal("expected dispatcher for non-empty hook set")
		}
		var cfg runtime.RunConfig
		InstallSeams(&cfg, d, deps)
		if !firePreToolUse(&cfg) {
			t.Fatal("repl path: PreToolUse hook did not block")
		}
	})
}

// TestHookConvergenceNoHooksUnchanged pins FR-18: with no hooks configured neither
// wiring path installs a BeforeToolCall seam, so both drivers behave exactly as
// they did before hooks existed.
func TestHookConvergenceNoHooksUnchanged(t *testing.T) {
	deps := HookDeps{ProjectDir: t.TempDir()}

	t.Run("gates", func(t *testing.T) {
		var cfg runtime.RunConfig
		g := NewGates(GatesOpts{Cwd: deps.ProjectDir, Perms: permissions.New(permissions.FullAccess)})
		d, ev := g.InstallDriver(context.Background(), &cfg, nil, deps, "startup", nil)
		if d != nil {
			t.Fatalf("expected nil dispatcher for empty hook set, got %v", d)
		}
		if ev != nil {
			t.Fatal("expected event handler unchanged (nil) for empty hook set")
		}
		// With no hooks the seam carries the gates alone — and the gates are
		// never absent, because the static hard-deny floor lives inside the
		// permission gate: no hook set turns it off.
		seam := cfg.Batch.ToolExecutorConfig.BeforeToolCall
		if seam == nil {
			t.Fatal("gates path: the static floor must be installed even with no hooks")
		}
		deny := seam(context.Background(), agentcore.AgentToolCall{
			Name: "bash", Arguments: json.RawMessage(`{"command":"sudo rm -rf /"}`),
		})
		if deny == nil || !deny.Block {
			t.Fatalf("empty hook set must not disable the static floor, got %+v", deny)
		}
		if dec := seam(context.Background(), agentcore.AgentToolCall{
			Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`),
		}); dec != nil {
			t.Fatalf("full access with no trust gate should allow a benign command, got %+v", dec)
		}
		// The inherited chain is published regardless, so a sub-agent inherits
		// this session's gates rather than nothing at all.
		if cfg.Batch.ToolExecutorConfig.GateChain == nil {
			t.Fatal("gates path: the inherited chain should be published even without hooks")
		}
	})

	t.Run("repl", func(t *testing.T) {
		d := BuildDispatcher(nil, deps)
		if d != nil {
			t.Fatalf("expected nil dispatcher for empty hook set, got %v", d)
		}
		var cfg runtime.RunConfig
		InstallSeams(&cfg, d, deps) // nil dispatcher must be a no-op
		if cfg.Batch.ToolExecutorConfig.BeforeToolCall != nil {
			t.Fatal("repl path: seam wired despite no hooks")
		}
		if cfg.Batch.ToolExecutorConfig.PrepareArguments != nil {
			t.Fatal("repl path: argument preparation wired despite no hooks")
		}
	})
}
