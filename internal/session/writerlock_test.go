package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriterLockRefusesSecondWriterAndReportsHolder(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AcquireWriter("session-1", "GUI")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := store.AcquireWriter("session-1", "TUI")
	if second != nil || !IsSessionInUse(err) {
		t.Fatalf("second=%v err=%v", second, err)
	}
	var inUse *SessionInUseError
	if !errors.As(err, &inUse) || inUse.Holder.Frontend != "GUI" || inUse.Holder.PID != os.Getpid() || inUse.Holder.StartedAt.IsZero() {
		t.Fatalf("in-use=%+v", inUse)
	}
	first.Release()
	third, err := store.AcquireWriter("session-1", "TUI")
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	third.Release()
	if _, err := os.Stat(filepath.Join(store.Dir(), "session-1.lock")); err != nil {
		t.Fatalf("stable lock inode was removed: %v", err)
	}
}

func TestWriterLockRejectsSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink privileges vary on Windows")
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(store.Dir(), "session-2.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireWriter("session-2", "GUI"); err == nil {
		t.Fatal("symlink lock was accepted")
	}
}
