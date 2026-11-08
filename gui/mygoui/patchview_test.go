package mygoui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
)

const sampleApplyPatch = `*** Begin Patch
*** Update File: internal/demo.go
@@
-func old() {
-    return
-}
+func current() {
+    return nil
+}
*** Add File: docs/note.md
+# Note
+
+  indented detail
*** End Patch`

const samplePatchDiff = "--- a/internal/demo.go\n+++ b/internal/demo.go\n@@ -1,3 +1,3 @@\n-func old() {\n-    return\n+func current() {\n+    return nil\n }\n" +
	"--- a/docs/note.md\n+++ b/docs/note.md\n@@ -0,0 +1,3 @@\n+# Note\n+\n+  indented detail\n"

const samplePatchSummary = "Applied 2 change(s):\n  U internal/demo.go\n  A docs/note.md\n  note: context matched with whitespace normalization"

func TestParseApplyPatchPreservesIndentationAndFiles(t *testing.T) {
	files := parseApplyPatch(sampleApplyPatch)
	if len(files) != 2 || files[0].path != "internal/demo.go" || files[0].action != "Modified" || files[1].path != "docs/note.md" || files[1].action != "Added" {
		t.Fatalf("files=%+v", files)
	}
	var foundIndented bool
	for _, line := range files[0].lines {
		if line.kind == patchAdd && line.text == "    return nil" {
			foundIndented = true
		}
	}
	if !foundIndented {
		t.Fatalf("indentation was lost: %+v", files[0].lines)
	}
	if got := applyPatchSubject(map[string]any{"patch": sampleApplyPatch}); got != "2 files" {
		t.Fatalf("subject=%q", got)
	}
}

func TestApplyPatchCardRendersGroupedDiffInsteadOfRawInput(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{{
		ID: "patch", Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{Name: "apply_patch", Input: map[string]any{"patch": sampleApplyPatch}, Output: "Applied 2 changes"},
	}}
	a.index["patch"] = 0
	a.toolOpen["patch"] = true
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"apply_patch · 2 files · done", "internal/demo.go", "Modified", "docs/note.md", "Added", "    return nil", "Applied 2 changes"} {
		if !tt.HasText(want) {
			t.Fatalf("patch card is missing %q; texts=%q", want, tt.Texts())
		}
	}
	for _, raw := range []string{"*** Begin Patch", "patch: *** Begin Patch"} {
		if tt.HasText(raw) {
			t.Fatalf("patch card leaked raw wrapper %q", raw)
		}
	}
}

func TestApplyPatchCardShowsDiffOnceAndKeepsNotes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details map[string]any
	}{
		{"live details", map[string]any{"diff": samplePatchDiff}},
		{"replayed without details", nil},
		{"redacted result differs from details", map[string]any{"diff": "unredacted diff"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.items = []appsession.Item{{
				ID: "patch", Kind: appsession.ItemTool, Done: true,
				Tool: &appsession.ToolState{Name: "apply_patch", Input: map[string]any{"patch": sampleApplyPatch},
					Output: samplePatchSummary + "\n" + samplePatchDiff + "\n\nnote: repaired input", Details: tc.details},
			}}
			a.index["patch"] = 0
			a.toolOpen["patch"] = true
			tt := ui.NewTester(a.view, 1200, 800)
			texts := strings.Join(tt.Texts(), "\n")
			for _, want := range []string{"func current() {", "Applied 2 change(s):", "context matched with whitespace normalization", "note: repaired input"} {
				if !strings.Contains(texts, want) {
					t.Errorf("missing %q in %q", want, texts)
				}
			}
			for _, duplicate := range []string{"--- a/internal/demo.go", "+++ b/internal/demo.go", "+func current()"} {
				if strings.Contains(texts, duplicate) {
					t.Errorf("duplicate unified diff %q in %q", duplicate, texts)
				}
			}
			if n := strings.Count(texts, "func current() {"); n != 1 {
				t.Errorf("change displayed %d times in %q", n, texts)
			}
		})
	}
}

func TestApplyPatchDisplayOutputKeepsDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		details            map[string]any
	}{
		{"exact diff with trailing note", samplePatchSummary + "\n" + samplePatchDiff + "\n\nnote: repaired", samplePatchSummary + "\n\nnote: repaired", map[string]any{"diff": samplePatchDiff}},
		{"legacy diff with trailing note", samplePatchSummary + "\n" + samplePatchDiff + "\n\nnote: repaired", samplePatchSummary + "\n\nnote: repaired", nil},
		{"legacy diff with indented note", samplePatchSummary + "\n" + samplePatchDiff + "  note: important\n", samplePatchSummary + "\n  note: important\n", nil},
		{"legacy diff with plus note", samplePatchSummary + "\n" + samplePatchDiff + "+ additional diagnostic\n", samplePatchSummary + "\n+ additional diagnostic\n", nil},
		{"diff truncation notice", samplePatchSummary + "\n" + samplePatchDiff + "... (diff truncated; details carry the full change list)\n", samplePatchSummary + "\n... (diff truncated; details carry the full change list)\n", nil},
		{"ordinary output", "done\n--- a/file\nnot a diff", "done\n--- a/file\nnot a diff", nil},
		{"malformed diff", samplePatchSummary + "\n--- a/file\nunexpected", samplePatchSummary + "\n--- a/file\nunexpected", nil},
		{"incomplete hunk", samplePatchSummary + "\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-old\n", samplePatchSummary + "\n--- a/x\n+++ b/x\n@@ -1,1 +1,1 @@\n-old\n", nil},
		{"metadata is not a diff", samplePatchSummary + "\nimportant", samplePatchSummary + "\nimportant", map[string]any{"diff": "important"}},
		{"no changes", "apply_patch: no changes — nothing was written", "apply_patch: no changes — nothing was written", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyPatchDisplayOutput(tc.output, tc.details); got != tc.want {
				t.Fatalf("display output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyPatchTruncatedPreviewKeepsResult(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: many.txt\n" + strings.Repeat("+line\n", maxRenderedPatchLines+1) + "*** End Patch\n"
	files, truncated := parseApplyPatchView(patch)
	if !truncated || len(files) != 1 || len(files[0].lines) != maxRenderedPatchLines {
		t.Fatalf("preview: files=%d, truncated=%t", len(files), truncated)
	}
	a := testApp()
	a.items = []appsession.Item{{
		ID: "large-patch", Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{Name: "apply_patch", Input: map[string]any{"patch": patch}, Output: samplePatchSummary + "\n" + samplePatchDiff},
	}}
	a.index["large-patch"] = 0
	a.toolOpen["large-patch"] = true
	tt := ui.NewTester(a.view, 1200, 800)
	texts := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(texts, "Patch preview truncated; the tool result is shown below.") || !strings.Contains(texts, "--- a/internal/demo.go") {
		t.Fatal("truncated preview did not retain the result diff and its notice")
	}
}

func TestApplyPatchPreviewLimitBoundary(t *testing.T) {
	prefix := "*** Begin Patch\n*** Add File: first.txt\n" + strings.Repeat("+line\n", maxRenderedPatchLines)
	for _, tc := range []struct {
		name, suffix string
		truncated    bool
	}{
		{"exact limit", "*** End Patch\n", false},
		{"empty omitted line", "\n*** End Patch\n", true},
		{"later file omitted", "*** Add File: second.txt\n+content\n*** End Patch\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := parseApplyPatchView(prefix + tc.suffix)
			if got != tc.truncated {
				t.Fatalf("truncated = %t, want %t", got, tc.truncated)
			}
		})
	}
}

func TestApplyPatchRealResultDoesNotRepeatDiff(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Add File: b.txt\n+created\n*** End Patch\n"
	args, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&agenttool.ApplyPatchTool{Root: root}).Execute(context.Background(), "call", args, nil)
	if err != nil || result.IsError {
		t.Fatalf("apply_patch failed: %+v, %v", result, err)
	}
	output := agentcore.ContentToText(result.Content)
	details, ok := result.Details.(map[string]any)
	if !ok || !strings.Contains(output, "--- a/a.txt") {
		t.Fatalf("test needs real unified output and metadata: %q, %#v", output, result.Details)
	}
	for _, metadata := range []map[string]any{details, nil} {
		displayed := applyPatchDisplayOutput(output, metadata)
		if strings.Contains(displayed, "--- a/") || !strings.Contains(displayed, "Applied 2 change(s):") || !strings.Contains(displayed, "  U a.txt") || !strings.Contains(displayed, "  A b.txt") {
			t.Fatalf("unexpected display output with metadata %v: %q", metadata != nil, displayed)
		}
	}
	if agentcore.ContentToText(result.Content) != output {
		t.Fatal("display transformation modified the tool result")
	}
}

func TestApplyPatchDeletionKeepsRemovedContent(t *testing.T) {
	patch := "*** Begin Patch\n*** Delete File: deleted.txt\n*** End Patch\n"
	diff := "--- a/deleted.txt\n+++ b/deleted.txt\n@@ -1,1 +0,0 @@\n-removed content\n"
	a := testApp()
	a.items = []appsession.Item{{
		ID: "delete", Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{Name: "apply_patch", Input: map[string]any{"patch": patch},
			Output: "Applied 1 change(s):\n  D deleted.txt\n" + diff, Details: map[string]any{"diff": diff}},
	}}
	a.index["delete"] = 0
	a.toolOpen["delete"] = true
	texts := strings.Join(ui.NewTester(a.view, 1200, 800).Texts(), "\n")
	if !strings.Contains(texts, "Deleted file contents are shown in the tool result below.") || !strings.Contains(texts, "-removed content") {
		t.Fatalf("deleted contents were hidden: %q", texts)
	}
}

func TestApplyPatchFailureAndUnrenderedInputKeepOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   map[string]any
		failed  bool
		running bool
	}{
		{"failed patch", map[string]any{"patch": sampleApplyPatch}, true, false},
		{"missing patch input", nil, false, false},
		{"running patch", map[string]any{"patch": sampleApplyPatch}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.items = []appsession.Item{{
				ID: "patch", Kind: appsession.ItemTool, Done: true, Failed: tc.failed,
				Tool: &appsession.ToolState{Name: "apply_patch", Input: tc.input, Output: samplePatchSummary + "\n" + samplePatchDiff, Error: tc.failed, Running: tc.running},
			}}
			a.index["patch"] = 0
			a.toolOpen["patch"] = true
			texts := strings.Join(ui.NewTester(a.view, 1200, 800).Texts(), "\n")
			if !strings.Contains(texts, "--- a/internal/demo.go") {
				t.Fatalf("original output was hidden: %q", texts)
			}
		})
	}
}

func TestOtherToolUnifiedDiffIsNotSuppressed(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{{
		ID: "bash", Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{Name: "bash", Input: map[string]any{"command": "git diff"}, Output: samplePatchSummary + "\n" + samplePatchDiff},
	}}
	a.index["bash"] = 0
	a.toolOpen["bash"] = true
	texts := strings.Join(ui.NewTester(a.view, 1200, 800).Texts(), "\n")
	if !strings.Contains(texts, "--- a/internal/demo.go") {
		t.Fatalf("other tool's diff was hidden: %q", texts)
	}
}

func TestFailedToolUsesCompactFailurePanel(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{{
		ID: "failed", Kind: appsession.ItemTool, Done: true, Failed: true,
		Tool: &appsession.ToolState{Name: "bash", Input: map[string]any{"command": "go test ./..."}, Output: "exit status 1", Error: true},
	}}
	a.index["failed"] = 0
	a.toolOpen["failed"] = true
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"bash · go test ./... · failed", "Command failed", "exit status 1"} {
		if !tt.HasText(want) {
			t.Fatalf("failure card is missing %q; texts=%q", want, tt.Texts())
		}
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "fatal diagnostics withheld") {
		t.Fatal("failure card replaced the actual bounded output")
	}
}
