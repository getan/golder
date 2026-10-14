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

// userHome returns the REAL home directory (HOME may have been redirected by an
// earlier test), so a fixture can be placed under it — outside both $TMPDIR and
// the system whitelist — rather than inside either.
func userHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	return home
}

// readFixture gives a test a workspace, an outside directory holding a
// readable file, and a HOME of its own, so the broad-root policy is
// deterministic. Grants are cleared afterwards: the registry is process-wide.
func readFixture(t *testing.T) (workspace, outside, target string) {
	t.Helper()
	// The fixture must NOT live under $TMPDIR: the system whitelist exempts the
	// machine's temp root (the sandbox admits its own temp dir too), so a
	// fixture there would be silently exempt and every "outside" case would
	// pass for the wrong reason — or, as here, fail to ask at all. A fabricated
	// HOME under the real one puts it outside the whitelist, because $HOME is
	// deliberately absent from the list; that is exactly the boundary these
	// tests are about.
	home := filepath.Join(userHome(t), ".golder-test", t.Name())
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	workspace = filepath.Join(home, "workspace")
	outside = filepath.Join(home, "outside")
	for _, dir := range []string{workspace, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target = filepath.Join(outside, "a.go")
	if err := os.WriteFile(target, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	return workspace, outside, target
}

// TestBoundaryGateSilentInsideWorkspace pins the common case: a path inside
// the workspace is not a question, so the gate stays out of the way entirely.
func TestBoundaryGateSilentInsideWorkspace(t *testing.T) {
	workspace, _, _ := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
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

// TestBoundaryGateAsksAndGrantsOnce covers the one-read answer: the user is
// asked, and approving returns a decision carrying the resolved path as a
// one-call grant for the tool to honour.
func TestBoundaryGateAsksAndGrantsOnce(t *testing.T) {
	workspace, _, target := readFixture(t)
	var got ApprovalRequest
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			got = req
			return ApprovalAnswer{Approve: true, Answered: true}
		},
	})
	dec := gate(context.Background(), readCall("read", target))
	if got.Kind != ApprovalBoundary {
		t.Errorf("kind = %q, want %q", got.Kind, ApprovalBoundary)
	}
	if got.BoundaryDir != filepath.Dir(target) {
		t.Errorf("offered directory = %q, want %q", got.BoundaryDir, filepath.Dir(target))
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

// TestBoundaryGateOffersDirectoryItself pins the grant's granularity: a call
// that names a directory (an `ls` operand, or a shell command's directory
// argument) is offered that very directory, not the tree above it. The coarse
// answer was the one that stuck for the session, so the dialog handed over
// more than the read had asked for.
func TestBoundaryGateOffersDirectoryItself(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	sub := filepath.Join(outside, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var got ApprovalRequest
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			got = req
			return ApprovalAnswer{Answered: true} // deny: the question is what matters
		},
	})
	if dec := gate(context.Background(), readCall("ls", sub)); dec == nil || !dec.Block {
		t.Fatalf("a directory outside the workspace must be gated, got %+v", dec)
	}
	if got.BoundaryDir != sub {
		t.Errorf("offered directory = %q, want the directory itself %q", got.BoundaryDir, sub)
	}
	// The shell spelling of the same read must land on the same answer, trailing
	// separator and all.
	if dec := gate(context.Background(), bashCall("ls "+sub+"/")); dec == nil || !dec.Block {
		t.Fatalf("a command naming a directory outside must be gated, got %+v", dec)
	}
	if got.BoundaryDir != sub {
		t.Errorf("offered directory = %q, want %q", got.BoundaryDir, sub)
	}
}

