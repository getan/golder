package prompts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/runtime"
)

// TestPermissionCommandWritable drives the /boundary writable surface through
// the registry exactly as the REPL does: list, add (persisted), list again,
// remove, and the usage text for a bogus verb.
func TestPermissionCommandWritable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	grantstest.Install(t)

	reg := runtime.NewSlashRegistry()
	RegisterPermissionCommand(reg, nil, nil)
	RegisterBoundaryCommand(reg, nil)

	out, err := reg.ResolveOutcome("/boundary writable list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.Message, "add: /boundary writable add <path>") {
		t.Fatalf("empty list should show usage, got:\n%s", out.Message)
	}

	granted := filepath.Join(dir, "cache", "build")
	out, err = reg.ResolveOutcome("/boundary writable add " + granted)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out.Message, "saved to grants.json") {
		t.Fatalf("add should report the persistent store, got:\n%s", out.Message)
	}
	if !grants.Allows(granted, grants.Write) {
		t.Fatalf("the table should hold the grant: %+v", grants.Process().Entries().All())
	}

	out, err = reg.ResolveOutcome("/boundary writable")
	if err != nil {
		t.Fatalf("bare writable: %v", err)
	}
	if !strings.Contains(out.Message, granted) || !strings.Contains(out.Message, "grants.json, remembered") {
		t.Fatalf("list should mark the saved entry, got:\n%s", out.Message)
	}

	out, err = reg.ResolveOutcome("/boundary writable rm " + granted)
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out.Message, "revoked") {
		t.Fatalf("rm should confirm, got:\n%s", out.Message)
	}
	if grants.Allows(granted, grants.Write) {
		t.Fatal("the table should hold no write row after rm")
	}

	if out, _ = reg.ResolveOutcome("/boundary writable bogus"); !strings.Contains(out.Message, "usage: /boundary writable") {
		t.Fatalf("bogus verb should print usage, got:\n%s", out.Message)
	}
	// The bare form is the mode picker, and it points at the command that owns
	// path grants — they are no longer its subject.
	if out, _ = reg.ResolveOutcome("/permissions"); !strings.Contains(out.Message, "/boundary list") {
		t.Fatalf("bare /permissions should point at the boundary report, got:\n%s", out.Message)
	}
}

// TestPermissionCommandList pins the split between the two subjects: /permissions
// answers for the approval mode and names where the paths are audited, while
// /boundary list is that audit — both grant kinds, each with the file it came
// from (the file a user edits by hand, so it is part of the contract).
func TestPermissionCommandList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	grantstest.Install(t)

	reg := runtime.NewSlashRegistry()
	st := permissions.New(permissions.Ask)
	RegisterPermissionCommand(reg, st, nil)
	RegisterBoundaryCommand(reg, nil)

	cmd, ok := reg.Lookup("permissions")
	if !ok {
		t.Fatal("/permissions not registered")
	}
	if !strings.Contains(cmd.ArgumentHint, "list") {
		t.Errorf("the hint should offer list: %q", cmd.ArgumentHint)
	}

	// Grant one of each kind, so the boundary report has something to show.
	granted := filepath.Join(dir, "cache", "build")
	if _, err := reg.ResolveOutcome("/boundary writable add " + granted); err != nil {
		t.Fatalf("add writable: %v", err)
	}
	readable := filepath.Join(dir, "elsewhere")
	if _, err := reg.ResolveOutcome("/boundary readable add " + readable); err != nil {
		t.Fatalf("add readable: %v", err)
	}

	got := cmd.Action("list")
	for _, want := range []string{
		"approval mode: Ask",                   // the live mode, not a default
		"read-only | ask | auto | full-access", // what it can be switched to
		"/boundary list",                       // where the paths are audited
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q:\n%s", want, got)
		}
	}
	// The mode report keeps to its subject. Paths on this screen taught people
	// to look for "permissions" when they meant "where may this read".
	if strings.Contains(got, "writable paths") || strings.Contains(got, granted) {
		t.Errorf("/permissions list should not carry path grants:\n%s", got)
	}

	bd, ok := reg.Lookup("boundary")
	if !ok {
		t.Fatal("/boundary not registered")
	}
	bgot := bd.Action("list")
	for _, want := range []string{
		"writable paths",                     // the grant kind
		granted, "(grants.json, remembered)", // the grant and its provenance
		"readable paths outside the workspace", // the other grant kind
		readable, "(grants.json, remembered)",  // and its provenance
		"grants.json",                          // the file the grants live in, named for hand editing
		"/boundary writable add", "add <path>", // how to change what is shown
		"/grants", // the whole table, refusals included
	} {
		if !strings.Contains(bgot, want) {
			t.Errorf("boundary list output missing %q:\n%s", want, bgot)
		}
	}

	// The mode shown is the live one, not a constant.
	st.Set(permissions.FullAccess)
	if got := cmd.Action("list"); !strings.Contains(got, "approval mode: Full Access") {
		t.Errorf("list should report the live mode:\n%s", got)
	}

	// The bare form stays the mode picker, and points at the boundary report.
	bare := cmd.Action("")
	if !strings.Contains(bare, "(current)") {
		t.Errorf("bare /permissions should still mark the current mode:\n%s", bare)
	}
	if !strings.Contains(bare, "/boundary list") {
		t.Errorf("bare /permissions should point at the boundary report:\n%s", bare)
	}
}
