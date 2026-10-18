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
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/execsess"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
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
	// Permissions, when set, is the live approval mode. Read-only blocks every
	// command here too (the gate normally does it first; this covers paths
	// without a gate, e.g. in-process sub-agents), full-access skips grading,
	// and an auto-mode reviewer failure routes the command into the sandbox
	// instead of letting it run unisolated.
	Permissions *permissions.State
	// OnSandboxDenied, when set, is asked before a sandbox-denied command gives
	// up: the sandbox refused a path this command needs, and the person may want
	// to re-admit it — or to run this one command without the sandbox at all.
	// It receives the path (already extracted from the command and its
	// justification) and the command line the question is about, and reports
	// what was granted (see SandboxDenialAnswer). Wired by the drivers through
	// cli/run, so the question is literally the same one the refused-escape
	// dialog asks; a nil hook keeps the old behavior (the denial is labelled for
	// the model, nobody is interrupted).
	OnSandboxDenied func(ctx context.Context, path, summary string) SandboxDenialAnswer
	// deniedAsked is this tool's once-per-path memo for that question, the same
	// rule the gate applies: asked and answered is answered.
	deniedAsked judge.OnceSet
	// Sandbox builds the sandboxed argv for a command. Nil means no isolation
	// is available: sandbox-tier commands run directly (the gate already
	// prompted for them).
	Sandbox SandboxRunner
	// ForceSandbox routes every foreground command through Sandbox
	// (GOLDER_SANDBOX=enforce) and fails closed when Sandbox is nil.
	ForceSandbox bool
	// RequireSandbox is set where isolation IS the default (a platform with a
	// sandbox implementation, and GOLDER_SANDBOX not off). With it set, a
	// missing runner fails closed rather than silently running unconfined;
	// where isolation was never implemented (Windows) it stays false, and the
	// command runs directly as it always did.
	RequireSandbox bool
	// sessionsMu guards the lazy Sessions fallback in manager(). Production
	// wires one manager before any call runs (BuiltinTools shares it with
	// write_stdin); the lock exists for direct callers, and because bash is
	// ToolExecutionParallel — two commands in one batch reach manager() at the
	// same time.
	sessionsMu sync.Mutex
}

// SandboxDenialAnswer is what came back from the question a sandbox denial puts
// to the person. It is a choice, not a boolean, because "let it reach that path"
// and "let it out of the box" are different decisions with different
// consequences — and the second one exists because the command was already
// reviewed and approved before it ran; what the sandbox refused is a path, not
// the call.
type SandboxDenialAnswer int

const (
	// SandboxDenialRefused: nothing was granted. The original failure stands,
	// with its labelling for the model.
	SandboxDenialRefused SandboxDenialAnswer = iota
	// SandboxDenialReAdmitted: the refused path is registered, so re-running the
	// command inside the sandbox can now reach it.
	SandboxDenialReAdmitted
	// SandboxDenialUnconfined: run this one command outside the sandbox. The
	// asker has already recorded the decision (which emits a note, so an
	// unconfined run is never silent); the tool re-runs it once with a Direct
	// grant. Nothing about this is remembered.
	SandboxDenialUnconfined
)

// SandboxRunner builds a sandboxed argv for one shell invocation: argv[0] is
// the executable, argv[1:] its args. dir is advisory (the caller keeps
// cmd.Dir); cleanup removes any generated profile and runs once the session
// exits. It is a local interface so agenttool stays free of platform imports:
// internal/seatbelt satisfies it structurally and the cli layer injects it.
type SandboxRunner interface {
	SandboxArgv(shell, flag, command, dir string) (argv []string, cleanup func(), err error)
	SandboxCall(argv []string, dir string, policy seatbelt.CallPolicy) (sandboxed []string, cleanup func(), err error)
}

// manager returns the tool's session manager, creating a private one when the
// tool was constructed without wiring (tests, direct callers).
func (t *BashTool) manager() *execsess.Manager {
	t.sessionsMu.Lock()
	defer t.sessionsMu.Unlock()
	if t.Sessions == nil {
		t.Sessions = execsess.NewManager()
	}
	return t.Sessions
}

