// This file wires the `!command` passthrough into the TUI: a locally run shell
// command (no model call, no approval, no sandbox) that opens its own card in
// the transcript while it streams and, once settled, joins the conversation as
// the same user-role <user_shell_command> record the REPL writes — so the model
// sees exactly what the user ran and what came back (codex parity). The model
// is the tea-side owner of the state; the command's blocking Wait runs inside a
// tea.Cmd goroutine and reports back through userShellDoneMsg.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/run"
)

// userShellPollInterval bounds how often a streaming passthrough command
// repaints its card: fast enough to feel live, slow enough that a chatty
// command's output does not force a reflow every few milliseconds.
const userShellPollInterval = 150 * time.Millisecond

// userShellState is one in-flight `!` command: the run itself, the card it
// renders into, its cancel hook (Ctrl+C / Esc kills the process), and the last
// output snapshot so a poll only reflows when the text actually changed.
type userShellState struct {
	gen    int
	run    *cli.UserShellRun
	card   *toolCard
	cancel context.CancelFunc
	last   string
}

// userShellTickMsg asks the model to repaint the card of the command with this
// gen from the command's buffered output (see refreshUserShell). It is only
// re-armed while the command is still tracked, so a settled command stops
// polling.
type userShellTickMsg struct{ gen int }

// userShellDoneMsg reports that the command with this gen settled (exited,
// timed out, or was aborted). outcome carries the codex-shaped result.
type userShellDoneMsg struct {
	gen     int
	outcome cli.UserShellOutcome
}

// submitUserShell handles a submitted "!command" line: it runs locally and is
// never sent to the model. A bare "!" shows the usage hint instead (codex's
// two-line help). The raw line — with the "!" — is what ↑ recall gets back, so
// recalling and re-running it works verbatim.
func (m Model) submitUserShell(line string) (tea.Model, tea.Cmd) {
	command := trimUserShellCommand(line)
	if command == "" {
		m.transcript.addSystem("Prefix a command with ! to run it locally\nExample: !ls")
		m.relayout()
		return m, nil
	}
	m.recordHistory(line)
	// The line was expanded at submit, so the stashed paste bodies and image
	// paths are consumed; drop them like submit does (ids keep climbing).
	m.pastes = make(map[int]string)
	m.images = make(map[int]string)
	m.input.Clear()
	m.menu.close()
	// The remote-control tee mirrors the conversation: show the command the
	// way the composer had it, prefixed with the bang.
	m.remoteEcho("\n! " + command + "\n")
	cmd := m.startUserShell(command)
	m.relayout()
	return m, cmd
}

// trimUserShellCommand strips the leading "!" and the surrounding whitespace.
func trimUserShellCommand(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, "!"))
}

// startUserShell launches `command` as the `!` passthrough and opens its card
// live (Running), mirroring how a tool call is announced before it executes.
// An unavailable session manager (the shell tool is disabled) fails closed
// with a system note instead of a card that could never settle.
func (m *Model) startUserShell(command string) tea.Cmd {
	runnable, err := cli.StartUserShell(run.ExecSessionsFromTools(m.opts.Tools), m.cwd, command)
	if err != nil {
		m.transcript.addSystem("shell: " + err.Error())
		m.relayout()
		return nil
	}
	card := &toolCard{
		name:     "shell",
		input:    map[string]any{"command": command},
		state:    cardRunning,
		expanded: defaultCardExpanded("shell"),
	}
	m.userShellGen++
	gen := m.userShellGen
	// The command is deliberately tied to the program, not to a run: it keeps
	// running while the agent works (teeing output into its card), and only
	// Ctrl+C / Esc — or quitting golder — kills it.
	ctx, cancel := context.WithCancel(context.Background())
	m.userShells = append(m.userShells, &userShellState{
		gen:    gen,
		run:    runnable,
		card:   card,
		cancel: cancel,
	})
	m.lastToolCard = card
	m.transcript.addToolCard(card)
	return tea.Batch(m.tickUserShell(gen), waitUserShell(ctx, runnable, gen))
}

