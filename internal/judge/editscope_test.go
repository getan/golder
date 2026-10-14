package judge

// Tests for the one fact about a patch its contents do not reveal: whether the
// edit's effect outlives the call. This was the exception list of a
// routine-edit fast path (ordinary in-workspace patches skipped the reviewer,
// these kept it); the fast path is gone, so what is tested here is the fact the
// reviewer's state now receives, not an exemption.
//
// Every case is written as a patch, the same way the model sends it, so the
// path extraction is exercised rather than assumed.

import (
	"encoding/json"
	"fmt"
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

// TestOutlivingPathsReportsTheGitDirectoryAndTheInstructionFile pins the fact
// itself, including the spellings a patch can produce: a hook runs on the next
// git command, and AGENTS.md is injected into every later session's prompt, so
// both outlive the call that wrote them.
func TestOutlivingPathsReportsTheGitDirectoryAndTheInstructionFile(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
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
		// Case folding: macOS resolves these to the same files, so a table that
		// only matched the canonical spellings would be one keystroke from useless.
		{".GIT/hooks/pre-commit", "case-insensitive filesystems resolve it"},
		{"agents.md", "case-insensitive filesystems resolve it"},
	}
	for _, c := range cases {
		got := OutlivingPaths("apply_patch", patchCall(c.path).Arguments)
		if len(got) != 1 || got[0] != c.path {
			t.Errorf("OutlivingPaths(%q) = %v, want it reported (%s)", c.path, got, c.why)
		}
	}
}

// TestOutlivingPathsIsSilentForOrdinaryEdits is the other half, and the reason
// the fact is worth reporting at all: a source file, a test, a document and CI
// config are edits whose effect stays inside the edit. A name that merely
// contains the protected words is not protected either — a false positive costs
// the reviewer's attention, which is the thing this distinction exists to
// spend wisely.
func TestOutlivingPathsIsSilentForOrdinaryEdits(t *testing.T) {
	routine := []string{
		"internal/main.go",
		"README.md",
		"docs/notes.md",
		".github/workflows/ci.yml",
		"internal/deep/nested/file.go",
		"internal/gitconfig.go",
		"docs/git/hooks.md",
		"AGENTS.md.bak",
		"docs/AGENTS.md.txt",
		"my.gitignore",
		".gitignore",
	}
	for _, p := range routine {
		if got := OutlivingPaths("apply_patch", patchCall(p).Arguments); len(got) != 0 {
			t.Errorf("OutlivingPaths(%q) = %v, want none", p, got)
		}
	}
}

// TestOutlivingPathsIgnoresOtherTools keeps the fact about one tool: a shell
// command's effects are the reviewer's ordinary question (what does it do?),
// and reporting "outliving paths" for it would be a fact about a patch that the
// call is not.
func TestOutlivingPathsIgnoresOtherTools(t *testing.T) {
	args, err := json.Marshal(map[string]string{"command": "git commit -m x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := OutlivingPaths("bash", args); len(got) != 0 {
		t.Errorf("OutlivingPaths(bash) = %v, want none", got)
	}
}

// TestProtectedEditPath pins the predicate on its own, including an unreadable
// path: one that cannot be resolved is reported as outliving the call, which is
// the safe direction — it costs the reviewer's attention rather than hiding a
// hook.
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
	// An empty path cannot be resolved, so it is reported as outliving the call.
	if !protectedEditPath("") || !protectedEditPath("   ") {
		t.Error("an unreadable path must be reported rather than assumed harmless")
	}
}
