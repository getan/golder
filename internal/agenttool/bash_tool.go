// This file implements the bash tool: run a shell command under a managed
// session (internal/execsess), streaming stdout/stderr back as
// tool_execution_update partials. A command that finishes within its yield
// window returns its output directly; one that is still running hands back a
// bash_id the model polls with write_stdin. A non-zero exit is surfaced as an
// error (isError) whose message carries the captured output.
package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/execsess"
	"github.com/smallnest/pigo/internal/judge"
	"github.com/smallnest/pigo/internal/permissions"
)

// bashMaxOutputBytes caps how many bytes of combined stdout/stderr one result
// returns to the model. A single command can emit megabytes (build logs, a big
// cat), which would otherwise flow into context whole and blow the window.
// Output past this size is truncated to a head + tail preview (see
// truncateBashOutput), mirroring search's "[truncated …]" convention. The
// session's own buffer is bounded separately and larger (execsess.OutputCapBytes).
const bashMaxOutputBytes = 30_000

const (
	// bashDefaultYield is how long a bash call waits for its command to finish
	// before returning a session id for the still-running process.
	bashDefaultYield = 10 * time.Second
	// bashMinYield / bashMaxYield clamp a caller-supplied yield_time_ms.
	bashMinYield = 250 * time.Millisecond
	bashMaxYield = 30 * time.Second
	// bashMaxTimeout caps an explicit timeout_ms hard deadline.
	bashMaxTimeout = 10 * time.Minute
	// interruptChar is the Ctrl-C byte write_stdin accepts; interruptHint is
	// its escaped, model-facing spelling.
	interruptChar = "\u0003"
	interruptHint = `\u0003`
)

// truncateBashOutput caps s at bashMaxOutputBytes using the shared
// truncateToBudget idiom (head + "[truncated N bytes]" marker + tail, cut on
// UTF-8 rune boundaries). It is the bash tool's own inner cap; the executor
// layer applies a separate, uniform outer budget afterward.
func truncateBashOutput(s string) string {
	return truncateToBudget(s, bashMaxOutputBytes)
}

// BashTool runs shell commands. Dir bounds the working directory (empty = the
// process CWD). Shell selects the interpreter (empty = "bash -c").
type BashTool struct {
	// Dir is the working directory for commands. Empty uses the process CWD.
	Dir string
	// Shell is the interpreter path. Empty defaults to "bash".
	Shell string
	// Sessions tracks running commands so they can be polled and killed.
	// Production always wires one shared manager (BuiltinTools); tests may
	// omit it and get a private manager per tool.
	Sessions *execsess.Manager
	// Judge grades each command before it runs. When nil the command runs
	// directly (the production path: the BeforeToolCall gate grades — sharing
	// its verdict cache — and publishes sandbox requirements through the
	// executor context instead of a per-turn classifier here). When set, a
	// sandbox-tier verdict routes the command through Sandbox and a deny-tier
	// verdict blocks it, for direct callers that drive the tool without a gate.
	Judge judge.Classifier
	// Permissions, when set, is the live approval mode. Read-only blocks every
	// command here too (the gate normally does it first; this covers paths
	// without a gate, e.g. in-process sub-agents), full-access skips grading,
	// and an auto-mode reviewer failure routes the command into the sandbox
	// instead of letting it run unisolated.
	Permissions *permissions.State
	// Sandbox builds the sandboxed argv for a command. Nil means no isolation
	// is available: sandbox-tier commands run directly (the gate already
	// prompted for them).
	Sandbox SandboxRunner
	// ForceSandbox routes every foreground command through Sandbox
	// (PIGO_SANDBOX=enforce) and fails closed when Sandbox is nil.
	ForceSandbox bool
}

// SandboxRunner builds a sandboxed argv for one shell invocation: argv[0] is
// the executable, argv[1:] its args. dir is advisory (the caller keeps
// cmd.Dir); cleanup removes any generated profile and runs once the session
// exits. It is a local interface so agenttool stays free of platform imports:
// internal/seatbelt satisfies it structurally and the cli layer injects it.
type SandboxRunner interface {
	SandboxArgv(shell, flag, command, dir string) (argv []string, cleanup func(), err error)
}

// manager returns the tool's session manager, creating a private one when the
// tool was constructed without wiring (tests, direct callers).
func (t *BashTool) manager() *execsess.Manager {
	if t.Sessions == nil {
		t.Sessions = execsess.NewManager()
	}
	return t.Sessions
}

// StopSessions terminates every still-running session this tool spawned. It
// lets a driver drop a tool set (sub-agent teardown) without leaking OS
// processes.
func (t *BashTool) StopSessions() {
	if t.Sessions != nil {
		t.Sessions.KillAll()
	}
}

