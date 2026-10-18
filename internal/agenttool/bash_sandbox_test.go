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
	"github.com/getan/golder/internal/seatbelt"
)

// stubRunner stands in for the platform sandbox: it records that it was asked
// to isolate a command and rewrites the command so the test can see which argv
// ran.
type stubRunner struct {
	calls  int
	argv   []string
	policy seatbelt.CallPolicy
}

func (s *stubRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	s.calls++
	s.argv = []string{shell, flag, command, dir}
	return []string{"echo", "sandboxed"}, func() {}, nil
}

func (s *stubRunner) SandboxCall(argv []string, dir string, policy seatbelt.CallPolicy) ([]string, func(), error) {
	s.calls++
	s.argv = append(append([]string{}, argv...), dir)
	s.policy = policy
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

// No shell content inspection is needed for sandbox routing: every approved
// command gets the same file sandbox, unless its gate decision grants Direct.
func TestBashExecutionLayerDoesNotGrade(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{
		Sandbox: runner,
	}
	res, err := runBash(t, tool, map[string]any{"command": "git --version"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 1 {
		t.Fatal("an approved bash command must be isolated regardless of its spelling")
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want isolated output", got)
	}
}

// A bare envelope is the gate's "the command is fully enumerable" decision: it
// skips the sandbox, while enforce still wins and the absence of the envelope
// keeps the default containment.
func TestBareEnvelopeSkipsTheSandbox(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	ctx := agentcore.WithBareEnvelope(context.Background(), "enumerable")

	if sandbox, block := tool.sandboxRoute(ctx, ""); sandbox || block != "" {
		t.Fatalf("a bare envelope must skip the sandbox, got sandbox=%v block=%q", sandbox, block)
	}
	tool.ForceSandbox = true
	if sandbox, _ := tool.sandboxRoute(ctx, ""); !sandbox {
		t.Error("enforce must contain even an enumerable call")
	}
	tool.ForceSandbox = false
	if sandbox, _ := tool.sandboxRoute(context.Background(), ""); !sandbox {
		t.Error("without the gate's envelope a call is contained")
	}
}

// TestBashSandboxRequestRoutesToRunner verifies the execution layer honors a
// sandbox request published by the permission gate (via the executor context):
// auto-mode sandbox-tier commands must run isolated without a per-turn
// classifier on the tool.
func TestBashSandboxRequestRoutesToRunner(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	res, err := runBashCtx(t, context.Background(), tool, map[string]any{"command": "git --version"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 1 {
		t.Fatal("normal call should be sandboxed by default")
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want sandboxed output", got)
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
	tool := &BashTool{RequireSandbox: true}
	res, err := runBashCtx(t, agentcore.WithSandboxRequest(context.Background()), tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "refusing to run unconfined") {
		t.Fatalf("output = %q, want fail-closed message", got)
	}
}

func TestBashEnforceWithoutRunnerFailsClosed(t *testing.T) {
	tool := &BashTool{ForceSandbox: true}
	res, err := runBash(t, tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The message says what was not done and what to do next, rather than the
	// old bare "failing closed".
	got := agentcore.ContentToText(res.Content)
	for _, want := range []string{"was not run", "Install a platform sandbox"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
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

// The policy no longer depends on whether a command is classified read-only.
func TestBashOrdinaryReadIsContained(t *testing.T) {
	runner := &stubRunner{}
	tool := &BashTool{Sandbox: runner}
	res, err := runBash(t, tool, map[string]any{"command": "ls -la ."}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1: every read must be contained", runner.calls)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want the sandboxed argv's output", got)
	}

	// Missing sandbox does not permit an unisolated run where isolation is the
	// platform default (RequireSandbox), while a platform without any sandbox
	// implementation keeps its previous behavior.
	bare := &BashTool{RequireSandbox: true}
	res, err = runBashCtx(t, context.Background(), bare, map[string]any{"command": "ls -la ."}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "refusing to run unconfined") {
		t.Fatalf("output = %q, want fail-closed refusal when no runner exists", got)
	}
}

// A request without the gate's Direct grant must not escape containment.
func TestBashEscalationRequestWithoutGrantIsRefused(t *testing.T) {
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
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "requires explicit approval") {
		t.Fatalf("output = %q, want refusal without a Direct grant", got)
	}
}

// A one-call file read is an exact sandbox allow, not a sandbox escape.
func TestBashReadGrantRunsContained(t *testing.T) {
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
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1: a one-call read grant must stay sandboxed", runner.calls)
	}
	if len(runner.policy.ReadFiles) != 1 || runner.policy.ReadFiles[0] != granted {
		t.Fatalf("one-call sandbox grant = %+v, want only %s", runner.policy, granted)
	}
	if got := agentcore.ContentToText(res.Content); !strings.Contains(got, "sandboxed") {
		t.Fatalf("output = %q, want contained run", got)
	}
}
