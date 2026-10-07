package repl

// Integration tests for the REPL's /dream command: they drive runDream through
// the dreamcmd.Spawn seam with canned results, without spawning a real
// LLM-backed dream. The renderer/format/lock unit tests live in
// internal/cli/dreamcmd.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/dreamcmd"
	"github.com/getan/golder/internal/dream"
)

func sampleReport() dream.Report {
	return dream.Report{Merged: 2, Deduped: 1, BytesBefore: 4096, BytesAfter: 2048}
}

// TestRunDreamRendersCannedReport exercises the parse+render path via the spawn
// seam with a canned Report, without spawning a real LLM-backed dream.
func TestRunDreamRendersCannedReport(t *testing.T) {
	orig := dreamcmd.Spawn
	t.Cleanup(func() { dreamcmd.Spawn = orig })
	dreamcmd.Spawn = func(_ context.Context, _ string, dryRun bool) (dreamcmd.Result, error) {
		r := sampleReport()
		r.DryRun = dryRun
		return dreamcmd.Result{Report: r}, nil
	}

	var buf bytes.Buffer
	runDream(context.Background(), &buf, replDeps{}, "/dream")
	got := buf.String()
	if !strings.Contains(got, "dream report") || !strings.Contains(got, "merged") {
		t.Errorf("runDream should render the full table:\n%s", got)
	}
	if strings.Contains(got, "DRY-RUN") {
		t.Errorf("non-dry-run runDream should not show DRY-RUN:\n%s", got)
	}
}

func TestRunDreamDryRunLabel(t *testing.T) {
	orig := dreamcmd.Spawn
	t.Cleanup(func() { dreamcmd.Spawn = orig })
	dreamcmd.Spawn = func(_ context.Context, _ string, dryRun bool) (dreamcmd.Result, error) {
		r := sampleReport()
		r.DryRun = dryRun
		return dreamcmd.Result{Report: r}, nil
	}
	var buf bytes.Buffer
	runDream(context.Background(), &buf, replDeps{}, "/dream --dry-run")
	if !strings.Contains(buf.String(), "DRY-RUN") {
		t.Errorf("/dream --dry-run should render DRY-RUN label:\n%s", buf.String())
	}
}

// TestRunDreamFailure asserts a subprocess failure (exit 1 / unparseable stdout)
// prints a clear error and does not crash the REPL (SPEC §6.1).
func TestRunDreamFailure(t *testing.T) {
	orig := dreamcmd.Spawn
	t.Cleanup(func() { dreamcmd.Spawn = orig })
	dreamcmd.Spawn = func(_ context.Context, _ string, _ bool) (dreamcmd.Result, error) {
		return dreamcmd.Result{}, errFake
	}
	var buf bytes.Buffer
	runDream(context.Background(), &buf, replDeps{}, "/dream")
	if !strings.Contains(buf.String(), "dream failed") {
		t.Errorf("failed run should print an error:\n%s", buf.String())
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "boom" }

// TestRunDreamCancelled: a SIGINT-style cancellation must report clearly and
// return to the prompt instead of surfacing as a crash or hanging.
func TestRunDreamCancelled(t *testing.T) {
	orig := dreamcmd.Spawn
	t.Cleanup(func() { dreamcmd.Spawn = orig })
	dreamcmd.Spawn = func(ctx context.Context, _ string, _ bool) (dreamcmd.Result, error) {
		<-ctx.Done()
		return dreamcmd.Result{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	runDream(ctx, &buf, replDeps{}, "/dream")
	if !strings.Contains(buf.String(), "dream cancelled") {
		t.Errorf("cancelled run should say so:\n%s", buf.String())
	}
}

// TestRunDreamLockedNotice asserts the manual command surfaces the locked
// message and does NOT spawn when a live lock is present (SPEC §6.1).
func TestRunDreamLockedNotice(t *testing.T) {
	root := t.TempDir()
	lock, err := dream.AcquireLock(root)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	orig := dreamcmd.Spawn
	t.Cleanup(func() { dreamcmd.Spawn = orig })
	spawned := false
	dreamcmd.Spawn = func(_ context.Context, _ string, _ bool) (dreamcmd.Result, error) {
		spawned = true
		return dreamcmd.Result{}, nil
	}
	var buf bytes.Buffer
	runDream(context.Background(), &buf, replDeps{memoryRoot: root}, "/dream")
	if spawned {
		t.Error("runDream must not spawn while a live lock is held")
	}
	if !strings.Contains(buf.String(), "already running") {
		t.Errorf("locked run should print the locked notice:\n%s", buf.String())
	}
}
