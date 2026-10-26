package agenttool

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapshotPendingBytesTrackCommitAndImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	writeFileT(t, path, "1234")
	r := NewFileSnapshotRecorder()
	r.Record(path)
	if got := r.PendingBytes(); got != 4 {
		t.Fatalf("pending bytes = %d, want 4", got)
	}
	r.Commit("leaf", "turn")
	if got := r.PendingBytes(); got != 0 {
		t.Fatalf("pending bytes after commit = %d, want 0", got)
	}
	r.ImportWorkerBaseline([]SnapshotTransfer{{Path: "/imported", Existed: true, Content: []byte("abc")}})
	if got := r.PendingBytes(); got != 3 {
		t.Fatalf("pending bytes after import = %d, want 3", got)
	}
}

func TestSnapshotTurnBudgetMarksOversizedFileTooLarge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparse truncate differs on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, snapshotTurnMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	r := NewFileSnapshotRecorder()
	r.Record(path)
	if got := r.PendingBytes(); got != 0 {
		t.Fatalf("oversized snapshot retained %d bytes", got)
	}
	if snap := r.pending[path]; !snap.TooLarge || snap.Content != nil {
		t.Fatalf("snapshot = %+v, want TooLarge with no content", snap)
	}
	if !r.CanRecord(1) {
		t.Fatal("a skipped oversized file must not consume the turn budget")
	}
}
