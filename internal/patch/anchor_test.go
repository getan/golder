package patch

import (
	"strings"
	"testing"
)

// computeFor parses text and computes it against dir, returning the error
// instead of failing, for the cases that are supposed to refuse the patch.
func computeFor(t *testing.T, dir, text string) ([]Change, error) {
	t.Helper()
	ops, err := Parse(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Compute(ops, resolverFor(t, dir))
}

// The same short body repeats twice; without an anchor the first copy wins,
// and with one the second does. This is the case codex's "@@ class" lines
// exist for.
func TestAnchorDisambiguatesRepeatedContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "func a() {\n\tx := 1\n\tuse(x)\n}\n\nfunc b() {\n\tx := 1\n\tuse(x)\n}\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.go
@@ func b()
 	x := 1
-	use(x)
+	use(x, 2)
*** End Patch
`)
	want := "func a() {\n\tx := 1\n\tuse(x)\n}\n\nfunc b() {\n\tx := 1\n\tuse(x, 2)\n}\n"
	if got := changes[0].NewContent; got != want {
		t.Fatalf("NewContent = %q, want the second copy edited", got)
	}
}

// An anchor that no longer exists is documentation, not a promise: the hunk
// still applies where its own context is unique.
func TestStaleAnchorStillApplies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "func a() {\n\told()\n}\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.go
@@ func gone()
 func a() {
-	old()
+	new()
 }
*** End Patch
`)
	if got := changes[0].NewContent; got != "func a() {\n\tnew()\n}\n" {
		t.Fatalf("NewContent = %q", got)
	}
}

// Two "@@" lines are two anchors, as codex's instructions describe (a class,
// then a method), and the parser used to reject that shape outright.
func TestMultipleAnchorsParse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.py", "class Base:\n    def f():\n        old()\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.py
@@ class Base:
@@     def f():
         old()
+        new()
*** End Patch
`)
	if got := changes[0].NewContent; !strings.Contains(got, "new()") {
		t.Fatalf("NewContent = %q", got)
	}
}

// A hunk whose trailing context has drifted still lands when the remaining
// lines are unique, and the result says so instead of pretending it was exact.
func TestReducedContextAppliesAndSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "func a() {\n\told()\n}\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.go
@@
 func a() {
-	old()
+	new()
 }
 // a trailing line the file never had
*** End Patch
`)
	if got := changes[0].NewContent; got != "func a() {\n\tnew()\n}\n" {
		t.Fatalf("NewContent = %q", got)
	}
	if !strings.Contains(changes[0].Note, "context line(s) reduced") {
		t.Errorf("Note = %q, want the reduction reported", changes[0].Note)
	}
}

// Reduction must not become guessing: when what is left matches twice, the
// patch fails and the message shows the nearest block.
func TestAmbiguousReductionIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "func a() {\n\tgoon()\n}\n\nfunc b() {\n\tgoon()\n}\n")
	_, err := computeFor(t, dir, `*** Begin Patch
*** Update File: a.go
@@
-	goon()
+	goon(1)
 trailing context that matches nothing
*** End Patch
`)
	if err == nil {
		t.Fatal("an ambiguous reduced match must fail")
	}
	if !strings.Contains(err.Error(), "closest match at line") {
		t.Fatalf("error = %v, want the nearest-match diagnostic", err)
	}
	if !strings.Contains(err.Error(), "The file there reads:") {
		t.Fatalf("error = %v, want the surrounding block", err)
	}
}

// A blank line between hunks is formatting. Treating it as an empty context
// line used to extend the pattern past the text the author meant, which is one
// of the ways a patch that looks right failed to locate its hunk.
func TestBlankLineBetweenHunksIsNotContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "one\ntwo\nthree\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.go
@@
-one
+ONE

@@
-three
+THREE
*** End Patch
`)
	if got := changes[0].NewContent; got != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("NewContent = %q", got)
	}
}

// The same allowance at the end of a hunk that replaces the file's tail: the
// trailing blank stands for the final newline, not for a line to find.
func TestTrailingBlankLineAtEOFIsNotContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "a\nb\n")
	changes := patch(t, dir, `*** Begin Patch
*** Update File: a.go
@@
-b
+B

*** End Patch
`)
	if got := changes[0].NewContent; got != "a\nB\n" {
		t.Fatalf("NewContent = %q", got)
	}
}

// A hunk line that arrives without a prefix is usually a forgotten "+". When
// every neighbour is an addition the reading is unambiguous: apply it and say
// so, instead of spending a round trip on a typo.
func TestUnprefixedAdditionIsRepaired(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "a\nb\n")
	ops, err := Parse(`*** Begin Patch
*** Update File: a.go
@@
-a
+A
second added line
*** End Patch
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ops) != 1 || len(ops[0].Notes) != 1 || !strings.Contains(ops[0].Notes[0], "additions") {
		t.Fatalf("notes = %+v, want the repair reported", ops)
	}
	changes, err := Compute(ops, resolverFor(t, dir))
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if got := changes[0].NewContent; got != "A\nsecond added line\nb\n" {
		t.Fatalf("NewContent = %q", got)
	}
}

// When the neighbours disagree the reading is a guess, and guessing changes the
// file: refuse, and name the fix.
func TestUnprefixedAmbiguousLineIsRefused(t *testing.T) {
	_, err := Parse(`*** Begin Patch
*** Update File: a.go
@@
-a
+A
bare line between an addition and a context line
 b
*** End Patch
`)
	if err == nil || !strings.Contains(err.Error(), "no prefix") {
		t.Fatalf("err = %v, want the ambiguous-line refusal", err)
	}
}

// With no context line matching at all, name the closest line rather than only
// saying nothing matched.
func TestDiagnosticNamesTheClosestLine(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Title\n\n## Installation\n\nRun it.\n")
	_, err := computeFor(t, dir, `*** Begin Patch
*** Update File: README.md
@@
## Install
 new text
*** End Patch
`)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "closest line is") || !strings.Contains(err.Error(), "Installation") {
		t.Fatalf("error = %v, want the nearest line named", err)
	}
}
