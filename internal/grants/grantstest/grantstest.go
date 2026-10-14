// Package grantstest installs an empty authorization table for a test.
//
// It exists because the table is process-wide: a test that grants a path must
// not leak that row into the next one, and every package that exercises a gate,
// a tool or a dialog needs the same setup. One helper means one way to do it,
// so a test cannot accidentally run against the developer's real grants.json.
package grantstest

import (
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/grants"
)

// Install gives the test a fresh, empty table backed by a temp file, and clears
// the process table when the test ends. The returned store is the same one
// grants.Process() reports, so a test can seed rows through either.
func Install(t *testing.T) *grants.Store {
	t.Helper()
	s, err := grants.New(grants.Options{Path: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatalf("grants.New: %v", err)
	}
	grants.SetProcess(s)
	t.Cleanup(func() { grants.SetProcess(nil) })
	return s
}

// Nothing gives the test no table at all, for the case a caller must handle:
// grants.Process() returns nil and every question is "undecided".
func Nothing(t *testing.T) {
	t.Helper()
	grants.SetProcess(nil)
	t.Cleanup(func() { grants.SetProcess(nil) })
}
