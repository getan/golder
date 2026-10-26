package patch

import (
	"strings"
	"testing"
)

func TestComputeLimitedRejectsBeforeReturningChanges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "alpha\nbeta\ngamma\n")
	ops, err := Parse("*** Begin Patch\n*** Update File: a.txt\n@@\n-alpha\n+ALPHA\n*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeLimited(ops, resolverFor(t, dir), 4)
	if err == nil || !strings.Contains(err.Error(), "content budget") || changes != nil {
		t.Fatalf("ComputeLimited = %#v, %v; want no changes and a budget error", changes, err)
	}
	if !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("budget error must promise the no-write contract: %v", err)
	}
}

func TestComputeLimitedAllowsWithinBudget(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "alpha\nbeta\ngamma\n")
	ops, err := Parse("*** Begin Patch\n*** Update File: a.txt\n@@\n-alpha\n+ALPHA\n*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeLimited(ops, resolverFor(t, dir), 1024)
	if err != nil || len(changes) != 1 {
		t.Fatalf("ComputeLimited = %#v, %v; want one change", changes, err)
	}
}