// TestBoundaryGateSessionGrant covers the directory answer: it registers the
// grant and returns no decision, so the call proceeds under the widened
// boundary and the next read there is not asked about again.
func TestBoundaryGateSessionGrant(t *testing.T) {
	workspace, outside, target := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
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

// TestBoundaryGateDenyIsActionable pins the refusal: the model is told which
// path was blocked and how a person would allow it, so the next attempt is
// informed rather than blind.
func TestBoundaryGateDenyIsActionable(t *testing.T) {
	workspace, outside, target := readFixture(t)
	gate := BoundaryGate(BoundaryOpts{
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

// TestBoundaryGateNoPromptFailsClosed is the headless contract: with nobody to
// ask, the read is refused and the message says so, naming the config that
// would allow it. Guessing here would quietly widen what data a script reads.
func TestBoundaryGateNoPromptFailsClosed(t *testing.T) {
	workspace, _, target := readFixture(t)
	gate := BoundaryGate(BoundaryOpts{WorkspaceRoot: workspace})
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

// TestBoundaryGateCredentialPathIsNotOffered locks the safety property: a path
// the static floor refuses is never put to the user, so no dialog invites
// granting ~/.ssh or ~/.zshrc. The call passes through unchanged and the floor
// denies it.
func TestBoundaryGateCredentialPathIsNotOffered(t *testing.T) {
	workspace, _, _ := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
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

// TestBoundaryGateExtraRootsAreSilent pins the skills-directory case: roots
// golder itself advertises are not boundary questions.
func TestBoundaryGateExtraRootsAreSilent(t *testing.T) {
	workspace, outside, target := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
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

// TestBoundaryGateOnlyReadTools pins the tool set: the gate answers for the
// read-only tools, and never for the mutating one (a read grant says nothing
// about writing) or for tools with no path argument.
func TestBoundaryGateOnlyReadTools(t *testing.T) {
	workspace, _, target := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
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
	// bash IS gated: a command's file operands are the same boundary question
	// as a path argument (this used to be exempt, which is how `cat /outside/x`
	// read whatever it liked).
	if dec := gate(context.Background(), bashCall("cat "+target)); dec == nil || !dec.Block {
		t.Errorf("a command reading outside must be gated, got %+v", dec)
	}
	// A command with no path, and the in-memory tools, stay silent.
	for _, call := range []agentcore.AgentToolCall{
		bashCall("go build ./..."),
		{Name: "todo"},
		{Name: "memory_search"},
	} {
		if dec := gate(context.Background(), call); dec != nil {
			t.Errorf("%q must not be gated, got %+v", call.Name, dec)
		}
	}
	if asked != 1 {
		t.Errorf("asked %d times, want exactly the one outside read", asked)
	}
}

// TestChainGatesPreservesReadGrant is the regression for a real bug: the
// boundary gate runs first in the TUI chain, and the permission gate behind
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

// TestBoundaryGateNotWaivedByMode pins the consent rule: full-access disables
// review and sandboxing, not the user's say over what is read. The gate is a
// sibling of the permission gate precisely so no mode reaches it.
func TestBoundaryGateNotWaivedByMode(t *testing.T) {
	workspace, _, target := readFixture(t)
	asked := 0
	scope := BoundaryGate(BoundaryOpts{
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

// TestBoundaryGateAsksAboutCommandOperands covers the shell half of the gate:
// a command that names a file outside the workspace is a consent question, and
// the dialog shows the command (what will run) with the path as its reason.
func TestBoundaryGateAsksAboutCommandOperands(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	target := filepath.Join(outside, "notes.md")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got ApprovalRequest
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			asked++
			got = req
			return ApprovalAnswer{Answered: true} // deny
		},
	})

	cmd := "cat " + target
	dec := gate(context.Background(), bashCall(cmd))
	if asked != 1 {
		t.Fatalf("asked %d times, want 1 — a command reading outside must ask", asked)
	}
	if dec == nil || !dec.Block {
		t.Fatalf("denial = %+v, want a block", dec)
	}
	if got.Kind != ApprovalBoundary {
		t.Errorf("kind = %q, want %q", got.Kind, ApprovalBoundary)
	}
	if got.Summary != cmd {
		t.Errorf("summary = %q, want the command %q", got.Summary, cmd)
	}
	if !strings.Contains(got.Rationale, target) {
		t.Errorf("rationale = %q, want it to name %q", got.Rationale, target)
	}
}

// TestBoundaryGateSilentForCommandInsideWorkspace: a command that touches only
// the workspace, or names no path at all, is not a boundary question.
func TestBoundaryGateSilentForCommandInsideWorkspace(t *testing.T) {
	workspace, _, _ := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	for _, cmd := range []string{
		"go build ./...",
		"cat internal/main.go",
		`git commit -m "fix /outside/thing"`,
		"make -j8",
	} {
		if dec := gate(context.Background(), bashCall(cmd)); dec != nil {
			t.Errorf("%q was gated (%+v), want silent", cmd, dec)
		}
	}
	if asked != 0 {
		t.Errorf("asked %d times, want 0", asked)
	}
}

// TestBoundaryGateSilentForSystemPaths: the sandbox's read whitelist is not a
// consent question — asking about /usr/bin would train blind approval.
func TestBoundaryGateSilentForSystemPaths(t *testing.T) {
	workspace, _, _ := readFixture(t)
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), bashCall("ls /usr/bin")); dec != nil {
		t.Errorf("a system path was gated (%+v), want silent", dec)
	}
	if asked != 0 {
		t.Errorf("asked %d times, want 0", asked)
	}
}

// TestBoundaryGateCommandHonorsSessionGrant: once the directory is granted for
// the session, later commands reading it are silent — the same contract the file
// tools have.
func TestBoundaryGateCommandHonorsSessionGrant(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	target := filepath.Join(outside, "notes.md")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := permissions.AddReadRoot(outside); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(context.Context, ApprovalRequest) ApprovalAnswer {
			asked++
			return ApprovalAnswer{Answered: true}
		},
	})
	if dec := gate(context.Background(), bashCall("cat "+target)); dec != nil {
		t.Errorf("a granted directory was gated (%+v), want silent", dec)
	}
	if asked != 0 {
		t.Errorf("asked %d times, want 0", asked)
	}
}

// TestBoundaryGateAsksAboutTildeOperands is the regression for a silent hole:
// the shell expands a leading `~` itself, but the scanner read the word
// literally, so `ls ~/Desktop` resolved against the workspace, looked like an
// in-workspace read, and reached the user's home with no question asked.
func TestBoundaryGateAsksAboutTildeOperands(t *testing.T) {
	workspace, outside, target := readFixture(t)
	var got ApprovalRequest
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			asked++
			got = req
			return ApprovalAnswer{Answered: true} // deny: the question is what matters
		},
	})
	// The fixture's HOME is the directory holding workspace and outside, so
	// `~/outside` is the very directory a literal reading places in the
	// workspace.
	for _, cmd := range []string{"ls ~/outside", "ls ~/outside/", "cat ~/outside/a.go"} {
		if dec := gate(context.Background(), bashCall(cmd)); dec == nil || !dec.Block {
			t.Fatalf("%q must be gated, got %+v", cmd, dec)
		}
	}
	if asked != 3 {
		t.Errorf("asked %d times, want 3 — every ~ operand is a question", asked)
	}
	if got.BoundaryDir != outside {
		t.Errorf("offered = %q, want %q", got.BoundaryDir, outside)
	}
	if !strings.Contains(got.Rationale, outside) {
		t.Errorf("rationale = %q, want the expanded path %q (not the ~ spelling)", got.Rationale, outside)
	}
	_ = target
}

