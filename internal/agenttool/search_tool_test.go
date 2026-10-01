package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func runSearch(t *testing.T, tool agentcore.AgentTool, args map[string]any) agentcore.AgentToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, gerr := tool.Execute(context.Background(), "call-1", raw, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res
}

// requireRG skips a test when ripgrep is not on PATH. grep and find delegate to
// rg by design (there is no in-process fallback), so their search behavior is
// only exercisable where rg exists.
func requireRG(t *testing.T) {
	t.Helper()
	if _, err := rgLookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) not found on PATH")
	}
}

// seedTree writes a small directory tree with a .gitignore for the search tests.
func seedTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mustWrite("main.go", "package main\nfunc main() { hello() }\n")
	mustWrite("util.go", "package main\nfunc hello() {}\n")
	mustWrite("README.md", "# project\nhello world\n")
	mustWrite("sub/deep.go", "package sub\n// hello from sub\n")
	mustWrite("build/generated.go", "package build\nfunc hello() {}\n")
	mustWrite(".gitignore", "build/\n*.log\n")
	mustWrite("debug.log", "hello log line\n")
	return dir
}

func TestGrepBasic(t *testing.T) {
	requireRG(t)
	dir := seedTree(t)
	tool := &GrepTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"pattern": "hello"})
	txt := resultText(res)
	// Matches in tracked files.
	if !strings.Contains(txt, "main.go") || !strings.Contains(txt, "util.go") {
		t.Errorf("expected go file matches, got %q", txt)
	}
	// .gitignore'd paths must be skipped.
	if strings.Contains(txt, "build/generated.go") {
		t.Errorf("ignored dir should be skipped: %q", txt)
	}
	if strings.Contains(txt, "debug.log") {
		t.Errorf("ignored *.log should be skipped: %q", txt)
	}
}

func TestGrepGlobFilter(t *testing.T) {
	requireRG(t)
	dir := seedTree(t)
	tool := &GrepTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"pattern": "hello", "glob": "*.md"})
	txt := resultText(res)
	if !strings.Contains(txt, "README.md") {
		t.Errorf("expected README match, got %q", txt)
	}
	if strings.Contains(txt, ".go") {
		t.Errorf("glob *.md should exclude .go files: %q", txt)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	dir := seedTree(t)
	tool := &GrepTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"pattern": "["})
	if !strings.Contains(resultText(res), "invalid pattern") {
		t.Errorf("expected invalid-pattern error, got %q", resultText(res))
	}
}

func TestFindGlob(t *testing.T) {
	requireRG(t)
	dir := seedTree(t)
	tool := &FindTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"glob": "*.go"})
	txt := resultText(res)
	if !strings.Contains(txt, "main.go") || !strings.Contains(txt, "sub/deep.go") {
		t.Errorf("expected go files, got %q", txt)
	}
	if strings.Contains(txt, "build/generated.go") {
		t.Errorf("ignored dir should be skipped: %q", txt)
	}
	if strings.Contains(txt, "README.md") {
		t.Errorf("*.go should not match README.md: %q", txt)
	}
}

func TestLsDistinguishesFilesAndDirs(t *testing.T) {
	dir := seedTree(t)
	tool := &LsTool{Root: dir}
	res := runSearch(t, tool, map[string]any{})
	txt := resultText(res)
	// Directories carry a trailing slash.
	if !strings.Contains(txt, "sub/") {
		t.Errorf("expected sub/ dir marker, got %q", txt)
	}
	if !strings.Contains(txt, "main.go") {
		t.Errorf("expected main.go file, got %q", txt)
	}
	details, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details missing: %+v", res.Details)
	}
	if details["files"] == nil || details["dirs"] == nil {
		t.Errorf("expected file/dir counts, got %+v", details)
	}
}

func TestLsNotADirectory(t *testing.T) {
	dir := seedTree(t)
	tool := &LsTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"path": "main.go"})
	if !strings.Contains(resultText(res), "not a directory") {
		t.Errorf("expected not-a-directory error, got %q", resultText(res))
	}
}

