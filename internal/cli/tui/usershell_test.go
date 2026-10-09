// Tests for the `!command` passthrough glue: submitting runs the command
// locally (never as a model turn), the card reads `Running …` then `You ran …`,
// Ctrl+Z hands control to the terminal's job control, Ctrl+C kills the command
// without arming quit, and a record that settles while a run is in flight is
// parked until the run drains (the run goroutine owns the context while it
// runs).

package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/execsess"
)

// userShellTestModel builds a model whose tool set carries a real session
// manager, which is what a `!` command resolves to spawn its process on.
func userShellTestModel() Model {
	return NewModel(Options{
		Tools: []agentcore.AgentTool{&agenttool.BashTool{Sessions: execsess.NewManager()}},
	})
}

// submitUserShellLine types line into the composer and presses Enter, returning
// the submit Cmd (the passthrough's tick+wait batch, or nil for a hint).
func submitUserShellLine(t *testing.T, m Model, line string) (Model, tea.Cmd) {
	t.Helper()
	for _, r := range line {
		m = apply(t, m, runeKey(r))
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return got, cmd
}

// awaitUserShellDone runs the batch a passthrough submit returned and returns
// the settled done message. The batch's two commands (the poll tick and the
// blocking wait) are run concurrently so the tick's sleep never delays the
// settle.
func awaitUserShellDone(t *testing.T, cmd tea.Cmd) userShellDoneMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("submit returned no command")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("submit cmd returned %T, want tea.BatchMsg", msg)
	}
	done := make(chan userShellDoneMsg, 1)
	for _, c := range batch {
		go func(c tea.Cmd) {
			if m, ok := c().(userShellDoneMsg); ok {
				done <- m
			}
		}(c)
	}
	select {
	case d := <-done:
		return d
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the passthrough command to settle")
		return userShellDoneMsg{}
	}
}

