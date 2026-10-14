package agenttool

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

// stubRunner stands in for the platform sandbox: it records that it was asked
// to isolate a command and rewrites the command so the test can see which argv
// ran.
type stubRunner struct {
	calls int
	argv  []string
}

func (s *stubRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	s.calls++
	s.argv = []string{shell, flag, command, dir}
	return []string{"echo", "sandboxed"}, func() {}, nil
}

// runBashCtx runs one bash call against an explicit context, for tests that
// exercise execution-context signals (sandbox requests).
func runBashCtx(t *testing.T, ctx context.Context, tool *BashTool, args map[string]any, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(ctx, "call-1", raw, onUpdate)
}

// TestBashExecutionLayerDoesNotGrade is the boundary this file pins: the bash
// tool holds no judge. A verdict is decided by the permission gate, and it
// reaches the execution layer in exactly two shapes — a context request
// (containment; TestBashSandboxRequestRoutesToRunner) or a block before the tool
// is ever called (judge.PermissionGate). What the tool still owns is the
// envelope: enforce mode, the live approval mode, and the sandbox-escalation
// request (see bash_escalation_test.go).
//
// Concretely: an ordinary command with a runner available and no gate decision
// runs direct, because nothing at this layer weighs it.
func TestBashExecutionLayerDoesNotGrade(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{
		Sandbox: runner,
	}
	// The command must be one the read-only recognizer does NOT match: a
	// recognized command is contained even on an allow verdict (see
	// TestBashReadOnlyFastPathIsContained), so `echo direct` would route to the
	// runner and this test would be pinning the wrong thing. `git --version` is
	// fast and not recognized (no subcommand word), unlike a build or test run
	// that would outlive the yield window.
	res, err := runBash(t, tool, map[string]any{"command": "git --version"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Fatal("nothing at this layer should have asked for isolation")
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "git version") {
		t.Fatalf("output = %q, want direct output", got)
	}
}

// TestBashSandboxRequestRoutesToRunner verifies the execution layer honors a
// sandbox request published by the permission gate (via the executor context):
// auto-mode sandbox-tier commands must run isolated without a per-turn
// classifier on the tool.
func TestBashSandboxRequestRoutesToRunner(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	// Not a read-only-recognized command: a recognized read is contained on its
	// own now, which would make this first assertion (no request, no runner)
	// fail for the wrong reason.
	res, err := runBashCtx(t, context.Background(), tool, map[string]any{"command": "git --version"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Fatal("no sandbox request should route to the runner")
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "git version") {
		t.Fatalf("output = %q, want direct output without a request", got)
	}

	// The gate's request rides agentcore.WithSandboxRequest.
	runner.calls = 0
	res, err = runBashCtx(t, agentcore.WithSandboxRequest(context.Background()), tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1 for a sandbox request", runner.calls)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want sandboxed argv output", got)
	}
}

// TestBashSandboxRequestWithoutRunnerFailsClosed verifies a gate request that
// cannot be honored refuses instead of running unconfined.
func TestBashSandboxRequestWithoutRunnerFailsClosed(t *testing.T) {
	tool := &BashTool{}
	res, err := runBashCtx(t, agentcore.WithSandboxRequest(context.Background()), tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "failing closed") {
		t.Fatalf("output = %q, want fail-closed message", got)
	}
}

func TestBashEnforceWithoutRunnerFailsClosed(t *testing.T) {
	tool := &BashTool{ForceSandbox: true}
	res, err := runBash(t, tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "failing closed") {
		t.Fatalf("output = %q, want fail-closed message", got)
	}
}

// TestBashPermissionModeReadOnlyBlocks verifies the execution layer honors the
// live mode even on paths without a BeforeToolCall gate (in-process
// sub-agents): read-only refuses every command, full-access skips grading.
func TestBashPermissionModeReadOnlyBlocks(t *testing.T) {
	mode := permissions.New(permissions.ReadOnly)
	tool := &BashTool{Permissions: mode}
	res, err := runBash(t, tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "read-only") {
		t.Fatalf("output = %q, want read-only block", got)
	}
	// A mid-run switch applies to the very next command.
	mode.Set(permissions.FullAccess)
	res, err = runBash(t, tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "hi") {
		t.Fatalf("output = %q, want the command to run in full-access", got)
	}
}

// A reviewer-failure containment test used to live here, driving a judge held by
// the bash tool. The containment is decided by the permission gate now
// (judge.PermissionGate turns a failed review into a Sandbox request when a
// runner exists and a block when it does not), and it is covered there by
// TestPermissionGateReviewerFailure; the execution layer's half — honoring the
// request, refusing when it cannot — is TestBashSandboxRequestRoutesToRunner and
// TestBashSandboxRequestWithoutRunnerFailsClosed above.

// TestBashReadOnlyFastPathIsContained is the containment floor under the
// read-only recognizer: a command it recognizes runs INSIDE the sandbox when a
// runner is available. Recognition is a statement about the command; it is not
// a promise that the consent gate saw every path, because that rests on two
// tables in another package staying in step. The sandbox's read whitelist — the
// workspace plus the roots the user granted — is what refuses a path the
// scanner missed.
func TestBashReadOnlyFastPathIsContained(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	res, err := runBash(t, tool, map[string]any{"command": "ls -la ."}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1: a recognized read must still be contained", runner.calls)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want the sandboxed argv's output", got)
	}

	// Without a runner there is nothing to contain with, so the command runs
	// directly — the exemption is about the reviewer, not about isolation.
	bare := &BashTool{}
	res, err = runBash(t, bare, map[string]any{"command": "ls -la ."}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want an unsandboxed run when no runner exists", got)
	}
}

// TestBashReadOnlyFastPathEscalationIsNotContained keeps the containment
// contract honest: a recognized read that ASKS to run outside the sandbox is
// not silently re-contained. Escalation is weighed by the permission gate's
// reviewer (judge.ReadOnlyShellCommand declines such a call precisely so it is),
// and re-containing it here after that verdict would make the model's request
// invisible — the retry loop the escalation path exists to avoid.
func TestBashReadOnlyFastPathEscalationIsNotContained(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	res, err := runBash(t, tool, map[string]any{
		"command":             "ls -la .",
		"sandbox_permissions": "require_escalated",
		"justification":       "the sandbox hides a tree I need",
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Errorf("runner calls = %d, want 0: an escalation request must not be silently re-contained", runner.calls)
	}
	if got := agentcore.ContentToText(res.Content); strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want a direct run", got)
	}
}

// TestBashReadOnlyFastPathReadGrantRunsDirect covers the "allow this read once"
// answer: a single approved path has no sandbox-whitelist form, so containing the
// call would fail the very read the user just approved.
func TestBashReadOnlyFastPathReadGrantRunsDirect(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	granted := filepath.Join(t.TempDir(), "granted.txt")
	if err := os.WriteFile(granted, []byte("granted contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithReadGrant(context.Background(), granted)
	res, err := runBashCtx(t, ctx, tool, map[string]any{"command": "cat " + granted}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0: a one-call read grant must run directly", runner.calls)
	}
	if got := agentcore.ContentToText(res.Content); strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want the direct run the grant was given for", got)
	}
}
