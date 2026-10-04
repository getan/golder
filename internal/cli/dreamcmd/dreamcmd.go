// Package dreamcmd holds the CLI-side plumbing shared by the REPL and the TUI
// for the /dream memory-consolidation command: spawning the process-isolated
// `golder --dream` child, detecting a live consolidation lock, parsing the
// dry-run flag, and rendering the returned Report. It was extracted from the
// REPL package so the TUI can offer the same command without importing repl.
package dreamcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/dream"
)

// RunTimeout bounds one dream subprocess (SPEC §6.3 default 10min): a hung
// LLM-backed pass is killed rather than wedging the caller forever.
var RunTimeout = 10 * time.Minute

// Result is the parsed outcome of one spawn: the decoded Report plus whatever
// the child wrote to stderr (surfaced in error messages so a failing run is
// diagnosable).
type Result struct {
	Report dream.Report
	Stderr string
}

// Spawn launches the dream subprocess and decodes its stdout Report. It is a
// package var so tests can substitute a canned Result (avoiding a real
// LLM-backed run); the production implementation is spawnSubprocess.
var Spawn = spawnSubprocess

// spawnSubprocess runs `golder --dream [--dream-dry-run] -C <projectDir>` to
// completion, capturing stdout (the single-line Report JSON, SPEC §4.2) and
// stderr (progress/diagnostics). A non-zero exit or unparseable stdout is
// returned as an error carrying the stderr tail so the caller can print a clear
// failure (SPEC §6.1).
func spawnSubprocess(ctx context.Context, projectDir string, dryRun bool) (Result, error) {
	exe, err := os.Executable()
	if err != nil {
		return Result{}, fmt.Errorf("resolve golder executable: %w", err)
	}
	args := []string{"--dream"}
	if dryRun {
		args = append(args, "--dream-dry-run")
	}
	if projectDir != "" {
		args = append(args, "-C", projectDir)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	errTail := strings.TrimSpace(stderr.String())
	if runErr != nil {
		if errTail != "" {
			return Result{Stderr: errTail}, fmt.Errorf("%w: %s", runErr, errTail)
		}
		return Result{}, runErr
	}
	var report dream.Report
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		return Result{Stderr: errTail}, fmt.Errorf("parse report: %w", err)
	}
	return Result{Report: report, Stderr: errTail}, nil
}

// HasDryRun reports whether the command line carries the --dry-run flag. It
// accepts "--dry-run" as a standalone token so "/dream --dry-run" and
// "/dream  --dry-run" both match, while a bare "/dream" does not.
func HasDryRun(line string) bool {
	for _, f := range strings.Fields(line) {
		if f == "--dry-run" {
			return true
		}
	}
	return false
}

// lockPayload mirrors the on-disk dream.lock body (SPEC §3.1:
// {"pid":..,"started_at":..}). It is decoded read-only to detect a running
// dream; the authoritative lock logic lives in internal/dream/lock.go.
type lockPayload struct {
	StartedAt time.Time `json:"started_at"`
}

// LockHeld reports whether a live (non-stale) dream lock exists under
// memoryRoot. A missing root/lock or a stale lock (older than
// dream.DefaultStaleAfter, matching the runner's takeover rule) reads as not
// held. Read-only: it never creates, removes, or takes over the lock — that is
// the subprocess Runner's job.
func LockHeld(memoryRoot string) bool {
	if memoryRoot == "" {
		return false
	}
	path := filepath.Join(memoryRoot, "global", "dream", "dream.lock")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var info lockPayload
	if err := json.Unmarshal(data, &info); err != nil {
		// Malformed lock body: the runner treats it as stale/takeable, so it is
		// not a live lock from the user's perspective.
		return false
	}
	if info.StartedAt.IsZero() {
		return false
	}
	return time.Since(info.StartedAt) <= dream.DefaultStaleAfter
}

// RenderReportTable writes the full change report (SPEC §2.2/§6.1 manual row):
// one aligned row per counter, byte/file before→after, and any Notes. A dry-run
// report is clearly labeled DRY-RUN and states that nothing was written.
func RenderReportTable(out io.Writer, r dream.Report) {
	enabled := ui.Enabled()
	if r.DryRun {
		fmt.Fprintln(out, ui.Colorize(enabled, ui.Bold, "dream report [DRY-RUN — nothing written]"))
	} else {
		fmt.Fprintln(out, ui.Colorize(enabled, ui.Bold, "dream report"))
	}
	rows := []struct {
		label string
		value string
	}{
		{"merged", fmt.Sprintf("%d", r.Merged)},
		{"deduped", fmt.Sprintf("%d", r.Deduped)},
		{"paths-cleaned", fmt.Sprintf("%d", r.PathsCleaned)},
		{"pruned", fmt.Sprintf("%d", r.Pruned)},
		{"distilled", fmt.Sprintf("%d", r.Distilled)},
		{"bytes", fmt.Sprintf("%s → %s", formatBytes(r.BytesBefore), formatBytes(r.BytesAfter))},
		{"files", fmt.Sprintf("%d → %d", r.FilesBefore, r.FilesAfter)},
		{"reconciled", fmt.Sprintf("indexed %d, pruned %d", r.Reconciled.Indexed, r.Reconciled.Pruned)},
	}
	width := 0
	for _, row := range rows {
		if len(row.label) > width {
			width = len(row.label)
		}
	}
	for _, row := range rows {
		label := ui.Colorize(enabled, ui.Dim, fmt.Sprintf("  %-*s", width, row.label))
		fmt.Fprintf(out, "%s  %s\n", label, row.value)
	}
	if len(r.Notes) > 0 {
		fmt.Fprintln(out, ui.Colorize(enabled, ui.Dim, "  notes:"))
		for _, n := range r.Notes {
			fmt.Fprintf(out, "    - %s\n", n)
		}
	}
}

// RenderReportLine renders a compact one-line summary of a dream Report. It is
// used by the startup background trigger's non-intrusive notice (SPEC §6.1
// background row). A dry-run report is prefixed [DRY-RUN].
func RenderReportLine(r dream.Report) string {
	prefix := "dream:"
	if r.DryRun {
		prefix = "dream [DRY-RUN]:"
	}
	return fmt.Sprintf("%s merged %d, deduped %d, paths-cleaned %d, pruned %d, distilled %d, %s→%s, %d→%d files",
		prefix, r.Merged, r.Deduped, r.PathsCleaned, r.Pruned, r.Distilled,
		formatBytes(r.BytesBefore), formatBytes(r.BytesAfter), r.FilesBefore, r.FilesAfter)
}

// formatBytes renders a byte count as a compact human-readable string (B/KB/MB).
// It uses 1024-based units and one decimal place above 1KB, matching the terse
// style of the CLI status output.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}
