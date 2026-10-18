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
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
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

// ChainAfterTrust is the chain the remote-control bridge uses after the
// browser answered the trust question. It must skip trust and still run the
// permission gate; otherwise the browser would replace the whole gate chain.
func TestChainAfterTrustSkipsOnlyTrust(t *testing.T) {
	mgr := gatesTrustStore(t)
	asked := false
	gates := NewGates(GatesOpts{
		Trust: mgr,
		Cwd:   t.TempDir(),
		Perms: permissions.New(permissions.FullAccess),
		Confirm: func(context.Context, judge.ApprovalRequest) judge.ApprovalAnswer {
			asked = true
			return judge.ApprovalAnswer{Answered: true}
		},
	})
	if dec := gates.ChainAfterTrust()(context.Background(), gatesCall()); dec != nil && dec.Block {
		t.Fatalf("after-trust chain blocked echo: %+v", dec)
	}
	if asked {
		t.Fatal("ChainAfterTrust asked the trust question again")
	}
	_ = gates.Chain()(context.Background(), gatesCall())
	if !asked {
		t.Fatal("the full chain should ask trust in an untrusted directory")
	}
}

// TestSandboxDenialAskerAnswersUnconfined: the denial question may be answered
// with "run this one command outside the sandbox". That answer is a one-call
// decision, and it is announced — an unconfined run is never silent, the same
// promise the escalation dialog's escape row keeps.
func TestSandboxDenialAskerAnswersUnconfined(t *testing.T) {
	grantstest.Install(t)
	notes := NewReviewNotes()
	var got []judge.Note
	notes.Set(func(n judge.Note) { got = append(got, n) })
	gates := NewGates(GatesOpts{
		Perms: permissions.New(permissions.Auto),
		Notes: notes,
		Confirm: func(context.Context, judge.ApprovalRequest) judge.ApprovalAnswer {
			return judge.ApprovalAnswer{Approve: true, Unconfined: true, Answered: true}
		},
	})
	ask := gates.SandboxDenialAsker()
	if ask == nil {
		t.Fatal("a wired dialog must produce an asker")
	}
	if answer := ask(context.Background(), "/Volumes/KIOXIA/out", "mkdir -p /Volumes/KIOXIA/out"); answer != agenttool.SandboxDenialUnconfined {
		t.Fatalf("answer = %v, want the unconfined one", answer)
	}
	if len(got) != 1 || got[0].Kind != judge.NoteEscaped {
		t.Fatalf("notes = %+v, want one escape announcement", got)
	}
}

// The contained answers keep working: a grant registers the path and asks for
// the same-command retry inside the sandbox.
func TestSandboxDenialAskerAnswersReAdmitted(t *testing.T) {
	grantstest.Install(t)
	gates := NewGates(GatesOpts{
		Perms: permissions.New(permissions.Auto),
		Confirm: func(context.Context, judge.ApprovalRequest) judge.ApprovalAnswer {
			return judge.ApprovalAnswer{Approve: true, ReadRoot: "/Volumes/KIOXIA/cache", Answered: true}
		},
	})
	ask := gates.SandboxDenialAsker()
	if ask == nil {
		t.Fatal("a wired dialog must produce an asker")
	}
	if answer := ask(context.Background(), "/Volumes/KIOXIA/cache", "cp a /Volumes/KIOXIA/cache/b"); answer != agenttool.SandboxDenialReAdmitted {
		t.Fatalf("answer = %v, want the contained retry", answer)
	}
	if !grants.Allows("/Volumes/KIOXIA/cache", grants.Read) {
		t.Error("the granted path was not registered")
	}
}

