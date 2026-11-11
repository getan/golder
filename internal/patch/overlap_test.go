package patch

import (
	"strings"
	"testing"
)

func TestOverlappingHunksNameOriginalLineAndBothHunks(t *testing.T) {
	for _, second := range []string{"original component", "updated component"} {
		t.Run(second, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.tsx", strings.Repeat("// preceding\n", 99)+"original component\n")
			changes, err := computeFor(t, dir, "*** Begin Patch\n*** Update File: a.tsx\n@@\n-original component\n+updated component\n@@\n-"+second+"\n+final component\n*** End Patch\n")
			if err == nil || changes != nil {
				t.Fatal("overlapping edits should fail atomically")
			}
			for _, want := range []string{"hunks 1 and 2", "original line 100", "merge", "nothing was written"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "could not find the context") {
				t.Fatalf("overlap was mislabeled as missing context: %v", err)
			}
		})
	}
}

func TestOverlapTracksOriginalLineAcrossInsertionsAndDeletion(t *testing.T) {
	for _, replacement := range []string{"", "+new first\n+new second\n"} {
		dir := t.TempDir()
		writeFile(t, dir, "a.txt", "before\noriginal\nafter\n")
		_, err := computeFor(t, dir, "*** Begin Patch\n*** Update File: a.txt\n@@\n before\n+inserted before\n@@\n-original\n"+replacement+"@@\n-original\n+final\n*** End Patch\n")
		if err == nil || !strings.Contains(err.Error(), "hunks 2 and 3 both modify original line 2") {
			t.Fatalf("lost original coordinates after insertion/deletion: %v", err)
		}
	}
}

func TestDifferentEditsWithSharedContextAreNotOverlaps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "first\nshared\nlast\n")
	changes, err := computeFor(t, dir, "*** Begin Patch\n*** Update File: a.txt\n@@\n-first\n+FIRST\n shared\n@@\n shared\n-last\n+LAST\n*** End Patch\n")
	if err != nil || len(changes) != 1 || changes[0].NewContent != "FIRST\nshared\nLAST\n" {
		t.Fatalf("shared unchanged context became an overlap: %v", err)
	}
}

func TestRepeatedLinesAtDifferentOriginalPositionsStillApply(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "same\nsame\n")
	changes, err := computeFor(t, dir, "*** Begin Patch\n*** Update File: a.txt\n@@\n-same\n+first\n@@\n-same\n+second\n*** End Patch\n")
	if err != nil || len(changes) != 1 || changes[0].NewContent != "first\nsecond\n" {
		t.Fatalf("separate original lines were misdiagnosed: %v", err)
	}
}
