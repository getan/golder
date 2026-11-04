package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
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