// TestTrimUserShellCommand pins the bang-stripping contract: the leading "!"
// and surrounding whitespace go, internal spacing survives.
func TestTrimUserShellCommand(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"!ls", "ls"},
		{"!  ls -la  ", "ls -la"},
		{"!echo 'two  spaces'", "echo 'two  spaces'"},
		{"!", ""},
		{"!  ", ""},
	} {
		if got := trimUserShellCommand(tc.in); got != tc.want {
			t.Errorf("trimUserShellCommand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUserShellSubmitRunsLocally is the end-to-end passthrough test: a "!echo"
// line starts no run, never enters the steer queue, records the raw "!" line
// for ↑ recall, announces a Running card, and settles it into `You ran …` with
// the command's output.
func TestUserShellSubmitRunsLocally(t *testing.T) {
	// /bin/sh keeps the login-shell spawn fast and deterministic; the passthrough
	// itself prefers $SHELL, so pinning it makes the test independent of the
	// developer's zsh profile.
	t.Setenv("SHELL", "/bin/sh")
	m := userShellTestModel()
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	m, cmd := submitUserShellLine(t, m, "!echo user-shell-ok")

	if m.running {
		t.Fatal("a ! line must never start a model run")
	}
	if q := m.steerQ.snapshot(); len(q) != 0 {
		t.Fatalf("a ! line must never enter the steer queue, got %v", q)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("composer after submit = %q, want empty", got)
	}
	if h := m.history; len(h) == 0 || h[len(h)-1] != "!echo user-shell-ok" {
		t.Errorf("history tail = %v, want the raw ! line (↑ recall must replay it verbatim)", h)
	}
	if len(m.userShells) != 1 {
		t.Fatalf("tracked passthrough commands = %d, want 1", len(m.userShells))
	}
	card := m.userShells[0].card
	if card.state != cardRunning || card.title() != "Running echo user-shell-ok" {
		t.Fatalf("live card = state %v title %q, want running", card.state, card.title())
	}
	last := m.transcript.blocks[len(m.transcript.blocks)-1]
	if last.role != roleTool || last.card != card {
		t.Fatalf("last transcript block = %+v, want the live passthrough card", last)
	}

	m = apply(t, m, awaitUserShellDone(t, cmd))

	if len(m.userShells) != 0 {
		t.Fatalf("tracked passthrough commands after settle = %d, want 0", len(m.userShells))
	}
	if card.state != cardSuccess || card.title() != "You ran echo user-shell-ok" {
		t.Fatalf("settled card = state %v title %q, want success `You ran`", card.state, card.title())
	}
	if view := stripANSI(card.render(m.theme, 80)); !strings.Contains(view, "user-shell-ok") {
		t.Errorf("settled card render is missing the command output:\n%s", view)
	}
}

// TestUserShellBareBangShowsHint verifies a lone "!" explains the feature
// instead of running an empty command.
func TestUserShellBareBangShowsHint(t *testing.T) {
	m := userShellTestModel()
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	m, cmd := submitUserShellLine(t, m, "!")

	if cmd != nil {
		t.Fatal("a bare ! must not start a command")
	}
	if len(m.userShells) != 0 {
		t.Fatalf("tracked passthrough commands = %d, want 0", len(m.userShells))
	}
	last := m.transcript.blocks[len(m.transcript.blocks)-1]
	if last.role != roleSystem || !strings.Contains(last.text, "Prefix a command with !") {
		t.Fatalf("bare ! block = %+v, want the usage hint", last)
	}
	// The hint also documents the shape with a concrete example.
	if !strings.Contains(last.text, "Example: !ls") {
		t.Errorf("hint is missing the example line:\n%s", last.text)
	}
}

// TestUserShellMidRunParksUntilRunDrains covers the data-race guard: while a
// run is in flight the loop goroutine owns agentCtx.Messages, so the settled
// record is parked in pendingUserShell and only merged when the run ends.
func TestUserShellMidRunParksUntilRunDrains(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := userShellTestModel()
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})
	m.session = &runSession{agentCtx: &agentcore.AgentContext{}}
	m.running = true

	m, cmd := submitUserShellLine(t, m, "!echo parked")
	if q := m.steerQ.snapshot(); len(q) != 0 {
		t.Fatalf("! during a run must run beside the run, not queue as a steer, got %v", q)
	}
	m = apply(t, m, awaitUserShellDone(t, cmd))

	if len(m.session.agentCtx.Messages) != 0 {
		t.Fatal("record must not touch Messages while the run owns the context")
	}
	if len(m.session.pendingUserShell) != 1 {
		t.Fatalf("pendingUserShell = %d, want 1", len(m.session.pendingUserShell))
	}

	// This is what runEndMsg does before persisting.
	m.session.mergePendingUserShell()
	if n := len(m.session.agentCtx.Messages); n != 1 {
		t.Fatalf("merged messages = %d, want 1", n)
	}
	if m.session.pendingUserShell != nil {
		t.Fatalf("pendingUserShell = %v after merge, want cleared", m.session.pendingUserShell)
	}
	um, ok := m.session.agentCtx.Messages[0].(agentcore.UserMessage)
	if !ok {
		t.Fatalf("merged message is %T, want agentcore.UserMessage", m.session.agentCtx.Messages[0])
	}
	d, ok := cli.ParseUserShellRecord(agentcore.ContentToText(um.Content))
	if !ok {
		t.Fatalf("merged record does not parse as a user shell command:\n%s", agentcore.ContentToText(um.Content))
	}
	if d.Command != "echo parked" || d.ExitCode != 0 || !strings.Contains(d.Output, "parked") {
		t.Errorf("merged record = %+v, want `echo parked` with its output", d)
	}
}

// TestUserShellCtrlZSuspends verifies Ctrl+Z is bubbletea's suspend command in
// every state: the program releases the terminal, raises SIGTSTP, and `fg`
// resumes it.
func TestUserShellCtrlZSuspends(t *testing.T) {
	for _, running := range []bool{false, true} {
		m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
		m.running = running
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
		if _, ok := next.(Model); !ok {
			t.Fatalf("Update returned %T, want tui.Model", next)
		}
		if cmd == nil {
			t.Fatalf("ctrl+z (running=%v) returned no command", running)
		}
		if msg := cmd(); !msgIsSuspend(msg) {
			t.Fatalf("ctrl+z (running=%v) produced %T, want tea.SuspendMsg", running, msg)
		}
	}
}

// msgIsSuspend reports whether a message is bubbletea's suspend signal.
func msgIsSuspend(msg tea.Msg) bool {
	_, ok := msg.(tea.SuspendMsg)
	return ok
}

// TestUserShellCtrlCInterruptsCommandWithoutArmingQuit verifies one Ctrl+C is
// enough to kill an in-flight passthrough — and that the press is read as
// "stop the command", not as the first half of the quit chord.
func TestUserShellCtrlCInterruptsCommandWithoutArmingQuit(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := userShellTestModel()
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 20})

	m, cmd := submitUserShellLine(t, m, "!sleep 30")
	m = apply(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	if m.quitting {
		t.Fatal("Ctrl+C must not quit while a passthrough command is in flight")
	}
	if !m.quitArmedAt.IsZero() {
		t.Fatal("a Ctrl+C aimed at the passthrough command must not arm quit")
	}
	if len(m.userShells) != 1 {
		t.Fatalf("tracked commands = %d, want the command still settling", len(m.userShells))
	}
	card := m.userShells[0].card

	done := awaitUserShellDone(t, cmd)
	if !done.outcome.Aborted || done.outcome.ExitCode != -1 {
		t.Fatalf("outcome = %+v, want aborted with exit -1", done.outcome)
	}
	m = apply(t, m, done)
	if card.state != cardWarn {
		t.Fatalf("aborted card state = %v, want warn", card.state)
	}
}

// TestReplayUserShellCard verifies a persisted <user_shell_command> record
// replays as the same card the live command produced, not as raw XML.
func TestReplayUserShellCard(t *testing.T) {
	card := replayUserShellCard(cli.UserShellRecordData{
		Command:  "ls -la",
		ExitCode: 0,
		Output:   "total 0\ndrwxr-xr-x\n",
	})
	if card.name != "shell" || card.state != cardSuccess {
		t.Fatalf("replayed card = name %q state %v, want shell/success", card.name, card.state)
	}
	if got := card.title(); got != "You ran ls -la" {
		t.Errorf("replayed title = %q, want `You ran ls -la`", got)
	}
	if view := stripANSI(card.render(DefaultTheme(), 80)); !strings.Contains(view, "drwxr-xr-x") {
		t.Errorf("replayed card render is missing the recorded output:\n%s", view)
	}

	failed := replayUserShellCard(cli.UserShellRecordData{Command: "false", ExitCode: 1, Output: ""})
	if failed.state != cardWarn {
		t.Errorf("failed command card state = %v, want warn", failed.state)
	}
}