// StopSessions terminates every still-running session this tool spawned. It
// lets a driver drop a tool set (sub-agent teardown) without leaking OS
// processes.
func (t *BashTool) StopSessions() {
	t.sessionsMu.Lock()
	m := t.Sessions
	t.sessionsMu.Unlock()
	if m != nil {
		m.KillAll()
	}
}

// sandboxRoute chooses the execution envelope, NOT whether the command may
// execute. Approval is settled by the three gates. Every approved bash call
// runs confined by default, regardless of command text or risk level. Only an
// explicitly authorized Direct grant, or the user's full-access mode, permits a bare
// shell. A model's require_escalated argument is never itself an authorization.
func (t *BashTool) sandboxRoute(ctx context.Context, perms string) (sandbox bool, block string) {
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
	if (agentcore.DirectGrantedFromContext(ctx) || unconfinedApproved(ctx)) && !agentcore.SandboxRequestedFromContext(ctx) {
		return false, ""
	}
	if strings.EqualFold(strings.TrimSpace(perms), sandboxPermissionEscalated) &&
		!agentcore.SandboxRequestedFromContext(ctx) {
		return false, "bash: running outside the sandbox requires explicit approval; the command was not run"
	}
	if t.Sandbox == nil {
		if t.RequireSandbox {
			return false, "bash: no OS sandbox runner available; refusing to run unconfined (install bubblewrap, use full-access, or approve an explicit one-call escape)"
		}
		return false, ""
	}
	return true, ""
}

// sandboxArgv resolves the executable argv for a command: the direct
// interpreter by default, or the sandboxed argv when routing says so. ok=false
// with a message means the call must not run; ok=false with an empty message
// means run directly (auto mode without a runner: the gate already prompted).
// sandboxed reports whether the returned argv goes through the OS sandbox, so
// the result builder can label sandbox-shaped failures for the model.
func (t *BashTool) sandboxArgv(ctx context.Context, shell, flag, command, perms string) (argv []string, cleanup func(), msg string, ok bool, sandboxed bool) {
	argv = []string{shell, flag, command}
	sandbox, block := t.sandboxRoute(ctx, perms)
	if block != "" {
		return nil, nil, block, false, false
	}
	if !sandbox {
		return argv, nil, "", true, false
	}
	if t.Sandbox == nil {
		return nil, nil, "bash: no OS sandbox runner is available; the command was not run. Install a platform sandbox, use full-access, or approve an explicit one-call escape", false, false
	}
	policy := seatbelt.CallPolicy{Network: agentcore.NetworkGrantedFromContext(ctx)}
	seatbelt.RefusedPaths(&policy)
	if p := agentcore.ReadGrantFromContext(ctx); p != "" {
		policy.ReadFiles = []string{p}
	}
	sa, cl, err := t.Sandbox.SandboxCall(argv, t.Dir, policy)
	if err != nil {
		return nil, nil, fmt.Sprintf("bash: the sandbox could not be created (%v); the command was not run. Check the platform sandbox, or approve an explicit one-call escape", err), false, false
	}
	return sa, cl, "", true, true
}

// bashToolArgs is the decoded argument shape for BashTool.
type bashToolArgs struct {
	// Command is the shell command line to run.
	Command string `json:"command"`
	// Network asks the authorization gate for outbound access inside the file
	// sandbox. "on" is a request, NOT an entitlement; the gate's one-call
	// network grant is required before execution.
	Network string `json:"network,omitempty"`
	// TimeoutMs is an optional hard deadline in milliseconds: the command is
	// killed when it elapses. 0 (the default) means no deadline.
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// YieldMs overrides how long to wait for the command to finish before
	// returning a session id. 0 uses bashDefaultYield.
	YieldMs int `json:"yield_time_ms,omitempty"`
	// TTY runs the command on a pseudo-terminal, making its stdin writable
	// with arbitrary input via write_stdin (interactive programs).
	TTY bool `json:"tty,omitempty"`
	// SandboxPermissions is the per-command sandbox override (codex parity):
	// "use_default" runs under the turn's routing; "require_escalated" asks to
	// run OUTSIDE the sandbox, which the permission gate must approve — the
	// model asks, the user (or the auto reviewer) decides. Justification is
	// surfaced in that approval prompt.
	SandboxPermissions string `json:"sandbox_permissions,omitempty"`
	// Justification is the user-facing reason for a require_escalated request.
	Justification string `json:"justification,omitempty"`
}

