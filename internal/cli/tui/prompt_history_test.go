package tui

// Tests for the global prompt history (history.jsonl): the lazy one-time merge
// into ↑/↓ browsing, and that submissions land on disk in their expanded form so
// a recalled entry is faithful and submittable.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/history"
)

// TestModelCrossSessionHistoryLazyMerge seeds the history file with prompts from
// an earlier session and verifies the first ↑ lazily merges them (nothing is read
// until the user actually browses), newest first, then continues into this
// session's own entries.
func TestModelCrossSessionHistoryLazyMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), history.FileName)
	for _, text := range []string{"older still", "from an earlier session"} {
		if err := history.Append(path, history.Entry{TS: time.Now().Unix(), Text: text}); err != nil {
			t.Fatalf("seed history: %v", err)
		}
	}

	m := apply(t, NewModel(Options{HistoryPath: path}), tea.WindowSizeMsg{Width: 80, Height: 12})
	// Lazy contract: construction and submissions never read the file.
	if m.histLoaded || len(m.history) != 0 {
		t.Fatalf("history must stay unloaded until first browse (loaded=%v len=%d)", m.histLoaded, len(m.history))
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "from an earlier session" {
		t.Errorf("first ↑ recalled %q, want the newest earlier-session prompt", got)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "older still" {
		t.Errorf("second ↑ recalled %q, want the older prompt", got)
	}

	// Back to the live draft, submit something new, and browse again: the new
	// entry comes first and the earlier-session window follows without
	// duplicating the just-recalled text.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Fatalf("↓ past newest should restore the empty draft, got %q", got)
	}
	for _, r := range "new prompt" {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "new prompt" {
		t.Errorf("↑ after submit recalled %q, want the new prompt", got)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "from an earlier session" {
		t.Errorf("↑ past the new prompt recalled %q, want the earlier-session prompt", got)
	}
}

// TestModelSubmitRecordsExpandedPrompt verifies the paste-placeholder fix: the
// history stores and browses the EXPANDED prompt, not the "[Pasted text #N]"
// token whose body is dropped after submit (recalling it would send the literal
// placeholder).
func TestModelSubmitRecordsExpandedPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), history.FileName)
	m := apply(t, NewModel(Options{HistoryPath: path}), tea.WindowSizeMsg{Width: 80, Height: 12})

	m.pastes = map[int]string{1: "line one\nline two"}
	placeholder := "[Pasted text #1 +2 lines]"
	for _, r := range placeholder {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	entries, err := history.Load(path, history.MaxEntries)
	if err != nil {
		t.Fatalf("Load history: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("history entries = %d (%+v), want exactly the submitted prompt", len(entries), entries)
	}
	if got := entries[0].Text; got != "line one\nline two" {
		t.Errorf("persisted text = %q, want the expanded paste body", got)
	}
	if strings.Contains(entries[0].Text, "Pasted text") {
		t.Errorf("persisted text still carries the placeholder: %q", entries[0].Text)
	}

	// ↑ recalls the submittable expanded text, not the dead placeholder (the
	// paste body map was cleared at submit).
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.input.Value(); got != "line one\nline two" {
		t.Errorf("↑ recalled %q, want the expanded paste body", got)
	}
}
