// This file implements the bash tool (US-018): run a shell command, streaming
// stdout/stderr back as tool_execution_update partials, honoring a timeout and
// context cancellation (which kills the child process group). A non-zero exit
// is surfaced as an error (isError) whose message carries the captured output.
package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/judge"
)

// bashDefaultTimeout bounds a command that does not specify one.
const bashDefaultTimeout = 2 * time.Minute

// bashMaxTimeout caps any requested timeout.
const bashMaxTimeout = 10 * time.Minute

// bashMaxOutputBytes caps how many bytes of combined stdout/stderr the bash tool
// returns to the model. A single command can emit megabytes (build logs, a big
// cat), which — unlike the timeout cap — would otherwise flow into context whole
// and blow the window. Output past this size is truncated to a head + tail
// preview (see truncateBashOutput), mirroring search's searchMaxResults/"[truncated
// …]" convention. This is the tool's own inner cap; a later executor-layer budget
// may impose a stricter outer limit.
const bashMaxOutputBytes = 30_000

// truncateBashOutput caps s at bashMaxOutputBytes using the shared
// truncateToBudget idiom (head + "[truncated N bytes]" marker + tail, cut on
// UTF-8 rune boundaries). It is the bash tool's own inner cap; the executor
// layer applies a separate, uniform outer budget afterward.
func truncateBashOutput(s string) string {
	return truncateToBudget(s, bashMaxOutputBytes)
}

// trimUTF8Prefix drops trailing bytes of s that form an incomplete rune, so the
// returned prefix ends on a rune boundary.
func trimUTF8Prefix(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// trimUTF8Suffix drops leading bytes of s that form an incomplete rune, so the
// returned suffix starts on a rune boundary.
func trimUTF8Suffix(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[1:]
			continue
		}
		break
	}
	return s
}

// BashTool runs shell commands. Dir bounds the working directory (empty = the
// process CWD). Shell selects the interpreter (empty = "bash -c").
type BashTool struct {
	// Dir is the working directory for commands. Empty uses the process CWD.
	Dir string
	// Shell is the interpreter path. Empty defaults to "bash".
	Shell string
	// Jobs holds background jobs launched with run_in_background. When nil,
	// run_in_background is rejected (the front-end did not wire a store).
	Jobs *BashJobStore
	// Judge grades each command before it runs. When nil the command runs
	// directly (today's behavior). When set, a sandbox-tier verdict routes
	// the command through Sandbox and a deny-tier verdict blocks it, so the
	// execution layer honors the same grade the BeforeToolCall gate saw
	// (the shared verdict cache makes the second grade free).
	Judge judge.Classifier
	// Sandbox builds the sandboxed argv for a command. Nil means no
	// isolation is available: sandbox-tier commands run directly (the gate
	// already prompted for them).
	Sandbox SandboxRunner
	// ForceSandbox routes every foreground command through Sandbox
	// (PIGO_SANDBOX=enforce) and fails closed when Sandbox is nil.
	ForceSandbox bool
}

// SandboxRunner builds a sandboxed argv for one shell invocation: argv[0] is
// the executable, argv[1:] its args. dir is advisory (the caller keeps
// cmd.Dir); cleanup removes any generated profile and runs after the command
// finishes. It is a local interface so agenttool stays free of platform
// imports: internal/seatbelt satisfies it structurally and the cli layer
// injects it.
type SandboxRunner interface {
	SandboxArgv(shell, flag, command, dir string) (argv []string, cleanup func(), err error)
}