// Name implements AgentTool.
func (t *BashTool) Name() string { return "bash" }

// Description implements AgentTool.
func (t *BashTool) Description() string {
	return "Run a shell command, streaming stdout/stderr. If it is still running " +
		"after yield_time_ms (default 10s) it keeps running as a session and the " +
		"result carries a bash_id: read more output with write_stdin, and send " +
		"chars \"" + interruptHint + "\" (Ctrl-C) to interrupt it. timeout_ms is a " +
		"hard deadline that kills the command (capped at 10 minutes). Commands run " +
		"inside an OS sandbox by default (writes confined to the workspace and temp dir, " +
		"network off). Set network=on for a call that needs outbound access, such as " +
		"git push or a package download: the authorization gate decides execution " +
		"AND network in one review, keeping the file sandbox. A sandbox denial is " +
		"labelled in the output — when a command legitimately needs files the " +
		"sandbox denies, retry it with " +
		"sandbox_permissions=\"require_escalated\" plus a short justification, which " +
		"asks for a one-call escape (the reviewer decides that under auto, the user " +
		"under ask; a declined request leaves the command in the sandbox). Network " +
		"access alone never requires " +
		"running outside the sandbox. Set tty=true " +
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
	    "network": {"type": "string", "enum": ["off", "on"], "description": "Outbound network access for THIS invocation inside the file sandbox. Default off. For git push/fetch, curl or package downloads, request on; the authorization gate must approve it alongside execution. This does not allow reading additional files."},
    "timeout_ms": {"type": "integer", "description": "Hard deadline in milliseconds; the command is killed when it elapses (capped at 10 minutes). Omit for no deadline.", "minimum": 0},
    "yield_time_ms": {"type": "integer", "description": "How long to wait for the command to finish before returning a bash_id for a running session. Defaults to 10000 ms (capped at 30000).", "minimum": 0},
    "tty": {"type": "boolean", "description": "Run the command on a pseudo-terminal so its stdin can be written via write_stdin (interactive programs). Defaults to false (pipes)."},
	    "sandbox_permissions": {"type": "string", "enum": ["use_default", "require_escalated"], "description": "Per-command file sandbox override. Defaults to use_default. Set require_escalated only if a legitimate call needs FILE access the sandbox denies (e.g. a build cache outside the project); it asks for a one-call sandbox escape, decided by the reviewer under auto and by the user under ask. For network use network=on instead."},
    "justification": {"type": "string", "description": "User-facing reason for a require_escalated request; omit otherwise."}
  },
  "required": ["command"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Shell calls batch in parallel, matching
// codex: there the shell is a shareable tool (supports_parallel_tool_calls =
// true, dispatched under a read lock) and the exclusive lock belongs to the
// editing tools, which act as barriers. Here that division is the grouping
// rule: apply_patch is sequential and stands alone, bash shares a concurrent
// group with its neighbours. Two shell commands ordered against each other
// must therefore be split across messages, or chained inside one call (the
// batching guide says so). What makes sharing safe: each call owns its process
// and its session, the session manager and per-session buffers are
// mutex-guarded, and a sandbox profile is written per call.
func (t *BashTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// shellLookPath resolves a program on PATH. It is a package var so tests can
// simulate a Windows box with or without bash installed.
var shellLookPath = exec.LookPath

// sandboxPermissionEscalated is the bash-tool argument value that asks to run
// the command outside the sandbox (codex's "require_escalated"). The model sets
// it; the permission gate decides, so this is a request, not a grant.
const sandboxPermissionEscalated = "require_escalated"

// looksSandboxDenied reports whether a sandboxed command's output looks like a
// sandbox denial rather than an ordinary command failure. exitCode must be
// non-zero; the caller only consults this for failed sandboxed runs. The
// signatures live in judge, beside the path extraction that reads the same
// lines, so one list both recognizes a denial and describes it.
func looksSandboxDenied(output string) bool {
	return judge.LooksSandboxDenied(output)
}

// sandboxFalseNegativeNote warns the model that sandbox-denied file checks can
// masquerade as ordinary results: a denied stat looks exactly like a missing
// file (test -e / ls), and rm -f exits 0 without deleting. It rides on both
// escalation hints so the model never reports a deletion (or a missing file)
// as fact based on the very checks the sandbox distorts.
const sandboxFalseNegativeNote = " Note: sandbox-denied file checks are false negatives — a denied stat looks like a missing file (test -e, ls), and rm -f can exit 0 without deleting anything. Do not report a deletion or a missing file as fact from those checks; when it matters, tell the user what to verify outside the sandbox."

// sandboxEscalationHint is appended to a sandboxed failure that looks like a
// denial. It tells the model exactly what the situation is and how to proceed,
// mirroring codex's guidance to retry with escalation when the sandbox (not the
// command) is the blocker.
const sandboxEscalationHint = "\n[sandboxed: this failure looks like sandbox policy (writes are confined to the project and TMPDIR; the network is off). If the command is legitimate and needs access the sandbox denies, retry it once with sandbox_permissions=\"require_escalated\" and a short justification — that asks for a one-call escape, which the reviewer decides under auto and the user under ask. If it is not granted, the command runs sandboxed again.]" + sandboxFalseNegativeNote

// sandboxEscalationDeniedHint is the loop-breaker: the command already carried
// sandbox_permissions=require_escalated and still ran sandboxed, so the request
// was not granted (the user denied it, nobody could approve it, or the reviewer
// kept the call contained). Telling the model to retry again would loop
// forever, so this hint names the dead end and points at the way out.
const sandboxEscalationDeniedHint = "\n[sandboxed: sandbox_permissions=\"require_escalated\" was not granted (the reviewer declined it, the user declined it, or there was nobody to ask), so this command ran inside the sandbox again. Retrying the same escalation will fail the same way — stop and tell the user what access the task needs, or finish with a sandbox-compatible approach.]" + sandboxFalseNegativeNote

// sandboxEscalationAutoNote is appended to the denied hint when the run is in
// auto mode. Review is the only decider there, so the model is pointed at the
// one step that actually changes the answer instead of being left to retry an
// escape no human will be shown.
const sandboxEscalationAutoNote = " This run is in auto mode, where the reviewer alone answers escapes: retrying cannot change the answer, but two smaller fixes can — (1) if the blocked access is a read, ask the user to re-admit that path inside the sandbox (`/boundary readable add <path>`, or GOLDER_SANDBOX_READABLE for the next run); (2) to write a directory, ask them to switch /permissions to ask, where the dialog's w/s rows grant the path and keep the call contained."

// looksRGMissing reports whether a failed command's output says ripgrep is not
// installed. Only the failure path consults it.
//
// The shapes were measured on this tool's unix shells: bash prints
// "bash: rg: command not found", zsh prints "zsh:1: command not found: rg",
// dash — which is /bin/sh on Debian and Ubuntu, so `sh -c` and #!/bin/sh
// scripts land here — prints "/bin/dash: 1: rg: not found", and a shell with
// PATH unset prints "rg: No such file or directory". Each names rg explicitly,
// which is what keeps the match from firing on some other missing program; a
// shell with different wording simply does not get the hint (fail-quiet, never
// a wrong claim).
//
// Two markers are anchored rather than searched for anywhere in the line. The
// PATH-unset shape starts a line, so a grep that failed on a file literally
// named `rg` ("grep: rg: No such file or directory") is not read as ripgrep
// being missing; the dash shape ends a line, because that is where dash puts
// it, while a program mentioning rg mid-sentence ("plugin rg: not found in
// registry") does not end there.
func looksRGMissing(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		switch {
		case lower == "":
			continue
		case strings.Contains(lower, "rg: command not found"),
			strings.Contains(lower, "command not found: rg"),
			strings.HasSuffix(lower, "rg: not found"),
			strings.HasPrefix(lower, "rg: no such file or directory"):
			return true
		}
	}
	return false
}

// rgMissingHint answers the machine where ripgrep is absent, which is the one
// case the search guidance cannot help with: the grep and find tools are
// ripgrep-backed, so they fail there too, and the only route left is the
// shell's own grep/find. Without this the model tends to retry `rg`, then try
// the grep tool (which fails with an install hint), and only then fall back —
// three turns for something the environment block already knows.
const rgMissingHint = "\n[ripgrep is not installed on this machine. The grep and find tools cannot run either (they are ripgrep-backed), so use the shell's own grep/find for this and later searches — e.g. `grep -rn \"pattern\" .` and `find . -name '*.go'`. Installing ripgrep (macOS: brew install ripgrep, Debian/Ubuntu: apt install ripgrep) restores the faster tools.]"

// looksPTYUnavailable reports whether a failed command could not get a
// pseudo-terminal. The shapes are the ones a tool like expect prints when the
// environment has no PTYs left ("The system has no more ptys", "out of ptys",
// "failed to allocate pty"); the match is deliberately narrow so ordinary
// output mentioning a pty does not get the hint.
func looksPTYUnavailable(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{
		"no more ptys",
		"out of ptys",
		"no ptys available",
		"failed to allocate pty",
		"cannot allocate pty",
		"could not allocate pty",
		"unable to allocate pty",
		"pty allocation failed",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ptyUnavailableHint stops the escalation loop a PTY failure would otherwise
// start. A pseudo-terminal is an OS capability, not a file path: no sandbox
// escape grants one, so retrying with require_escalated repeats the same
// human-facing path-consent dialog without ever fixing the failure. The
// alternative is to test the interactive behavior in-process.
const ptyUnavailableHint = "\n[pty: this environment cannot allocate a pseudo-terminal (the sandbox provides no PTYs). A sandbox_permissions=\"require_escalated\" retry will not create one — do not retry it. Test the interactive/TUI behavior with an in-process or unit test instead.]"

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
// sandboxRetryKey marks a command that is already the retry after a path was
// re-admitted: the second run must not re-open the question that produced it.
type sandboxRetryKey struct{}

func withSandboxRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, sandboxRetryKey{}, true)
}

func sandboxRetryDone(ctx context.Context) bool {
	done, _ := ctx.Value(sandboxRetryKey{}).(bool)
	return done
}

// unconfinedKey marks a command the person approved to re-run outside the
// sandbox from the denial question (SandboxDenialUnconfined). It is the gate's
// own answer travelling to the retry, exactly as agentcore.WithDirectGrant is
// for a gate-approved escape — the difference is only which question produced
// it: this one is asked at the moment the sandbox refused, with the command
// already in front of the person.
type unconfinedKey struct{}

func withUnconfinedRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, unconfinedKey{}, true)
}

