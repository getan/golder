package patch

import (
	"strings"
	"testing"
)

// A patch that touches two files and breaks one hunk reports both facts in one
// message: the failure, and that the rest would have applied. That is what turns
// "resend everything" into "fix this one hunk and resend".
func TestFailureReportCountsWhatWouldApply(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "one\ntwo\n")
	writeFile(t, dir, "b.go", "alpha\nbeta\n")
	changes, err := computeFor(t, dir, `*** Begin Patch
*** Update File: a.go
@@
-one
+ONE
*** Update File: b.go
@@
-missing line
+MISSING
*** End Patch
`)
	if err == nil {
		t.Fatalf("expected a failure, got changes %+v", changes)
	}
	if changes != nil {
		t.Errorf("a failed patch must not return changes, got %+v", changes)
	}
	msg := err.Error()
	for _, want := range []string{"Invalid Context", "b.go", "1 hunk(s)/change(s) across 1 file(s) would apply", "nothing was written"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want %q", msg, want)
		}
	}
}

// When nothing else fits, the message says so instead of inviting a resend.
func TestFailureReportSaysNothingElseApplies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "one\n")
	_, err := computeFor(t, dir, `*** Begin Patch
*** Update File: a.go
@@
-two
+TWO
*** End Patch
`)
	if err == nil || !strings.Contains(err.Error(), "No other part of this patch applies either") {
		t.Fatalf("error = %v, want the nothing-else sentence", err)
	}
}

// Every broken hunk is reported in the same pass: two failures cost one round
// trip, not one per hunk.
func TestEveryFailureIsReportedInOnePass(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "keep\n")
	_, err := computeFor(t, dir, `*** Begin Patch
*** Update File: a.go
@@
-first missing
+FIRST
@@
-second missing
+SECOND
*** End Patch
`)
	if err == nil {
		t.Fatal("expected failures")
	}
	msg := err.Error()
	if !strings.Contains(msg, "also: a.go hunk 2") {
		t.Errorf("error = %q, want the second hunk reported too", msg)
	}
	if strings.Count(msg, "could not find the context") < 2 {
		t.Errorf("error = %q, want both hunks named", msg)
	}
}
