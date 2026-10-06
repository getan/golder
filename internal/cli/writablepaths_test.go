package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/seatbelt"
)

// isolateWritablePaths points the config discovery at a temp XDG dir, resets
// the session registry, and restores both when the test ends.
func isolateWritablePaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	seatbelt.SetWritableRoots(nil)
	t.Cleanup(func() { seatbelt.SetWritableRoots(nil) })
	return dir
}

// TestWritablePathsLifecycle covers the merge order and the add/remove
// round-trip: a config.toml seed is never duplicated into the managed store,
// a granted path lands in both the registry and permissions.toml, and removal
// revokes the session grant and deletes the saved entry.
func TestWritablePathsLifecycle(t *testing.T) {
	dir := isolateWritablePaths(t)
	granted := filepath.Join(dir, "cache", "build")

	entries, warnings := ListWritablePaths()
	if len(entries) != 0 || len(warnings) != 0 {
		t.Fatalf("fresh state should list nothing, got %+v warnings=%v", entries, warnings)
	}

	root, seeded, err := AddWritablePath(granted)
	if err != nil || root != granted || seeded {
		t.Fatalf("AddWritablePath = %q seeded=%v err=%v, want %q seeded=false", root, seeded, err, granted)
	}
	saved, err := config.LoadPermissionsConfig()
	if err != nil || len(saved.WritableRoots) != 1 || saved.WritableRoots[0] != granted {
		t.Fatalf("managed store should persist the grant, got %+v err=%v", saved.WritableRoots, err)
	}
	entries, _ = ListWritablePaths()
	if len(entries) != 1 || entries[0].Source != WritableSourceSaved {
		t.Fatalf("list should show the saved grant, got %+v", entries)
	}

	// Adding again must not duplicate the entry.
	if _, _, err := AddWritablePath(granted); err != nil {
		t.Fatalf("second AddWritablePath: %v", err)
	}
	saved, _ = config.LoadPermissionsConfig()
	if len(saved.WritableRoots) != 1 {
		t.Fatalf("second add duplicated the entry: %v", saved.WritableRoots)
	}

	res, err := RemoveWritablePath(granted)
	if err != nil || !res.Found() || !res.WasSession || !res.WasSaved || res.WasConfig {
		t.Fatalf("RemoveWritablePath = %+v err=%v", res, err)
	}
	if entries, _ = ListWritablePaths(); len(entries) != 0 {
		t.Fatalf("after removal the list should be empty, got %+v", entries)
	}
	if got := seatbelt.WritableRoots(); len(got) != 0 {
		t.Fatalf("registry should be empty after removal, got %v", got)
	}
}

// TestWritablePathsConfigSeed verifies a config.toml seed is labeled as such
// and re-adding it does not write a duplicate into the managed store.
func TestWritablePathsConfigSeed(t *testing.T) {
	dir := isolateWritablePaths(t)
	seed := filepath.Join(dir, "seeded")
	cfgPath := config.FileConfigPath()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[permissions]\nwritable_roots = [\"" + seed + "\"]\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Startup applies the same merge the command does; mirror it so the
	// registry has the seed too.
	seatbelt.SetWritableRoots([]string{seed})

	entries, warnings := ListWritablePaths()
	if len(warnings) != 0 || len(entries) != 1 || entries[0].Source != WritableSourceConfig {
		t.Fatalf("seed should list once with source config, got %+v warnings=%v", entries, warnings)
	}
	root, seeded, err := AddWritablePath(seed)
	if err != nil || root != seed || !seeded {
		t.Fatalf("AddWritablePath(seed) = %q seeded=%v err=%v", root, seeded, err)
	}
	saved, err := config.LoadPermissionsConfig()
	if err != nil || len(saved.WritableRoots) != 0 {
		t.Fatalf("seed must not be duplicated into the store, got %+v err=%v", saved.WritableRoots, err)
	}
	res, err := RemoveWritablePath(seed)
	if err != nil || !res.WasConfig || res.WasSaved || !res.WasSession {
		t.Fatalf("removing a seed should flag WasConfig, got %+v err=%v", res, err)
	}
}

// TestWritablePathTildeExpansionAndDisplay checks the "~/" form from both
// ends: it normalizes against $HOME and renders back as "~/…".
func TestWritablePathTildeExpansionAndDisplay(t *testing.T) {
	isolateWritablePaths(t)
	root, _, err := AddWritablePath("~/cargo/target")
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
