package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCurrentAtToken(t *testing.T) {
	cases := []struct {
		name   string
		buffer string
		caret  int
		ok     bool
		query  string
		start  int
		end    int
	}{
		{name: "bare at", buffer: "@", caret: 1, ok: true, query: "", start: 0, end: 1},
		{name: "query", buffer: "@foo", caret: 4, ok: true, query: "foo", start: 0, end: 4},
		{name: "mid token", buffer: "see @src/ma text", caret: 10, ok: true, query: "src/ma", start: 4, end: 11},
		{name: "at token end", buffer: "see @src/ma text", caret: 11, ok: true, query: "src/ma", start: 4, end: 11},
		{name: "after separator", buffer: "see @src/ma text", caret: 12, ok: false},
		{name: "email is not a mention", buffer: "mail me@example.com", caret: 10, ok: false},
		{name: "image reference excluded", buffer: "@image:/tmp/a.png", caret: 18, ok: false},
		{name: "newline ends token", buffer: "@foo\nbar", caret: 4, ok: true, query: "foo", start: 0, end: 4},
		{name: "next line", buffer: "@foo\nbar", caret: 5, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := currentAtToken(tc.buffer, tc.caret)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (token %+v)", ok, tc.ok, got)
			}
			if !ok {
				return
			}
			if got.Query != tc.query || got.Start != tc.start || got.End != tc.end {
				t.Fatalf("token = %+v, want query=%q range=%d..%d", got, tc.query, tc.start, tc.end)
			}
		})
	}
}

func TestFuzzyFileRankPrefersBasename(t *testing.T) {
	entries := []fileEntry{
		{Path: "internal/cli/tui/filesearch.go"},
		{Path: "src/filesearch.go"},
		{Path: "docs/filesystem.md"},
	}
	matches, total := rankFileEntries(entries, "filesearch", 10)
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].Path != "src/filesearch.go" {
		t.Errorf("top match = %q, want the shorter basename match", matches[0].Path)
	}
}

func TestRankFileEntriesLimitsRowsButKeepsTotal(t *testing.T) {
	entries := []fileEntry{
		{Path: "a/file.go"},
		{Path: "b/file.go"},
		{Path: "c/file.go"},
	}
	matches, total := rankFileEntries(entries, "file", 2)
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
}

func TestFileMenuLoadsAndFilters(t *testing.T) {
	fm := newFileMenu(Theme{})
	if !fm.refresh("@fi", 3, "/tmp/root") {
		t.Fatal("first non-empty query should request an index load")
	}
	if !fm.active || !fm.loading {
		t.Fatalf("menu = active=%v loading=%v, want both true", fm.active, fm.loading)
	}
	fm.setIndex([]fileEntry{
		{Path: "src/filesearch.go"},
		{Path: "docs/readme.md"},
	}, nil)
	if fm.loading {
		t.Fatal("menu still loading after setIndex")
	}
	if len(fm.matches) != 1 || fm.matches[0].Path != "src/filesearch.go" {
		t.Fatalf("matches = %+v", fm.matches)
	}
	if got, want := fm.rows(80), len(fm.lines(80)); got != want {
		t.Fatalf("rows = %d, lines = %d", got, want)
	}
}

func TestFileMenuDirectoryAndQuotedSelection(t *testing.T) {
	fm := newFileMenu(Theme{})
	fm.setIndex([]fileEntry{
		{Path: "docs", Dir: true},
		{Path: "my file.txt"},
	}, nil)
	fm.query = "docs"
	fm.recompute()
	if got, ok := fm.selection(); !ok || got != "docs/" {
		t.Fatalf("directory selection = %q, %v", got, ok)
	}

	fm.query = "my file"
	fm.recompute()
	if got, ok := fm.selection(); !ok || got != `"my file.txt"` {
		t.Fatalf("spaced selection = %q, %v", got, ok)
	}
}

func TestFileMenuDismissRequiresTokenChange(t *testing.T) {
	fm := newFileMenu(Theme{})
	fm.setIndex([]fileEntry{{Path: "foo.go"}}, nil)
	fm.refresh("@fo", 3, "/tmp/root")
	if !fm.active {
		t.Fatal("menu should be active")
	}
	fm.dismiss()
	if fm.active {
		t.Fatal("menu should be closed after dismiss")
	}
	fm.refresh("@fo", 3, "/tmp/root")
	if fm.active {
		t.Fatal("same token should stay dismissed")
	}
	fm.refresh("@foo", 4, "/tmp/root")
	if !fm.active {
		t.Fatal("changed token should reopen completion")
	}
}

