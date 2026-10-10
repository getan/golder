package judge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// readCall builds a read tool call for a path argument.
func readCall(tool, path string) agentcore.AgentToolCall {
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		panic(err)
	}
	return agentcore.AgentToolCall{Name: tool, Arguments: raw}
}

// readScopeFixture gives a test a workspace, an outside directory holding a
// readable file, and a HOME of its own, so the broad-root policy is
// deterministic. Grants are cleared afterwards: the registry is process-wide.
func readScopeFixture(t *testing.T) (workspace, outside, target string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace, outside = t.TempDir(), t.TempDir()
	target = filepath.Join(outside, "a.go")
	if err := os.WriteFile(target, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	return workspace, outside, target
}

// TestReadScopeGateSilentInsideWorkspace pins the common case: a path inside
// the workspace is not a question, so the gate stays out of the way entirely.
func TestReadScopeGateSilentInsideWorkspace(t *testing.T) {
	workspace, _, _ := readScopeFixture(t)
	asked := 0
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	for _, p := range []string{"a.go", "sub/b.go", workspace} {
		if dec := gate(context.Background(), readCall("read", p)); dec != nil {
			t.Errorf("path %q inside the workspace must not be gated, got %+v", p, dec)
		}
	}
	if asked != 0 {
		t.Errorf("no question should have been asked, got %d", asked)
	}
}

// TestReadScopeGateAsksAndGrantsOnce covers the one-read answer: the user is
// asked, and approving returns a decision carrying the resolved path as a
// one-call grant for the tool to honour.
func TestReadScopeGateAsksAndGrantsOnce(t *testing.T) {
	workspace, _, target := readScopeFixture(t)
	var got ApprovalRequest
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			got = req
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	})
	dec := gate(context.Background(), readCall("read", target))
	if got.Kind != ApprovalReadScope {
		t.Errorf("kind = %q, want %q", got.Kind, ApprovalReadScope)
	}
	if got.ReadScope != filepath.Dir(target) {
		t.Errorf("offered directory = %q, want %q", got.ReadScope, filepath.Dir(target))
	}
	if dec == nil || dec.Block {
		t.Fatalf("an approved read must return a grant, got %+v", dec)
	}
	if dec.ReadGrant != target {
		t.Errorf("grant = %q, want the resolved path %q", dec.ReadGrant, target)
	}
	// "Once" means once: the registry must not have widened.
	if permissions.ReadableAt(target) {
		t.Error("a one-read answer must not register a session grant")
	}
}

// TestReadScopeGateSessionGrant covers the directory answer: it registers the
// grant and returns no decision, so the call proceeds under the widened
// boundary and the next read there is not asked about again.
func TestReadScopeGateSessionGrant(t *testing.T) {
	workspace, outside, target := readScopeFixture(t)
	asked := 0
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Approve: true, Answered: true, ReadRoot: outside}
		},
	})
	if dec := gate(context.Background(), readCall("read", target)); dec != nil {
		t.Fatalf("a session grant needs no per-call decision, got %+v", dec)
	}
	if !permissions.ReadableAt(target) {
		t.Error("the directory answer must register the grant")
	}
	if dec := gate(context.Background(), readCall("read", filepath.Join(outside, "b.go"))); dec != nil {
		t.Errorf("a granted directory must not be asked about again, got %+v", dec)
	}
	if asked != 1 {
		t.Errorf("expected exactly one question, got %d", asked)
	}
}

// TestReadScopeGateDenyIsActionable pins the refusal: the model is told which
// path was blocked and how a person would allow it, so the next attempt is
// informed rather than blind.
func TestReadScopeGateDenyIsActionable(t *testing.T) {
	workspace, outside, target := readScopeFixture(t)
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			return ApprovalAnswer{Answered: true} // explicit no
		},
	})
	dec := gate(context.Background(), readCall("grep", target))
	if dec == nil || !dec.Block {
		t.Fatalf("a denied read must be blocked, got %+v", dec)
	}
	msg := agentcore.ContentToText(*dec.Content)
	for _, want := range []string{target, outside, "readable_roots"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal should mention %q: %q", want, msg)
		}
	}
}

// TestReadScopeGateNoPromptFailsClosed is the headless contract: with nobody to
// ask, the read is refused and the message says so, naming the config that
// would allow it. Guessing here would quietly widen what data a script reads.
func TestReadScopeGateNoPromptFailsClosed(t *testing.T) {
	workspace, _, target := readScopeFixture(t)
	gate := ReadScopeGate(ReadScopeOpts{WorkspaceRoot: workspace})
	if gate == nil {
		t.Fatal("a gate with a workspace root must be built")
	}
	dec := gate(context.Background(), readCall("read", target))
	if dec == nil || !dec.Block {
		t.Fatalf("no prompt must fail closed, got %+v", dec)
	}
	msg := agentcore.ContentToText(*dec.Content)
	if !strings.Contains(msg, "no prompt is available") {
		t.Errorf("the refusal should say no prompt was available: %q", msg)
	}
	if permissions.ReadableAt(target) {
		t.Error("a failed-closed read must not register a grant")
	}
}

