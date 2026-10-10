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

	"github.com/getan/golder/internal/agentcore"
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
	if !strings.Contains(txt, fmt.Sprintf("[showing the first %d matches;", searchMaxResults)) {
		t.Errorf("expected truncation marker, got %q", txt)
	}
	if !strings.Contains(txt, "raise limit") {
		t.Errorf("truncation marker should name how to see more, got %q", txt)
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

// TestGrepContext covers the argument that keeps "show me the code around this
// hit" out of a shell: a context search returns the match line with ':' and the
// neighbouring lines with '-', and reports the number of MATCHES rather than
// the number of printed lines.
func TestGrepContext(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	content := "first\nsecond\nTARGET\nfourth\nfifth\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "TARGET", "context": 1})
	txt := resultText(res)
	for _, want := range []string{"a.txt:3:TARGET", "a.txt-2-second", "a.txt-4-fourth"} {
		if !strings.Contains(txt, want) {
			t.Errorf("context result missing %q: %q", want, txt)
		}
	}
	if !strings.Contains(txt, `1 match(es) for "TARGET" (with 1 line(s) of context)`) {
		t.Errorf("unexpected headline: %q", txt)
	}
	details, _ := res.Details.(map[string]any)
	if got := details["matches"]; got != 1 {
		t.Errorf("matches detail = %v, want 1 (matches, not printed lines)", got)
	}
	if got := details["context"]; got != 1 {
		t.Errorf("context detail = %v, want 1", got)
	}
}

// TestGrepContextWithoutFlagKeepsOutput pins the no-context path unchanged:
// no rewrite, no suffix, and one match per printed line.
func TestGrepContextWithoutFlagKeepsOutput(t *testing.T) {
	requireRG(t)
	dir := seedTree(t)
	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "hello"})
	txt := resultText(res)
	if strings.Contains(txt, "line(s) of context") {
		t.Errorf("no-context search must not mention context: %q", txt)
	}
	if strings.Contains(txt, "main.go-") {
		t.Errorf("no-context search must not print context lines: %q", txt)
	}
	details, _ := res.Details.(map[string]any)
	if _, ok := details["context"]; ok {
		t.Errorf("context detail must be absent without the flag: %v", details)
	}
}

// TestGrepContextOutOfRange pins the argument bounds: a context outside
// [0, grepMaxContext] is refused with a clear message instead of being passed
// to ripgrep.
func TestGrepContextOutOfRange(t *testing.T) {
	dir := seedTree(t)
	for _, c := range []int{-1, grepMaxContext + 1} {
		res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "hello", "context": c})
		if !strings.Contains(resultText(res), "context must be between") {
			t.Errorf("context %d: want a range error, got %q", c, resultText(res))
		}
	}
}