// sandboxRoute decides whether one command must run isolated. Sources, in
// order: PIGO_SANDBOX=enforce (every foreground command), a sandbox request
// published by the permission gate (auto/ask routing of a sandbox-tier grade
// or reviewer-failure containment), then the optional Judge for direct
// callers. It returns sandbox=true to run isolated, a non-empty message to
// refuse, or both zero values to run directly.
func (t *BashTool) sandboxRoute(ctx context.Context, command string) (sandbox bool, block string) {
	if t.Permissions != nil {
		switch t.Permissions.Mode() {
		case permissions.ReadOnly:
			return false, "bash: blocked by read-only permissions mode (/permissions to change)"
		case permissions.FullAccess:
			return false, ""
		}
	}
	if t.ForceSandbox {
		return true, ""
	}
	// The permission gate graded this exact call into the sandbox tier (or the
	// reviewer failed and auto mode chose containment); the decision rides the
	// executor context so routing holds in every driver without per-turn
	// classifier wiring. No runner cannot be honored safely → fail closed.
	if agentcore.SandboxRequestedFromContext(ctx) {
		if t.Sandbox == nil {
			return false, "bash: the permission gate requires sandboxing but no sandbox runner is available; failing closed"
		}
		return true, ""
	}
	if t.Judge == nil {
		return false, ""
	}
	raw, _ := json.Marshal(map[string]string{"command": command})
	v := t.Judge.Classify(ctx, "bash", raw)
	if v.Failed && t.Permissions != nil && t.Permissions.Mode() == permissions.Auto {
		// The reviewer could not decide; auto mode contains the command in
		// the sandbox rather than running it unisolated.
		if t.Sandbox == nil {
			return false, "bash: reviewer unavailable and no sandbox runner; failing closed"
		}
		return true, ""
	}
	switch v.Level {
	case judge.Deny:
		msg := "bash: blocked by risk judge (deny"
		if len(v.Reasons) > 0 {
			msg += ": " + v.Reasons[0]
		}
		return false, msg + ")"
	case judge.Sandbox:
		return true, ""
	default:
		return false, ""
	}
}

// sandboxArgv resolves the executable argv for a command: the direct
// interpreter by default, or the sandboxed argv when routing says so. ok=false
// with a message means the call must not run; ok=false with an empty message
// means run directly (auto mode without a runner: the gate already prompted).
func (t *BashTool) sandboxArgv(ctx context.Context, shell, flag, command string) (argv []string, cleanup func(), msg string, ok bool) {
	argv = []string{shell, flag, command}
	sandbox, block := t.sandboxRoute(ctx, command)
	if block != "" {
		return nil, nil, block, false
	}
	if !sandbox {
		return argv, nil, "", true
	}
	if t.Sandbox == nil {
		if t.ForceSandbox {
			return nil, nil, "bash: PIGO_SANDBOX=enforce but no sandbox runner is available (macOS sandbox-exec required); failing closed", false
		}
		return argv, nil, "", true
	}
	sa, cl, err := t.Sandbox.SandboxArgv(shell, flag, command, t.Dir)
	if err != nil {
		if t.ForceSandbox {
			return nil, nil, fmt.Sprintf("bash: sandbox setup failed (%v); failing closed", err), false
		}
		return argv, nil, "", true
	}
	return sa, cl, "", true
}

// bashToolArgs is the decoded argument shape for BashTool.
type bashToolArgs struct {
	// Command is the shell command line to run.
	Command string `json:"command"`
	// TimeoutMs is an optional hard deadline in milliseconds: the command is
	// killed when it elapses. 0 (the default) means no deadline.
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// YieldMs overrides how long to wait for the command to finish before
	// returning a session id. 0 uses bashDefaultYield.
	YieldMs int `json:"yield_time_ms,omitempty"`
	// TTY runs the command on a pseudo-terminal, making its stdin writable
	// with arbitrary input via write_stdin (interactive programs).
	TTY bool `json:"tty,omitempty"`
}

// Name implements AgentTool.
func (t *BashTool) Name() string { return "bash" }

// Description implements AgentTool.
func (t *BashTool) Description() string {
	return "Run a shell command, streaming stdout/stderr. If it is still running " +
		"after yield_time_ms (default 10s) it keeps running as a session and the " +
		"result carries a bash_id: read more output with write_stdin, and send " +
		"chars \"" + interruptHint + "\" (Ctrl-C) to interrupt it. timeout_ms is a " +
		"hard deadline that kills the command (capped at 10 minutes). Set tty=true " +
		"for interactive programs whose stdin must be written (a REPL, a pager, a " +
		"prompt). A non-zero " +
		"exit code is reported as an error. On Windows the command runs under bash " +
		"if available (Git Bash/WSL), else PowerShell, else cmd — prefer portable " +
		"commands."
}

