// This file implements the write_stdin tool: the model's handle on a running
// bash session. An empty chars polls new output and status; chars "\u0003"
// (Ctrl-C) interrupts the session's process group, and a second interrupt
// force-kills a process that ignores SIGINT. Arbitrary input requires a PTY
// session (bash with tty=true); pipe sessions reject it with a clear message
// rather than silently ignoring it.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/execsess"
)

const (
	// writeStdinDefaultPoll is how long an empty poll waits for output or exit.
	writeStdinDefaultPoll = 5 * time.Second
	// writeStdinInterruptPoll is how long the interrupt path waits for the
	// process to react and produce output.
	writeStdinInterruptPoll = 250 * time.Millisecond
	// exitReportGrace is how long a poll that just saw new output waits for the
	// process to be reaped, so a command that finished during the poll reports
	// its exit in the same result instead of forcing another poll.
	exitReportGrace = 50 * time.Millisecond
)

// WriteStdinTool polls and controls sessions created by the bash tool. Sessions
// is the shared manager the bash tool populates.
type WriteStdinTool struct {
	Sessions *execsess.Manager
}

// writeStdinArgs is the decoded argument shape for WriteStdinTool.
type writeStdinArgs struct {
	// BashID is the session handle returned by a bash call.
	BashID string `json:"bash_id"`
	// Chars is optional input: "\u0003" (Ctrl-C) interrupts any session;
	// other text is written to stdin on a tty session.
	Chars string `json:"chars,omitempty"`
	// YieldMs overrides how long the poll waits before returning.
	YieldMs int `json:"yield_time_ms,omitempty"`
}

// Name implements AgentTool.
func (t *WriteStdinTool) Name() string { return "write_stdin" }

// Description implements AgentTool.
func (t *WriteStdinTool) Description() string {
	return "Read new output from a running bash session (addressed by bash_id) " +
		"and report whether it is still running or has exited with a code. With " +
		"chars \"" + interruptHint + "\" it sends Ctrl-C to the session's process " +
		"group; a second interrupt force-kills a process that ignores it. Ctrl-C " +
		"is also accepted as the literal text " + interruptHint + ", ^C, or the " +
		"U+E002 private-use spelling. On a " +
		"session started with tty=true, other input is written to the program's " +
		"stdin (include \\n to submit a line)."
}

// Schema implements AgentTool.
func (t *WriteStdinTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "bash_id": {"type": "string", "description": "The bash_id of a running session."},
    "chars": {"type": "string", "description": "Optional input. Ctrl-C (\"\\u0003\", the literal text \\u0003, ^C, or U+E002) interrupts on any session; any other text is written to stdin on a tty session (bash with tty=true)."},
    "yield_time_ms": {"type": "integer", "description": "How long to wait for new output or exit before returning. Defaults to 5000 ms for a poll and 250 ms for an interrupt (capped at 30000).", "minimum": 0}
  },
  "required": ["bash_id"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Ordering matters: a poll must not race an
// interrupt, so calls are sequential.
func (t *WriteStdinTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// writeStdinYield clamps the poll window: a poll waits up to 5s by default, an
// interrupt up to 250ms, both within the shared yield bounds.
func writeStdinYield(chars string, ms int) time.Duration {
	def := writeStdinDefaultPoll
	if chars != "" {
		def = writeStdinInterruptPoll
	}
	if ms <= 0 {
		return def
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

// isInterruptChars reports whether chars asks for a Ctrl-C interrupt. Besides
// the control character itself, models emit several escaped spellings when
// they serialize the key: the literal "\u0003" text, "^C", and — observed in
// practice — the U+E002 private-use rune some harnesses use to pass control
// keys through JSON. All of them mean the same thing here.
func isInterruptChars(chars string) bool {
	t := strings.TrimSpace(chars)
	if strings.Contains(t, interruptChar) {
		return true
	}
	switch strings.ToLower(t) {
	// Raw-string cases are the literal escaped spellings; the last case is the
	// private-use rune itself.
	case `\u0003`, `^c`, `\ue002`, "\ue002":
		return true
	}
	return false
}

// sessionStatusLine renders one session's state as a single bracketed line,
// e.g. "[bash_2: running]" or "[bash_3: exited code 1: signal: killed]".
func sessionStatusLine(s execsess.Snapshot) string {
	switch {
	case s.Status == execsess.StatusRunning:
		return fmt.Sprintf("[%s: running]", s.ID)
	case s.TimedOut:
		return fmt.Sprintf("[%s: exited: timed out after %s]", s.ID, s.Timeout)
	case s.Err != "":
		if s.ExitCode < 0 {
			// A signal death has no meaningful exit code; report the signal.
			return fmt.Sprintf("[%s: exited: %s]", s.ID, s.Err)
		}
		return fmt.Sprintf("[%s: exited code %d: %s]", s.ID, s.ExitCode, s.Err)
	default:
		return fmt.Sprintf("[%s: exited code %d]", s.ID, s.ExitCode)
	}
}

// Execute implements AgentTool. It returns output produced since the previous
// read plus a status line, waits up to the yield window for either new output
// or exit, and never fails the call for a non-zero exit code: the status line
// carries the code so the model can decide what to do next.
func (t *WriteStdinTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[writeStdinArgs](args, "write_stdin")
	if bad != nil {
		return *bad, nil
	}
	if t.Sessions == nil {
		return errorResult("write_stdin: shell sessions are not available in this environment"), nil
	}
	sess, ok := t.Sessions.Get(a.BashID)
	if !ok {
		return errorResult(fmt.Sprintf("write_stdin: no shell session with id %q", a.BashID)), nil
	}

	if isInterruptChars(a.Chars) {
		sess.Interrupt()
	} else if a.Chars != "" {
		if !sess.IsTTY() {
			return errorResult("write_stdin: this session has no tty, so input cannot be written; rerun bash with tty=true for interactive input, or send \"" + interruptHint + "\" (Ctrl-C)"), nil
		}
		if err := sess.SendInput(a.Chars); err != nil {
			return errorResult(fmt.Sprintf("write_stdin: could not write to the session (%v); poll again for its output and status", err)), nil
		}
	}
	// An empty poll returns as soon as new output exists. A write (input or
	// interrupt) must not stop at the terminal echo of that input: wait for the
	// process to exit or the window to elapse, then report everything it said.
	mode := execsess.WaitOutput
	if a.Chars != "" {
		mode = execsess.WaitExit
	}
	sess.Wait(ctx, writeStdinYield(a.Chars, a.YieldMs), mode)
	// A poll returns the instant output arrives, a hair before the reaper
	// records the process exit; give it a brief grace so a command that just
	// finished reports its status in this same result.
	if a.Chars == "" && !sess.Exited() && sess.PendingOutput() {
		sess.Wait(ctx, exitReportGrace, execsess.WaitExit)
	}

	out := truncateBashOutput(sess.ReadNew())
	snap := sess.Snapshot()
	status := sessionStatusLine(snap)
	text := status
	if out != "" {
		text = out + "\n" + status
	}
	details := map[string]any{"bash_id": snap.ID, "status": string(snap.Status)}
	if snap.Status == execsess.StatusExited {
		details["exitCode"] = snap.ExitCode
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		Details: details,
	}, nil
}
