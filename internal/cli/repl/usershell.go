// This file wires the `!command` passthrough into the REPL. A passthrough
// command runs locally — no model call, no approval, no sandbox — and its
// settled record joins the shared context as a user-role
// <user_shell_command> message, so the next prompt's model sees what the user
// ran (codex parity; the TUI implements the same escape hatch in
// internal/cli/tui/usershell.go).
package repl

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/getan/golder/internal/cli"
)

// runUserShell executes a "!command" line: it blocks until the command exits,
// prints its output, and records the result in the conversation. A bare "!"
// prints the usage hint instead of running anything (codex's help shape).
// setCancel is the REPL's SIGINT plumbing — while the command runs, Ctrl+C
// kills it (the run settles as aborted) instead of being swallowed.
func runUserShell(out io.Writer, deps *replDeps, line string, setCancel func(context.CancelFunc)) {
	command := strings.TrimSpace(strings.TrimPrefix(line, "!"))
	if command == "" {
		fmt.Fprintln(out, "Prefix a command with ! to run it locally")
		fmt.Fprintln(out, "Example: !ls")
		return
	}
	runnable, err := cli.StartUserShell(deps.sessions, deps.cwd, command)
	if err != nil {
		fmt.Fprintf(out, "golder: %v\n", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	setCancel(cancel)
	outcome := runnable.Wait(ctx)
	cancel()
	setCancel(nil)
	if output := cli.UserShellDisplayOutput(outcome.Output); output != "" {
		fmt.Fprint(out, output)
		if !strings.HasSuffix(output, "\n") {
			fmt.Fprintln(out)
		}
	}
	// The settled command joins the shared context as the same user-role record
	// the TUI appends, then the turn is persisted so the record survives a
	// restart exactly like a normal conversational turn.
	deps.agentCtx.Messages = append(deps.agentCtx.Messages,
		cli.UserShellMessage(outcome.Command, outcome.ExitCode, outcome.Duration, outcome.Output))
	cli.PersistTurn(out, deps)
}
