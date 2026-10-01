package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolverFor maps patch paths onto dir, failing anything that escapes it —
// mirroring the tool layer's workspace guard.
func resolverFor(t *testing.T, dir string) Resolver {
	t.Helper()
	return func(p string) (string, error) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		rel, err := filepath.Rel(dir, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", applyErrf("path %q is outside the workspace root", p)
		}
		return full, nil
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func patch(t *testing.T, dir, text string) []Change {
	t.Helper()
	ops, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	changes, err := Compute(ops, resolverFor(t, dir))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return changes
}

func TestParseAddDeleteUpdateMove(t *testing.T) {
	ops, err := Parse(`*** Begin Patch
*** Add File: a.txt
+hello
+world
*** Delete File: b.txt
*** Update File: c.txt
*** Move to: d.txt
@@ func f()
 context
-old
+new
*** End Patch
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(ops))
	}
	if ops[0].Kind != OpAdd || ops[0].Path != "a.txt" || strings.Join(ops[0].Body, "|") != "hello|world" {
		t.Errorf("add op = %+v", ops[0])
	}
	if ops[1].Kind != OpDelete || ops[1].Path != "b.txt" {
		t.Errorf("delete op = %+v", ops[1])
	}
	up := ops[2]
	if up.Kind != OpUpdate || up.Path != "c.txt" || up.MoveTo != "d.txt" {
		t.Errorf("update op = %+v", up)
	}
	if len(up.Chunks) != 1 || up.Chunks[0].Header != "func f()" || len(up.Chunks[0].Lines) != 3 {
		t.Fatalf("chunk = %+v", up.Chunks)
	}
	if up.Chunks[0].Lines[1].Kind != LineDelete || up.Chunks[0].Lines[1].Text != "old" {
		t.Errorf("delete line = %+v", up.Chunks[0].Lines[1])
	}
	if up.Chunks[0].Lines[2].Kind != LineAdd || up.Chunks[0].Lines[2].Text != "new" {
		t.Errorf("add line = %+v", up.Chunks[0].Lines[2])
	}
}

// TestParseEndOfFileMarker verifies the "*** End of File" anchor and the
// blank-line-as-context leniency.
func TestParseEndOfFileMarker(t *testing.T) {
	ops, err := Parse(`*** Begin Patch
*** Update File: a.txt
@@
-last
+new last
*** End of File
*** End Patch
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !ops[0].Chunks[0].EOF {
		t.Error("chunk should carry the EOF anchor")
	}
}

func TestParseErrors(t *testing.T) {
	for name, text := range map[string]string{
		"missing begin":  "*** Add File: a\n+x\n*** End Patch\n",
		"no ops":         "*** Begin Patch\n*** End Patch\n",
		"unclosed":       "*** Begin Patch\n*** Add File: a\n+x\n",
		"add no plus":    "*** Begin Patch\n*** Add File: a\nhello\n*** End Patch\n",
		"add no body":    "*** Begin Patch\n*** Add File: a\n*** End Patch\n",
		"bad op line":    "*** Begin Patch\nwat\n*** End Patch\n",
		"update empty":   "*** Begin Patch\n*** Update File: a\n*** End Patch\n",
		"bad chunk line": "*** Begin Patch\n*** Update File: a\n@@\nold without prefix\n*** End Patch\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(text); err == nil {
				t.Fatal("Parse = nil error, want a parse failure")
			} else if !strings.Contains(err.Error(), "Invalid patch") {
				t.Errorf("error = %v, want the Invalid patch prefix", err)
			}
		})
	}
}

func TestComputeAddThenRead(t *testing.T) {
	dir := t.TempDir()
	changes := patch(t, dir, `*** Begin Patch
*** Add File: sub/new.txt
+line one
+line two
*** End Patch
`)
	if len(changes) != 1 || changes[0].Kind != ChangeAdd {
		t.Fatalf("changes = %+v", changes)
	}
	if want := "line one\nline two\n"; changes[0].NewContent != want {
		t.Errorf("NewContent = %q, want %q", changes[0].NewContent, want)
	}
	// Compute must not have written anything.
	if _, err := os.Stat(filepath.Join(dir, "sub/new.txt")); !os.IsNotExist(err) {
		t.Error("Compute wrote to disk; it must be read-only")
	}
}

func TestComputeAddExistingFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "x\n")
	ops, err := Parse("*** Begin Patch\n*** Add File: a.txt\n+y\n*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Compute(ops, resolverFor(t, dir))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Compute = %v, want an already-exists error", err)
	}
}

func TestComputeUpdateExactAndFuzzy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "func a() {\n\told()\n}\n")

	// Fuzzy: the patch's context carries trailing spaces the file lacks.
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.go
@@
 func a() {   
-	old()
+	new()
 }
*** End Patch
`)
	if got, want := changes[0].NewContent, "func a() {\n\tnew()\n}\n"; got != want {
		t.Errorf("NewContent = %q, want %q", got, want)
	}
}

func TestComputeUpdateUnicodePunctuation(t *testing.T) {
	dir := t.TempDir()
	// Source uses a curly apostrophe and an em dash; the patch uses ASCII.
	writeFile(t, dir, "doc.md", "it\u2019s here \u2014 really\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: doc.md
@@
-it's here - really
+it is here - really
*** End Patch
`)
	if got, want := changes[0].NewContent, "it is here - really\n"; got != want {
		t.Errorf("NewContent = %q, want %q", got, want)
	}
}

func TestComputeUpdateNoMatchFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo\n")
	ops, err := Parse("*** Begin Patch\n*** Update File: a.txt\n@@\n-missing\n+new\n*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Compute(ops, resolverFor(t, dir))
	if err == nil || !strings.Contains(err.Error(), "Invalid Context") {
		t.Fatalf("Compute = %v, want an Invalid Context error", err)
	}
}

func TestComputeEOFAnchor(t *testing.T) {
	dir := t.TempDir()
	// "end" appears twice; the EOF anchor must pick the final occurrence.
	writeFile(t, dir, "a.txt", "end\nmiddle\nend\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.txt
@@
-end
+tail
*** End of File
*** End Patch
`)
	if got, want := changes[0].NewContent, "end\nmiddle\ntail\n"; got != want {
		t.Errorf("NewContent = %q, want %q", got, want)
	}
}

func TestComputeMove(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "old.txt", "hello\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: old.txt
*** Move to: new.txt
@@
-hello
+hello world
*** End Patch
`)
	c := changes[0]
	if c.Kind != ChangeMove || filepath.Base(c.Path) != "new.txt" || filepath.Base(c.OldPath) != "old.txt" {
		t.Fatalf("change = %+v", c)
	}
	if c.NewContent != "hello world\n" {
		t.Errorf("NewContent = %q", c.NewContent)
	}
}

// TestComputeSequentialOpsOnSameFile verifies the overlay: a second operation
// on a file sees the first operation's result, not the stale disk content.
func TestComputeSequentialOpsOnSameFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.txt
@@
-one
+ONE
*** Update File: a.txt
@@
-two
+TWO
*** End Patch
`)
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(changes))
	}
	if got, want := changes[1].NewContent, "ONE\nTWO\n"; got != want {
		t.Errorf("second change NewContent = %q, want %q", got, want)
	}
}

func TestComputeTrailingNewlinePreserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "one\ntwo") // no trailing newline
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.txt
@@
-two
+TWO
*** End Patch
`)
	if got, want := changes[0].NewContent, "one\nTWO"; got != want {
		t.Errorf("NewContent = %q, want %q (newline convention preserved)", got, want)
	}
}

func TestPaths(t *testing.T) {
	got := Paths("*** Begin Patch\n*** Add File: a\n+x\n*** Update File: b\n*** Move to: c\n@@\n-b\n+c\n*** Delete File: d\n*** End Patch\n")
	want := []string{"a", "b", "c", "d"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Paths = %v, want %v", got, want)
	}
	if Paths("not a patch") != nil {
		t.Error("Paths on unparseable input should be nil")
	}
}
