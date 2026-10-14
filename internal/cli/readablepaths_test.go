package cli

// Tests for the read-side of the managed permissions store: the merge order and
// the add/remove round-trip. The writable side has the same tests; these are
// its twin so the two grants cannot drift.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
)

// isolateReadablePaths points config discovery at a temp XDG dir and resets the
// session registry, restoring both when the test ends.
func isolateReadablePaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	grantstest.Install(t)
	return dir
}

// TestReadablePathsLifecycle covers add → list → remove: the grant lands in
// the table, a second add does not
// duplicate it, and removal revokes both halves.
func TestReadablePathsLifecycle(t *testing.T) {
	dir := isolateReadablePaths(t)
	granted := filepath.Join(dir, "notes")

	entries, warnings := ListReadablePaths()
	if len(entries) != 0 || len(warnings) != 0 {
		t.Fatalf("fresh state should list nothing, got %+v warnings=%v", entries, warnings)
	}

	root, err := AddReadablePath(granted)
	if err != nil || root != granted {
		t.Fatalf("AddReadablePath = %q err=%v, want %q", root, err, granted)
	}
	if !grants.Allows(filepath.Join(granted, "a.md"), grants.Read) {
		t.Error("the table did not take the grant")
	}
	entries, _ = ListReadablePaths()
	if len(entries) != 1 || entries[0].Source != ReadableSourceSaved {
		t.Fatalf("list should show the saved grant, got %+v", entries)
	}

	if _, err := AddReadablePath(granted); err != nil {
		t.Fatalf("second AddReadablePath: %v", err)
	}
	if n := len(grants.ReadRoots()); n != 1 {
		t.Fatalf("second add duplicated the entry: %d rows", n)
	}

	res, err := RemoveReadablePath(granted)
	if err != nil || !res.Found() || res.WasSession || !res.WasRemembered {
		t.Fatalf("RemoveReadablePath = %+v err=%v", res, err)
	}
	if grants.Allows(filepath.Join(granted, "a.md"), grants.Read) {
		t.Error("the table should hold no read row after removal")
	}
}

// TestReadablePathsRejectsBroadRoots pins re-use of the boundary guard: a grant
// that would amount to removing the boundary is refused outright.
func TestReadablePathsRejectsBroadRoots(t *testing.T) {
	isolateReadablePaths(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	if _, err := AddReadablePath(home); err == nil {
		t.Errorf("granting the home directory itself must be refused")
	}
	if _, err := AddReadablePath("/"); err == nil {
		t.Errorf("granting the filesystem root must be refused")
	}
}

// TestReadableSourceLabel covers the provenance wording the command prints.
func TestReadableSourceLabel(t *testing.T) {
	for _, c := range []struct {
		source string
		zh     bool
		want   string
	}{
		{ReadableSourceSaved, false, "(grants.json, remembered)"},
		{ReadableSourceSession, false, "(this session)"},
		{ReadableSourceSaved, true, "（grants.json，已持久化）"},
	} {
		if got := ReadableSourceLabel(c.source, c.zh); got != c.want {
			t.Errorf("ReadableSourceLabel(%q, zh=%v) = %q, want %q", c.source, c.zh, got, c.want)
		}
	}
}
