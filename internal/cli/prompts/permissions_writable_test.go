package prompts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/permissions"
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
	// The bare form is the mode picker; it points at the full report, which is
	// where the writable and readable grants are listed together.
	if out, _ = reg.ResolveOutcome("/permissions"); !strings.Contains(out.Message, "/permissions list") {
		t.Fatalf("mode listing should point at the full report, got:\n%s", out.Message)
	}
}

// TestPermissionCommandList is the overview the per-scope subcommands are slices
// of: one invocation that shows the approval mode, both grant kinds with their
// provenance, and the two files they come from — the "everything, not just what
// applies here" report. It is also where a user finds out which file to edit by
// hand, so the file paths are part of the contract.
func TestPermissionCommandList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	seatbelt.SetWritableRoots(nil)
	t.Cleanup(func() { seatbelt.SetWritableRoots(nil) })

	reg := runtime.NewSlashRegistry()
	st := permissions.New(permissions.Ask)
	RegisterPermissionCommand(reg, st, nil)

	cmd, ok := reg.Lookup("permissions")
	if !ok {
		t.Fatal("/permissions not registered")
	}
	if !strings.Contains(cmd.ArgumentHint, "list") {
		t.Errorf("the hint should offer list: %q", cmd.ArgumentHint)
	}

	// Grant one of each kind, so the report has something to show.
	granted := filepath.Join(dir, "cache", "build")
	if _, err := reg.ResolveOutcome("/permissions writable add " + granted); err != nil {
		t.Fatalf("add writable: %v", err)
	}
	readable := filepath.Join(dir, "elsewhere")
	if _, err := reg.ResolveOutcome("/permissions readable add " + readable); err != nil {
		t.Fatalf("add readable: %v", err)
	}

	got := cmd.Action("list")
	for _, want := range []string{
		"approval mode: Ask",                     // the live mode, not a default
		"read-only | ask | auto | full-access",   // what it can be switched to
		"writable paths",                         // the grant kind
		granted, "(permissions.toml, persisted)", // the grant and its provenance
		"readable paths outside the workspace",    // the other grant kind
		readable, "(permissions.toml, persisted)", // and its provenance
		"permissions.toml", "config.toml", // the files, named for hand editing
		"/permissions writable add", "add <path>", // how to change what is shown
		"/trust list", // the report this one does not cover
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q:\n%s", want, got)
		}
	}

	// The mode shown is the live one, not a constant.
	st.Set(permissions.FullAccess)
	if got := cmd.Action("list"); !strings.Contains(got, "approval mode: Full Access") {
		t.Errorf("list should report the live mode:\n%s", got)
	}

	// The bare form stays the mode picker, and points at the full report.
	bare := cmd.Action("")
	if !strings.Contains(bare, "(current)") {
		t.Errorf("bare /permissions should still mark the current mode:\n%s", bare)
	}
	if !strings.Contains(bare, "/permissions list") {
		t.Errorf("bare /permissions should point at the list:\n%s", bare)
	}
}
