//go:build unix

package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockSerializesSeparateFileDescriptors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	acquired := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		unlock, err := Acquire(path)
		if err != nil {
			errs <- err
			return
		}
		acquired <- unlock
	}()
	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("a second writer acquired the held lock")
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case unlock := <-acquired:
		unlock()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("waiting writer did not acquire the released lock")
	}
}

func TestLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if unlock, err := Acquire(path); err == nil {
		unlock()
		t.Fatal("accepted a symlink as the lock file")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "unchanged" {
		t.Fatal("lock attempt changed the symlink target")
	}
}
