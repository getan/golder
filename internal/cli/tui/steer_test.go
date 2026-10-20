package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/runtime"
)

// TestModelMidRunEnterQueuesNotSubmits pins the mid-run composer contract:
// typing stays enabled while a run streams, and Enter queues the message for
// steering instead of starting a second run. The message leaves the composer,
// waits in the steer queue (rendered by the queued preview, not the transcript),
// and is recorded for ↑ recall.
func TestModelMidRunEnterQueuesNotSubmits(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	for _, r := range "steer left" {
		m = apply(t, m, runeKey(r))
	}
	if got := m.input.Value(); got != "steer left" {
		t.Fatalf("typing mid-run = %q, want the buffer to accept text", got)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.input.Value(); got != "" {
		t.Errorf("after queueing, input = %q, want cleared", got)
	}
	if got := m.steerQ.snapshot(); len(got) != 1 || got[0] != "steer left" {
		t.Errorf("steer queue = %v, want [steer left]", got)
	}
	if !m.running {
		t.Error("queueing must not end the in-flight run")
	}
	if joined := strings.Join(blockTexts(m.transcript), "\n"); strings.Contains(joined, "steer left") {
		t.Errorf("queued input must not appear as a submitted user block yet, transcript=%q", joined)
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != "steer left" {
		t.Errorf("queued input should be recorded for ↑ recall, history=%v", m.history)
	}
}

// TestModelMidRunSlashActionRuns covers the mid-run slash contract for a command
// that only reports or adjusts live state: it runs there and then, exactly as it
// does when idle. It is never queued (the model would receive the command as
// literal prose), and the refusal note from the old blanket rule must be gone.
func TestModelMidRunSlashActionRuns(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	for _, r := range "/help" {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.steerQ.snapshot(); len(got) != 0 {
		t.Errorf("slash commands must not queue, got %v", got)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("an executed action command should clear the composer, got %q", got)
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "/model") {
		t.Errorf("/help output should have rendered into the transcript:\n%s", joined)
	}
	if strings.Contains(joined, "slash commands are not available") {
		t.Errorf("the blanket mid-run refusal should be gone:\n%s", joined)
	}
}

// TestModelMidRunModelPickerDispatches pins the command the report named: bare
// /model typed while a run streams reaches its picker rather than the old
// refusal note. A session-less model answers "No active session." — which is
// what makes the assertion meaningful: the line was dispatched as the command it
// is, so a live session gets the picker (the same call, guarded one level in).
func TestModelMidRunModelPickerDispatches(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	for _, r := range "/model" {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.steerQ.snapshot(); len(got) != 0 {
		t.Errorf("/model must not be queued as steering text, got %v", got)
	}
	// The composer keeps the line here — bare /model opens a picker that clears
	// it on confirm — so the note, not the buffer, is what proves dispatch.
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "No active session.") {
		t.Errorf("bare /model should reach its picker path, transcript:\n%s", joined)
	}
}

// TestModelMidRunPromptCommandRefused pins the one command class that must not
// run mid-run: a prompt (Expand) command starts an agent turn, and starting one
// would tear the in-flight run down. The refusal keeps the composer intact so
// the line can be run after an interrupt, and says why.
func TestModelMidRunPromptCommandRefused(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.slash.AddUser(runtime.SlashCommand{
		Name:   "greet",
		Expand: func(args string) string { return "hello " + args },
	})
	var ran string
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		ran = prompt
		ch := make(chan tea.Msg, 1)
		return ch, func() tea.Msg { return nil }
	}
	m.running = true
	for _, r := range "/greet world" {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if ran != "" {
		t.Errorf("a prompt command must not start a second run mid-run; ran %q", ran)
	}
	if got := m.steerQ.snapshot(); len(got) != 0 {
		t.Errorf("a slash line must never be queued as steering text, got %v", got)
	}
	if got := m.input.Value(); got != "/greet world" {
		t.Errorf("refused slash input = %q, want the buffer kept", got)
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "/greet starts a new run") {
		t.Errorf("missing refusal note, transcript:\n%s", joined)
	}
	// The invocation must not be echoed as a user turn it never was.
	if strings.Contains(joined, "hello world") {
		t.Errorf("refused command must not reach the transcript as a run:\n%s", joined)
	}
}

// TestModelQueuedPreviewRendersAndReservesRows drives the codex-style pending
// preview: queued messages render above the composer (header + one ↳ line
// each), the transcript shrinks by exactly those rows, and the frame keeps its
// terminal height.
func TestModelQueuedPreviewRendersAndReservesRows(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	m.spinner.begin(time.Now(), "")
	m.relayout()
	base := m.transcript.viewportHeight()

	for _, text := range []string{"first queued", "second queued"} {
		for _, r := range text {
			m = apply(t, m, runeKey(r))
		}
		m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	}

	if got := m.transcript.viewportHeight(); got != base-3 {
		t.Errorf("viewport height with preview = %d, want %d (base %d - header - 2 rows)", got, base-3, base)
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Messages to be submitted after the next tool call (2):") {
		t.Errorf("preview header missing:\n%s", view)
	}
	if !strings.Contains(view, "↳ first queued") || !strings.Contains(view, "↳ second queued") {
		t.Errorf("queued messages missing from the preview:\n%s", view)
	}
	if got := strings.Count(m.View().Content, "\n"); got != 19 {
		t.Errorf("newline count = %d, want 19 (20 rows) with the preview shown", got)
	}
}

// TestModelSteerInjectedAddsUserBlock verifies the delivery echo: when the loop
// drains queued input and reports it via steerInjectedMsg, the message becomes
// a user turn in the transcript at that moment (and the preview row disappears).
func TestModelSteerInjectedAddsUserBlock(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	m.steerQ.push("do it differently")
	texts := m.steerQ.drain()

	m = apply(t, m, steerInjectedMsg{texts: texts})

	if joined := strings.Join(blockTexts(m.transcript), "\n"); !strings.Contains(joined, "do it differently") {
		t.Errorf("injected steer missing from the transcript: %q", joined)
	}
	if got := m.steerQ.snapshot(); len(got) != 0 {
		t.Errorf("queue should be empty after injection, got %v", got)
	}
	if view := stripANSI(m.View().Content); strings.Contains(view, "↳ do it differently") {
		t.Errorf("preview should clear once injected:\n%s", view)
	}
}

// TestModelRunEndSendsQueuedNext verifies the never-lose-input fallback: a
// message that reaches run end still queued (typed after the loop's last drain,
// or left by an interrupt) starts as the very next turn, exactly one per run.
func TestModelRunEndSendsQueuedNext(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	var prompts []string
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		prompts = append(prompts, prompt)
		return make(chan tea.Msg, 1), nil
	}
	m.running = true
	m.steerQ.push("second turn")

	m = apply(t, m, runEndMsg{})

	if len(prompts) != 1 || prompts[0] != "second turn" {
		t.Fatalf("prompts after run end = %v, want [second turn]", prompts)
	}
	if !m.running {
		t.Error("the queued message should have started a new run")
	}
	if joined := strings.Join(blockTexts(m.transcript), "\n"); !strings.Contains(joined, "second turn") {
		t.Errorf("queued turn missing from the transcript: %q", joined)
	}
	if got := m.steerQ.snapshot(); len(got) != 0 {
		t.Errorf("queue should be drained by the next-run start, got %v", got)
	}
}

// TestModelInterruptWithQueueSendsNow verifies Esc semantics with pending input:
// the running turn is interrupted and the queued message is announced as the
// next turn (delivered by runEnd), rather than lingering in the preview.
func TestModelInterruptWithQueueSendsNow(t *testing.T) {
	m := NewModel(Options{})
	interrupted := false
	m.interruptFn = func() { interrupted = true }
	m.running = true
	m.steerQ.push("queued instruction")

	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !interrupted {
		t.Fatal("esc while running with queued input must interrupt")
	}
	if joined := strings.Join(blockTexts(got.(Model).transcript), "\n"); !strings.Contains(joined, "queued input will be sent next") {
		t.Errorf("interrupt notice should mention the queued input, transcript=%q", joined)
	}
	if len(got.(Model).steerQ.snapshot()) != 1 {
		t.Error("the queue must survive the interrupt so runEnd can deliver it")
	}
}

// TestModelMidRunContextReportsRefused pins what stays refused, and why: /status
// and /memory read agentCtx.Messages, which the run goroutine owns until the run
// settles, so reading it from the TUI goroutine mid-run would race the loop's
// appends. The refusal names the command, keeps the composer (the line runs
// after an Esc), and points at the gauge that does track the figure mid-run.
func TestModelMidRunContextReportsRefused(t *testing.T) {
	for _, name := range []string{"/status", "/memory"} {
		t.Run(name, func(t *testing.T) {
			m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
			m.running = true
			for _, r := range name {
				m = apply(t, m, runeKey(r))
			}
			m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

			if got := m.input.Value(); got != name {
				t.Errorf("refused %s should keep the line for a later run, got %q", name, got)
			}
			joined := strings.Join(blockTexts(m.transcript), "\n")
			if !strings.Contains(joined, "Interrupt the current run first, then "+name) {
				t.Errorf("%s should say why it is refused, transcript:\n%s", name, joined)
			}
			// The refusal must land before the invocation is echoed as a turn.
			if strings.Contains(joined, "context:") || strings.Contains(joined, "persistent memory:") {
				t.Errorf("%s must not have rendered a report mid-run:\n%s", name, joined)
			}
		})
	}
}

// TestModelMidRunExitRefused pins the quit path: /exit mid-run is refused rather
// than quitting, because the in-flight turn is persisted when the run settles —
// quitting first would drop it. It stays available once idle.
func TestModelMidRunExitRefused(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	next, cmd := m.runSlash("/exit")
	m = next.(Model)
	if m.quitting {
		t.Error("/exit mid-run must not quit")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Error("/exit mid-run must not return tea.Quit")
		}
	}
	if joined := strings.Join(blockTexts(m.transcript), "\n"); !strings.Contains(joined, "then /exit") {
		t.Errorf("the refusal should say how to exit, transcript:\n%s", joined)
	}

	// Idle, the same command quits as before.
	idle := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	next, cmd = idle.runSlash("/exit")
	if !next.(Model).quitting {
		t.Error("/exit while idle should still quit")
	}
	if cmd == nil {
		t.Fatal("/exit while idle should return tea.Quit")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Error("/exit while idle should return tea.Quit")
	}
}

// TestMidRunCommandsDoNotReadRunOwnedContext is the invariant behind the mid-run
// gate, checked by the race detector: none of the commands admitted while a run
// is in flight may read agentCtx.Messages, because the run goroutine owns that
// list until the run settles (see the runEndMsg handler). A goroutine stands in
// for the loop, appending to the list throughout, while the TUI side runs every
// path the gate now admits; under -race a read from the tea goroutine is reported
// as the race it is.
func TestMidRunCommandsDoNotReadRunOwnedContext(t *testing.T) {
	s, _, err := newRunSessionWithStore(newTestStore(t), Options{Approve: true})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := NewModel(Options{}).withSession(s, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.running = true

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				s.agentCtx.Messages = append(s.agentCtx.Messages, agentcore.UserMessage{
					RoleField: agentcore.RoleUser,
					Content:   agentcore.ContentList{agentcore.NewTextContent("chatter")},
				})
			}
		}
	}()

	// The composer keys: history browsing, the popup and Tab completion.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = typeInto(t, m, "/mod").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	// The commands, Enter-driven but with the returned Cmd dropped: the async
	// catalog fetch a picker may start is not what this test is about. Each line
	// starts from a clean composer: a refused command deliberately keeps its text
	// for a later run, and a picker left open would capture the next Enter.
	enter := func(line string) {
		m.input.Clear()
		m.menu.close()
		m = typeInto(t, m, line).(Model)
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
	}
	enter("/help")
	enter("/status")
	enter("/memory")
	enter("/exit")
	enter("/model")
	enter("/permissions")

	close(stop)
	<-done

	// The reports and the quit are the commands that must still be refused; naming
	// them here keeps the invariant honest if a guard is dropped later.
	joined := strings.Join(blockTexts(m.transcript), "\n")
	for _, want := range []string{
		"Interrupt the current run first, then /status",
		"Interrupt the current run first, then /memory",
		"Interrupt the current run first, then /exit",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing mid-run refusal %q, transcript:\n%s", want, joined)
		}
	}
}
