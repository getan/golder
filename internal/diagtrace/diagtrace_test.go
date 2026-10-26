package diagtrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reset(t *testing.T) {
	t.Helper()
	processMu.Lock()
	old, oldInit := process, initialized
	process, initialized = nil, false
	processMu.Unlock()
	t.Cleanup(func() {
		processMu.Lock()
		process, initialized = old, oldInit
		processMu.Unlock()
	})
}

func TestDisabledTraceHasNoSideEffects(t *testing.T) {
	reset(t)
	t.Setenv("GOLDER_TUI_TRACE", "")
	t.Setenv("GOLDER_STARTUP_TRACE", "")
	if err := InitFromEnv(); err != nil {
		t.Fatal(err)
	}
	if Enabled() || Process() != nil {
		t.Fatal("an unset trace path enabled diagnostics")
	}
	Event("main", "must not panic")
	Close()
	var nilTrace *Trace
	nilTrace.Event("x", "no-op")
	nilTrace.Close()
	if nilTrace.Path() != "" {
		t.Fatal("nil trace reported a path")
	}
}

func TestInitIsIdempotentAndRecordsOneClock(t *testing.T) {
	reset(t)
	first := filepath.Join(t.TempDir(), "first.log")
	second := filepath.Join(t.TempDir(), "second.log")
	if err := Init(first); err != nil {
		t.Fatal(err)
	}
	if err := Init(second); err != nil {
		t.Fatal(err)
	}
	if got := Process().Path(); got != first {
		t.Fatalf("second Init moved the trace to %q", got)
	}
	Event("main", "one clock")
	Close()
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("second Init created a file")
	}
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"pid=", "+", "diag start", "main one clock", "diag trace end"} {
		if !strings.Contains(text, want) {
			t.Errorf("trace missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "diag trace end") != 1 {
		t.Errorf("repeat Close wrote more than one end event:\n%s", text)
	}
	// Events after Close are harmless and do not reopen the file.
	Event("main", "after close")
	data, _ = os.ReadFile(first)
	if strings.Contains(string(data), "after close") {
		t.Fatal("an event was written after Close")
	}
}

func TestTraceIsPrivateAndLeaksNoEnvironmentValues(t *testing.T) {
	reset(t)
	path := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv("TERM", "xterm")
	t.Setenv("SSH_CONNECTION", "private remote address")
	t.Setenv("GOLDER_TRACE_TEST_CREDENTIAL", "private credential")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	Event("plugin", "load name=example ok=true duration=%s", time.Millisecond)
	Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trace mode = %04o, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`env TERM="xterm"`, "SSH_CONNECTION_set=true", "plugin load name=example", "duration=1ms"} {
		if !strings.Contains(text, want) {
			t.Errorf("trace missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "private") || strings.Contains(text, "GOLDER_TRACE_TEST_CREDENTIAL") {
		t.Fatalf("trace leaked an environment value or credential name:\n%s", text)
	}
}

func TestOpenTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tr.Event("main", "appended")
	tr.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("existing trace mode = %04o, want 0600", perm)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "old\n") || !strings.Contains(string(data), "appended") {
		t.Fatalf("append mode changed existing content:\n%s", data)
	}
}

func TestBadPathReportsOnce(t *testing.T) {
	reset(t)
	bad := filepath.Join(t.TempDir(), "missing", "trace.log")
	if err := Init(bad); err == nil {
		t.Fatal("a missing directory should fail")
	}
	if err := Init(filepath.Join(t.TempDir(), "other.log")); err != nil {
		t.Fatalf("repeat Init repeated the first failure: %v", err)
	}
	if Enabled() {
		t.Fatal("a failed Init left diagnostics enabled")
	}
}

func TestStageClockIsSingle(t *testing.T) {
	reset(t)
	path := filepath.Join(t.TempDir(), "trace.log")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	before := time.Since(Started())
	if before < 0 {
		t.Fatal("process clock is in the future")
	}
	Event("tui", "view begin")
	Close()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "tui view begin") {
		t.Fatalf("stage tag missing:\n%s", data)
	}
}

func TestPathFromEnvPrefersTheTUINameAndFallsBackToTheAlias(t *testing.T) {
	t.Setenv("GOLDER_TUI_TRACE", "primary.log")
	t.Setenv("GOLDER_STARTUP_TRACE", "alias.log")
	if got := PathFromEnv(); got != "primary.log" {
		t.Errorf("primary path = %q", got)
	}
	t.Setenv("GOLDER_TUI_TRACE", "  ")
	if got := PathFromEnv(); got != "alias.log" {
		t.Errorf("alias fallback = %q", got)
	}
}