// Schema implements AgentTool.
func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command":    {"type": "string", "description": "Shell command line to run."},
    "timeout_ms": {"type": "integer", "description": "Hard deadline in milliseconds; the command is killed when it elapses (capped at 10 minutes). Omit for no deadline.", "minimum": 0},
    "yield_time_ms": {"type": "integer", "description": "How long to wait for the command to finish before returning a bash_id for a running session. Defaults to 10000 ms (capped at 30000).", "minimum": 0},
    "tty": {"type": "boolean", "description": "Run the command on a pseudo-terminal so its stdin can be written via write_stdin (interactive programs). Defaults to false (pipes)."}
  },
  "required": ["command"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Commands can have side effects → sequential.
func (t *BashTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// shellLookPath resolves a program on PATH. It is a package var so tests can
// simulate a Windows box with or without bash installed.
var shellLookPath = exec.LookPath

// resolveShell picks the interpreter and the flag that makes it read the command
// from the next argument. An explicit shell (BashTool.Shell) is always honored as
// a POSIX-style "<shell> -c <command>".
//
// On Windows with no explicit shell, the naive "bash -c" hardcode fails on stock
// machines that have no bash on PATH — the model then retries bash blindly and
// every call errors (issue #518). So we prefer a real bash when one is present
// (Git Bash / WSL / MSYS), since commands are authored in bash syntax, and fall
// back to PowerShell, then cmd, so a command still runs on a bare Windows box.
func resolveShell(explicit, goos string, lookPath func(string) (string, error)) (shell, flag string) {
	if explicit != "" {
		return explicit, "-c"
	}
	if goos == "windows" {
		if p, err := lookPath("bash"); err == nil {
			return p, "-c"
		}
		if p, err := lookPath("powershell"); err == nil {
			return p, "-Command"
		}
		return "cmd", "/C"
	}
	return "bash", "-c"
}

// bashYield clamps the caller's yield window, defaulting to bashDefaultYield.
func bashYield(ms int) time.Duration {
	if ms <= 0 {
		return bashDefaultYield
	}
	d := time.Duration(ms) * time.Millisecond
	if d < bashMinYield {
		return bashMinYield
	}
	if d > bashMaxYield {
		return bashMaxYield
	}
	return d
}

// Execute implements AgentTool. It streams combined stdout/stderr via onUpdate,
// waits up to the yield window for the command to finish, and hands back a
// bash_id when it is still running. Caller cancellation during that initial
// wait kills the command (the behavior Ctrl+C users expect); once Execute has
// returned a session, the process survives until write_stdin or exit.
func (t *BashTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[bashToolArgs](args, "bash")
	if bad != nil {
		return *bad, nil
	}
	if a.Command == "" {
		return errorResult("bash: command is required"), nil
	}

	shell, flag := resolveShell(t.Shell, runtime.GOOS, shellLookPath)
	argv, cleanup, msg, ok := t.sandboxArgv(ctx, shell, flag, a.Command)
	if !ok {
		if msg == "" {
			return errorResult("bash: command blocked"), nil
		}
		return errorResult(msg), nil
	}

	var timeout time.Duration
	if a.TimeoutMs > 0 {
		timeout = time.Duration(a.TimeoutMs) * time.Millisecond
		if timeout > bashMaxTimeout {
			timeout = bashMaxTimeout
		}
	}
	yield := bashYield(a.YieldMs)

	// Stream output chunks while this call is in flight. The gate ensures a
	// chunk racing the end of the initial wait cannot call onUpdate after
	// Execute has returned.
	var obsMu sync.Mutex
	obsOpen := onUpdate != nil
	var observer func(string)
	if onUpdate != nil {
		observer = func(chunk string) {
			obsMu.Lock()
			defer obsMu.Unlock()
			if obsOpen {
				onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(chunk)}})
			}
		}
	}

	sess, err := t.manager().Start(execsess.StartRequest{
		Command:  a.Command,
		Argv:     argv,
		Dir:      t.Dir,
		Timeout:  timeout,
		Cleanup:  cleanup,
		Observer: observer,
		TTY:      a.TTY,
	})
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("")}},
				fmt.Errorf("bash: could not start shell %q: %v. On Windows install Git Bash or WSL (or configure a shell); commands are bash syntax", shell, execErr.Err)
		}
		return errorResult(fmt.Sprintf("bash: could not start command: %v", err)), nil
	}

	exited := sess.Wait(ctx, yield, execsess.WaitExit)
	if onUpdate != nil {
		obsMu.Lock()
		obsOpen = false
		obsMu.Unlock()
		sess.SetObserver(nil)
	}

	// Caller cancellation during the initial wait stops the command, matching
	// the old foreground behavior (Ctrl+C kills the running command).
	if !exited && ctx.Err() != nil {
		sess.Kill()
		out := truncateBashOutput(sess.ReadNew())
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(out)}},
			fmt.Errorf("bash: command canceled\n%s", out)
	}

	snap := sess.Snapshot()
	out := truncateBashOutput(sess.ReadNew())

	if !exited {
		text := out
		if text != "" {
			text += "\n"
		}
		text += fmt.Sprintf("[%s: running]\nUse write_stdin with bash_id %q to read more output; send chars \"%s\" to interrupt.",
			sess.ID, sess.ID, interruptHint)
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(text)},
			Details: map[string]any{"bash_id": sess.ID, "status": string(execsess.StatusRunning)},
		}, nil
	}

	if snap.TimedOut {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(out)}},
			fmt.Errorf("bash: command timed out after %s\n%s", snap.Timeout, out)
	}
	if snap.ExitCode != 0 {
		return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(out)},
				Details: map[string]any{"exitCode": snap.ExitCode},
			},
			fmt.Errorf("bash: command exited with code %d\n%s", snap.ExitCode, out)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(out)},
		Details: map[string]any{"exitCode": 0},
	}, nil
}
