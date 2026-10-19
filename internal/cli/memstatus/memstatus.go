// This file implements the /memory slash command (US-011, FR-20, #484) that
// prints a colored report describing the persistent memory store and the
// infinite-context state: entry counts by scope, the current context window and
// auto-compaction trigger point, current context usage, and checkpoint status.
//
// Unlike /status (which reads everything through cli.Host), /memory takes its
// inputs explicitly so both the REPL and the TUI can call it with the live
// memory store, which the Host contract does not expose.
package memstatus

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/memory"
	"github.com/getan/golder/internal/runtime"
)

// scopeOrder is the display order for memory scopes. Scopes not listed here are
// appended afterwards in lexical order.
var scopeOrder = []memory.Scope{
	memory.ScopeGlobal,
	memory.ScopeProjects,
	memory.ScopeSessions,
	memory.ScopeCC,
}

// RunMemory prints a colored multi-section report about persistent memory and
// infinite context to out. store may be nil (persistent memory disabled), in
// which case the memory-store section reports that state and the checkpoint /
// context sections still render from msgs and the checkpoint file.
func RunMemory(out io.Writer, store *memory.Store, memoryRoot, sessionID string, msgs agentcore.MessageList, contextWindow int, settings compaction.CompactionSettings) {
	color := ui.Enabled()

	fmt.Fprintln(out)
	printStoreSection(out, color, store, memoryRoot)
	fmt.Fprintln(out)
	printContextSection(out, color, msgs, contextWindow, settings)
	fmt.Fprintln(out)
	printCheckpointSection(out, color, memoryRoot, sessionID)
}

// printStoreSection prints the persistent memory store section: whether memory
// is enabled, the root directory, and entry counts per scope. It reconciles the
// on-disk files into the index first so the counts are fresh.
func printStoreSection(out io.Writer, color bool, store *memory.Store, memoryRoot string) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "persistent memory:"))
	if store == nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Yellow, "disabled"))
		return
	}
	fmt.Fprintf(out, "  %s %s\n",
		ui.Colorize(color, ui.Dim, "status:"),
		ui.Colorize(color, ui.Green, "enabled"))
	printRecallSection(out, color)
	if memoryRoot != "" {
		fmt.Fprintf(out, "  %s %s\n", ui.Colorize(color, ui.Dim, "root:"), memoryRoot)
	}

	if _, err := store.Reconcile(); err != nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "entries:"),
			ui.Colorize(color, ui.Red, fmt.Sprintf("reconcile failed: %v", err)))
		return
	}
	counts, err := store.CountByScope()
	if err != nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "entries:"),
			ui.Colorize(color, ui.Red, fmt.Sprintf("count failed: %v", err)))
		return
	}

	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "entries:"), total)
	for _, scope := range orderedScopes(counts) {
		fmt.Fprintf(out, "    %s %d\n",
			ui.Colorize(color, ui.Dim, string(scope)+":"), counts[scope])
	}
}

// printRecallSection reports the per-turn recall policy and the last decision it
// made. It exists because a sudden recall is otherwise invisible: the reminder
// enters one turn's prompt and never the transcript, so "why did it remember
// that?" has no answer without this line.
func printRecallSection(out io.Writer, color bool) {
	pol := config.ResolveMemoryRecall()
	state := ui.Colorize(color, ui.Dim, "off (default)")
	if pol.Recall {
		state = ui.Colorize(color, ui.Green, "on")
	}
	fmt.Fprintf(out, "  %s %s  %s\n",
		ui.Colorize(color, ui.Dim, "recall:"), state,
		ui.Colorize(color, ui.Dim, fmt.Sprintf("(min %d units, overlap %d, reconcile %s)",
			pol.RecallMinUnits, pol.RecallMinOverlap, recallIntervalLabel(pol.RecallReconcile))))
	if !pol.Recall {
		fmt.Fprintf(out, "    %s\n", ui.Colorize(color, ui.Dim,
			"the memory_search tool still works; enable the per-turn reminder with /memory recall on"))
		return
	}
	stats := runtime.MemoryRecallStats()
	if stats.LastAt.IsZero() {
		fmt.Fprintf(out, "    %s\n", ui.Colorize(color, ui.Dim, "no turn has consulted it yet in this process"))
		return
	}
	verdict := ui.Colorize(color, ui.Dim, "skipped")
	if stats.LastFired {
		verdict = ui.Colorize(color, ui.Green, "injected")
	}
	fmt.Fprintf(out, "    %s %s — %s\n", ui.Colorize(color, ui.Dim, "last:"), verdict, stats.LastReason)
	if q := oneLine(stats.LastQuery); q != "" {
		fmt.Fprintf(out, "    %s %s\n", ui.Colorize(color, ui.Dim, "query:"), truncate(q, 100))
	}
	for _, p := range stats.LastPaths {
		fmt.Fprintf(out, "      %s\n", ui.Colorize(color, ui.Dim, p))
	}
	fmt.Fprintf(out, "    %s %d injected, %d skipped\n",
		ui.Colorize(color, ui.Dim, "this process:"), stats.Fires, stats.Skips)
}

func recallIntervalLabel(d time.Duration) string {
	switch {
	case d < 0:
		return "never"
	case d == 0:
		return "every turn"
	default:
		return "every " + d.String()
	}
}

