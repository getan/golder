package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/diagtrace"
)

// Only classify control keys. Never log Key.String(), Text, runes, command
// names or clipboard data: even custom slash-command names can be private.
func traceControlKey(key tea.KeyPressMsg) string {
	switch key.Code {
	case tea.KeyTab:
		return "tab"
	case tea.KeyEnter:
		return "enter"
	case tea.KeyEscape:
		return "escape"
	case 'c':
		if key.Mod.Contains(tea.ModCtrl) {
			return "ctrl_c"
		}
	case 'd':
		if key.Mod.Contains(tea.ModCtrl) {
			return "ctrl_d"
		}
	}
	return ""
}

func (m Model) traceCompletion(phase string) {
	if !diagtrace.Enabled() {
		return
	}
	diagtrace.Event("tui", "completion %s draft_bytes=%d active=%t picking=%t candidates=%d selected=%d width=%d height=%d running=%t quitting=%t",
		phase, len(m.input.Value()), m.menu.active, m.menu.picking(), len(m.menu.filtered), m.menu.selected, m.width, m.height, m.running, m.quitting)
}

func (m Model) traceExit(source string, accepted bool) {
	if diagtrace.Enabled() {
		diagtrace.Event("tui", "exit intent source=%s accepted=%t running=%t draft_bytes=%d", source, accepted, m.running, len(m.input.Value()))
	}
}

func programErrorClass(err error) string {
	switch {
	case errors.Is(err, tea.ErrProgramPanic):
		return "panic"
	case errors.Is(err, tea.ErrInterrupted):
		return "interrupted"
	default:
		// Preserve a wrapped I/O/context cause instead of hiding it behind
		// ErrProgramKilled. This does not identify an OS signal by itself.
		if class := diagtrace.ErrorClass(err); class != "other" {
			return class
		}
		if errors.Is(err, tea.ErrProgramKilled) {
			return "program_killed"
		}
		return "other"
	}
}