// sandboxRoute grades one command for sandbox routing. It returns sandbox=true
// when the command must run isolated, or a non-empty block message when the
// grade denies the command outright (defense in depth for paths where the
// BeforeToolCall gate is unset; wiring keeps Judge nil then, so this is
// normally unreachable).
func (t *BashTool) sandboxRoute(ctx context.Context, command string) (sandbox bool, block string) {
	if t.ForceSandbox {
		return true, ""
	}
	if t.Judge == nil {
		return false, ""
	}
	raw, _ := json.Marshal(map[string]string{"command": command})
	v := t.Judge.Classify(ctx, "bash", raw)
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
	// TimeoutMs optionally overrides the default timeout (milliseconds).
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// RunInBackground detaches the command from the turn: it keeps running after
	// Execute returns, and its output is drained later via bash_output. A
	// background command has no default timeout (so dev servers/watchers run
	// indefinitely); timeout_ms still caps it if given.
	RunInBackground bool `json:"run_in_background,omitempty"`
}

// Name implements AgentTool.
func (t *BashTool) Name() string { return "bash" }

// Description implements AgentTool.
func (t *BashTool) Description() string {
	return "Run a shell command, streaming stdout/stderr. Supports a timeout " +
		"and cancellation. A non-zero exit code is reported as an error. " +
		"Set run_in_background=true for long-running commands (dev servers, " +
		"watchers): it returns immediately with a bash_id you drain with " +
		"bash_output and stop with kill_bash. " +
		"On Windows the command runs under bash if available (Git Bash/WSL), " +
		"else PowerShell, else cmd — prefer portable commands."
}

// Schema implements AgentTool.
func (t *BashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command":    {"type": "string", "description": "Shell command line to run."},
    "timeout_ms": {"type": "integer", "description": "Timeout in milliseconds (capped at 10 minutes). Ignored in background unless set.", "minimum": 0},
    "run_in_background": {"type": "boolean", "description": "Run detached and return immediately with a bash_id; drain output with bash_output, stop with kill_bash."}
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

// streamWriter forwards each written chunk to onUpdate as an incremental
// delta while accumulating the full output. Deltas are the update contract:
// consumers append partials (tui subagentPanel.appendOutput, stream-json),
// so resending the whole snapshot per chunk would duplicate output
// quadratically. The full text still lands in the final ToolResult. It is
// safe for concurrent use so stdout and stderr can share the same combined
// buffer.
type streamWriter struct {
	mu       *sync.Mutex
	buf      *bytes.Buffer
	onUpdate agentcore.ToolUpdateFunc
}

func (w streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf.Write(p)
	w.mu.Unlock()
	if w.onUpdate != nil {
		w.onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(string(p))}})
	}
	return len(p), nil
}

// Execute implements AgentTool. It streams combined stdout/stderr via onUpdate,
// enforces a timeout, and kills the process on context cancellation. A non-zero
// exit returns a Go error (→ isError) carrying the exit code and output.
func (t *BashTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[bashToolArgs](args, "bash")
	if bad != nil {
		return *bad, nil
	}
	if a.Command == "" {
		return errorResult("bash: command is required"), nil
	}

	if a.RunInBackground {
		return t.startBackground(a)
	}

	timeout := bashDefaultTimeout
	if a.TimeoutMs > 0 {
		timeout = time.Duration(a.TimeoutMs) * time.Millisecond
	}
	if timeout > bashMaxTimeout {
		timeout = bashMaxTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell, flag := resolveShell(t.Shell, runtime.GOOS, shellLookPath)
	argv, cleanup, msg, ok := t.sandboxArgv(ctx, shell, flag, a.Command)
	if !ok {
		if msg == "" {
			return errorResult("bash: command blocked"), nil
		}
		return errorResult(msg), nil
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	configureProcessGroup(cmd)
	if t.Dir != "" {
		cmd.Dir = t.Dir
	}

	var mu sync.Mutex
	var combined bytes.Buffer
	sw := streamWriter{mu: &mu, buf: &combined, onUpdate: onUpdate}
	cmd.Stdout = sw
	cmd.Stderr = sw

	if err := cmd.Start(); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("")}},
				fmt.Errorf("bash: could not start shell %q: %v. On Windows install Git Bash or WSL (or configure a shell); commands are bash syntax", shell, execErr.Err)
		}
		return errorResult(fmt.Sprintf("bash: could not start command: %v", err)), nil
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var err error
	select {
	case err = <-done:
	case <-runCtx.Done():
		pid := 0
		if cmd.Process != nil {
			pid = cmd.Process.Pid
		}
		shouldEscalate := terminateProcessGroup(pid)
		timer := time.NewTimer(terminationGracePeriod)
		select {
		case err = <-done:
			timer.Stop()
			if shouldEscalate {
				killProcessGroup(pid)
			}
		case <-timer.C:
			killProcessGroup(pid)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = <-done
		}
	}

	mu.Lock()
	output := combined.String()
	mu.Unlock()

	// Cap the output before it enters any ToolResult / error message, so a single
	// command's huge output cannot blow the model's context. Truncation keeps a
	// head + tail preview with a "[truncated N bytes]" marker in the middle.
	output = truncateBashOutput(output)

	// Context cancellation / timeout takes precedence in the message.
	if runCtx.Err() == context.DeadlineExceeded {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(output)}},
			fmt.Errorf("bash: command timed out after %s\n%s", timeout, output)
	}
	if ctx.Err() == context.Canceled {
		return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(output)}},
			fmt.Errorf("bash: command canceled\n%s", output)
	}

	if err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(output)},
				Details: map[string]any{"exitCode": exitCode},
			},
			fmt.Errorf("bash: command exited with code %d\n%s", exitCode, output)
	}

	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(output)},
		Details: map[string]any{"exitCode": 0},
	}, nil
}