func unconfinedApproved(ctx context.Context) bool {
	v, _ := ctx.Value(unconfinedKey{}).(bool)
	return v
}

// Execute runs one command: sandboxed by default, direct only where the gate
// granted a Direct decision. A sandbox denial that names a path is offered back
// to the person before the failure is returned (see OnSandboxDenied).
func (t *BashTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[bashToolArgs](args, "bash")
	if bad != nil {
		return *bad, nil
	}
	if a.Command == "" {
		return errorResult("bash: command is required"), nil
	}
	if a.Network == "on" && (t.Permissions == nil || t.Permissions.Mode() != permissions.FullAccess) &&
		!agentcore.NetworkGrantedFromContext(ctx) && !agentcore.DirectGrantedFromContext(ctx) &&
		!unconfinedApproved(ctx) {
		return errorResult("bash: network access was requested but was not authorized; the command was not run. Ask for a network approval, or keep the command local"), nil
	}

	shell, flag := resolveShell(t.Shell, runtime.GOOS, shellLookPath)
	argv, cleanup, msg, ok, sandboxed := t.sandboxArgv(ctx, shell, flag, a.Command, a.SandboxPermissions)
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
	// A failed sandboxed command is labelled so the model can tell sandbox
	// policy from an ordinary failure and, when warranted, ask for escalation.
	// The hint is appended to the output the model sees (the error line and the
	// result content), not to the user's view.
	hint := ""
	sandboxDenied := sandboxed && snap.ExitCode != 0 && looksSandboxDenied(out)
	// A missing PTY is a capability answer, not a sandbox-path denial: no
	// escalation can create one, so it gets its own hint before the sandbox
	// advice and the retry loop it would otherwise invite.
	ptyUnavailable := snap.ExitCode != 0 && looksPTYUnavailable(out)
	switch {
	case ptyUnavailable:
		hint = ptyUnavailableHint
	case sandboxDenied:
		if strings.EqualFold(strings.TrimSpace(a.SandboxPermissions), sandboxPermissionEscalated) {
			hint = sandboxEscalationDeniedHint
			// Under auto the request never reached a person, so the hint names
			// the one step that changes the answer instead of leaving the model
			// to retry an escape nobody will be shown.
			if t.Permissions != nil && t.Permissions.Mode() == permissions.Auto {
				hint += sandboxEscalationAutoNote
			}
		} else {
			hint = sandboxEscalationHint
		}
	}
	// A missing ripgrep is the one failure the search guidance cannot cover:
	// the grep/find tools are ripgrep-backed too, so the hint has to point at
	// the shell's own grep/find rather than at a tool that will fail next.
	// Checked after the sandbox hint so a genuine sandbox denial keeps its
	// escalation advice; the two markers do not overlap in practice (a missing
	// binary prints "command not found", not "operation not permitted").
	rgMissing := false
	if snap.ExitCode != 0 && !ptyUnavailable && !sandboxDenied && looksRGMissing(out) {
		rgMissing = true
		hint = rgMissingHint
	}

	// The sandbox denied a path this command needs. Offer it back to the person
	// — the question is the refused-escape dialog's, asked at the moment the
	// failure makes it concrete — and on a yes run the command again with the
	// path admitted: the grant is in the table before the retry builds its
	// profile, so the second run happens INSIDE the sandbox. A refusal, or a
	// second denial, leaves the original failure in place. Escalation requests
	// are skipped: the escape flow already had its say about that path.
	denialPath := ""
	if sandboxDenied && t.OnSandboxDenied != nil && !sandboxRetryDone(ctx) &&
		!strings.EqualFold(strings.TrimSpace(a.SandboxPermissions), sandboxPermissionEscalated) {
		// The denial's own message is the ground truth about which path was
		// refused: the OS prints the file the syscall failed on, read or write,
		// whatever the shell built it from. The command text only gets a say
		// when the message names nothing grantable.
		p := judge.PathsFromDenial(out, t.Dir)
		if p == "" {
			p = judge.GrantablePathFromText(a.Command, a.Justification, t.Dir)
		}
		denialPath = p
		if p != "" && t.deniedAsked.First(p) {
			switch t.OnSandboxDenied(ctx, p, a.Command) {
			case SandboxDenialReAdmitted:
				return t.Execute(withSandboxRetry(ctx), id, args, onUpdate)
			case SandboxDenialUnconfined:
				// The person chose to run this one command outside the sandbox,
				// at the moment the sandbox refused it. The retry flag keeps the
				// denial question from being asked again if it fails for another
				// reason; the unconfined flag is what sandboxRoute honours.
				return t.Execute(withUnconfinedRun(withSandboxRetry(ctx)), id, args, onUpdate)
			}
		}
	}
	if sandboxDenied && denialPath == "" {
		// Every path the OS named was system-protected or credential material.
		// Offering a one-click path grant would be a lie: SIP will still refuse
		// /private/var/select/sh, and ~/.ssh/known_hosts is not something this
		// dialog may hand over. Say what the user can actually do.
		if why := judge.DenialExplanation(out, t.Dir); why != "" {
			hint += "\n[sandboxed: " + why + "]" + sandboxFalseNegativeNote
		}
	}

	if !exited {
		text := out
		if text != "" {
			text += "\n"
		}
		text += fmt.Sprintf("[%s: running]\nUse write_stdin with bash_id %q to read more output; send chars \"%s\" (Ctrl-C) to interrupt.",
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
		details := map[string]any{"exitCode": snap.ExitCode}
		if sandboxDenied {
			details["sandboxDenied"] = true
		}
		if rgMissing {
			details["rgMissing"] = true
		}
		return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(out)},
				Details: details,
			},
			fmt.Errorf("bash: command exited with code %d\n%s%s", snap.ExitCode, out, hint)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(out)},
		Details: map[string]any{"exitCode": 0},
	}, nil
}
