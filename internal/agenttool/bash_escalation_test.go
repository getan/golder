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
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

// TestBashEscapeNoteTellsTheModelTheEnvelope locks the line that closes the
// loop between the gate and the model: the result text — all the model ever
// sees — must say which envelope the call actually ran in. The person already
// sees it on the card and in the gate's note; before this, a request that was
// re-contained looked exactly like a granted escape to the model.
func TestBashEscapeNoteTellsTheModelTheEnvelope(t *testing.T) {
	dir := t.TempDir()
	args := map[string]any{
		"command":             "echo hi",
		"sandbox_permissions": "require_escalated",
		"justification":       "needs a path outside the workspace",
	}

	// The gate contained the call: the escape was not granted, and the result
	// says so instead of letting the model assume it is outside the sandbox.
	runner := &stubRunner{}
	tool := &BashTool{Dir: dir, Sandbox: runner}
	res, err := runBashCtx(t, agentcore.WithSandboxRequest(context.Background()), tool, args, nil)
	if err != nil {
		t.Fatalf("contained run: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("sandbox runs = %d, want the call contained", runner.calls)
	}
	if got := resultText(res); !strings.Contains(got, "escape not granted") {
		t.Fatalf("contained escalation result must name the envelope, got %q", got)
	}

	// The gate granted the escape: the command runs as itself and the result
	// says so, so a later "why is this blocked?" is not asked of the sandbox.
	direct := &stubRunner{}
	tool = &BashTool{Dir: dir, Sandbox: direct}
	res, err = runBashCtx(t, agentcore.WithDirectGrant(context.Background()), tool, args, nil)
	if err != nil {
		t.Fatalf("direct run: %v", err)
	}
	if direct.calls != 0 {
		t.Fatalf("a granted escape must not go through the runner, calls=%d", direct.calls)
	}
	if got := resultText(res); !strings.Contains(got, "escape granted") {
		t.Fatalf("granted escalation result must name the envelope, got %q", got)
	}

	// An ordinary call carries no envelope noise: the line is about a request
	// the model made, not about every command.
	plain := &stubRunner{}
	tool = &BashTool{Dir: dir, Sandbox: plain}
	res, err = runBashCtx(t, context.Background(), tool, map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatalf("plain run: %v", err)
	}
	if got := resultText(res); strings.Contains(got, "escape") {
		t.Fatalf("an ordinary call must not mention the escape, got %q", got)
	}
}

// TestBashRequireEscalatedRunsDirect proves the request is not authorization:
// without a Direct grant it cannot run. An explicit decision from the gate
// permits a single direct invocation, without re-entering the sandbox.
func TestBashRequireEscalatedRunsDirect(t *testing.T) {
	dir := t.TempDir()
	runner := &stubRunner{}
	tool := &BashTool{Dir: dir, Sandbox: runner}
	args := map[string]any{
		"command":             "echo escalated",
		"sandbox_permissions": "require_escalated",
		"justification":       "needs a build cache outside the workspace",
	}
	res, err := runBashCtx(t, context.Background(), tool, args, nil)
	if err != nil || !strings.Contains(resultText(res), "requires explicit approval") {
		t.Fatalf("request without authorization ran or gave the wrong refusal: result=%q err=%v", resultText(res), err)
	}
	res, err = runBashCtx(t, agentcore.WithDirectGrant(context.Background()), tool, args, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("require_escalated must run direct, calls=%d", runner.calls)
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
	if !strings.Contains(err.Error(), "false negatives") {
		t.Errorf("sandbox denial hint should warn about false-negative checks, got: %v", err)
	}
	_ = runner
}

// TestBashEscalationDeniedHint breaks the retry loop: a command that already
// carried require_escalated and still ran sandboxed must be told the request
// was not granted — not told to retry it again.
func TestBashEscalationDeniedHint(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{
		Dir:          dir,
		Sandbox:      failingRunner{},
		ForceSandbox: true,
	}
	_, err := runBashErr(t, tool, map[string]any{
		"command":             "true",
		"sandbox_permissions": "require_escalated",
		"justification":       "please",
	})
	if err == nil {
		t.Fatal("expected a failure from the sandboxed probe")
	}
	if !strings.Contains(err.Error(), "was not granted") {
		t.Errorf("denied escalation hint missing, got: %v", err)
	}
	if strings.Contains(err.Error(), "retry it once with sandbox_permissions") {
		t.Errorf("denied escalation must not advise retrying the same request: %v", err)
	}
	if !strings.Contains(err.Error(), "false negatives") {
		t.Errorf("denied escalation hint should warn about false-negative checks, got: %v", err)
	}
}

// TestBashEscalationAutoNote: under auto nobody will ever see the escape
// request, so the denied hint names the one step that changes the answer
// (switching to ask) instead of leaving the model to retry a dead end — and
// under ask, where the question did reach the person, it says nothing of the
// sort.
func TestBashEscalationAutoNote(t *testing.T) {
	dir := t.TempDir()
	run := func(mode permissions.Mode) string {
		tool := &BashTool{
			Dir:          dir,
			Sandbox:      failingRunner{},
			ForceSandbox: true,
			Permissions:  permissions.New(mode),
		}
		_, err := runBashErr(t, tool, map[string]any{
			"command":             "true",
			"sandbox_permissions": "require_escalated",
			"justification":       "please",
		})
		if err == nil {
			t.Fatal("expected a failure from the sandboxed probe")
		}
		return err.Error()
	}
	if got := run(permissions.Auto); !strings.Contains(got, "auto mode") ||
		!strings.Contains(got, "/permissions to ask") || !strings.Contains(got, "/boundary readable add") {
		t.Errorf("auto denied hint should name both smaller fixes (read re-admission, mode switch), got: %v", got)
	}
	if got := run(permissions.Ask); strings.Contains(got, "auto mode") || strings.Contains(got, "/boundary readable add") {
		t.Errorf("ask denied hint must not talk about auto or its escape hatches, got: %v", got)
	}
}

// countedRunner counts how many times a sandbox was built, so a test can tell a
// retry from a single run; every run fails denial-shaped, the way a real
// sandbox refuses a path outside the whitelist.
type countedRunner struct {
	runs int
}

func (c *countedRunner) SandboxArgv(shell, flag, _ string, _ string) ([]string, func(), error) {
	c.runs++
	return []string{shell, flag, "echo 'Operation not permitted' >&2; exit 1"}, func() {}, nil
}

func (c *countedRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	c.runs++
	return []string{argv[0], argv[1], "echo 'Operation not permitted' >&2; exit 1"}, func() {}, nil
}

// TestBashSandboxDenialOffersPathAndRetries: a path the sandbox refused is
// offered to the person at the moment the failure makes it concrete, and a yes
// re-runs the command with the path admitted — still contained, asked once.
func TestBashSandboxDenialOffersPathAndRetries(t *testing.T) {
	probe := func(t *testing.T, command string, answer func(path string) SandboxDenialAnswer) (*countedRunner, []string) {
		t.Helper()
		runner := &countedRunner{}
		var asked []string
		tool := &BashTool{
			Dir: t.TempDir(), Sandbox: runner, ForceSandbox: true,
			Permissions: permissions.New(permissions.Auto),
			OnSandboxDenied: func(_ context.Context, path, _ string) SandboxDenialAnswer {
				asked = append(asked, path)
				return answer(path)
			},
		}
		if _, err := runBashErr(t, tool, map[string]any{"command": command}); err == nil {
			t.Fatal("expected the sandboxed probe to fail")
		}
		return runner, asked
	}

	// A yes: one question, and the command runs a second time with the path in
	// the table (the retry's own failure does not re-open the question).
	runner, asked := probe(t, "go build -o /Volumes/KIOXIA/build/out .", func(string) SandboxDenialAnswer {
		return SandboxDenialReAdmitted
	})
	if len(asked) != 1 || asked[0] != "/Volumes/KIOXIA/build/out" {
		t.Fatalf("asked = %v, want the one path the command named", asked)
	}
	if runner.runs != 2 {
		t.Errorf("sandbox runs = %d, want a retry after the grant", runner.runs)
	}

	// A no: the failure stands, with one question and one run.
	runner, asked = probe(t, "go build -o /Volumes/KIOXIA/build/out .", func(string) SandboxDenialAnswer {
		return SandboxDenialRefused
	})
	if len(asked) != 1 || runner.runs != 1 {
		t.Errorf("refusal: asked = %v, runs = %d, want one of each", asked, runner.runs)
	}

	// Nothing to offer: a command that names no path never opens the question.
	runner, asked = probe(t, "make", func(string) SandboxDenialAnswer { return SandboxDenialReAdmitted })
	if len(asked) != 0 || runner.runs != 1 {
		t.Errorf("no path: asked = %v, runs = %d, want no question and one run", asked, runner.runs)
	}
}

// failingRunner simulates a sandbox whose command fails with a denial-shaped
// message (the shape a real sandbox-exec/bwrap refusal produces).
type failingRunner struct{}

func (failingRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	return []string{shell, flag, "echo 'Operation not permitted' >&2; exit 1"}, func() {}, nil
}

func (failingRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	return []string{argv[0], argv[1], "echo 'Operation not permitted' >&2; exit 1"}, func() {}, nil
}

// TestBashNonSandboxFailureHasNoHint asserts an unsandboxed failure is not
// labelled as a sandbox denial.
func TestBashNonSandboxFailureHasNoHint(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{Dir: dir, Permissions: permissions.New(permissions.FullAccess)}
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

// TestLooksPTYUnavailable locks the marker matching: a PTY exhaustion message
// is recognised, while ordinary output that merely mentions a pty is not.
func TestLooksPTYUnavailable(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"The system has no more ptys.  Ask your system administrator to create more.", true},
		{"expect: out of ptys", true},
		{"failed to allocate pty: No such file or directory", true},
		{"cannot allocate pty", true},
		{"tests passed; pty helpers skipped", false},
		{"bash: /Users/x/y: Operation not permitted", false},
	}
	for _, c := range cases {
		if got := looksPTYUnavailable(c.out); got != c.want {
			t.Errorf("looksPTYUnavailable(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

// ptyRunner simulates a sandbox whose command cannot get a pseudo-terminal —
// the shape expect prints when the environment has no PTYs left.
type ptyRunner struct{}

const ptyUnavailableOutput = "The system has no more ptys.  Ask your system administrator to create more."

func (ptyRunner) SandboxArgv(shell, flag, _, _ string) ([]string, func(), error) {
	return []string{shell, flag, "echo '" + ptyUnavailableOutput + "' >&2; exit 1"}, func() {}, nil
}

func (ptyRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	return []string{argv[0], argv[1], "echo '" + ptyUnavailableOutput + "' >&2; exit 1"}, func() {}, nil
}

// TestBashPTYUnavailableHint: a PTY allocation failure must not look like a
// path denial. It gets its own hint, which says an escalation cannot create a
// PTY and points at in-process tests — even when the call did ask for an
// escalation. This is the loop-breaker for the repeated path-consent dialogs.
func TestBashPTYUnavailableHint(t *testing.T) {
	for _, args := range []map[string]any{
		{"command": "expect -c 'spawn true'"},
		{
			"command":             "expect -c 'spawn true'",
			"sandbox_permissions": "require_escalated",
			"justification":       "drive the TUI through a pty",
		},
	} {
		tool := &BashTool{Dir: t.TempDir(), Sandbox: ptyRunner{}, ForceSandbox: true}
		_, err := runBashErr(t, tool, args)
		if err == nil {
			t.Fatal("expected the PTY probe to fail")
		}
		if !strings.Contains(err.Error(), "pseudo-terminal") || !strings.Contains(err.Error(), "do not retry") {
			t.Errorf("PTY failure should carry the no-retry hint, got: %v", err)
		}
		if strings.Contains(err.Error(), "retry it once with sandbox_permissions") {
			t.Errorf("PTY failure must not advise an escalation retry: %v", err)
		}
		if strings.Contains(err.Error(), "was not granted") {
			t.Errorf("PTY failure is a capability answer, not a denied escalation: %v", err)
		}
	}
}

// runBashErr is the error-returning sibling of runBash for failure assertions.
func runBashErr(t *testing.T, tool *BashTool, args map[string]any) (string, error) {
	t.Helper()
	res, err := runBashCtx(t, context.Background(), tool, args, nil)
	return resultText(res), err
}
