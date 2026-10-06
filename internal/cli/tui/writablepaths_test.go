package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/seatbelt"
)

// TestWritablePathsPickerFlow covers the TUI surface end to end: bare
// /permissions grows the writable row, selecting it opens the path picker,
// Enter on a path revokes it (registry + managed store) and rebuilds the list,
// and the Add row seeds the composer with the add command instead of running
// it.
func TestWritablePathsPickerFlow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	seatbelt.SetWritableRoots(nil)
	t.Cleanup(func() { seatbelt.SetWritableRoots(nil) })

	granted := filepath.Join(dir, "cache", "build")
	if _, _, err := cli.AddWritablePath(granted); err != nil {
		t.Fatal(err)
	}

	s := newRemoteTestSession(t)
	m := NewModel(Options{})
	m.session = s
	m.slash = s.slash
	got, _ := m.runSlash("/permissions")
	m = got.(Model)
	last := m.menu.pick[len(m.menu.pick)-1]
	if last.Value != "writable" || !strings.Contains(last.Title, "Sandbox writable paths") {
		t.Fatalf("mode picker missing the writable row: %+v", last)
	}

	// Enter on that row runs /permissions writable, which opens the path
	// picker (Enter on the Add row with the cursor still on the mode picker
	// would otherwise switch modes).
	m.menu.selected = len(m.menu.pick) - 1
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	if m.menu.pickKind != "permissions-writable" {
		t.Fatalf("writable picker not open: kind=%q", m.menu.pickKind)
	}
	if len(m.menu.pick) != 2 { // Add path… + the granted entry
		t.Fatalf("rows = %+v", m.menu.pick)
	}
	if m.menu.pick[1].Value != granted || !strings.Contains(m.menu.pick[1].Detail, "Enter removes") {
		t.Fatalf("path row = %+v", m.menu.pick[1])
	}

	// Enter on the path revokes it and keeps the picker open on the now-empty
	// list.
	m.menu.selected = 1
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	if m.menu.pickKind != "permissions-writable" || len(m.menu.pick) != 1 {
		t.Fatalf("picker should stay open after removal: kind=%q rows=%d", m.menu.pickKind, len(m.menu.pick))
	}
	if roots := seatbelt.WritableRoots(); len(roots) != 0 {
		t.Fatalf("registry not revoked: %v", roots)
	}
	if !strings.Contains(m.transcriptText(), "Revoked") {
		t.Fatalf("transcript should confirm removal:\n%s", m.transcriptText())
	}

	// The Add row hands the composer the add command instead of running it.
	got, _ = m.openWritablePathsPicker()
	m = got.(Model)
	if item, _ := m.menu.pickCurrent(); item != writableAddPick {
		t.Fatalf("Add row should be preselected, got %q", item)
	}
	got, _ = m.submitSlashSelected()
	m = got.(Model)
	if m.input.Value() != "/permissions writable add " {
		t.Fatalf("Add row should seed the composer, got %q", m.input.Value())
	}
}