// tickUserShell schedules the next streaming repaint of the command's card.
func (m Model) tickUserShell(gen int) tea.Cmd {
	return tea.Tick(userShellPollInterval, func(time.Time) tea.Msg {
		return userShellTickMsg{gen: gen}
	})
}

// waitUserShell runs the command to completion off the tea loop. Cancelling
// ctx (Ctrl+C / Esc, or program quit) kills the process and settles the
// command as aborted, mirroring codex.
func waitUserShell(ctx context.Context, run *cli.UserShellRun, gen int) tea.Cmd {
	return func() tea.Msg {
		return userShellDoneMsg{gen: gen, outcome: run.Wait(ctx)}
	}
}

// findUserShell returns the tracked state for gen, or nil when the command
// already settled (a stale tick or a duplicate done message).
func (m Model) findUserShell(gen int) *userShellState {
	for _, st := range m.userShells {
		if st.gen == gen {
			return st
		}
	}
	return nil
}

// removeUserShell drops the settled command's state.
func (m *Model) removeUserShell(gen int) {
	for i, st := range m.userShells {
		if st.gen == gen {
			m.userShells = append(m.userShells[:i], m.userShells[i+1:]...)
			return
		}
	}
}

// refreshUserShell repaints a streaming command's card from its buffered
// output. Only a changed snapshot reflows: an idle command must not re-lay the
// whole transcript every tick for nothing.
func (m Model) refreshUserShell(gen int) (tea.Model, tea.Cmd) {
	st := m.findUserShell(gen)
	if st == nil {
		return m, nil
	}
	out := cli.UserShellDisplayOutput(st.run.Output())
	if out != st.last {
		st.last = out
		st.card.response = parseToolResult(out)
		m.transcript.reflow()
	}
	return m, m.tickUserShell(gen)
}

// finishUserShell closes the command's card and records its settled result in
// the conversation. While an agent run is in flight the record is parked in
// runSession.pendingUserShell — the run goroutine owns agentCtx.Messages until
// the run drains, so appending here would race it; runEndMsg merges the buffer
// before persisting. Idle, the record is appended and persisted right away.
func (m Model) finishUserShell(msg userShellDoneMsg) (tea.Model, tea.Cmd) {
	st := m.findUserShell(msg.gen)
	if st == nil {
		return m, nil
	}
	m.removeUserShell(msg.gen)
	outcome := msg.outcome
	st.card.complete(outcome.ExitCode == 0, cli.UserShellDisplayOutput(outcome.Output), nil)
	m.lastToolCard = st.card
	if m.session != nil {
		record := cli.UserShellMessage(outcome.Command, outcome.ExitCode, outcome.Duration, outcome.Output)
		if m.running {
			m.session.pendingUserShell = append(m.session.pendingUserShell, record)
		} else {
			m.session.agentCtx.Messages = append(m.session.agentCtx.Messages, record)
			if err := m.session.persist(); err != nil {
				m.transcript.addSystem("Session save failed: " + err.Error())
			}
		}
		m.refreshContextUsage(m.session.agentCtx.Messages)
	}
	m.transcript.reflow()
	return m, nil
}

// cancelUserShells kills every in-flight passthrough command (Ctrl+C / Esc).
// Their done messages arrive shortly after and settle the cards as aborted.
func (m Model) cancelUserShells() {
	for _, st := range m.userShells {
		st.cancel()
	}
}

// replayUserShellCard rebuilds the settled card a `<user_shell_command>`
// record produces, so a resumed session (or a replayed transcript) shows
// `You ran …` with its output instead of dumping the raw record XML.
func replayUserShellCard(d cli.UserShellRecordData) *toolCard {
	card := &toolCard{
		name:     "shell",
		input:    map[string]any{"command": d.Command},
		expanded: defaultCardExpanded("shell"),
	}
	card.complete(d.ExitCode == 0, d.Output, nil)
	return card
}