// startBackground launches the command detached from the turn context and
// returns immediately with a bash_id. The job runs under its own cancelable
// context (rooted at context.Background(), not the turn ctx which is canceled
// when the turn ends), so it survives past Execute. A background command has no
// default timeout — a dev server or watcher is expected to run indefinitely —
// but an explicit timeout_ms still caps it. Its combined output accumulates in
// the job's buffer for bash_output to drain; kill_bash cancels its context.
func (t *BashTool) startBackground(a bashToolArgs) (agentcore.AgentToolResult, error) {
	if t.Jobs == nil {
		return errorResult("bash: run_in_background is not available in this environment"), nil
	}

	var jobCtx context.Context
	var cancel context.CancelFunc
	if a.TimeoutMs > 0 {
		timeout := time.Duration(a.TimeoutMs) * time.Millisecond
		if timeout > bashMaxTimeout {
			timeout = bashMaxTimeout
		}
		jobCtx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		jobCtx, cancel = context.WithCancel(context.Background())
	}

	shell, flag := resolveShell(t.Shell, runtime.GOOS, shellLookPath)
	argv, cleanup, blockMsg, ok := t.sandboxArgv(context.Background(), shell, flag, a.Command)
	if !ok {
		cancel()
		if blockMsg == "" {
			return errorResult("bash: command blocked"), nil
		}
		return errorResult(blockMsg), nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	configureProcessGroup(cmd)
	if t.Dir != "" {
		cmd.Dir = t.Dir
	}

	job := t.Jobs.create(a.Command, cancel)
	w := job.writer()
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Start(); err != nil {
		cancel()
		if cleanup != nil {
			cleanup()
		}
		job.finish(-1, err.Error())
		return errorResult(fmt.Sprintf("bash: could not start background command: %v", err)), nil
	}

	go func(pid int, ctx context.Context) {
		<-ctx.Done()
		if terminateProcessGroup(pid) {
			time.Sleep(terminationGracePeriod)
			killProcessGroup(pid)
		} else {
			killProcess(pid)
		}
	}(cmd.Process.Pid, jobCtx)

	go func() {
		err := cmd.Wait()
		cancel()
		if cleanup != nil {
			cleanup()
		}
		exitCode := 0
		errMsg := ""
		if err != nil {
			exitCode = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			}
			errMsg = err.Error()
		}
		job.finish(exitCode, errMsg)
	}()

	msg := fmt.Sprintf("started background command %s: %s\nuse bash_output %q to read its output, kill_bash %q to stop it", job.ID, a.Command, job.ID, job.ID)
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"bash_id": job.ID, "background": true},
	}, nil
}
