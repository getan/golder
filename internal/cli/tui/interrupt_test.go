package tui

// Tests for the unified Ctrl+C feedback: every branch acknowledges the press,
// repeated presses during one run are idempotent, and the settlement prints the
// same single line whether or not the aborting turn-end won the cancellation
// race. The behavior these pin was the complaint: the same keystroke read as
// five different things (or nothing) depending on where the run happened to be.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
)

// abortedTurn is the terminal assistant message an interrupted turn carries.
func abortedTurn() agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		StopReason: agentcore.StopReasonAborted,
	}
}

// TestModelInterruptIsIdempotent: the first press during a run cancels and says
// so; further presses are absorbed. Before this, pressing again because the run
// had not visibly stopped printed the notice a second time, which read as "the
// key did nothing".
func TestModelInterruptIsIdempotent(t *testing.T) {
	m := NewModel(Options{})
	calls := 0
	m.interruptFn = func() { calls++ }
	m.running = true

	next, _ := m.Update(keyPress("ctrl+c"))
	m = next.(Model)
	next, _ = m.Update(keyPress("ctrl+c"))
	m = next.(Model)
	next, _ = m.Update(keyPress("esc"))
	m = next.(Model)

	if calls == 0 {
		t.Fatal("the first press must request the interrupt")
	}
	if got := strings.Count(strings.Join(blockTexts(m.transcript), "\n"), ui.InterruptingNotice); got != 1 {
		t.Errorf("interrupt notice printed %d times, want exactly 1", got)
	}
}

// TestModelInterruptStateResetsPerRun: the idempotency and settlement flags
// belong to one run, so the next run interrupts and reports normally.
func TestModelInterruptStateResetsPerRun(t *testing.T) {
	m := NewModel(Options{})
	m.running = true
	m.interruptFn = func() {}
	next, _ := m.Update(keyPress("ctrl+c"))
	m = next.(Model)
	if !m.interruptRequested {
		t.Fatal("a press during a run should record the interrupt")
	}

	m = apply(t, m, runEndMsg{err: context.Canceled})
	if m.interruptRequested || m.interruptNoticePrinted {
		t.Error("run end must clear the per-run interrupt state")
	}

	// The next run interrupts again rather than being absorbed.
	m.running = true
	next, _ = m.Update(keyPress("ctrl+c"))
	m = next.(Model)
	if !m.interruptRequested {
		t.Error("the next run must accept a fresh interrupt")
	}
}

// TestModelInterruptSettlementOneLine: whichever way the cancellation race goes,
// the transcript carries exactly one settlement line. The aborting turn-end
// event may or may not reach the UI (EventStream.Emit drops it when the cancel
// wins), so the settlement is decided at run end instead.
func TestModelInterruptSettlementOneLine(t *testing.T) {
	cases := []struct {
		name  string
		run   func(t *testing.T, m Model) Model
		wantN int
	}{
		{
			// The turn-end event lost the race: nothing said it yet.
			name:  "turn-end dropped",
			run:   func(t *testing.T, m Model) Model { return apply(t, m, runEndMsg{err: context.Canceled}) },
			wantN: 1,
		},
		{
			// The event won the race (the aborted-turn branch printed it).
			name: "turn-end delivered",
			run: func(t *testing.T, m Model) Model {
				m = apply(t, m, turnEndMsg{msg: abortedTurn()})
				return apply(t, m, runEndMsg{err: context.Canceled})
			},
			wantN: 1,
		},
		{
			// A cancelled ctx with a nil error: the run settled cleanly after the
			// interrupt, which is the common shape now that a recorded result
			// outranks the cancelled context.
			name:  "clean settle after interrupt",
			run:   func(t *testing.T, m Model) Model { return apply(t, m, runEndMsg{err: nil}) },
			wantN: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(Options{})
			m.running = true
			m.interruptFn = func() {}
			next, _ := m.Update(keyPress("ctrl+c"))
			m = tc.run(t, next.(Model))
			if got := strings.Count(strings.Join(blockTexts(m.transcript), "\n"), ui.InterruptNotice); got != tc.wantN {
				t.Errorf("settlement printed %d time(s), want %d:\n%s",
					got, tc.wantN, strings.Join(blockTexts(m.transcript), "\n"))
			}
		})
	}
}

// TestModelRunEndKeepsRealErrors: a genuine failure is still reported as one —
// the interrupt wording must not swallow a provider error.
func TestModelRunEndKeepsRealErrors(t *testing.T) {
	m := NewModel(Options{})
	m.running = true
	m = apply(t, m, runEndMsg{err: errors.New("upstream 500")})
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "upstream 500") {
		t.Errorf("a real error must be surfaced, got %q", joined)
	}
	if strings.Contains(joined, ui.InterruptNotice) {
		t.Errorf("a real error must not be reported as an interrupt: %q", joined)
	}
}

// TestModelInterruptPrintsWhileIdleAgain: after a run ends, an idle Ctrl+C goes
// back to the arm/quit path (its own notice), not the interrupt path.
func TestModelInterruptPrintsWhileIdleAgain(t *testing.T) {
	m := NewModel(Options{})
	m.running = true
	m.interruptFn = func() {}
	next, _ := m.Update(keyPress("ctrl+c"))
	m = apply(t, next.(Model), runEndMsg{err: context.Canceled})

	next, _ = m.Update(keyPress("ctrl+c"))
	m = next.(Model)
	if m.quitArmedAt.IsZero() {
		t.Error("an idle Ctrl+C after a run should arm the quit again")
	}
	if joined := strings.Join(blockTexts(m.transcript), "\n"); !strings.Contains(joined, ui.QuitArmedNotice) {
		t.Errorf("idle press should print the quit notice, got %q", joined)
	}
}
