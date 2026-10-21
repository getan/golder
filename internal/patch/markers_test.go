package patch

import (
	"strings"
	"testing"
)

// Marker text inside an added file is data, not a new operation. This used to
// split one Add File into a bogus "Update File: nested.go" operation.
func TestAddFileCanContainPatchMarkers(t *testing.T) {
	input := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: doc.md",
		"+*** Begin Patch",
		"+*** Update File: nested.go",
		"+*** End Patch",
		"*** End Patch",
		"",
	}, "\n")
	ops, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Kind != OpAdd || len(ops[0].Body) != 3 {
		t.Fatalf("ops = %+v, want one three-line Add", ops)
	}
	if got := strings.Join(ops[0].Body, "\n"); got != "*** Begin Patch\n*** Update File: nested.go\n*** End Patch" {
		t.Fatalf("body = %q", got)
	}
}

// The same rule inside an update hunk: an added line beginning "+***" is an
// addition, not a section boundary.
func TestUpdateCanAddMarkerText(t *testing.T) {
	input := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.md",
		"@@",
		"-old",
		"+*** Update File: nested.go",
		"+*** End Patch",
		"*** End Patch",
		"",
	}, "\n")
	ops, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Kind != OpUpdate {
		t.Fatalf("ops = %+v, want one Update", ops)
	}
	if len(ops[0].Chunks) != 1 || len(ops[0].Chunks[0].Lines) != 3 {
		t.Fatalf("chunks = %+v", ops[0].Chunks)
	}
}