// TestGrepResultLinesNullForm is the unit test for the ambiguity the --null
// capture exists to remove: with plain output, `x-1-3-2-y` reads as a context
// line 1 of file `x-1` or as one of file `x`, and nothing in the line decides
// it. With the NUL terminator the path ends where it says it does.
func TestGrepResultLinesNullForm(t *testing.T) {
	lines := []string{
		"x-1\x003-2-y",     // context line 3 of x-1 whose text starts "2-y"
		"a-b.go\x0012:hit", // match on line 12 of a-b.go
		"--",
		"plain\x004:match",
	}
	got := grepResultLines(lines, 1)
	want := []struct {
		text  string
		match bool
	}{
		{"x-1-3-2-y", false},
		{"a-b.go:12:hit", true},
		{"--", false},
		{"plain:4:match", true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].text != w.text || got[i].match != w.match {
			t.Errorf("line %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestCapGrepLinesCutsOnMatchBoundaries pins the cap under context: the cut
// lands before the (max+1)-th match, the trailing context of the last kept
// match survives, and a group separator left dangling by the cut is dropped.
func TestCapGrepLinesCutsOnMatchBoundaries(t *testing.T) {
	lines := grepResultLines([]string{
		"f\x001:TARGET",
		"f\x002-tail",
		"--",
		"f\x005:TARGET",
		"f\x006-tail",
		"--",
		"f\x009:TARGET",
	}, 1)
	kept, matches, truncated := capGrepLines(lines, 2)
	if !truncated {
		t.Error("a third match exists, so the result must be marked truncated")
	}
	if matches != 2 {
		t.Errorf("matches = %d, want 2", matches)
	}
	var got []string
	for _, ln := range kept {
		got = append(got, ln.text)
	}
	want := []string{"f:1:TARGET", "f-2-tail", "--", "f:5:TARGET", "f-6-tail"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("kept:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestGrepContextPassesRegistryValidation guards the seam the argument has to
// cross before it reaches Execute: the executor validates every call against
// the tool's schema, and the schema sets additionalProperties=false, so a
// context argument the schema did not declare would be rejected before the
// tool ever saw it — the feature would be dead on arrival with tests on
// grepResultLines still passing.
func TestGrepContextPassesRegistryValidation(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(&GrepTool{}); err != nil {
		t.Fatalf("register grep: %v", err)
	}
	valid := map[string]any{
		"pattern": "x",
		"context": 3,
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("grep", raw); errs != nil {
		t.Errorf("context argument rejected by schema validation: %+v", errs)
	}
	// The declared bounds are enforced by the schema too, so an out-of-range
	// value never has to reach the tool's own check to be refused.
	over, err := json.Marshal(map[string]any{"pattern": "x", "context": grepMaxContext + 1})
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("grep", over); len(errs) == 0 {
		t.Errorf("context %d must be rejected by the schema", grepMaxContext+1)
	}
}

// seedGrepTree writes a small tree for the flag tests: a match in two cases,
// a word-boundary neighbour, a file with a literal-matching name, an ignored
// build product, a hidden file, and VCS metadata.
func seedGrepTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("prose.md", "hello world\n")
	write("shout.md", "HELLO WORLD\n")
	write("code.go", "needle := 1\nhello_world()\n")
	write("quirk.go", "foo(bar) baz\n")
	write("notes.txt", "keep\n")
	write(".env", "needle=1\n")
	write(".git/config", "needle url\n")
	write("build/gen.go", "needle\n")
	write(".gitignore", "build/\n")
	return dir
}

// grepText runs grep with args against dir and returns the model-facing text.
func grepText(t *testing.T, dir string, args map[string]any) string {
	t.Helper()
	return resultText(runSearch(t, &GrepTool{Root: dir}, args))
}

// TestGrepFlags covers the eight structured flags, one behaviour each: they are
// whitelisted spellings of the ripgrep options worth having, chosen over a raw
// flag passthrough because rg's --pre executes commands and this tool runs
// without an LLM review (it is a read-only tool).
func TestGrepFlags(t *testing.T) {
	requireRG(t)
	dir := seedGrepTree(t)

	t.Run("ignore_case", func(t *testing.T) {
		if got := grepText(t, dir, map[string]any{"pattern": "hello", "glob": "*.md"}); strings.Contains(got, "shout.md") {
			t.Errorf("case-sensitive search must not match HELLO: %q", got)
		}
		got := grepText(t, dir, map[string]any{"pattern": "hello", "glob": "*.md", "ignore_case": true})
		if !strings.Contains(got, "prose.md") || !strings.Contains(got, "shout.md") {
			t.Errorf("ignore_case should match both files: %q", got)
		}
	})

	t.Run("word", func(t *testing.T) {
		// hello_world contains hello, but not as a whole word.
		if got := grepText(t, dir, map[string]any{"pattern": "hello", "glob": "*.go"}); !strings.Contains(got, "code.go") {
			t.Errorf("substring search should match hello_world: %q", got)
		}
		if got := grepText(t, dir, map[string]any{"pattern": "hello", "glob": "*.go", "word": true}); strings.Contains(got, "code.go") {
			t.Errorf("word search must not match hello_world: %q", got)
		}
	})

	t.Run("literal", func(t *testing.T) {
		// As a regexp, foo(bar) matches the text "foobar"; as a literal it
		// matches only the parentheses as typed.
		res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "foo(bar)", "literal": true})
		if got := resultText(res); !strings.Contains(got, "quirk.go") {
			t.Errorf("literal search should find foo(bar): %q", got)
		}
	})

	t.Run("invert", func(t *testing.T) {
		got := grepText(t, dir, map[string]any{"pattern": "hello", "path": "prose.md", "invert": true})
		if strings.Contains(got, "hello world") {
			t.Errorf("invert must drop matching lines: %q", got)
		}
	})

	t.Run("files_only", func(t *testing.T) {
		res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "hello", "files_only": true})
		got := resultText(res)
		// The search is case-sensitive, so shout.md (HELLO WORLD) is not a
		// match; code.go carries hello inside hello_world.
		if !strings.Contains(got, "prose.md") || !strings.Contains(got, "code.go") {
			t.Errorf("files_only should name the matching files: %q", got)
		}
		if strings.Contains(got, "shout.md") {
			t.Errorf("files_only must respect case sensitivity: %q", got)
		}
		if strings.Contains(got, "hello world") {
			t.Errorf("files_only must not print matching lines: %q", got)
		}
		details, _ := res.Details.(map[string]any)
		if details["files"] == nil {
			t.Errorf("files_only details should carry a file count: %v", details)
		}
	})

	t.Run("type", func(t *testing.T) {
		got := grepText(t, dir, map[string]any{"pattern": "needle", "type": "go"})
		if !strings.Contains(got, "code.go") {
			t.Errorf("type go should search .go files: %q", got)
		}
		if strings.Contains(got, "notes.txt") {
			t.Errorf("type go must not search .txt files: %q", got)
		}
		if strings.Contains(got, "gen.go") {
			t.Errorf("type go must still respect .gitignore: %q", got)
		}
	})

	t.Run("hidden_excludes_git", func(t *testing.T) {
		if got := grepText(t, dir, map[string]any{"pattern": "needle"}); strings.Contains(got, ".env") {
			t.Errorf("hidden files must be skipped by default: %q", got)
		}
		got := grepText(t, dir, map[string]any{"pattern": "needle", "hidden": true})
		if !strings.Contains(got, ".env") {
			t.Errorf("hidden=true should search .env: %q", got)
		}
		// The tree walk keeps .git out even when hidden files are in scope:
		// its object blobs and refs are noise in a content search. Hygiene,
		// not a boundary — an explicit path into .git is not subject to it.
		if strings.Contains(got, ".git/") {
			t.Errorf("hidden=true must still exclude .git: %q", got)
		}
	})

	t.Run("no_ignore", func(t *testing.T) {
		if got := grepText(t, dir, map[string]any{"pattern": "needle", "type": "go"}); strings.Contains(got, "gen.go") {
			t.Errorf("ignored files must be skipped by default: %q", got)
		}
		got := grepText(t, dir, map[string]any{"pattern": "needle", "type": "go", "no_ignore": true})
		if !strings.Contains(got, "gen.go") {
			t.Errorf("no_ignore=true should search the ignored file: %q", got)
		}
	})
}

// TestGrepPatternDialect pins the seam the compile fix moved: the pattern is
// graded by ripgrep, the engine that actually matches, instead of by Go's
// regexp. The two dialects disagree in both directions, so a Go pre-check got
// it wrong whichever way the pattern went.
func TestGrepPatternDialect(t *testing.T) {
	requireRG(t)
	dir := seedGrepTree(t)

	// Go compiles \Q...\E; the Rust regex crate rejects it. Before the fix the
	// pre-check passed and ripgrep failed anyway, with its multi-line dump.
	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": `\Qhello world\E`})
	if got := resultText(res); !strings.Contains(got, "invalid pattern") {
		t.Errorf("rg rejects \\Q..\\E, so the message should say invalid pattern: %q", got)
	}
	if got := resultText(res); strings.Contains(got, "\n  ") {
		t.Errorf("the multi-line parse dump must be collapsed: %q", got)
	}

	// rg's verbose mode is valid there and invalid in Go, so the old pre-check
	// refused a search ripgrep would have run.
	// In verbose mode the pattern's own whitespace is ignored, so the space
	// between the words has to be spelled \s to match the file's.
	res = runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": `(?x) hello \s world`, "glob": "prose.md"})
	if got := resultText(res); !strings.Contains(got, "prose.md") {
		t.Errorf("(?x) is valid ripgrep syntax and should search: %q", got)
	}
}

// TestRGErrorReason covers the two stderr shapes ripgrep uses on exit 2: the
// multi-line parse-error block (where the reason is on its own "error:" line)
// and the single-line "rg: <reason>" form an unknown --type or a missing path
// produces. The parse block is asserted in BOTH spellings, because the header
// line depends on the ripgrep version: 13 (Debian 12, Ubuntu 22.04) prints
// "regex parse error:", 14+ prefixes it with "rg: ". Keying the label off the
// prefixed line made a Debian 12 container report a bare "unrecognized escape
// sequence" where this machine's ripgrep 15 reported "invalid pattern: …".
func TestRGErrorReason(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "parse block, ripgrep 14+",
			stderr: "rg: regex parse error:\n    (?:\\Qx\\E)\n       ^^\nerror: unrecognized escape sequence\n",
			want:   "invalid pattern: unrecognized escape sequence",
		},
		{
			name:   "parse block, ripgrep 13",
			stderr: "regex parse error:\n    \\Qx\\E\n    ^^\nerror: unrecognized escape sequence\n",
			want:   "invalid pattern: unrecognized escape sequence",
		},
		{
			name:   "unclosed class, ripgrep 13",
			stderr: "regex parse error:\n    [\n    ^\nerror: unclosed character class\n",
			want:   "invalid pattern: unclosed character class",
		},
		{
			name:   "single line",
			stderr: "rg: unrecognized file type: nosuchtype\n",
			want:   "unrecognized file type: nosuchtype",
		},
		{
			name:   "empty",
			stderr: "",
			want:   "unknown error",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rgErrorReason(c.stderr); got != c.want {
				t.Errorf("rgErrorReason = %q, want %q", got, c.want)
			}
		})
	}
}

