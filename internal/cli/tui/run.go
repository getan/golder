package tui

import (
	"errors"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/diagtrace"
	"github.com/getan/golder/internal/terminalstate"
)

// Run starts the full-screen TUI and blocks until the user quits (Ctrl+C /
// Ctrl+D) or the program errors. It is the alt-screen counterpart to repl.Run:
// cmd/golder's dispatch calls it on the (no prompt + TTY + no --no-tui) path and
// maps its error to the process exit code. The alt-screen is entered/left via
// the View returned by the root Model, so a clean return here restores the
// terminal to the user's prior scrollback.
func Run(opts Options) (runErr error) {
	// main initializes this before any startup work. Calling it again here is
	// idempotent and keeps direct tui.Run callers (tests, embedders) working.
	if err := diagtrace.InitFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "golder: TUI timing trace disabled: %v\n", err)
	}
	trace := newTimingTrace(diagtrace.Process())
	defer trace.close()
	terminal, err := terminalstate.Capture(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, terminal.Restore()) }()
	if trace != nil {
		defer trace.trace.Recover("tui.run")
		restore := uv.SetInputObserver(func(e uv.InputObservation) {
			diagtrace.Event("input", "%s bytes=%d events=%d consumed=%d type=%s failed=%t duration=%s", e.Phase, e.Bytes, e.Events, e.Consumed, e.Type, e.Failed, e.Duration)
		})
		defer restore()
	}
	trace.event("session begin")
	// Assemble the session (store, resume-or-fresh context, live config) before
	// entering the alt-screen, mirroring repl.Run: a store/resume failure is a
	// clean pre-launch error rather than a broken interactive session.
	s, history, err := newRunSession(opts)
	trace.event("session end failed=%t", err != nil)
	if err != nil {
		return err
	}
	// Kill still-running shell sessions (bash_ids) on exit so long-running
	// commands are not orphaned when the TUI quits.
	defer run.KillShellSessions(opts.Tools)
	// Clear this pane's agent in the multiplexer around it (tuios or herdr) on
	// the way out, and flush the release before the process exits. Deferred
	// here rather than at the three quit call sites because every path out of
	// the program returns through this line.
	defer s.hostClose()
	prepareTerminalPalette(startupQueriesDisabled())
	trace.event("model begin")
	m := NewModel(opts).withSession(s, history)
	trace.event("model end trust_pending=%t logo_running=%t", m.pendingTrust, m.logoRunning)
	model, programOpts := trace.program(m)
	if m.noTerminalQueries {
		var sink io.Writer = os.Stdout
		if trace != nil {
			sink = timingOutput{File: os.Stdout, trace: trace}
		}
		filtered := &queryOutput{File: os.Stdout, sink: sink}
		programOpts = append(programOpts, tea.WithOutput(filtered))
		defer func() { trace.event("query output flush failed=%t", filtered.Flush() != nil) }()
	}
	p := tea.NewProgram(model, programOpts...)
	trace.event("program constructed")
	trace.event("program begin")
	_, err = p.Run()
	trace.event("program end failed=%t error_class=%s", err != nil, programErrorClass(err))
	return err
}
