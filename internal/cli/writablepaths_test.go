package cli

import (
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
)

// isolateWritablePaths points the config discovery at a temp XDG dir, resets
// the session registry, and restores both when the test ends.
func isolateWritablePaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	grantstest.Install(t)
	return dir
}

// TestWritablePathsLifecycle covers the merge order and the add/remove
// round-trip: a granted path lands in both the registry and permissions.toml,
// and removal revokes the session grant and deletes the saved entry.
func TestWritablePathsLifecycle(t *testing.T) {
	dir := isolateWritablePaths(t)
	granted := filepath.Join(dir, "cache", "build")

	entries, warnings := ListWritablePaths()
	if len(entries) != 0 || len(warnings) != 0 {
		t.Fatalf("fresh state should list nothing, got %+v warnings=%v", entries, warnings)
	}

	root, err := AddWritablePath(granted)
	if err != nil || root != granted {
		t.Fatalf("AddWritablePath = %q err=%v, want %q", root, err, granted)
	}
	if !grants.Allows(filepath.Join(granted, "x"), grants.Write) {
		t.Fatalf("the persisted grant should be a row in the table: %+v", grants.Process().Entries().All())
	}
	entries, _ = ListWritablePaths()
	if len(entries) != 1 || entries[0].Source != WritableSourceSaved {
		t.Fatalf("list should show the saved grant, got %+v", entries)
	}

	// Adding again must not duplicate the entry.
	if _, err := AddWritablePath(granted); err != nil {
		t.Fatalf("second AddWritablePath: %v", err)
	}
	if n := len(grants.WritableRoots()); n != 1 {
		t.Fatalf("second add duplicated the entry: %d rows", n)
	}

	res, err := RemoveWritablePath(granted)
	if err != nil || !res.Found() || res.WasSession || !res.WasRemembered {
		t.Fatalf("RemoveWritablePath = %+v err=%v", res, err)
	}
	if entries, _ = ListWritablePaths(); len(entries) != 0 {
		t.Fatalf("after removal the list should be empty, got %+v", entries)
	}
	if got := grants.WritableRoots(); len(got) != 0 {
		t.Fatalf("registry should be empty after removal, got %v", got)
	}
}

// TestWritablePathTildeExpansionAndDisplay checks the "~/" form from both
// ends: it normalizes against $HOME and renders back as "~/…".
func TestWritablePathTildeExpansionAndDisplay(t *testing.T) {
	isolateWritablePaths(t)
	root, err := AddWritablePath("~/cargo/target")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) != "target" || filepath.Base(filepath.Dir(root)) != "cargo" {
		t.Fatalf("unexpected expansion: %q", root)
	}
	if got := DisplayHomePath(root); got != "~/cargo/target" {
		t.Fatalf("DisplayHomePath = %q", got)
	}
}
