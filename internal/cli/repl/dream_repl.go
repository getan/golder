// This file implements the manual `/dream` REPL command (SPEC §4.1, US-007):
// it spawns the process-isolated memory-consolidation subprocess
// (`golder --dream [--dream-dry-run] -C <projectDir>`) and renders the returned
// Report as a full-table change report. The spawn/lock/render plumbing is
// shared with the TUI and lives in internal/cli/dreamcmd.
package repl

import (
	"context"
	"fmt"
	"io"

	"github.com/getan/golder/internal/cli/dreamcmd"
	"github.com/getan/golder/internal/cli/ui"
)

// runDream handles an intercepted `/dream` (or `/dream --dry-run`) line: it
// checks for a live lock (so a background dream already running yields a clear
// notice rather than a confusing empty report), spawns the consolidation
// subprocess with a progress indication, and renders the returned Report as a
// full table. A failed or unparseable run prints a clear error and returns to
// the prompt without crashing the REPL (SPEC §6.1).
func runDream(parent context.Context, out io.Writer, deps replDeps, line string) {
	dryRun := dreamcmd.HasDryRun(line)

	// Pre-spawn lock check: if a background (or other) dream already holds a live
	// lock, the subprocess would just skip and emit an all-zero report,
	// indistinguishable from "nothing changed". Detect it here so the manual
	// command can tell the user (SPEC §6.1 locked row).
	if dreamcmd.LockHeld(deps.memoryRoot) {
		fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Yellow, "a dream consolidation is already running"))
		return
	}

	progress := "Dreaming… (consolidating memory)"
	if dryRun {
		progress = "Dreaming… (dry-run, analyzing memory — nothing will be written)"
	}
	fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Dim, progress))

	// Bound the subprocess so a hung LLM-backed run cannot wedge the REPL
	// indefinitely (SPEC §6.3/§11.2: parent context timeout, default 10min). On
	// timeout CommandContext kills the child and Spawn surfaces a failed run.
	// parent carries the REPL's SIGINT cancellation: Ctrl+C kills the child
	// instead of being swallowed while the REPL waits.
	ctx, cancel := context.WithTimeout(parent, dreamcmd.RunTimeout)
	defer cancel()
	res, err := dreamcmd.Spawn(ctx, deps.cwd, dryRun)
	if err != nil {
		if parent.Err() == context.Canceled {
			fmt.Fprintln(out, ui.Colorize(ui.Enabled(), ui.Yellow, "dream cancelled"))
			return
		}
		fmt.Fprintf(out, "%s %v\n", ui.Colorize(ui.Enabled(), ui.Red, "dream failed:"), err)
		return
	}
	dreamcmd.RenderReportTable(out, res.Report)
}
