package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// TestModelMidRunSlashRefused verifies a "/command" typed mid-run is not queued
// (it would reach the model as literal text): it stays in the composer with a
// note, so the user can edit it or interrupt first.
func TestModelMidRunSlashRefused(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 20})
	m.running = true
	for _, r := range "/model" {
		m = apply(t, m, runeKey(r))
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.input.Value(); got != "/model" {
		t.Errorf("refused slash input = %q, want the buffer kept", got)
	}
	if got := m.steerQ.snapshot(); len(got) != 0 {
		t.Errorf("slash commands must not queue, got %v", got)
	}
	if joined := strings.Join(blockTexts(m.transcript), "\n"); !strings.Contains(joined, "slash commands are not available") {
		t.Errorf("missing refusal note, transcript=%q", joined)
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
