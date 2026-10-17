package tui

// Newline gestures reach the composer in terminal-specific encodings. These
// tests pin the shapes that used to fall through as unhandled keys: Shift+Enter
// carrying associated text (String() "\r") and xterm modifyOtherKeys' Ctrl+J
// code 10 (String() "ctrl+\n").

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestNewlineKeyEncodings(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"shift+enter with associated text", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift, Text: "\r"}},
		{"ctrl+j as code 10", tea.KeyPressMsg{Code: 10, Mod: tea.ModCtrl}},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
		{"ctrl+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 10})
			for _, r := range "ab" {
				m = apply(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			m = apply(t, m, tc.key)
			if got := m.input.Value(); got != "ab\n" {
				t.Errorf("input = %q, want %q", got, "ab\n")
			}
		})
	}
}

// TestPlainEnterStillSubmits guards the boundary: only modified Enter is a
// newline; the plain key keeps its submit meaning.
func TestPlainEnterStillSubmits(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 10})
	for _, r := range "ab" {
		m = apply(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.input.Value(); got != "" {
		t.Errorf("plain Enter left the draft %q; it must submit", got)
	}
}
