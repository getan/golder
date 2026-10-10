package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readRootTestHome points the home directory at a temp dir for the duration of
// a test, so the broad-root policy (which is defined against $HOME) is
// exercised deterministically on any machine.
func readRootTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(func() { SetReadRoots(nil) })
	return home
}

func TestInside(t *testing.T) {
	cases := []struct {
		root, p string
		want    bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c/d.go", true},
		{"/a/b", "/a/bc/d.go", false},
		{"/a/b", "/a", false},
		{"/a/b", "/a/b/../c", false},
		{"/a/b/", "/a/b/c", true},
		{"", "/a/b", false},
		{"/a/b", "", false},
	}
	for _, c := range cases {
		if got := Inside(c.root, c.p); got != c.want {
			t.Errorf("Inside(%q, %q) = %v, want %v", c.root, c.p, got, c.want)
		}
	}
}

// TestSetReadRootsRejectsBroadRoots pins the guard that keeps a grant from
// removing the boundary: the filesystem root, the home directory itself and
// $HOME's parent are refused, while a directory that merely sits outside the
// workspace (including a workspace parent) is fine.
func TestSetReadRootsRejectsBroadRoots(t *testing.T) {
	home := readRootTestHome(t)
	sibling := filepath.Join(home, "work", "proj")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	errs := SetReadRoots([]string{"/", home, filepath.Dir(home), sibling})
	if len(errs) != 3 {
		t.Errorf("expected 3 refusals (/, $HOME, $HOME's parent), got %d: %v", len(errs), errs)
	}
	roots := ReadRoots()
	if len(roots) != 1 || roots[0] != sibling {
		t.Fatalf("only the ordinary directory should be granted, got %v", roots)
	}
	if !ReadableAt(filepath.Join(sibling, "a", "b.go")) {
		t.Error("a path under the grant should read as readable")
	}
	if ReadableAt(home) {
		t.Error("$HOME must not be readable by a grant to a directory inside it")
	}
}

// TestAddAndRemoveReadRoot covers the session grant lifecycle: the same
// directory granted twice is stored once, "~" expands, and removal takes it
// off the list.
func TestAddAndRemoveReadRoot(t *testing.T) {
	home := readRootTestHome(t)
	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := AddReadRoot("~/repo")
	if err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	if got != dir {
		t.Errorf("~ did not expand: %q, want %q", got, dir)
	}
	if _, err := AddReadRoot(dir); err != nil {
		t.Fatalf("second AddReadRoot: %v", err)
	}
	if roots := ReadRoots(); len(roots) != 1 {
		t.Errorf("a duplicate grant should be stored once, got %v", roots)
	}
	if !RemoveReadRoot(dir) {
		t.Error("RemoveReadRoot should report the path was present")
	}
	if ReadableAt(dir) {
		t.Error("a removed grant must not read as readable")
	}
	if RemoveReadRoot(dir) {
		t.Error("removing twice should report absence")
	}
}

// TestReadableRootsFromEnv covers GOLDER_READABLE_ROOTS: entries follow the
// same rules as the config list, so an invalid or too-broad entry is dropped
// rather than granting something the file would have refused.
func TestReadableRootsFromEnv(t *testing.T) {
	home := readRootTestHome(t)
	good := filepath.Join(home, "repo")
	env := func(string) string {
		return good + ":relative/path," + home + ":" + filepath.Join(home, "other")
	}
	got := ReadableRootsFromEnv(env)
	want := []string{good, filepath.Join(home, "other")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ReadableRootsFromEnv = %v, want %v (relative and $HOME dropped)", got, want)
	}
	if out := ReadableRootsFromEnv(nil); out != nil {
		t.Errorf("nil getenv should yield nothing, got %v", out)
	}
	if out := ReadableRootsFromEnv(func(string) string { return "   " }); out != nil {
		t.Errorf("blank value should yield nothing, got %v", out)
	}
}

// TestNormalizePathAgreesWithWritableSide pins the single-source rule: the
// read side and the write side (seatbelt) must read a spelling the same way,
// or a grant would apply to a different path than the user named.
func TestNormalizePathAgreesWithWritableSide(t *testing.T) {
	home := readRootTestHome(t)
	cases := []string{"~/x", home + "/x", filepath.Join(home, "x") + "/"}
	for _, raw := range cases {
		got, err := NormalizePath(raw)
		if err != nil {
			t.Fatalf("NormalizePath(%q): %v", raw, err)
		}
		if got != filepath.Join(home, "x") {
			t.Errorf("NormalizePath(%q) = %q, want the cleaned absolute path", raw, got)
		}
	}
	if _, err := NormalizePath("relative/path"); err == nil {
		t.Error("a relative path must be refused")
	}
}

// TestAbsAgainst pins the gate/tool agreement: resolving a call argument must
// not expand "~" (the file tools do not), so the gate asks about the same
// location the tool would visit.
func TestAbsAgainst(t *testing.T) {
	base := "/work/proj"
	if got, err := AbsAgainst(base, "sub/a.go"); err != nil || got != "/work/proj/sub/a.go" {
		t.Errorf("relative = %q (%v), want /work/proj/sub/a.go", got, err)
	}
	if got, err := AbsAgainst(base, "/abs/b.go"); err != nil || got != "/abs/b.go" {
		t.Errorf("absolute = %q (%v), want /abs/b.go", got, err)
	}
	if got, err := AbsAgainst(base, "~/x"); err != nil || got != "/work/proj/~/x" {
		t.Errorf("~ must stay literal for call arguments, got %q (%v)", got, err)
	}
	if got, err := AbsAgainst(base, "../a.go"); err != nil || got != "/work/a.go" {
		t.Errorf(".. should resolve upward, got %q (%v)", got, err)
	}
}

// TestReadScopeCandidate covers which directory a boundary question offers:
// the file's own directory normally, its parent when that is too broad, and
// nothing when both are (a file directly in $HOME).
func TestReadScopeCandidate(t *testing.T) {
	home := readRootTestHome(t)
	deep := filepath.Join(home, "work", "proj", "a.go")
	if got := ReadScopeCandidate(deep); got != filepath.Join(home, "work", "proj") {
		t.Errorf("candidate for %s = %q", deep, got)
	}
	// A file directly in $HOME: its directory is $HOME (too broad), and the
	// parent of $HOME is too broad too, so nothing is offered.
	if got := ReadScopeCandidate(filepath.Join(home, "notes.md")); got != "" {
		t.Errorf("a file in $HOME should offer no directory, got %q", got)
	}
}
