package agenttool

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/patch"
)

func TestApplyPatchFileLimitRejectsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	tool := &ApplyPatchTool{Root: dir}
	var b strings.Builder
	b.WriteString("*** Begin Patch\n")
	for i := 0; i <= applyPatchMaxFiles; i++ {
		fmt.Fprintf(&b, "*** Add File: f%04d.txt\n+x\n", i)
	}
	b.WriteString("*** End Patch\n")
	res := runPatch(t, tool, b.String())
	if !res.IsError || !strings.Contains(resultText(res), fmt.Sprintf("over the %d-file limit", applyPatchMaxFiles)) {
		t.Fatalf("result = %q, want a file-count refusal", resultText(res))
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("refused patch wrote files: entries=%v err=%v", entries, err)
	}
}

func TestApplyPatchBudgetEstimateAndSerialization(t *testing.T) {
	changes := []patch.Change{
		{Kind: patch.ChangeUpdate, Path: "/a", OldContent: strings.Repeat("o", 10), NewContent: strings.Repeat("n", 20)},
		{Kind: patch.ChangeAdd, Path: "/b", NewContent: strings.Repeat("x", 30)},
	}
	b := estimatePatchBudget("p", changes)
	if b.files != 2 || b.snapshot != 10 || b.total <= b.snapshot {
		t.Fatalf("estimate = %+v", b)
	}
	if msg := patchBudgetError(b); !strings.Contains(msg, "working budget") || !strings.Contains(msg, "nothing was written") {
		t.Fatalf("budget error = %q", msg)
	}

	if err := acquireApplyPatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := acquireApplyPatch(ctx); err == nil {
		releaseApplyPatch()
		t.Fatal("a second patch acquired the process-wide slot")
	}
	releaseApplyPatch()
	if err := acquireApplyPatch(context.Background()); err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
	releaseApplyPatch()
}