// TestBoundaryGateOffersFinestOperandDirectory pins the granularity of the
// single question one command gets: when the operands name a directory and a
// subtree inside it, the dialog must offer the subtree. Offering the ancestor
// (the first operand in command order) hands over a tree the read never asked
// for, and the session grant outlives the call.
func TestBoundaryGateOffersFinestOperandDirectory(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	sub := filepath.Join(outside, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var got ApprovalRequest
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			got = req
			return ApprovalAnswer{Answered: true} // deny
		},
	})
	// Both orders: the finer operand wins whether it is written first or last.
	for _, cmd := range []string{"ls " + outside + " " + sub, "ls -la " + sub + " " + outside} {
		got = ApprovalRequest{}
		if dec := gate(context.Background(), bashCall(cmd)); dec == nil || !dec.Block {
			t.Fatalf("%q must be gated, got %+v", cmd, dec)
		}
		if got.BoundaryDir != sub {
			t.Errorf("%q offered %q, want the subtree %q", cmd, got.BoundaryDir, sub)
		}
	}
}

// TestBoundaryGateReadsThePathListForm covers grep/find's list argument: the
// gate read only the string form, so a search scoped with an array reached
// outside the workspace with no question asked — the array failed the string
// unmarshal, which the gate read as "no path here".
func TestBoundaryGateReadsThePathListForm(t *testing.T) {
	workspace, outside, _ := readFixture(t)
	sub := filepath.Join(outside, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var got ApprovalRequest
	asked := 0
	gate := BoundaryGate(BoundaryOpts{
		WorkspaceRoot: workspace,
		Confirm: func(_ context.Context, req ApprovalRequest) ApprovalAnswer {
			asked++
			got = req
			return ApprovalAnswer{Answered: true} // deny
		},
	})
	listCall := func(paths ...string) agentcore.AgentToolCall {
		raw, err := json.Marshal(map[string]any{"pattern": "x", "path": paths})
		if err != nil {
			t.Fatal(err)
		}
		return agentcore.AgentToolCall{Name: "grep", Arguments: raw}
	}
	if dec := gate(context.Background(), listCall(outside)); dec == nil || !dec.Block {
		t.Fatalf("a list-form path outside must be gated, got %+v", dec)
	}
	if got.BoundaryDir != outside {
		t.Errorf("offered = %q, want %q", got.BoundaryDir, outside)
	}
	// Several entries resolve like several shell operands: the finest wins.
	if dec := gate(context.Background(), listCall(outside, sub)); dec == nil || !dec.Block {
		t.Fatalf("a list holding a subtree must be gated, got %+v", dec)
	}
	if got.BoundaryDir != sub {
		t.Errorf("offered = %q, want the subtree %q", got.BoundaryDir, sub)
	}
	if asked != 2 {
		t.Errorf("asked %d times, want 2", asked)
	}
	// A list that stays inside the workspace is not a boundary question.
	if dec := gate(context.Background(), listCall("internal", "internal/judge")); dec != nil {
		t.Errorf("an in-workspace list was gated (%+v), want silent", dec)
	}
}