// TestReadScopeGateCredentialPathIsNotOffered locks the safety property: a path
// the static floor refuses is never put to the user, so no dialog invites
// granting ~/.ssh or ~/.zshrc. The call passes through unchanged and the floor
// denies it.
func TestReadScopeGateCredentialPathIsNotOffered(t *testing.T) {
	workspace, _, _ := readScopeFixture(t)
	asked := 0
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	})
	for _, p := range []string{"~/.ssh/id_rsa", "~/.zshrc", "~/.aws/credentials"} {
		dec := gate(context.Background(), readCall("read", p))
		if dec != nil {
			t.Errorf("%q must pass through to the static floor, got %+v", p, dec)
		}
	}
	if asked != 0 {
		t.Errorf("a credential path must never be offered for approval, asked %d times", asked)
	}
	// And the floor does deny it, which is what makes passing it through safe.
	if _, denied := staticDeny("read", readCall("read", "~/.ssh/id_rsa").Arguments); !denied {
		t.Error("the static floor should deny a credential path")
	}
}

// TestReadScopeGateExtraRootsAreSilent pins the skills-directory case: roots
// golder itself advertises are not boundary questions.
func TestReadScopeGateExtraRootsAreSilent(t *testing.T) {
	workspace, outside, target := readScopeFixture(t)
	asked := 0
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Roots:         []string{outside},
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), readCall("read", target)); dec != nil {
		t.Errorf("an advertised root must not be gated, got %+v", dec)
	}
	if asked != 0 {
		t.Errorf("expected no question, got %d", asked)
	}
}

// TestReadScopeGateOnlyReadTools pins the tool set: the gate answers for the
// read-only tools, and never for the mutating one (a read grant says nothing
// about writing) or for tools with no path argument.
func TestReadScopeGateOnlyReadTools(t *testing.T) {
	workspace, _, target := readScopeFixture(t)
	asked := 0
	gate := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	applyPatch, err := json.Marshal(map[string]string{"patch": "*** Begin Patch\n*** Update File: " + target + "\n-x\n+y\n*** End Patch\n"})
	if err != nil {
		t.Fatal(err)
	}
	if dec := gate(context.Background(), agentcore.AgentToolCall{Name: "apply_patch", Arguments: applyPatch}); dec != nil {
		t.Errorf("apply_patch is not a read tool and must not be gated here, got %+v", dec)
	}
	if dec := gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"cat ` + target + `"}`)}); dec != nil {
		t.Errorf("bash is not gated by this gate, got %+v", dec)
	}
	if asked != 0 {
		t.Errorf("expected no question for non-read tools, got %d", asked)
	}
}

// TestChainGatesPreservesReadGrant is the regression for a real bug: the
// read-scope gate runs first in the TUI chain, and the permission gate behind
// it returns nil for an ungraded read tool. Dropping the grant on composition
// would refuse a read the user had just approved.
func TestChainGatesPreservesReadGrant(t *testing.T) {
	const granted = "/outside/a.go"
	first := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{ReadGrant: granted}
	}
	chained := ChainGates(first, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return nil
	})
	dec := chained(context.Background(), readCall("read", granted))
	if dec == nil || dec.ReadGrant != granted {
		t.Fatalf("the read grant must survive a nil second gate, got %+v", dec)
	}

	// A later gate that only rewrites input keeps it too.
	chained = ChainGates(first, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{UpdatedInput: json.RawMessage(`{"path":"x"}`)}
	})
	dec = chained(context.Background(), readCall("read", granted))
	if dec == nil || dec.ReadGrant != granted || len(dec.UpdatedInput) == 0 {
		t.Fatalf("the grant must merge with a later rewrite, got %+v", dec)
	}

	// Both requests survive together.
	both := func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Sandbox: true, ReadGrant: granted}
	}
	chained = ChainGates(both, func(context.Context, agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return nil
	})
	dec = chained(context.Background(), readCall("read", granted))
	if dec == nil || !dec.Sandbox || dec.ReadGrant != granted {
		t.Fatalf("sandbox and read grant must both survive, got %+v", dec)
	}
}

// TestReadScopeGateNotWaivedByMode pins the consent rule: full-access disables
// review and sandboxing, not the user's say over what is read. The gate is a
// sibling of the permission gate precisely so no mode reaches it.
func TestReadScopeGateNotWaivedByMode(t *testing.T) {
	workspace, _, target := readScopeFixture(t)
	asked := 0
	scope := ReadScopeGate(ReadScopeOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	perm := PermissionGate(permissions.New(permissions.FullAccess), GateOpts{
		Classifier: &stubClassifier{v: Verdict{Level: Allow}},
	})
	chained := ChainGates(scope, perm)
	dec := chained(context.Background(), readCall("read", target))
	if asked != 1 {
		t.Fatalf("full-access must not skip the consent question, asked %d times", asked)
	}
	if dec == nil || !dec.Block {
		t.Fatalf("the denial must stand even in full-access, got %+v", dec)
	}
}
