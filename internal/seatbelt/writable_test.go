package seatbelt

import (
	"path/filepath"
	"testing"
)

func TestWritableRootRegistry(t *testing.T) {
	defer SetWritableRoots(nil)
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := AddWritableRoot("~/cache dir")
	if err != nil {
		t.Fatalf("AddWritableRoot: %v", err)
	}
	if want := filepath.Join(home, "cache dir"); got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
	// Duplicates collapse; a second add is a no-op, not an error.
	if _, err := AddWritableRoot(got); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	other, err := AddWritableRoot("/Volumes/KIOXIA/target")
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if roots := WritableRoots(); len(roots) != 2 || roots[0] != got || roots[1] != other {
		t.Fatalf("roots = %v", roots)
	}
	// The returned slice is a copy: mutating it must not affect the registry.
	WritableRoots()[0] = "/bogus"
	if roots := WritableRoots(); roots[0] != got {
		t.Fatalf("WritableRoots must return a copy, got %v", roots)
	}
	if !RemoveWritableRoot(other) || RemoveWritableRoot(other) {
		t.Fatal("RemoveWritableRoot should report presence exactly once")
	}
	if roots := WritableRoots(); len(roots) != 1 || roots[0] != got {
		t.Fatalf("roots after remove = %v", roots)
	}
	// Relative paths are rejected; the registry stays untouched.
	if _, err := AddWritableRoot("relative/path"); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if _, err := AddWritableRoot(""); err == nil {
		t.Fatal("empty path must be rejected")
	}
}

func TestSetWritableRoots(t *testing.T) {
	defer SetWritableRoots(nil)
	errs := SetWritableRoots([]string{"/a", "relative", "/a", "  /b  "})
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly the relative path", errs)
	}
	if roots := WritableRoots(); len(roots) != 2 || roots[0] != "/a" || roots[1] != "/b" {
		t.Fatalf("roots = %v", roots)
	}
	SetWritableRoots(nil)
	if roots := WritableRoots(); len(roots) != 0 {
		t.Fatalf("reset failed: %v", roots)
	}
}

// TestWritableRootsInBwrapArgv pins the Linux policy: a granted path is bound
// writable (--bind-try, after the read-only pass) and also appears in the read
// whitelist, so the command can read back what it wrote.
func TestWritableRootsInBwrapArgv(t *testing.T) {
	defer SetWritableRoots(nil)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", filepath.Join(home, "ghome"))
	granted := t.TempDir()
	if _, err := AddWritableRoot(granted); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	r := &BwrapRunner{ProjectDir: project, TmpDir: t.TempDir()}
	argv, _, err := r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	writable := indexOfSeq(argv, "--bind-try", granted, granted)
	readOnly := indexOfSeq(argv, "--ro-bind", granted, granted)
	if writable < 0 {
		t.Fatalf("granted root missing from argv: %v", argv)
	}
	if readOnly < 0 {
		t.Fatalf("granted root is not readable: %v", argv)
	}
	if readOnly > writable {
		t.Fatalf("writable bind must override the read-only one: %v", argv)
	}
	SetWritableRoots(nil)
	argv, _, err = r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv after revoke: %v", err)
	}
	if indexOfSeq(argv, "--bind-try", granted, granted) >= 0 || indexOfSeq(argv, "--ro-bind", granted, granted) >= 0 {
		t.Fatalf("revoked root still mounted: %v", argv)
	}
}
