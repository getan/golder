package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// editResult builds the tool-result message shape the edit tool produces, with
// the diff both embedded in the text and reported in Details.
func editResult(text, diff string) agentcore.ToolResultMessage {
	return agentcore.ToolResultMessage{
		ToolName: "edit",
		Content:  agentcore.ContentList{agentcore.NewTextContent(text)},
		Details:  map[string]any{"path": "f.txt", "replacements": 1, "diff": diff},
	}
}

// TestDiffFromDetails verifies extraction of the diff from tool Details
// metadata, including the shapes that carry no diff.
func TestDiffFromDetails(t *testing.T) {
	if diff, ok := DiffFromDetails(map[string]any{"diff": "+++ x"}); !ok || diff != "+++ x" {
		t.Errorf("got (%q,%v), want (%q,true)", diff, ok, "+++ x")
	}
	for _, details := range []any{
		nil,
		"diff",
		123,
		[]any{"diff"},
		map[string]any{},
		map[string]any{"diff": ""},
		map[string]any{"diff": 42},
	} {
		if diff, ok := DiffFromDetails(details); ok || diff != "" {
			t.Errorf("DiffFromDetails(%#v) = (%q,%v), want empty/false", details, diff, ok)
		}
	}
}

// TestRenderDiffLine verifies the per-line color mapping: headers, hunk
// markers, removals, additions, context, and pass-through text.
func TestRenderDiffLine(t *testing.T) {
	cases := []struct {
		line string
		code string
	}{
		{"--- a/f.txt", Dim},
		{"+++ b/f.txt", Dim},
		{"@@ -1,3 +1,3 @@", Cyan},
		{"-removed", Red},
		{"+added", Green},
		{" unchanged", Dim},
		{"… (+3 lines elided)", Dim},
	}
	for _, tc := range cases {
		if got := RenderDiffLine(tc.line, true); got != tc.code+tc.line+Reset {
			t.Errorf("RenderDiffLine(%q) = %q, want %q", tc.line, got, tc.code+tc.line+Reset)
		}
	}
	// Plain text passes through uncolored, and color can be gated off.
	for _, line := range []string{"no marker", "+added", "-removed"} {
		if got := RenderDiffLine(line, false); got != line {
			t.Errorf("RenderDiffLine(%q, false) = %q, want unchanged", line, got)
		}
	}
}

// TestDiffPreviewLines verifies the cap: short diffs pass through whole, long
// diffs are cut to MaxDiffPreviewLines with an elision notice.
func TestDiffPreviewLines(t *testing.T) {
	short := "--- a/f\n+++ b/f\n@@ -1,1 +1,1 @@\n-x\n+y\n"
	if got := DiffPreviewLines(short); len(got) != 5 {
		t.Errorf("short diff line count = %d, want 5", len(got))
	}

	var b strings.Builder
	for i := 0; i < MaxDiffPreviewLines+10; i++ {
		b.WriteString("+line\n")
	}
	got := DiffPreviewLines(b.String())
	if len(got) != MaxDiffPreviewLines+1 {
		t.Fatalf("long diff line count = %d, want %d+1", len(got), MaxDiffPreviewLines)
	}
	notice := got[MaxDiffPreviewLines]
	if !strings.Contains(notice, "10 lines elided") {
		t.Errorf("elision notice = %q, want mention of 10 hidden lines", notice)
	}
	// The visible lines must be the leading ones, not a tail.
	if got[0] != "+line" || got[MaxDiffPreviewLines-1] != "+line" {
		t.Errorf("visible diff lines were not the leading ones: %q…", got[0])
	}
}

// TestRenderToolResultDiff verifies that a result reporting a diff renders its
// summary line plus the full diff (not the collapsed one-liner), and that
// errors without a diff keep the compact rendering. NO_COLOR pins the output
// plain so the assertions hold regardless of the test runner's terminal (the
// colors themselves are covered by TestRenderDiffLine).
func TestRenderToolResultDiff(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n"
	tr := editResult("Edited f.txt (1 replacement(s))\n"+diff, diff)

	var out bytes.Buffer
	RenderToolResult(&out, tr)
	got := out.String()

	for _, want := range []string{
		"← result: Edited f.txt (1 replacement(s))",
		"    @@ -1,2 +1,2 @@",
		"     alpha",
		"    -beta",
		"    +BETA",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered result missing %q\n%s", want, got)
		}
	}
	// The summary line must not carry the collapsed "…" marker.
	if strings.Contains(got, " …\n") {
		t.Errorf("diff result should not be collapsed to one line\n%s", got)
	}

	// An error result (no diff) keeps the compact one-line rendering.
	err := agentcore.ToolResultMessage{
		ToolName: "edit",
		Content:  agentcore.ContentList{agentcore.NewTextContent("edit: old_string not found in \"f.txt\"")},
		IsError:  true,
	}
	out.Reset()
	RenderToolResult(&out, err)
	if !strings.Contains(out.String(), "← error:") || !strings.Contains(out.String(), "not found") {
		t.Errorf("error result should render compactly\n%s", out.String())
	}
}

// TestRenderToolResultDiffElided verifies that an over-long diff is capped at
// MaxDiffPreviewLines (headers included) with an elision notice instead of
// scrolling pages of diff through the REPL.
func TestRenderToolResultDiffElided(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	const added = 55
	var b strings.Builder
	b.WriteString("--- a/f.txt\n+++ b/f.txt\n")
	for i := 0; i < added; i++ {
		b.WriteString("+line\n")
	}
	tr := editResult("Edited f.txt (99 replacement(s))\n"+b.String(), b.String())

	var out bytes.Buffer
	RenderToolResult(&out, tr)
	got := out.String()

	// 2 header lines count against the cap, so 48 of the 55 additions show.
	if got == "" || strings.Count(got, "+line\n") != MaxDiffPreviewLines-2 {
		t.Errorf("diff should show %d additions, got %d\n%s",
			MaxDiffPreviewLines-2, strings.Count(got, "+line\n"), got)
	}
	if !strings.Contains(got, "… (+7 lines elided)") {
		t.Errorf("missing elision notice\n%s", got)
	}
}
