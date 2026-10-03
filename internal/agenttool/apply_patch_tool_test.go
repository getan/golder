package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// runPatch executes one apply_patch call and returns the result.
func runPatch(t *testing.T, tool *ApplyPatchTool, patchText string) agentcore.AgentToolResult {
	t.Helper()
	args, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, gerr := tool.Execute(context.Background(), "call-1", args, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res
}

func patchTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.go"), []byte("package gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doomed.go"), []byte("package doomed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestApplyPatchToolAllOps drives one patch that adds, updates, moves, and
// deletes files, then verifies the tree, the summary, the diff metadata, and
// the /rewind snapshots.
func TestApplyPatchToolAllOps(t *testing.T) {
	dir := patchTree(t)
	snap := NewFileSnapshotRecorder()
	tool := &ApplyPatchTool{Root: dir, Snap: snap}

	res := runPatch(t, tool, `*** Begin Patch
*** Add File: sub/new.go
+package sub
*** Update File: a.go
@@
-func old() {}
+func new() {}
*** Update File: gone.go
*** Move to: moved.go
@@
-package gone
+package moved
*** Delete File: doomed.go
*** End Patch
`)
	txt := resultText(res)
	if !strings.Contains(txt, "Applied 4 change(s):") {
		t.Fatalf("summary = %q", txt)
	}
	for _, want := range []string{"  A sub/new.go", "  U a.go", "  M gone.go \u2192 moved.go", "  D doomed.go"} {
		if !strings.Contains(txt, want) {
			t.Errorf("summary missing %q\n%s", want, txt)
		}
	}
	if !strings.Contains(txt, "--- a/a.go") {
		t.Errorf("summary should carry a unified diff\n%s", txt)
	}

	// Disk state.
	if got := readToolFile(t, dir, "a.go"); !strings.Contains(got, "func new()") {
		t.Errorf("a.go = %q, want the updated body", got)
	}
	if got := readToolFile(t, dir, "sub/new.go"); got != "package sub\n" {
		t.Errorf("sub/new.go = %q", got)
	}
	if got := readToolFile(t, dir, "moved.go"); got != "package moved\n" {
		t.Errorf("moved.go = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.go")); !os.IsNotExist(err) {
		t.Error("gone.go should have been removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "doomed.go")); !os.IsNotExist(err) {
		t.Error("doomed.go should have been deleted")
	}

	// Rewind snapshots: every touched path was recorded, so the turn's Commit
	// yields exactly one restore point covering them.
	if !snap.Commit("leaf", "apply_patch") {
		t.Fatal("Commit reported no restore point despite the recorded files")
	}
	if pts := snap.Points(); len(pts) != 1 {
		t.Fatalf("restore points = %d, want 1", len(pts))
	}

	// Metadata: the TUI reads Details diff/files/count.
	details, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T", res.Details)
	}
	if details["changes"] != 4 {
		t.Errorf("changes = %v, want 4", details["changes"])
	}
	if diff, _ := details["diff"].(string); !strings.Contains(diff, "+package sub") {
		t.Errorf("details diff missing the add:\n%s", diff)
	}
}

// TestApplyPatchToolContextMismatchIsAtomic verifies a stale hunk fails the
// whole call before anything is written.
func TestApplyPatchToolContextMismatchIsAtomic(t *testing.T) {
	dir := patchTree(t)
	tool := &ApplyPatchTool{Root: dir}
	res := runPatch(t, tool, `*** Begin Patch
*** Add File: would-exist.go
+package x
*** Update File: a.go
@@
-func does_not_match() {}
+func new() {}
*** End Patch
`)
	if txt := resultText(res); !strings.Contains(txt, "Invalid Context") {
		t.Fatalf("result = %q, want an Invalid Context error", txt)
	}
	if _, err := os.Stat(filepath.Join(dir, "would-exist.go")); !os.IsNotExist(err) {
		t.Error("a failed patch must not leave earlier operations applied")
	}
	if got := readToolFile(t, dir, "a.go"); !strings.Contains(got, "func old()") {
		t.Errorf("a.go changed despite the failure: %q", got)
	}
}

// TestApplyPatchToolRejectsEscape verifies the workspace guard: a patch path
// outside Root fails as an error result and writes nothing.
func TestApplyPatchToolRejectsEscape(t *testing.T) {
	dir := patchTree(t)
	outside := filepath.Join(filepath.Dir(dir), "escape.txt")
	tool := &ApplyPatchTool{Root: dir}
	res := runPatch(t, tool, "*** Begin Patch\n*** Add File: ../escape.txt\n+oops\n*** End Patch\n")
	if txt := resultText(res); !strings.Contains(txt, "outside the workspace root") {
		t.Fatalf("result = %q, want an escape error", txt)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("a rejected patch must not write outside the root")
	}
}

// TestApplyPatchToolParseError verifies malformed patch text is an error
// result naming the offending line, not a Go error.
func TestApplyPatchToolParseError(t *testing.T) {
	tool := &ApplyPatchTool{Root: t.TempDir()}
	res := runPatch(t, tool, "not a patch at all")
	if txt := resultText(res); !strings.Contains(txt, "Invalid patch") {
		t.Fatalf("result = %q, want a parse error", txt)
	}
}

// TestApplyPatchToolExtraRoots verifies the skills-style extra root is
// writable while the workspace root still bounds everything else.
func TestApplyPatchToolExtraRoots(t *testing.T) {
	dir := patchTree(t)
	extra := t.TempDir()
	tool := &ApplyPatchTool{Root: dir, ExtraRoots: []string{extra}}
	res := runPatch(t, tool, "*** Begin Patch\n*** Add File: "+filepath.ToSlash(filepath.Join(extra, "SKILL.md"))+"\n+---\n+name: x\n*** End Patch\n")
	if txt := resultText(res); !strings.Contains(txt, "Applied 1 change(s):") {
		t.Fatalf("result = %q", txt)
	}
	if _, err := os.Stat(filepath.Join(extra, "SKILL.md")); err != nil {
		t.Fatalf("extra root file not written: %v", err)
	}
}

func readToolFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
