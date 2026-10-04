package agenttool

// Tests for the sandbox escalation path (codex parity): a command carrying
// sandbox_permissions=require_escalated skips the sandbox wrapper, and a
// sandboxed failure that looks like a policy denial is labelled with the
// escalation hint so the model knows how to retry.

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
)

// TestBashRequireEscalatedSkipsLocalJudge proves the request keeps a call out
// of the local judge's sandbox routing (the driver-level path where no
// permission gate decision exists).
func TestBashRequireEscalatedSkipsLocalJudge(t *testing.T) {
	dir := t.TempDir()
	runner := &stubRunner{}
	tool := &BashTool{
		Dir:     dir,
		Judge:   stubGrader{v: judge.Verdict{Level: judge.Sandbox}},
		Sandbox: runner,
	}
	res, err := runBash(t, tool, map[string]any{
		"command":             "echo escalated",
		"sandbox_permissions": "require_escalated",
		"justification":       "needs a build cache outside the workspace",
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("require_escalated must skip the local judge's routing, calls=%d", runner.calls)
	}
	if got := resultText(res); !strings.Contains(got, "escalated") {
		t.Fatalf("output = %q, want the direct command's output", got)
	}
}

// TestBashEscalationCannotBypassGateOrEnforce locks the security ordering: a
// keep-escalation request never defeats the permission gate's sandbox request
// (the gate saw the request in the args and still chose containment) nor
// GOLDER_SANDBOX=enforce (the user's explicit always-sandbox choice).
func TestBashEscalationCannotBypassGateOrEnforce(t *testing.T) {
	dir := t.TempDir()
	escalated := map[string]any{
		"command":             "echo escalated",
		"sandbox_permissions": "require_escalated",
		"justification":       "please",
	}

	// Gate request wins.
	gateRunner := &stubRunner{}
	gateTool := &BashTool{Dir: dir, Sandbox: gateRunner}
	ctx := agentcore.WithSandboxRequest(context.Background())
	if _, err := runBashCtx(t, ctx, gateTool, escalated, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gateRunner.calls != 1 {
		t.Fatalf("gate sandbox request must win over escalation, calls=%d", gateRunner.calls)
	}

	// Enforce mode wins.
	enforceRunner := &stubRunner{}
	enforceTool := &BashTool{Dir: dir, Sandbox: enforceRunner, ForceSandbox: true}
	if _, err := runBashCtx(t, context.Background(), enforceTool, escalated, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if enforceRunner.calls != 1 {
		t.Fatalf("enforce mode must win over escalation, calls=%d", enforceRunner.calls)
	}
}

// TestBashSandboxDenialHint asserts a sandboxed failure whose output looks
// like a policy denial carries the escalation hint, and that a non-sandboxed
// failure does not.
func TestBashSandboxDenialHint(t *testing.T) {
	// The stub runner rewrites the command to a fixed echo, so drive the real
	// path with a real shell instead: no runner, but ForceSandbox + a failing
	// sandbox-shaped command. Simpler: run a command that emits EPERM-like
	// text while routed through the sandbox.
	dir := t.TempDir()
	runner := &stubRunner{}
	// stubRunner replaces the command with "echo sandboxed" (exit 0), so it
	// cannot exercise failures. Use a BashTool whose Sandbox is a runner that
	// preserves a failing command via the actual shell and a denied marker.
	tool := &BashTool{
		Dir:          dir,
		Judge:        stubGrader{v: judge.Verdict{Level: judge.Sandbox}},
		Sandbox:      failingRunner{},
		ForceSandbox: true,
	}
	_, err := runBashErr(t, tool, map[string]any{"command": "true"})
	if err == nil {
		t.Fatal("expected a failure from the sandboxed probe")
	}
	if !strings.Contains(err.Error(), "require_escalated") {
		t.Errorf("sandbox denial should carry the escalation hint, got: %v", err)
	}
	_ = runner
}

// failingRunner simulates a sandbox whose command fails with a denial-shaped
// message (the shape a real sandbox-exec/bwrap refusal produces).
type failingRunner struct{}

func (failingRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	return []string{shell, flag, "echo 'Operation not permitted' >&2; exit 1"}, func() {}, nil
}

// TestBashNonSandboxFailureHasNoHint asserts an unsandboxed failure is not
// labelled as a sandbox denial.
func TestBashNonSandboxFailureHasNoHint(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{Dir: dir}
	_, err := runBashErr(t, tool, map[string]any{"command": "echo 'Operation not permitted' >&2; exit 1"})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "sandboxed: this failure looks like sandbox policy") {
		t.Errorf("unsandboxed failure must not carry the sandbox hint: %v", err)
	}
}

// TestLooksSandboxDenied locks the marker matching.
func TestLooksSandboxDenied(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"bash: /Users/x/y: Operation not permitted", true},
		{"curl: (7) Failed to connect", false},
		{"mkdir: cannot create directory: Read-only file system", true},
		{"grep: no matches found", false},
		{"sandbox-exec: file-write denied", true},
	}
	for _, c := range cases {
		if got := looksSandboxDenied(c.out); got != c.want {
			t.Errorf("looksSandboxDenied(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

// runBashErr is the error-returning sibling of runBash for failure assertions.
func runBashErr(t *testing.T, tool *BashTool, args map[string]any) (string, error) {
	t.Helper()
	res, err := runBashCtx(t, context.Background(), tool, args, nil)
	return resultText(res), err
}
