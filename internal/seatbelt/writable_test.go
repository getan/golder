package seatbelt

import (
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// TestWritableRootsComeFromTheTable pins where the profile's writable roots come
// from now: the authorization table, which is the one store every layer reads.
// The registry this used to test is gone, so what is worth asserting is the
// delegation — a row in the table is a root in the profile, and revoking the row
// takes it out again.
func TestWritableRootsComeFromTheTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := grantstest.Install(t)

	got, err := grants.AddWritableRoot("~/cache dir", grants.Session, "test")
	if err != nil {
		t.Fatalf("AddWritableRoot: %v", err)
	}
	if want := filepath.Join(home, "cache dir"); got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
	other, err := grants.AddWritableRoot("/Volumes/KIOXIA/target", grants.Session, "test")
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	canon := permissions.Canonical(got)
	if roots := WritableRoots(); len(roots) != 2 {
		t.Fatalf("roots = %v, want both grants", roots)
	}
	// One row answers both sides: a writable root is readable too.
	if !store.Allows(filepath.Join(got, "x"), grants.Write) {
		t.Errorf("the grant is not writable: %v", store.Entries().All())
	}
	if !store.Allows(filepath.Join(got, "x"), grants.Read) {
		t.Errorf("a writable root must be readable too: %v", store.Entries().All())
	}
	// Revoking the row removes it from the profile's list.
	if _, err := store.RemoveCapability(other, grants.Write); err != nil {
		t.Fatalf("RemoveCapability: %v", err)
	}
	if roots := WritableRoots(); len(roots) != 1 || roots[0] != canon {
		t.Fatalf("roots after revoke = %v, want only %q", roots, got)
	}
	// Relative and empty paths are still refused, at the table's door.
	if _, err := grants.AddWritableRoot("relative/path", grants.Session, "test"); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if _, err := grants.AddWritableRoot("", grants.Session, "test"); err == nil {
		t.Fatal("empty path must be rejected")
	}
}

// TestWritableRootsInBwrapArgv pins the Linux policy: a granted path is bound
// writable (--bind-try, after the read-only pass) and also appears in the read
// whitelist, so the command can read back what it wrote.
func TestWritableRootsInBwrapArgv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", filepath.Join(home, "ghome"))
	store := grantstest.Install(t)
	granted := t.TempDir()
	if _, err := grants.AddWritableRoot(granted, grants.Session, "test"); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	r := &BwrapRunner{ProjectDir: project, TmpDir: t.TempDir()}
	argv, _, err := r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	canon := permissions.Canonical(granted)
	writable := indexOfSeq(argv, "--bind-try", canon, canon)
	readOnly := indexOfSeq(argv, "--ro-bind", canon, canon)
	if writable < 0 {
		t.Fatalf("granted root missing from argv: %v", argv)
	}
	if readOnly < 0 {
		t.Fatalf("granted root is not readable: %v", argv)
	}
	if readOnly > writable {
		t.Fatalf("writable bind must override the read-only one: %v", argv)
	}
	// Revoking the writable root clears read and write together — that is the
	// row a write grant put there (write implies read). Clearing only Write
	// would leave the directory readable, which is the opposite of what
	// removing a writable root means.
	if _, err := store.RemoveCapability(granted, grants.ReadWrite); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	argv, _, err = r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv after revoke: %v", err)
	}
	if indexOfSeq(argv, "--bind-try", canon, canon) >= 0 || indexOfSeq(argv, "--ro-bind", canon, canon) >= 0 {
		t.Fatalf("revoked root still mounted: %v", argv)
	}
}