// TestGrepNewFlagsPassRegistryValidation guards the same seam the context
// argument crosses: the schema sets additionalProperties=false, so a field it
// does not declare is rejected before Execute runs.
func TestGrepNewFlagsPassRegistryValidation(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(&GrepTool{}); err != nil {
		t.Fatalf("register grep: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"pattern":     "x",
		"ignore_case": true,
		"word":        true,
		"literal":     true,
		"invert":      true,
		"files_only":  true,
		"type":        "go",
		"hidden":      true,
		"no_ignore":   true,
		"context":     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("grep", raw); errs != nil {
		t.Errorf("a documented grep field was rejected by schema validation: %+v", errs)
	}
}

// writeSearchTree writes rel→content pairs under dir for the argument tests.
func writeSearchTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// TestStringListUnmarshal pins the one-string-or-many decoder: the single form
// stays natural for one directory, the array form covers several, and neither a
// blank entry nor a null becomes a path.
func TestStringListUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single string", `"internal/cli"`, []string{"internal/cli"}},
		{"array", `["a","b"]`, []string{"a", "b"}},
		{"empty string", `""`, nil},
		{"null", `null`, nil},
		{"blanks dropped", `["a","","  "]`, []string{"a"}},
		{"empty array", `[]`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got stringList
			if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("unmarshal %s = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("unmarshal %s = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
	var bad stringList
	if err := json.Unmarshal([]byte(`{"path":"a"}`), &bad); err == nil {
		t.Error("an object must not decode as a path list")
	}
}

// TestGrepMultiPath: a path list searches every named tree in one call — what a
// "look in these three packages" query needs, and what a shell can only do with
// one rg per directory. The single-string form keeps working unchanged.
func TestGrepMultiPath(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	writeSearchTree(t, dir, map[string]string{
		"a/one.go":   "needle in a\n",
		"b/two.go":   "needle in b\n",
		"c/three.go": "needle in c\n",
	})
	got := grepText(t, dir, map[string]any{"pattern": "needle", "path": []string{"a", "b"}})
	if !strings.Contains(got, "a/one.go") || !strings.Contains(got, "b/two.go") {
		t.Errorf("path list should search both directories: %q", got)
	}
	if strings.Contains(got, "c/three.go") {
		t.Errorf("path list must not search the rest of the workspace: %q", got)
	}
	got = grepText(t, dir, map[string]any{"pattern": "needle", "path": "c"})
	if !strings.Contains(got, "c/three.go") || strings.Contains(got, "a/one.go") {
		t.Errorf("a single-string path should scope to one directory: %q", got)
	}
}

// TestGrepNestedPathsDoNotDuplicate pins the overlap case: ripgrep walks each
// path argument independently, so `rg needle -- a a/b` prints every match under
// a/b twice. The tool drops a start another start already covers, and treats a
// listed workspace root as covering everything under it.
func TestGrepNestedPathsDoNotDuplicate(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	writeSearchTree(t, dir, map[string]string{
		"a/one.go":   "needle\n",
		"a/b/two.go": "needle\n",
		"c/out.go":   "needle\n",
	})
	got := grepText(t, dir, map[string]any{"pattern": "needle", "path": []string{"a", "a/b"}})
	if n := strings.Count(got, "a/b/two.go"); n != 1 {
		t.Errorf("a nested path must not repeat matches (saw %d): %q", n, got)
	}
	if !strings.Contains(got, "a/one.go") {
		t.Errorf("the covering path must still be searched: %q", got)
	}
	if strings.Contains(got, "c/out.go") {
		t.Errorf("paths outside the list must not be searched: %q", got)
	}
	// A listed workspace root covers every other in-workspace path, and searching
	// it that way keeps output unprefixed (no "./").
	got = grepText(t, dir, map[string]any{"pattern": "needle", "path": []string{".", "a"}})
	for _, want := range []string{"a/one.go", "a/b/two.go", "c/out.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("a listed root should search the whole workspace, missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "./") {
		t.Errorf("a covering root should keep results unprefixed: %q", got)
	}
}

// TestGrepExclude: exclude is the negative half of glob (rg --glob=!X). It wins
// over a matching include glob, which is what makes "*.go minus the tests" one
// call instead of a shell pipeline.
func TestGrepExclude(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	writeSearchTree(t, dir, map[string]string{
		"main.go":      "needle\n",
		"main_test.go": "needle\n",
		"helper.go":    "needle\n",
	})
	got := grepText(t, dir, map[string]any{"pattern": "needle", "glob": "*.go", "exclude": "*_test.go"})
	if !strings.Contains(got, "main.go") || strings.Contains(got, "main_test.go") {
		t.Errorf("exclude should drop the test file: %q", got)
	}
	got = grepText(t, dir, map[string]any{"pattern": "needle", "glob": "*.go", "exclude": []string{"*_test.go", "helper.go"}})
	if strings.Contains(got, "main_test.go") || strings.Contains(got, "helper.go") {
		t.Errorf("an exclude list should drop every listed glob: %q", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("exclude must not drop the remaining file: %q", got)
	}
}

// TestGrepLimit: limit caps the result without a pipeline, reports the cap in
// the result, and is refused outside its bounds with the range spelled out.
func TestGrepLimit(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("needle\n")
	}
	writeSearchTree(t, dir, map[string]string{"big.txt": b.String()})

	res := runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "needle", "limit": 3})
	txt := resultText(res)
	if !strings.Contains(txt, "3 match(es)") {
		t.Errorf("limit 3 should report 3 matches: %q", txt)
	}
	if !strings.Contains(txt, "raise limit") {
		t.Errorf("a capped result should name the way to see more: %q", txt)
	}
	if details, _ := res.Details.(map[string]any); details["matches"] != 3 {
		t.Errorf("matches detail = %v, want 3", details["matches"])
	}
	tooBig := resultText(runSearch(t, &GrepTool{Root: dir}, map[string]any{"pattern": "needle", "limit": searchLimitMax + 1}))
	if !strings.Contains(tooBig, "limit must be between") {
		t.Errorf("an oversized limit should be refused with its bounds: %q", tooBig)
	}
}

// TestFindNewArgs covers the arguments find gained to match grep: a path list,
// exclude, type, limit, hidden and no_ignore.
func TestFindNewArgs(t *testing.T) {
	requireRG(t)
	dir := t.TempDir()
	writeSearchTree(t, dir, map[string]string{
		"a/one.go":      "package a\n",
		"a/one_test.go": "package a\n",
		"b/two.go":      "package b\n",
		"b/notes.txt":   "plain\n",
		".cfg/deep.go":  "package cfg\n",
		".git/config":   "[core]\n",
		"build/gen.go":  "package build\n",
		".gitignore":    "build/\n",
	})

	// Several paths in one call, with type and exclude alongside.
	got := resultText(runSearch(t, &FindTool{Root: dir}, map[string]any{
		"glob": "*.go", "type": "go", "path": []string{"a", "b"}, "exclude": "*_test.go",
	}))
	if !strings.Contains(got, "a/one.go") || !strings.Contains(got, "b/two.go") {
		t.Errorf("a multi-path find should cover both trees: %q", got)
	}
	if strings.Contains(got, "one_test.go") {
		t.Errorf("exclude should drop the test file: %q", got)
	}
	// hidden widens the walk into hidden directories but still skips .git.
	got = resultText(runSearch(t, &FindTool{Root: dir}, map[string]any{"glob": "*", "hidden": true}))
	if !strings.Contains(got, ".cfg/deep.go") {
		t.Errorf("hidden should walk into hidden directories: %q", got)
	}
	if strings.Contains(got, ".git/config") {
		t.Errorf("hidden must still skip .git: %q", got)
	}
	// no_ignore reaches the ignored build product.
	got = resultText(runSearch(t, &FindTool{Root: dir}, map[string]any{"glob": "*.go", "no_ignore": true}))
	if !strings.Contains(got, "build/gen.go") {
		t.Errorf("no_ignore should include the ignored file: %q", got)
	}
	// limit caps the list and says so.
	got = resultText(runSearch(t, &FindTool{Root: dir}, map[string]any{"glob": "*.go", "limit": 1}))
	if !strings.Contains(got, "raise limit") {
		t.Errorf("a capped find should name the way to see more: %q", got)
	}
}

// TestSearchListArgsPassRegistryValidation: the schema accepts both spellings
// of the list arguments (so the tool's oneOf and its decoder agree) and still
// refuses a value of the wrong type.
func TestSearchListArgsPassRegistryValidation(t *testing.T) {
	reg := NewToolRegistry()
	if err := reg.Register(&GrepTool{}); err != nil {
		t.Fatalf("register grep: %v", err)
	}
	if err := reg.Register(&FindTool{}); err != nil {
		t.Fatalf("register find: %v", err)
	}
	valid := map[string]any{
		"pattern": "x",
		"path":    []string{"a", "b"},
		"exclude": "*_test.go",
		"limit":   10,
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("grep", raw); errs != nil {
		t.Errorf("grep list arguments rejected by schema validation: %+v", errs)
	}
	valid["path"] = "a"
	valid["exclude"] = []string{"*_test.go", "vendor"}
	raw, err = json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("grep", raw); errs != nil {
		t.Errorf("single-string path / list exclude rejected: %+v", errs)
	}
	raw, err = json.Marshal(map[string]any{
		"glob": "*.go", "path": []string{"a"}, "exclude": "*_test.go",
		"limit": 5, "type": "go", "hidden": true, "no_ignore": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("find", raw); errs != nil {
		t.Errorf("a documented find field was rejected: %+v", errs)
	}

	bad, err := json.Marshal(map[string]any{"pattern": "x", "path": 3})
	if err != nil {
		t.Fatal(err)
	}
	if errs := reg.Validate("grep", bad); len(errs) == 0 {
		t.Error("a non-string path must be refused by the schema")
	}
}
