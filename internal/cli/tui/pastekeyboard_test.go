package tui

// A paste can leave the terminal's keyboard-enhancement flags lost (a
// multiplexer that pushes/pops them around the paste is the case this covers).
// Shift+Enter then arrives as a bare Enter and submits instead of inserting a
// newline, so every paste re-asserts the flags the TUI requested.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPasteReassertsKeyboardProtocol(t *testing.T) {
	m := NewModel(Options{})
	got, cmd := m.Update(tea.PasteMsg{Content: "hello"})
	if cmd == nil {
		t.Fatal("a paste should return the keyboard-protocol reassert command")
	}
	m = got.(Model)
	if m.input.Value() != "hello" {
		t.Fatalf("pasted value = %q, want hello", m.input.Value())
	}

	var check func(tea.Msg) bool
	check = func(msg tea.Msg) bool {
		switch v := msg.(type) {
		case tea.RawMsg:
			s, _ := v.Msg.(string)
			return strings.Contains(s, "\x1b[=1;1u") && strings.Contains(s, "\x1b[>4;2m")
		case tea.BatchMsg:
			for _, c := range v {
				if check(c()) {
					return true
				}
			}
		}
		return false
	}
	if !check(cmd()) {
		t.Fatal("paste command does not re-assert the Kitty/modifyOtherKeys flags")
	}
}
