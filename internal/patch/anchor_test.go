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