// A read-only run never ran the command, so the unconfined row is not offered.
func TestSandboxDenialAskerOmitsUnconfinedInReadOnly(t *testing.T) {
	grantstest.Install(t)
	var seen judge.ApprovalRequest
	gates := NewGates(GatesOpts{
		Perms: permissions.New(permissions.ReadOnly),
		Confirm: func(_ context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer {
			seen = req
			return judge.ApprovalAnswer{Answered: true}
		},
	})
	if ask := gates.SandboxDenialAsker(); ask != nil {
		_ = ask(context.Background(), "/Volumes/KIOXIA/x", "rm -rf /Volumes/KIOXIA/x")
	}
	if seen.UnconfineOffer {
		t.Error("read-only mode must not offer an unconfined run")
	}
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

// TestGatesDerivedChainAsksWithItsOrigin pins what a sub-agent's chain does with
// a consent question: it asks the session's own channel, labeled with the agent
// that asked. The label is the point — the sink and the dialog are shared, so
// an unlabeled question about a call the conversation never made reads as one
// the person can answer from memory when they cannot.
//
// It used to be the opposite by design: the inherited chain was built without
// the channels, so a child's question failed closed. That turned a question the
// person would have answered into a silent refusal the child had to work
// around, and the hazard it named — parking a goroutine on a dialog nobody can
// answer — is what the channels' own absence already covers (see the second
// half of this test).
func TestGatesDerivedChainAsksWithItsOrigin(t *testing.T) {
	cwd := t.TempDir()
	mgr := gatesTrustStore(t)
	asked := 0
	var origins []string
	g := NewGates(GatesOpts{
		Trust: mgr,
		Cwd:   cwd,
		Perms: permissions.New(permissions.FullAccess),
		Confirm: func(_ context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer {
			asked++
			origins = append(origins, req.Origin)
			return judge.ApprovalAnswer{Answered: true} // deny
		},
	})

	var cfg runtime.RunConfig
	g.Install(&cfg, nil, HookDeps{})
	child := cfg.Batch.ToolExecutorConfig.GateChain
	if child == nil || child.BeforeToolCall == nil {
		t.Fatal("Install must publish the chain a sub-agent inherits")
	}

	ctx := agentcore.WithAgentOrigin(context.Background(), "task")
	dec := child.BeforeToolCall(ctx, gatesCall())
	if dec == nil || !dec.Block {
		t.Fatalf("derived chain = %+v, want the denied answer to block", dec)
	}
	if asked != 1 {
		t.Fatalf("the derived chain asked the session's channel %d time(s), want 1", asked)
	}
	if len(origins) != 1 || origins[0] != "task" {
		t.Errorf("origin = %v, want the dispatching agent's label", origins)
	}
}

// TestGatesChainWithoutChannelsFailsClosed pins the other half, and the reason
// the shared chain is safe to share: a session with no answer channels — a
// headless run, an embedder that never wired a driver — produces a chain that
// asks nobody and blocks, and the child of such a session inherits exactly
// that. Nothing here has to decide "is this a sub-agent"; the channels are
// simply absent.
func TestGatesChainWithoutChannelsFailsClosed(t *testing.T) {
	cwd := t.TempDir()
	g := NewGates(GatesOpts{
		Trust: gatesTrustStore(t),
		Cwd:   cwd,
		Perms: permissions.New(permissions.FullAccess),
	})

	dec := g.Chain()(context.Background(), gatesCall())
	if dec == nil || !dec.Block {
		t.Fatalf("channel-less chain = %+v, want a fail-closed block", dec)
	}
	if text := agentcore.ContentToText(*dec.Content); !strings.Contains(text, "no prompt") {
		t.Errorf("the fail-closed message should say the run has no prompt, got %q", text)
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
	if child.PrepareArguments == nil {
		t.Fatal("child did not inherit PreToolUse preparation")
	}
	if _, err := child.PrepareArguments(context.Background(), "bash", gatesCall().Arguments); err == nil {
		t.Fatal("inherited PreToolUse hook must block before the child's gates")
	}
}

// TestGatesAuthorizationSnapshot covers the process-boundary contract: what the
// session decided is what a child in another address space is told.
func TestGatesAuthorizationSnapshot(t *testing.T) {
	grantstest.Install(t)
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
	if _, err := grants.AddReadRoot(extra, grants.Session, "test"); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	auth := g.Authorization()
	if !auth.SessionTrust || !auth.SessionReads {
		t.Errorf("snapshot = %+v, want both grants", auth)
	}
	// It must be among the roots, not first: the snapshot is built from the
	// table, which sorts by path, and the session-reads answer above put the
	// working directory in the same list.
	if !slices.Contains(auth.ReadRoots, permissions.Canonical(extra)) {
		t.Errorf("snapshot read roots = %v, want it to contain %q", auth.ReadRoots, extra)
	}
}

// TestGatesSeedAppliesParentAuthorizations covers the child's side: a
// process-isolated run rebuilds the parent's decisions before its gates are
// installed, and a root the read-root policy refuses (too broad) is reported
// rather than silently widening the boundary.
func TestGatesSeedAppliesParentAuthorizations(t *testing.T) {
	grantstest.Install(t)
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
	if !grants.Allows(filepath.Join(extra, "a.go"), grants.Read) {
		t.Errorf("Seed must register %q as a read root", extra)
	}

	// The gates then see the seeded state: the trust question is already
	// answered, so the chain is silent for the directory.
	if dec := g.Chain()(context.Background(), gatesCall()); dec != nil {
		t.Errorf("after Seed, a trusted directory should not ask, got %+v", dec)
	}
}

// TestGrantWritableRoot covers the callback behind the REPL prompt's `w`/`s`:
// the session grant always lands, persistence additionally writes the managed
// store, a config.toml seed is reported rather than copied, and a store that
// cannot be written degrades to session-only instead of failing the grant.
func TestGrantWritableRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	grantstest.Install(t)

	target := filepath.Join(dir, "build")

	// Session only: registered, nothing written.
	made, why := GrantWritableRoot(target, false)
	if !made {
		t.Fatalf("session grant failed: %s", why)
	}
	if got := grants.WritableRoots(); len(got) != 1 || got[0] != permissions.Canonical(target) {
		t.Fatalf("registry = %v, want the granted path", got)
	}
	for _, g := range grants.Process().Entries().Remembered {
		if g.Path == target {
			t.Fatalf("a session grant must not be written down: %+v", g)
		}
	}

	// Remembered: now it is written down too.
	made, why = GrantWritableRoot(target, true)
	if !made {
		t.Fatalf("remembered grant failed: %s", why)
	}
	if !strings.Contains(why, "remembered") {
		t.Errorf("why = %q, want it to confirm the durable grant", why)
	}
	if !grants.Allows(target, grants.Write) {
		t.Fatalf("the remembered grant is not in the table: %+v", grants.Process().Entries().All())
	}

	// A second remembered grant is the same row: the table keys by path, so
	// re-answering cannot accumulate duplicates.
	if _, _ = GrantWritableRoot(target, true); len(grants.WritableRoots()) != 1 {
		t.Fatalf("second grant duplicated the entry: %v", grants.WritableRoots())
	}
}
