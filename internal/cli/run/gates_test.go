package run

// Tests for the session's gate assembly (gates.go): the composition order, the
// two chains it produces, and the authorization snapshot that carries a
// session's decisions across a process boundary.
//
// These pin the invariants the refactor exists for. Before gates.go, the chain
// was assembled at seven sites, and the ways they drifted are exactly what is
// asserted here: a driver that gated fewer tools, a sub-agent that inherited
// fewer gates than its parent, and a process-isolated child that could not see
// what the user had already answered.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/trust"
)

// gatesTrustStore builds an empty trust store for a test. The file lives in the
// test's temp dir, so nothing reads the machine's real decisions.
func gatesTrustStore(t *testing.T) *trust.Manager {
	t.Helper()
	m, err := trust.NewManager(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatalf("trust.NewManager: %v", err)
	}
	return m
}

// gatesCall builds the tool call shape the gates classify: a bash command that
// is neither destructive nor credential-shaped, so only the gate under test
// decides its fate.
func gatesCall() agentcore.AgentToolCall {
	return agentcore.AgentToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}
}

// TestGatesAskTrustBeforeAnythingElse pins the composition order: in an
// untrusted directory the trust question is settled first, so a blocked trust
// answer is what the model sees — not a permission verdict or a static-floor
// note from a gate that should never have been reached.
func TestGatesAskTrustBeforeAnythingElse(t *testing.T) {
	cwd := t.TempDir()
	mgr := gatesTrustStore(t)
	var got judge.ApprovalRequest
	g := NewGates(GatesOpts{
		Trust: mgr,
		Cwd:   cwd,
		Perms: permissions.New(permissions.FullAccess),
		Confirm: func(_ context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer {
			got = req
			return judge.ApprovalAnswer{Answered: true} // deny
		},
	})

	dec := g.Chain()(context.Background(), gatesCall())
	if dec == nil || !dec.Block {
		t.Fatalf("denied trust question = %+v, want a block", dec)
	}
	if got.Kind != judge.ApprovalTrust {
		t.Errorf("first question kind = %q, want %q", got.Kind, judge.ApprovalTrust)
	}
	if got.TrustDir != cwd {
		t.Errorf("trust question dir = %q, want %q", got.TrustDir, cwd)
	}
}

// TestGatesDerivedChainFailsClosedWithoutPrompting pins the two chains apart:
// the interactive chain asks the driver's channel, while the derived chain a
// sub-agent inherits blocks without ever calling it. A child that could open a
// dialog would park a goroutine on a question the user cannot answer without
// abandoning the batch they are watching.
func TestGatesDerivedChainFailsClosedWithoutPrompting(t *testing.T) {
	cwd := t.TempDir()
	mgr := gatesTrustStore(t)
	asked := 0
	g := NewGates(GatesOpts{
		Trust: mgr,
		Cwd:   cwd,
		Perms: permissions.New(permissions.FullAccess),
		Confirm: func(context.Context, judge.ApprovalRequest) judge.ApprovalAnswer {
			asked++
			return judge.ApprovalAnswer{Answered: true} // deny
		},
	})

	var cfg runtime.RunConfig
	g.Install(&cfg, nil, HookDeps{})
	child := cfg.Batch.ToolExecutorConfig.GateChain
	if child == nil || child.BeforeToolCall == nil {
		t.Fatal("Install must publish the chain a sub-agent inherits")
	}

	dec := child.BeforeToolCall(context.Background(), gatesCall())
	if dec == nil || !dec.Block {
		t.Fatalf("derived chain = %+v, want a fail-closed block", dec)
	}
	if asked != 0 {
		t.Fatalf("the derived chain called the interactive channel %d time(s); a sub-agent must not prompt", asked)
	}
	if text := agentcore.ContentToText(*dec.Content); !strings.Contains(text, "no prompt") {
		t.Errorf("the fail-closed message should say the run has no prompt, got %q", text)
	}

	// The interactive chain (same inputs) does ask.
	if dec := g.Chain()(context.Background(), gatesCall()); dec == nil || !dec.Block {
		t.Fatalf("interactive chain = %+v, want a block", dec)
	}
	if asked != 1 {
		t.Errorf("interactive channel asked %d time(s), want 1", asked)
	}
}

// TestGatesInheritedChainCarriesToolUseHooks pins that a sub-agent inherits the
// session's PreToolUse/PostToolUse hooks (a hook guarding the session's files
// has no business skipping a child's calls) while Stop stays out of the
// inherited chain (a child's end is its own).
func TestGatesInheritedChainCarriesToolUseHooks(t *testing.T) {
	cwd := t.TempDir()
	set := hooks.HookSet{
		"PreToolUse": {{Matcher: "*", Hooks: []hooks.HookConfig{{Command: "exit 2"}}}},
	}
	deps := HookDeps{ProjectDir: cwd}
	g := NewGates(GatesOpts{Trust: gatesTrustStore(t), Cwd: cwd, Perms: permissions.New(permissions.FullAccess)})
	var cfg runtime.RunConfig
	g.Install(&cfg, BuildDispatcher(set, deps), deps)

	// The trust gate leads, so trust the directory to reach the hook seam.
	g.opts.Trust.SetSessionTrust(cwd)

	child := cfg.Batch.ToolExecutorConfig.GateChain
	if child.AfterToolCall == nil {
		t.Error("the inherited chain must carry the PostToolUse seam")
	}
	dec := child.BeforeToolCall(context.Background(), gatesCall())
	if dec == nil || !dec.Block {
		t.Fatalf("inherited PreToolUse hook = %+v, want a block", dec)
	}
}

// TestGatesAuthorizationSnapshot covers the process-boundary contract: what the
// session decided is what a child in another address space is told.
func TestGatesAuthorizationSnapshot(t *testing.T) {
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	cwd := t.TempDir()
	extra := t.TempDir()

	// No trust manager: the invocation itself is the authorization, so a child
	// must not be handed a question that could only be answered "no".
	bare := NewGates(GatesOpts{Cwd: cwd})
	if auth := bare.Authorization(); !auth.SessionTrust {
		t.Error("a run with no trust gate must report itself authorized")
	}

	mgr := gatesTrustStore(t)
	g := NewGates(GatesOpts{Trust: mgr, Cwd: cwd})
	if auth := g.Authorization(); auth.SessionTrust || auth.SessionReads {
		t.Errorf("undecided directory snapshot = %+v, want both false", auth)
	}

	mgr.SetSessionTrust(cwd)
	mgr.SetSessionReads(cwd)
	if _, err := permissions.AddReadRoot(extra); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	auth := g.Authorization()
	if !auth.SessionTrust || !auth.SessionReads {
		t.Errorf("snapshot = %+v, want both grants", auth)
	}
	if len(auth.ReadRoots) == 0 || auth.ReadRoots[0] != extra {
		t.Errorf("snapshot read roots = %v, want %q", auth.ReadRoots, extra)
	}
}

// TestGatesSeedAppliesParentAuthorizations covers the child's side: a
// process-isolated run rebuilds the parent's decisions before its gates are
// installed, and a root the read-root policy refuses (too broad) is reported
// rather than silently widening the boundary.
func TestGatesSeedAppliesParentAuthorizations(t *testing.T) {
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	cwd := t.TempDir()
	extra := t.TempDir()
	mgr := gatesTrustStore(t)
	g := NewGates(GatesOpts{Trust: mgr, Cwd: cwd})

	refused := g.Seed(agentcore.ToolAuth{SessionTrust: true, SessionReads: true, ReadRoots: []string{extra, "/"}})
	if refused != 1 {
		t.Errorf("refused roots = %d, want 1 (the filesystem root)", refused)
	}
	if !mgr.IsTrusted(cwd) {
		t.Error("Seed must apply the parent's session trust")
	}
	if !mgr.ReadsTrusted(cwd) {
		t.Error("Seed must apply the parent's session reads")
	}
	if !permissions.ReadableAt(filepath.Join(extra, "a.go")) {
		t.Errorf("Seed must register %q as a read root", extra)
	}

	// The gates then see the seeded state: the trust question is already
	// answered, so the chain is silent for the directory.
	if dec := g.Chain()(context.Background(), gatesCall()); dec != nil {
		t.Errorf("after Seed, a trusted directory should not ask, got %+v", dec)
	}
}
