package patch

import (
	"strings"
	"testing"
)

// A model often aligns the fields in a hunk (`"x":  value`) while the file has
// one space. That is a whitespace-only difference, not a stale context; the
// matcher must find it and still keep the file's original text.
func TestComputeUpdateInternalWhitespaceFuzzy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "var x = 1\n\t\"tuios\": {readOnly: true},\n")
	text := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.go",
		"@@",
		"-\t\"tuios\":  {readOnly: true},",
		"+\t\"tuios\":  {readOnly: false},",
		"*** End Patch",
		"",
	}, "\n")
	changes := patch(t, dir, text)
	if got, want := changes[0].NewContent, "var x = 1\n\t\"tuios\":  {readOnly: false},\n"; got != want {
		t.Errorf("NewContent = %q, want %q", got, want)
	}
}
