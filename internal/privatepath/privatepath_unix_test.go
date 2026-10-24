//go:build unix

package privatepath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The ownership refusal is the rule that makes the uid-scoped runtime directory
// meaningful, and a test account usually cannot chown a directory to a second
// one. Standing in for the second account tests the same branch everywhere: the
// directory exists, it is a real directory, and it is simply not ours.
func TestEnsureDirRefusesAForeignOwner(t *testing.T) {
	base := shortTemp(t)
	dir := filepath.Join(base, "theirs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ours := ownerUID
	ownerUID = func() int { return ours() + 1000 }
	t.Cleanup(func() { ownerUID = ours })

	if err := EnsureDir(dir); !errors.Is(err, ErrForeignOwner) {
		t.Fatalf("EnsureDir = %v, want ErrForeignOwner", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the other account's directory is now mode %04o, want it untouched at 0755", perm)
	}
	if err := CheckFile(filepath.Join(dir, "record"), FileMode); err == nil {
		t.Error("CheckFile described a file of another account")
	}
	// The same directory is accepted again once ownership is read as it really is.
	ownerUID = ours
	if err := EnsureDir(dir); err != nil {
		t.Errorf("EnsureDir on this user's own directory: %v", err)
	}
}