func TestCollectFileEntriesIndexesLocalTree(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "alpha.go")
	writeTestFile(t, root, "docs/readme.md")
	writeTestFile(t, root, ".build/artifact")
	writeTestFile(t, root, "node_modules/pkg/index.js")
	writeTestFile(t, root, ".git/config")

	entries, err := collectFileEntries(root)
	if err != nil {
		t.Fatalf("collectFileEntries: %v", err)
	}
	if !hasFileEntry(entries, "alpha.go", false) {
		t.Errorf("missing alpha.go in %v", entryPaths(entries))
	}
	if !hasFileEntry(entries, "docs/readme.md", false) {
		t.Errorf("missing docs/readme.md in %v", entryPaths(entries))
	}
	if !hasFileEntry(entries, "docs", true) {
		t.Errorf("missing docs directory in %v", entryPaths(entries))
	}
	for _, unwanted := range []string{".build/artifact", "node_modules/pkg/index.js", ".git/config"} {
		if hasPath(entries, unwanted) {
			t.Errorf("index contains skipped path %q", unwanted)
		}
	}
}

func TestWalkFileEntriesSkipsHeavyDirs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "main.go")
	writeTestFile(t, root, ".git/config")
	writeTestFile(t, root, "node_modules/pkg/index.js")
	writeTestFile(t, root, ".build/artifact")

	entries, err := walkFileEntries(root)
	if err != nil {
		t.Fatalf("walkFileEntries: %v", err)
	}
	if !hasPath(entries, "main.go") {
		t.Fatalf("missing main.go in %v", entryPaths(entries))
	}
	for _, unwanted := range []string{".git/config", "node_modules/pkg/index.js", ".build/artifact"} {
		if hasPath(entries, unwanted) {
			t.Errorf("walk contains skipped path %q", unwanted)
		}
	}
}

func TestFileCompletionInsertsPathOnEnter(t *testing.T) {
	m := NewModel(Options{NoPromptTemplates: true})
	m.fileMenu.root = m.fileSearchRoot()
	m.fileMenu.setIndex([]fileEntry{{Path: "src/foo.go"}}, nil)
	m.input.SetValue("@fo")
	if cmd := m.refreshFileMenu(); cmd != nil {
		t.Fatalf("refresh returned an index command with a cached index: %v", cmd)
	}
	if !m.fileMenu.active {
		t.Fatal("file menu should be active for @fo")
	}

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)
	if got.input.Value() != "src/foo.go " {
		t.Fatalf("input = %q, want %q", got.input.Value(), "src/foo.go ")
	}
	if got.fileMenu.active {
		t.Fatal("file menu should close after insertion")
	}
}

func TestFileCompletionPreservesSuffixAndCaret(t *testing.T) {
	m := NewModel(Options{NoPromptTemplates: true})
	m.fileMenu.root = m.fileSearchRoot()
	m.fileMenu.setIndex([]fileEntry{{Path: "src/foo.go"}}, nil)
	m.input.SetValueCursor("read @fo please", len("read @fo"))
	m.refreshFileMenu()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	got := next.(Model)
	want := "read src/foo.go  please"
	if got.input.Value() != want {
		t.Fatalf("input = %q, want %q", got.input.Value(), want)
	}
	if got.input.CursorByte() != len("read src/foo.go ") {
		t.Fatalf("caret = %d, want %d", got.input.CursorByte(), len("read src/foo.go "))
	}
}

func TestFileCompletionEscDismissesUntilTokenChanges(t *testing.T) {
	m := NewModel(Options{NoPromptTemplates: true})
	m.fileMenu.root = m.fileSearchRoot()
	m.fileMenu.setIndex([]fileEntry{{Path: "foo.go"}}, nil)
	m.input.SetValue("@fo")
	m.refreshFileMenu()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := next.(Model)
	if got.fileMenu.active {
		t.Fatal("Esc should close the file menu")
	}

	next, _ = got.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	got = next.(Model)
	if !got.fileMenu.active {
		t.Fatal("editing the dismissed token should reopen the file menu")
	}
}

func TestBareAtShowsPromptWithoutIndexing(t *testing.T) {
	m := NewModel(Options{NoPromptTemplates: true})
	m.input.SetValue("@")
	cmd := m.refreshFileMenu()
	if cmd != nil {
		t.Fatal("bare @ should not start an index load")
	}
	if !m.fileMenu.active {
		t.Fatal("bare @ should open the popup prompt")
	}
	if view := m.fileMenu.view(80); !strings.Contains(view, "type a file name") {
		t.Fatalf("view = %q, want the search prompt", view)
	}
}

func TestInputSetValueCursorRoundTripsByteOffset(t *testing.T) {
	in := newInput()
	for _, tc := range []struct {
		value  string
		offset int
	}{
		{value: "hello\nworld", offset: 8},
		{value: "héllo世界", offset: len("héllo世")},
		{value: "a\n", offset: 2},
	} {
		in.SetValueCursor(tc.value, tc.offset)
		if got := in.CursorByte(); got != tc.offset {
			t.Errorf("CursorByte(%q, %d) = %d", tc.value, tc.offset, got)
		}
	}
}

func writeTestFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasFileEntry(entries []fileEntry, path string, dir bool) bool {
	for _, e := range entries {
		if e.Path == path && e.Dir == dir {
			return true
		}
	}
	return false
}

func hasPath(entries []fileEntry, path string) bool {
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

func entryPaths(entries []fileEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}