func TestLsMissing(t *testing.T) {
	dir := seedTree(t)
	tool := &LsTool{Root: dir}
	res := runSearch(t, tool, map[string]any{"path": "nope"})
	if !strings.Contains(resultText(res), "does not exist") {
		t.Errorf("expected does-not-exist error, got %q", resultText(res))
	}
}

func TestSearchPathTraversal(t *testing.T) {
	dir := seedTree(t)
	for _, tc := range []struct {
		name string
		tool agentcore.AgentTool
		args map[string]any
	}{
		{"grep", &GrepTool{Root: dir}, map[string]any{"pattern": "x", "path": "../"}},
		{"find", &FindTool{Root: dir}, map[string]any{"glob": "*", "path": "../"}},
		{"ls", &LsTool{Root: dir}, map[string]any{"path": "../"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runSearch(t, tc.tool, tc.args)
			if !strings.Contains(resultText(res), "outside the workspace root") {
				t.Errorf("expected boundary error, got %q", resultText(res))
			}
		})
	}
}

func TestSearchToolModes(t *testing.T) {
	for _, tool := range []agentcore.AgentTool{&GrepTool{}, &FindTool{}, &LsTool{}} {
		if tool.ExecutionMode() != agentcore.ToolExecutionParallel {
			t.Errorf("%s should be parallel", tool.Name())
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
			t.Errorf("%s schema not valid JSON: %v", tool.Name(), err)
		}
	}
}

// TestGrepGitignoreNegation covers the .gitignore semantics end-to-end through
// ripgrep (the matcher itself now lives in rg): a later "!keep.txt" re-includes
// a file its ancestor "*.txt" rule ignored. It also pins the flag that makes
// this work outside a git checkout — t.TempDir() is not a repository.
func TestGrepGitignoreNegation(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	for name, content := range map[string]string{
		".gitignore": "*.txt\n!keep.txt\n",
		"drop.txt":   "hello drop\n",
		"keep.txt":   "hello keep\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "hello"})
	txt := resultText(res)
	if !strings.Contains(txt, "keep.txt") {
		t.Errorf("negated rule should re-include keep.txt, got %q", txt)
	}
	if strings.Contains(txt, "drop.txt") {
		t.Errorf("*.txt should stay ignored, got %q", txt)
	}
}

// TestGrepSingleFilePath keeps the file-path form covered: path may point at a
// file, not just a directory.
func TestGrepSingleFilePath(t *testing.T) {
	requireRG(t)
	dir := seedTree(t)
	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "hello", "path": "main.go"})
	txt := resultText(res)
	if !strings.Contains(txt, "main.go:2:") {
		t.Errorf("expected a main.go:2 match, got %q", txt)
	}
	if strings.Contains(txt, "util.go") {
		t.Errorf("path-scoped search must not touch util.go: %q", txt)
	}
}

// TestGrepTruncatesAtMaxMatches pins the result cap: a pattern matching more
// lines than searchMaxResults yields exactly that many matches plus the
// truncation marker.
func TestGrepTruncatesAtMaxMatches(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < searchMaxResults+1; i++ {
		b.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "needle"})
	txt := resultText(res)
	if !strings.Contains(txt, fmt.Sprintf("[truncated at %d matches]", searchMaxResults)) {
		t.Errorf("expected truncation marker, got %q", txt)
	}
	details, _ := res.Details.(map[string]any)
	if got := details["matches"]; got != searchMaxResults {
		t.Errorf("matches detail = %v, want %d", got, searchMaxResults)
	}
}

// TestSearchWithoutRG verifies the delegation is honest about its dependency:
// with no rg on PATH the tools fail with an actionable install hint instead of
// silently returning nothing.
func TestSearchWithoutRG(t *testing.T) {
	orig := rgLookPath
	rgLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { rgLookPath = orig })

	for _, tc := range []struct {
		name string
		tool agentcore.AgentTool
		args map[string]any
	}{
		{"grep", &GrepTool{Root: t.TempDir()}, map[string]any{"pattern": "x"}},
		{"find", &FindTool{Root: t.TempDir()}, map[string]any{"glob": "*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runSearch(t, tc.tool, tc.args)
			if !strings.Contains(resultText(res), "ripgrep") {
				t.Errorf("expected an install hint mentioning ripgrep, got %q", resultText(res))
			}
		})
	}
}
