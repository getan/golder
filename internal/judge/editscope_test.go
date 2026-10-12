package judge

// Tests for the routine-edit fast path: which patches skip the reviewer, and the
// two classes that must not.
//
// The negative half is the point of the file. A patch to an ordinary source file
// is the routine case and pays nothing; a patch to the git directory or the
// instruction file is an edit whose EFFECT outlives the call — planted execution
// on the next git command, or instructions injected into every later session —
// so it keeps its review. Every case is written as a patch, the same way the
// model sends it, so the path extraction is exercised rather than assumed.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// patchCall builds an apply_patch call whose patch touches the given paths.
func patchCall(paths ...string) agentcore.AgentToolCall {
	var b strings.Builder
	b.WriteString("*** Begin Patch\n")
	for i, p := range paths {
		fmt.Fprintf(&b, "*** Add File: %s\n+content %d\n", p, i)
	}
	b.WriteString("*** End Patch\n")
	raw, err := json.Marshal(map[string]string{"patch": b.String()})
	if err != nil {
		panic(err)
	}
	return agentcore.AgentToolCall{Name: "apply_patch", Arguments: raw}
}

func TestApplyPatchFastPathSkipsTheReviewer(t *testing.T) {
	root := t.TempDir()
	routine := []string{
		"internal/main.go",
		"README.md",
		"docs/notes.md",
		".github/workflows/ci.yml", // CI config: no local effect, and not the git dir
		"internal/deep/nested/file.go",
		// Absolute spellings are how most real patches arrive (measured: about
		// half the paths in ten sessions), so both must pass.
		filepath.Join(root, "internal/abs.go"),
		// A name that merely contains the protected words is not protected.
		"internal/gitconfig.go",
		"docs/git/hooks.md",
		"AGENTS.md.bak",
	}
	for _, p := range routine {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy, WorkspaceRoot: root}.Classify(context.Background(), "apply_patch", patchCall(p).Arguments)
		if spy.calls != 0 {
			t.Errorf("patch to %q reached the reviewer %d times, want 0", p, spy.calls)
		}
		if v.Level != Allow || v.Source != "workspace-edit" {
			t.Errorf("patch to %q verdict = %+v, want the workspace-edit fast path", p, v)
		}
	}
}

func TestApplyPatchFastPathKeepsTheReviewForProtectedPaths(t *testing.T) {
	root := t.TempDir()
	protected := []struct {
		path, why string
	}{
		{".git/hooks/pre-commit", "a hook executes on the next git command"},
		{".git/config", "core.hooksPath and credential helpers live here"},
		{".git", "the directory itself"},
		{"sub/.git/hooks/post-checkout", "a nested repository is the same door"},
		{filepath.Join(root, ".git/hooks/pre-push"), "absolute spelling of the same"},
		{"AGENTS.md", "read into the system prompt of every later session"},
		{"sub/AGENTS.md", "same, for a session started in that directory"},
		{filepath.Join(root, "AGENTS.md"), "absolute spelling of the same"},
		// Case folding: macOS resolves these to the same files, so a guard that
		// only matched the canonical spellings would be one keystroke away from
		// useless.
		{".GIT/hooks/pre-commit", "case-insensitive filesystems resolve it"},
		{"agents.md", "case-insensitive filesystems resolve it"},
	}
	for _, c := range protected {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy, WorkspaceRoot: root}.Classify(context.Background(), "apply_patch", patchCall(c.path).Arguments)
		if spy.calls != 1 {
			t.Errorf("patch to %q reached the reviewer %d times, want 1 (%s)", c.path, spy.calls, c.why)
		}
		if v.Source != "llm" {
			t.Errorf("patch to %q verdict = %+v, want the reviewer's", c.path, v)
		}
	}
}

