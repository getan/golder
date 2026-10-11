package cli

// Tests for the read-side of the managed permissions store: the merge order,
// the add/remove round-trip, and the config.toml seed that must never be
// duplicated into the store (the file belongs to the user). The writable side
// has the same tests; these are its twin so the two grants cannot drift.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/permissions"
)

// isolateReadablePaths points config discovery at a temp XDG dir and resets the
// session registry, restoring both when the test ends.
func isolateReadablePaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	permissions.SetReadRoots(nil)
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	return dir
}

// TestReadablePathsLifecycle covers add → list → remove: the grant lands in
// both the session registry and permissions.toml, a second add does not
// duplicate it, and removal revokes both halves.
func TestReadablePathsLifecycle(t *testing.T) {
	dir := isolateReadablePaths(t)
	granted := filepath.Join(dir, "notes")

	entries, warnings := ListReadablePaths()
	if len(entries) != 0 || len(warnings) != 0 {
		t.Fatalf("fresh state should list nothing, got %+v warnings=%v", entries, warnings)
	}

	root, seeded, err := AddReadablePath(granted)
	if err != nil || root != granted || seeded {
		t.Fatalf("AddReadablePath = %q seeded=%v err=%v, want %q seeded=false", root, seeded, err, granted)
	}
	if !permissions.ReadableAt(filepath.Join(granted, "a.md")) {
		t.Error("the session registry did not take the grant")
	}
	saved, err := config.LoadPermissionsConfig()
	if err != nil || len(saved.ReadableRoots) != 1 || saved.ReadableRoots[0] != granted {
		t.Fatalf("managed store should persist the grant, got %+v err=%v", saved.ReadableRoots, err)
	}
	entries, _ = ListReadablePaths()
	if len(entries) != 1 || entries[0].Source != ReadableSourceSaved {
		t.Fatalf("list should show the saved grant, got %+v", entries)
	}

	if _, _, err := AddReadablePath(granted); err != nil {
		t.Fatalf("second AddReadablePath: %v", err)
	}
	saved, _ = config.LoadPermissionsConfig()
	if len(saved.ReadableRoots) != 1 {
		t.Fatalf("second add duplicated the entry: %v", saved.ReadableRoots)
	}

	res, err := RemoveReadablePath(granted)
	if err != nil || !res.Found() || !res.WasSession || !res.WasSaved || res.WasConfig {
		t.Fatalf("RemoveReadablePath = %+v err=%v", res, err)
	}
	if permissions.ReadableAt(filepath.Join(granted, "a.md")) {
		t.Error("the registry should be empty after removal")
	}
	saved, _ = config.LoadPermissionsConfig()
	if len(saved.ReadableRoots) != 0 {
		t.Fatalf("managed store should be empty after removal, got %v", saved.ReadableRoots)
	}
}

// TestReadablePathsConfigSeed verifies a config.toml seed is labeled as such
// and re-adding it does not write a duplicate into the managed store.
func TestReadablePathsConfigSeed(t *testing.T) {
	dir := isolateReadablePaths(t)
	seed := filepath.Join(dir, "seeded")
	cfgPath := config.FileConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[permissions]\nreadable_roots = [\"" + seed + "\"]\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, warnings := ListReadablePaths()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(entries) != 1 || entries[0].Path != seed || entries[0].Source != ReadableSourceConfig {
		t.Fatalf("list = %+v, want the config seed labeled as such", entries)
	}

	root, seeded, err := AddReadablePath(seed)
	if err != nil || root != seed || !seeded {
		t.Fatalf("AddReadablePath(seed) = %q seeded=%v err=%v, want seeded=true", root, seeded, err)
	}
	saved, _ := config.LoadPermissionsConfig()
	if len(saved.ReadableRoots) != 0 {
		t.Fatalf("a config seed must not be copied into the managed store, got %v", saved.ReadableRoots)
	}
	if !permissions.ReadableAt(filepath.Join(seed, "a.md")) {
		t.Error("adding a seed path should still grant it for this session")
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
	if _, _, err := AddReadablePath(home); err == nil {
		t.Errorf("granting the home directory itself must be refused")
	}
	if _, _, err := AddReadablePath("/"); err == nil {
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
		{ReadableSourceConfig, false, "(config.toml seed)"},
		{ReadableSourceSaved, false, "(permissions.toml, persisted)"},
		{ReadableSourceSession, false, "(this session)"},
		{ReadableSourceSaved, true, "（permissions.toml，已持久化）"},
	} {
		if got := ReadableSourceLabel(c.source, c.zh); got != c.want {
			t.Errorf("ReadableSourceLabel(%q, zh=%v) = %q, want %q", c.source, c.zh, got, c.want)
		}
	}
}
