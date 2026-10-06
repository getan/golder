package prompts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/seatbelt"
)

// TestPermissionCommandWritable drives the /permissions writable surface
// through the registry exactly as the REPL does: list, add (persisted), list
// again, remove, and the usage text for a bogus verb.
func TestPermissionCommandWritable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	seatbelt.SetWritableRoots(nil)
	t.Cleanup(func() { seatbelt.SetWritableRoots(nil) })

	reg := runtime.NewSlashRegistry()
	RegisterPermissionCommand(reg, nil, nil)

	out, err := reg.ResolveOutcome("/permissions writable list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.Message, "add: /permissions writable add <path>") {
		t.Fatalf("empty list should show usage, got:\n%s", out.Message)
	}

	granted := filepath.Join(dir, "cache", "build")
	out, err = reg.ResolveOutcome("/permissions writable add " + granted)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out.Message, "saved to permissions.toml") {
		t.Fatalf("add should report the persistent store, got:\n%s", out.Message)
	}
	saved, err := config.LoadPermissionsConfig()
	if err != nil || len(saved.WritableRoots) != 1 || saved.WritableRoots[0] != granted {
		t.Fatalf("store should hold the grant, got %+v err=%v", saved.WritableRoots, err)
	}

	out, err = reg.ResolveOutcome("/permissions writable")
	if err != nil {
		t.Fatalf("bare writable: %v", err)
	}
	if !strings.Contains(out.Message, granted) || !strings.Contains(out.Message, "permissions.toml, persisted") {
		t.Fatalf("list should mark the saved entry, got:\n%s", out.Message)
	}

	out, err = reg.ResolveOutcome("/permissions writable rm " + granted)
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out.Message, "revoked") {
		t.Fatalf("rm should confirm, got:\n%s", out.Message)
	}
	if saved, _ := config.LoadPermissionsConfig(); len(saved.WritableRoots) != 0 {
		t.Fatalf("store should be empty after rm, got %v", saved.WritableRoots)
	}

	if out, _ = reg.ResolveOutcome("/permissions writable bogus"); !strings.Contains(out.Message, "usage: /permissions writable") {
		t.Fatalf("bogus verb should print usage, got:\n%s", out.Message)
	}
	if out, _ = reg.ResolveOutcome("/permissions"); !strings.Contains(out.Message, "/permissions writable") {
		t.Fatalf("mode listing should mention writable, got:\n%s", out.Message)
	}
}