// TestApplyPatchFastPathKeepsTheReviewOutsideTheWorkspace covers the boundary the
// exemption must not cross: the tool admits one extra root (the skills directory,
// whose descriptions are injected into later prompts), so a patch naming anything
// outside the workspace keeps its review rather than riding the exemption.
func TestApplyPatchFastPathKeepsTheReviewOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		filepath.Join(outside, "x.go"), // absolute, elsewhere
		"../escape.go",                 // relative, upward
		"sub/../../escape.go",          // same, spelled inside
	}
	for _, p := range cases {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		Chain{Inner: spy, WorkspaceRoot: root}.Classify(context.Background(), "apply_patch", patchCall(p).Arguments)
		if spy.calls != 1 {
			t.Errorf("patch to %q reached the reviewer %d times, want 1: it leaves the workspace", p, spy.calls)
		}
	}

	// One path inside and one outside: the patch as a whole keeps its review,
	// because the exemption is all-or-nothing per call.
	spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
	call := patchCall("internal/ok.go", filepath.Join(outside, "x.go"))
	Chain{Inner: spy, WorkspaceRoot: root}.Classify(context.Background(), "apply_patch", call.Arguments)
	if spy.calls != 1 {
		t.Errorf("a mixed patch reached the reviewer %d times, want 1", spy.calls)
	}
}

// TestApplyPatchFastPathNeedsAWorkspaceRoot pins the safe default: with no root
// configured the exemption is off, so a caller that forgot to wire one gets the
// reviewer rather than an unbounded pass.
func TestApplyPatchFastPathNeedsAWorkspaceRoot(t *testing.T) {
	spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
	Chain{Inner: spy}.Classify(context.Background(), "apply_patch", patchCall("internal/main.go").Arguments)
	if spy.calls != 1 {
		t.Fatalf("with no workspace root the reviewer ran %d times, want 1", spy.calls)
	}
}

// TestApplyPatchFastPathKeepsTheFloorFirst keeps the ordering property: a patch
// naming credential material is denied by the static floor, which runs before the
// exemption — so the fast path can never be the thing that waves it through.
func TestApplyPatchFastPathKeepsTheFloorFirst(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"~/.ssh/authorized_keys", ".ssh/config", ".golder/trust.json"} {
		spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
		v := Chain{Inner: spy, WorkspaceRoot: root}.Classify(context.Background(), "apply_patch", patchCall(p).Arguments)
		if v.Level != Deny {
			t.Errorf("patch to %q verdict = %+v, want Deny from the floor", p, v)
		}
		if spy.calls != 0 {
			t.Errorf("patch to %q reached the reviewer; the floor owns it", p)
		}
	}
}

// TestApplyPatchFastPathRespectsTheKillSwitch keeps the escape hatch working for
// this exemption too: an operator who distrusts the tables can send everything
// back to the reviewer with one env var.
func TestApplyPatchFastPathRespectsTheKillSwitch(t *testing.T) {
	t.Setenv("GOLDER_READONLY_FASTPATH", "off")
	root := t.TempDir()
	spy := &reviewerSpy{v: Verdict{Level: Allow, Source: "llm"}}
	Chain{Inner: spy, WorkspaceRoot: root}.Classify(context.Background(), "apply_patch", patchCall("main.go").Arguments)
	if spy.calls != 1 {
		t.Errorf("with the fast path off, a patch reached the reviewer %d times, want 1", spy.calls)
	}
}

// TestProtectedEditPath pins the predicate on its own, including the spellings a
// patch can produce and the lookalikes that must NOT be protected (a false
// positive is only a review, but a table of cases documents the intent).
func TestProtectedEditPath(t *testing.T) {
	protected := []string{
		".git", ".git/config", ".git/hooks/pre-commit", "sub/.git/config",
		"/abs/.git/hooks/x", "./.git/config", ".git/", "a/b/.git/c",
		".GIT/hooks/x", "AGENTS.md", "sub/AGENTS.md", "/abs/AGENTS.md",
		"./AGENTS.md", "agents.md", "Agents.MD", "sub/agents.md",
	}
	for _, p := range protected {
		if !protectedEditPath(p) {
			t.Errorf("protectedEditPath(%q) = false, want true", p)
		}
	}
	routine := []string{
		"main.go", "internal/git/status.go", "gitconfig.go",
		"AGENTS.md.bak", "docs/AGENTS.md.txt", "docs/git-hooks.md",
		"internal/.github/workflows/ci.yml", ".github/workflows/ci.yml",
		"my.gitignore", ".gitignore",
	}
	for _, p := range routine {
		if protectedEditPath(p) {
			t.Errorf("protectedEditPath(%q) = true, want false", p)
		}
	}
	// An empty path cannot be resolved, so it keeps the review.
	if !protectedEditPath("") || !protectedEditPath("   ") {
		t.Error("an unreadable path must keep its review")
	}
}
