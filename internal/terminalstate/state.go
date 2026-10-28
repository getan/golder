// Package terminalstate preserves the host's kernel TTY state across an
// embedded terminal UI, including failures before its event loop starts.
// It does not read input, close borrowed files or reset terminal protocols.
package terminalstate

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"

	"github.com/getan/golder/internal/diagtrace"
)

type saved struct {
	file  *os.File
	state *term.State
}

// Guard borrows terminal files; keeping them referenced prevents a finalizer
// from closing/reusing a descriptor before restoration. It never opens /dev/tty.
type Guard struct{ states []saved }

// Capture must run before a UI takes raw mode. Pipes and absent files are
// ignored; a terminal whose state cannot be saved fails before ownership changes.
func Capture(files ...*os.File) (Guard, error) {
	var g Guard
	seen := make(map[uintptr]bool)
	for _, file := range files {
		if file == nil {
			continue
		}
		fd := file.Fd()
		if seen[fd] || !term.IsTerminal(fd) {
			continue
		}
		seen[fd] = true
		state, err := term.GetState(fd)
		if err != nil {
			return Guard{}, fmt.Errorf("save terminal state: %w", err)
		}
		g.states = append(g.states, saved{file: file, state: state})
	}
	diagtrace.Event("terminal", "state saved files=%d", len(g.states))
	return g, nil
}

// Restore is a backstop for upstream Run early-error paths that leave ECHO,
// ICANON or ISIG disabled. A completed normal UI restoration is harmless to repeat.
// Failed network delivery of mouse/keyboard reset sequences is a separate issue.
func (g Guard) Restore() error {
	var errs []error
	for _, state := range g.states {
		if err := term.Restore(state.file.Fd(), state.state); err != nil {
			errs = append(errs, fmt.Errorf("restore terminal state: %w", err))
		}
	}
	err := errors.Join(errs...)
	diagtrace.Event("terminal", "state restore files=%d failed=%t error_class=%s", len(g.states), err != nil, diagtrace.ErrorClass(err))
	return err
}
