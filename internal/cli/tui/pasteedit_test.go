package tui

// Tests for Ctrl+G paste editing (pastepreview.go): the composer holds the
// body while editing, Enter/Ctrl+G saves, Esc cancels, and the draft/token
// pairing survives both paths.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func ctrlG(t *testing.T, m Model) Model {
	t.Helper()
	return apply(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
}

func TestPasteEditSaveUpdatesBodyAndToken(t *testing.T) {
	m := pasteInto(t, "old\nbody")
	m = ctrlG(t, m)
	if m.pasteEdit == nil || m.pasteEdit.id != 1 {
		t.Fatalf("ctrl+g did not start editing: %+v", m.pasteEdit)
	}
	if got := m.input.Value(); got != "old\nbody" {
		t.Fatalf("editor value = %q, want the paste body", got)
	}
	if m.pastePreview != nil {
		t.Fatal("preview should be hidden while editing")
	}

	m.input.SetValue("new\nbody\nlines")
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.pasteEdit != nil {
		t.Fatal("enter did not finish editing")
	}
	if got := m.pastes[1]; got != "new\nbody\nlines" {
		t.Fatalf("stored body = %q", got)
	}
	want := pastePlaceholder(1, "new\nbody\nlines")
	if got := m.input.Value(); got != want {
		t.Fatalf("draft = %q, want %q", got, want)
	}
}

func TestPasteEditCancelRestoresDraftAndBody(t *testing.T) {
	m := pasteInto(t, "old\nbody")
	draft := m.input.Value()
	m = ctrlG(t, m)
	m.input.SetValue("changed")
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.pasteEdit != nil {
		t.Fatal("esc did not finish editing")
	}
	if got := m.pastes[1]; got != "old\nbody" {
		t.Fatalf("stored body = %q, want the original", got)
	}
	if got := m.input.Value(); got != draft {
		t.Fatalf("draft = %q, want restored %q", got, draft)
	}
	if m.quitting {
		t.Fatal("esc while editing must cancel, not arm the quit")
	}
}

func TestPasteEditCtrlGCommits(t *testing.T) {
	m := pasteInto(t, "a\nb")
	m = ctrlG(t, m)
	m.input.SetValue("c\nd")
	m = ctrlG(t, m)

	if m.pasteEdit != nil {
		t.Fatal("ctrl+g did not finish editing")
	}
	if got := m.pastes[1]; got != "c\nd" {
		t.Fatalf("stored body = %q", got)
	}
}

func TestPasteEditKeepsOpenPreview(t *testing.T) {
	m := pasteInto(t, pasteLines(4))
	m = ctrlO(t, m)
	m = ctrlG(t, m)
	if m.pastePreview != nil {
		t.Fatal("preview should be hidden while editing")
	}
	m.input.SetValue(pasteLines(6))
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.pastePreview == nil || m.pastePreview.id != 1 {
		t.Fatalf("preview was not restored: %+v", m.pastePreview)
	}
	if got := len(pasteBodyLines(m.pastes[1])); got != 6 {
		t.Fatalf("stored body has %d lines, want 6", got)
	}
}

func TestPasteEditEmptyRemovesToken(t *testing.T) {
	m := pasteInto(t, "a\nb")
	m = ctrlG(t, m)
	m.input.Clear()
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if _, ok := m.pastes[1]; ok {
		t.Fatal("empty edit kept the paste body")
	}
	if got := m.pasteTokenCount(); got != 0 {
		t.Fatalf("draft still has %d paste tokens", got)
	}
	if m.pastePreview != nil {
		t.Fatal("preview survived an emptied paste")
	}
}

func TestPasteEditHintAndPreviewFooterMentionCtrlG(t *testing.T) {
	m := pasteInto(t, pasteLines(3))
	m = ctrlG(t, m)
	hint := stripTCardANSI(m.pasteEditHintLine(m.width))
	for _, want := range []string{"editing", "enter", "esc"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("edit hint %q does not name %q", hint, want)
		}
	}

	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m = ctrlO(t, m)
	block := m.pastePreviewLines(m.width)
	footer := stripTCardANSI(block[len(block)-1])
	for _, want := range []string{"ctrl+o", "ctrl+g"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("preview footer %q does not name %q", footer, want)
		}
	}
}
