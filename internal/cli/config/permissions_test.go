package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFileConfigPermissionsTable pins the hand-edited seed table:
// writable_roots under [permissions] in config.toml.
func TestLoadFileConfigPermissionsTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[permissions]\nwritable_roots = [\"/Volumes/KIOXIA\", \"~/cache\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if got := cfg.Permissions.WritableRoots; len(got) != 2 || got[0] != "/Volumes/KIOXIA" || got[1] != "~/cache" {
		t.Fatalf("writable_roots = %v", got)
	}
}

// TestPermissionsConfigRoundTrip covers the managed store: it lives next to
// config.toml, a missing file is not an error, and a save/load round-trips.
func TestPermissionsConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got := PermissionsConfigPath(); got != filepath.Join(dir, "golder", "permissions.toml") {
		t.Fatalf("PermissionsConfigPath = %q", got)
	}
	cfg, err := LoadPermissionsConfig()
	if err != nil || len(cfg.WritableRoots) != 0 {
		t.Fatalf("missing file should load empty, got %+v err=%v", cfg, err)
	}
	if err := SavePermissionsConfig(PermissionsConfig{WritableRoots: []string{"/a", "/b"}}); err != nil {
		t.Fatalf("SavePermissionsConfig: %v", err)
	}
	cfg, err = LoadPermissionsConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(cfg.WritableRoots) != 2 || cfg.WritableRoots[0] != "/a" || cfg.WritableRoots[1] != "/b" {
		t.Fatalf("round-trip = %v", cfg.WritableRoots)
	}
	// A nil list saves as an explicit empty array (removing the last entry
	// must not leave a stale file shape that fails to reload).
	if err := SavePermissionsConfig(PermissionsConfig{}); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadPermissionsConfig()
	if err != nil || cfg.WritableRoots == nil || len(cfg.WritableRoots) != 0 {
		t.Fatalf("empty save = %+v err=%v", cfg, err)
	}
}

func TestLoadPermissionsConfigMalformed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "golder", "permissions.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("writable_roots = [unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPermissionsConfig(); err == nil {
		t.Fatal("malformed permissions.toml must be an error")
	}
}
