package tui

// Tests for Ctrl+R reverse history search: query editing, unique-match
// traversal, accept/cancel semantics, and the rendering contract (the search
// line replaces the popup slot and the composer previews the match).

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// seedSearchModel builds a sized model with an in-memory browse history. No
// HistoryPath is set, so nothing touches disk.
func seedSearchModel(t *testing.T, entries ...string) Model {
	t.Helper()
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 12})
	m.history = append([]string(nil), entries...)
	m.histIdx = len(m.history)
	return m
}

// TestHistorySearchFindsAndAccepts walks the Ctrl+R flow end to end: opening
// leaves the draft alone, typing scans newest-first, repeated Ctrl+R walks
// older unique matches, Ctrl+S walks back, and Enter accepts the preview as an
// editable draft (without submitting).
func TestHistorySearchFindsAndAccepts(t *testing.T) {
	m := seedSearchModel(t, "alpha one", "beta two", "alpha three")

	m = apply(t, m, ctrlKey('r'))
	if m.histSearch == nil {
		t.Fatal("ctrl+r must open a search session")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("opening the search must not preview anything, got %q", got)
	}

	for _, r := range "alp" {
		m = apply(t, m, runeKey(r))
	}
	if got := m.input.Value(); got != "alpha three" {
		t.Errorf("first match = %q, want the newest matching prompt", got)
	}
	if got := len(m.histSearch.matches); got != 2 {
		t.Errorf("matches = %d, want 2 unique matches", got)
	}

	m = apply(t, m, ctrlKey('r')) // older
	if got := m.input.Value(); got != "alpha one" {
		t.Errorf("ctrl+r moved to %q, want the older match", got)
	}
	m = apply(t, m, ctrlKey('r')) // boundary: stays
	if got := m.input.Value(); got != "alpha one" {
		t.Errorf("ctrl+r past the oldest match changed the preview to %q", got)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp}) // ↑ is the same as ctrl+r
	if got := m.input.Value(); got != "alpha one" {
		t.Errorf("↑ at the boundary changed the preview to %q", got)
	}
	m = apply(t, m, ctrlKey('s')) // newer
	if got := m.input.Value(); got != "alpha three" {
		t.Errorf("ctrl+s moved to %q, want the newer match", got)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown}) // ↓ is the same as ctrl+s
	if got := m.input.Value(); got != "alpha three" {
		t.Errorf("↓ at the newest match changed the preview to %q", got)
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.histSearch != nil {
		t.Error("Enter must close the search session")
	}
	if got := m.input.Value(); got != "alpha three" {
		t.Errorf("accepted draft = %q, want the previewed match", got)
	}
	if m.running {
		t.Error("accepting a search must not submit or start a run")
	}
}

// TestHistorySearchCancelAndMissRestoreDraft verifies both escape hatches: a
// query with no matches leaves the composer on the pre-search draft, and Esc
// restores that draft exactly after any amount of searching.
func TestHistorySearchCancelAndMissRestoreDraft(t *testing.T) {
	m := seedSearchModel(t, "some old prompt")
	for _, r := range "draft in progress" {
		m = apply(t, m, runeKey(r))
	}

	m = apply(t, m, ctrlKey('r'))
	for _, r := range "zzz-no-such-question" {
		m = apply(t, m, runeKey(r))
	}
	if got := m.input.Value(); got != "draft in progress" {
		t.Errorf("a miss must leave the original draft in place, got %q", got)
	}
	if m.histSearch == nil || m.histSearch.cursor != -1 {
		t.Fatalf("search state = %+v, want an active session with no matches", m.histSearch)
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.histSearch != nil {
		t.Error("Esc must close the search session")
	}
	if got := m.input.Value(); got != "draft in progress" {
		t.Errorf("Esc restored %q, want the pre-search draft", got)
	}

	// Ctrl+C cancels the same way (it must not arm the quit path mid-search).
	m2 := seedSearchModel(t, "another prompt")
	for _, r := range "typed" {
		m2 = apply(t, m2, runeKey(r))
	}
	m2 = apply(t, m2, ctrlKey('r'))
	for _, r := range "another" {
		m2 = apply(t, m2, runeKey(r))
	}
	if got := m2.input.Value(); got != "another prompt" {
		t.Fatalf("search preview = %q, want the match", got)
	}
	m2 = apply(t, m2, ctrlKey('c'))
	if m2.histSearch != nil {
		t.Error("ctrl+c must cancel the search session")
	}
	if got := m2.input.Value(); got != "typed" {
		t.Errorf("ctrl+c restored %q, want the pre-search draft", got)
	}
	if m2.quitting {
		t.Error("ctrl+c during a search must not quit the program")
	}
}

// TestHistorySearchDedupsText locks the dedup rule: matches are unique by exact
// prompt text, so repeated identical entries appear once.
func TestHistorySearchDedupsText(t *testing.T) {
	m := seedSearchModel(t, "same text", "same text", "other")
	m = apply(t, m, ctrlKey('r'))
	for _, r := range "same" {
		m = apply(t, m, runeKey(r))
	}
	if got := len(m.histSearch.matches); got != 1 {
		t.Errorf("matches = %d, want 1 after text dedup", got)
	}
	if got := m.input.Value(); got != "same text" {
		t.Errorf("preview = %q, want the deduped match", got)
	}
}

// TestHistorySearchRendersLineAndReservesRow verifies the UI contract: the
// search line shows the query and position, appears in the rendered frame, and
// disappears when the search closes.
func TestHistorySearchRendersLineAndReservesRow(t *testing.T) {
	m := seedSearchModel(t, "deploy the server")
	m = apply(t, m, ctrlKey('r'))
	for _, r := range "deploy" {
		m = apply(t, m, runeKey(r))
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "reverse-search: deploy") {
		t.Errorf("rendered frame missing the search line:\n%s", view)
	}
	if !strings.Contains(view, "1/1") {
		t.Errorf("rendered frame missing the match position:\n%s", view)
	}
	if got := m.histSearchRows(); got != 1 {
		t.Errorf("histSearchRows = %d, want 1 while searching", got)
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.histSearchRows(); got != 0 {
		t.Errorf("histSearchRows = %d, want 0 after cancel", got)
	}
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "reverse-search:") {
		t.Errorf("search line must disappear after cancel:\n%s", view)
	}
}

// TestHistorySearchIgnoredWhileRunning confirms Ctrl+R does nothing mid-run:
// the composer is disabled then, so there is no draft to search from.
func TestHistorySearchIgnoredWhileRunning(t *testing.T) {
	m := seedSearchModel(t, "old prompt")
	m.running = true
	m = apply(t, m, ctrlKey('r'))
	if m.histSearch != nil {
		t.Error("ctrl+r while running must not open a search session")
	}
}