// RunMemoryRecall handles `/memory recall [on|off]`. It reports the policy, or
// persists a change to config.toml and applies it to the running session's
// providers. handled is false when the argument is not a recall subcommand, so
// the caller falls through to the full report.
func RunMemoryRecall(out io.Writer, args string, reminders *runtime.ReminderRegistry) bool {
	args = strings.TrimSpace(args)
	if args != "recall" && !strings.HasPrefix(args, "recall ") {
		return false
	}
	arg := strings.TrimSpace(strings.TrimPrefix(args, "recall"))
	pol := config.ResolveMemoryRecall()
	switch arg {
	case "":
		state := "off (default)"
		if pol.Recall {
			state = "on"
		}
		fmt.Fprintf(out, "memory recall: %s  (min %d units, overlap %d, reconcile %s)\n",
			state, pol.RecallMinUnits, pol.RecallMinOverlap, recallIntervalLabel(pol.RecallReconcile))
		fmt.Fprintf(out, "toggle with /memory recall on|off; the memory_search tool is unaffected\n")
		return true
	case "on", "off":
		on := arg == "on"
		if err := config.SetMemoryRecall(on); err != nil {
			fmt.Fprintf(out, "memory recall: cannot save: %v\n", err)
			return true
		}
		if reminders.SetMemoryRecall(on) {
			fmt.Fprintf(out, "memory recall: %s (saved to config.toml; applies to this session)\n", arg)
		} else {
			fmt.Fprintf(out, "memory recall: %s (saved to config.toml; applies from the next session, this one has no memory provider)\n", arg)
		}
		return true
	}
	fmt.Fprintf(out, "usage: /memory recall [on|off]\n")
	return true
}

// oneLine collapses runs of whitespace so a multi-line query stays on one row.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// orderedScopes returns the scopes present in counts, listing the well-known
// scopes first (scopeOrder) and any others afterwards in lexical order.
func orderedScopes(counts map[memory.Scope]int) []memory.Scope {
	seen := make(map[memory.Scope]bool, len(counts))
	var out []memory.Scope
	for _, s := range scopeOrder {
		if _, ok := counts[s]; ok {
			out = append(out, s)
			seen[s] = true
		}
	}
	var extra []memory.Scope
	for s := range counts {
		if !seen[s] {
			extra = append(extra, s)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(out, extra...)
}

// printContextSection prints the current context usage and the auto-compaction
// trigger point (window minus reserve), mirroring /status's context block so
// /memory is self-contained for infinite-context inspection.
func printContextSection(out io.Writer, color bool, msgs agentcore.MessageList, contextWindow int, settings compaction.CompactionSettings) {
	tokens := compaction.EstimateContextTokens(msgs).Tokens

	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "context:"))
	if contextWindow > 0 {
		fmt.Fprintf(out, "  %s %d / %d tokens\n",
			ui.Colorize(color, ui.Dim, "current:"), tokens, contextWindow)
		util := int(float64(tokens) / float64(contextWindow) * 100)
		utilColor := ui.Green
		if util >= 90 {
			utilColor = ui.Red
		} else if util >= 70 {
			utilColor = ui.Yellow
		}
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "utilization:"),
			ui.Colorize(color, utilColor, fmt.Sprintf("%d%%", util)))

		reserve := settings.ReserveDescription(contextWindow)
		threshold := settings.UsableTokens(contextWindow)
		remaining := threshold - tokens
		if remaining < 0 {
			fmt.Fprintf(out, "  %s %s (window: %d, %s)\n",
				ui.Colorize(color, ui.Dim, "compaction trigger:"),
				ui.Colorize(color, ui.Red, fmt.Sprintf("%d tokens over trigger (%d)", -remaining, threshold)),
				contextWindow, reserve)
		} else {
			fmt.Fprintf(out, "  %s %s (window: %d, %s)\n",
				ui.Colorize(color, ui.Dim, "compaction trigger:"),
				ui.Colorize(color, ui.Green, fmt.Sprintf("%d tokens until trigger (%d)", remaining, threshold)),
				contextWindow, reserve)
		}
	} else {
		fmt.Fprintf(out, "  %s %d tokens\n", ui.Colorize(color, ui.Dim, "current:"), tokens)
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "compaction trigger:"),
			ui.Colorize(color, ui.Yellow, "auto-compaction disabled (unknown window)"))
	}
}

// printCheckpointSection prints the infinite-context checkpoint status for the
// session: whether a checkpoint.md exists and, if so, its watermark, covered
// message count, and creation time.
func printCheckpointSection(out io.Writer, color bool, memoryRoot, sessionID string) {
	fmt.Fprintf(out, "%s\n", ui.Colorize(color, ui.Bold, "checkpoint:"))
	if memoryRoot == "" || sessionID == "" {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Yellow, "unavailable (no memory root or session id)"))
		return
	}
	cp, ok, err := runtime.LoadCheckpoint(sessionID, memoryRoot)
	if err != nil {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Red, fmt.Sprintf("load failed: %v", err)))
		return
	}
	if !ok {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "status:"),
			ui.Colorize(color, ui.Dim, "none yet"))
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "path:"),
			runtime.CheckpointPath(sessionID, memoryRoot))
		return
	}
	fmt.Fprintf(out, "  %s %s\n",
		ui.Colorize(color, ui.Dim, "status:"),
		ui.Colorize(color, ui.Green, "present"))
	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "watermark:"), cp.Watermark)
	fmt.Fprintf(out, "  %s %d\n", ui.Colorize(color, ui.Dim, "covered messages:"), cp.CoveredMessages)
	if !cp.CreatedAt.IsZero() {
		fmt.Fprintf(out, "  %s %s\n",
			ui.Colorize(color, ui.Dim, "created:"),
			cp.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	}
	fmt.Fprintf(out, "  %s %s\n",
		ui.Colorize(color, ui.Dim, "path:"),
		runtime.CheckpointPath(sessionID, memoryRoot))
}
