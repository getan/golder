// This file implements the /rewind command (edit checkpoint / rewind): golder's
// analogue of Claude Code's Esc-Esc rewind. Where /tree only moves the
// conversation leaf, /rewind also restores the working tree — it replays the
// file-snapshot journal (see agenttool.FileSnapshotRecorder) so a turn's write
// and edit mutations are rolled back, then switches the active conversation leaf
// to the point before that turn. The two together return the session to an
// earlier state in code and dialogue at once.
//
// Scope (v1): only golder's own write/edit tools are journaled. Files changed by
// bash commands are not captured and are left untouched by a rewind.
package repl

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/session"
)

// rewindLabel derives a short one-line description of a turn from its prompt, for
// display in the /rewind list. It collapses whitespace and truncates so the list
// stays scannable.
func rewindLabel(prompt string) string {
	label := strings.Join(strings.Fields(prompt), " ")
	const max = 60
	if len(label) > max {
		label = label[:max-1] + "…"
	}
	return label
}

// runRewind handles the /rewind command. With no argument it persists the live
// turn and prints the numbered restore points (most useful last). With "/rewind
// N" it restores files to their state before the N-th listed point and switches
// the conversation to the leaf that preceded that turn.
func runRewind(out io.Writer, deps *replDeps, line string) {
	if deps.snap == nil {
		fmt.Fprintln(out, "rewind is unavailable (file tools are disabled)")
		return
	}
	// Persist any un-saved turn first so the just-run turn's restore point exists
	// and the leaf ids we switch to are on disk.
	cli.PersistTurn(out, deps)

	points := deps.snap.Points()
	fields := strings.Fields(line)
	if len(fields) < 2 {
		printRewindPoints(out, points)
		return
	}
	if len(points) == 0 {
		fmt.Fprintln(out, "no restore points yet — file edits create them")
		return
	}

	n, err := strconv.Atoi(fields[1])
	if err != nil || n < 1 || n > len(points) {
		fmt.Fprintf(out, "invalid selection %q — run /rewind to list points (1..%d)\n", fields[1], len(points))
		return
	}

	leafID, restored, warnings, rErr := deps.snap.Restore(n - 1)
	if rErr != nil {
		fmt.Fprintf(out, "golder: rewind failed: %v\n", rErr)
		return
	}

	if len(restored) > 0 {
		fmt.Fprintf(out, "restored %d file(s):\n", len(restored))
		for _, p := range restored {
			fmt.Fprintf(out, "  %s\n", displayPath(deps.cwd, p))
		}
	} else {
		fmt.Fprintln(out, "no files to restore for this point")
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "  warning: %s\n", w)
	}

	// Move the conversation back to the leaf that preceded the turn, rebuilding the
	// shared context from that leaf's root→leaf path (same mechanism as /tree). An
	// empty leaf id means the turn was the first in the session: reset to an empty
	// conversation.
	if !rewindConversation(out, deps, leafID) {
		return
	}
	fmt.Fprintf(out, "rewound to before point %d — next prompt continues from here\n", n)
}

// rewindConversation switches the active leaf to leafID and rebuilds the shared
// context from its path. A "" leafID resets to an empty conversation (the turn
// was the session's first). It reports whether the switch succeeded.
func rewindConversation(out io.Writer, deps *replDeps, leafID string) bool {
	if leafID == "" {
		deps.agentCtx.Messages = nil
		deps.curLeaf = ""
		deps.persisted = 0
		return true
	}
	_, entries, err := deps.store.LoadEntries(deps.header.ID)
	if err != nil {
		fmt.Fprintf(out, "golder: cannot read session tree: %v\n", err)
		return false
	}
	path := session.PathToLeaf(entries, leafID)
	if len(path) == 0 {
		fmt.Fprintf(out, "golder: restore point's conversation node is no longer in the tree; files were restored but the conversation was left unchanged\n")
		return false
	}
	msgs := make(agentcore.MessageList, len(path))
	for i, e := range path {
		msgs[i] = e.Message
	}
	deps.agentCtx.Messages = msgs
	deps.curLeaf = leafID
	deps.persisted = len(msgs)
	return true
}

// printRewindPoints renders the numbered restore points, oldest first, showing
// when each was made, how many files it touched, and the turn's label.
func printRewindPoints(out io.Writer, points []agenttool.RestorePoint) {
	if len(points) == 0 {
		fmt.Fprintln(out, "no restore points yet — file edits create them")
		return
	}
	fmt.Fprintln(out, "restore points (run /rewind <n> to roll files + conversation back to before that point):")
	for i, p := range points {
		files := len(p.Snapshots)
		unit := "files"
		if files == 1 {
			unit = "file"
		}
		when := p.Time.Local().Format(time.Kitchen)
		label := p.Label
		if label == "" {
			label = "(no prompt)"
		}
		fmt.Fprintf(out, "  %d. %s  %d %s  %s\n", i+1, when, files, unit, label)
	}
}

// displayPath shortens an absolute snapshot path to a workspace-relative form for
// display when it lives under cwd; otherwise it returns the absolute path.
func displayPath(cwd, abs string) string {
	if cwd == "" {
		return abs
	}
	if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}
