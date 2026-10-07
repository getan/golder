package tui

// Tests for the session-op interrupt path (/compact, /dream) and the per-run
// tool-card index reset. Both close the same failure family as the approval
// pump fix: state that stays "running" (or a call that resolves to a finished
// card) while the user has no way to see or stop what is happening.

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/judge"
)

// TestSessionOpCtrlCCancels: /compact and /dream run off the tea loop with no
// event channel of their own; Ctrl+C while one runs must cancel its context.
// Before the fix the interrupt only touched a stale run cancel, the UI printed
// "interrupting" and the op kept going — the TUI could neither stop it nor
// quit while it ran.
func TestSessionOpCtrlCCancels(t *testing.T) {
	m := NewModel(Options{})
	m.session = &runSession{}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	next, cmd := m.startSessionOp("/compact", "Compacting conversation", func(ctx context.Context) tea.Cmd {
		return func() tea.Msg {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return rebuildDoneMsg{label: "compact", summary: "compact cancelled"}
		}
	})
	m = next.(Model)
	if !m.running {
		t.Fatal("the session op must put the model in the running state")
	}
	// The tea runtime runs a returned Cmd (and each element of a Batch) on its
	// own goroutine; do the same so the op's body actually starts.
	go func() {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			for _, sub := range batch {
				if sub != nil {
					go sub()
				}
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the session op never started")
	}

	got, _ := m.interruptOrQuit()
	m = got.(Model)
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+C did not cancel the session op")
	}
}

// TestSessionOpDoneClearsState: the op's done message must clear the running
// state and the registered cancel, so the next submit starts normally and a
// later Ctrl+C arms quit instead of re-interrupting a finished op.
func TestSessionOpDoneClearsState(t *testing.T) {
	m := NewModel(Options{})
	m.session = &runSession{}
	next, _ := m.startSessionOp("/dream", "Dreaming (consolidating memory)", func(ctx context.Context) tea.Cmd {
		return func() tea.Msg { return dreamDoneMsg{summary: "nothing to do"} }
	})
	m = next.(Model)
	if m.sessionOpCancel == nil {
		t.Fatal("a running session op must register its cancel")
	}

	got, _ := m.Update(dreamDoneMsg{summary: "nothing to do"})
	m = got.(Model)
	if m.running {
		t.Error("the done message must leave the running state")
	}
	if m.sessionOpCancel != nil {
		t.Error("the done message must clear the session-op cancel")
	}
}

// TestSessionOpHandsOffQueuedInput: the composer stays editable while
// /compact or /dream runs, so a message typed then sits in the steer queue.
// The op's done message must start it as the next turn instead of stranding
// it until the user sends another prompt.
func TestSessionOpHandsOffQueuedInput(t *testing.T) {
	m := NewModel(Options{})
	m.session = &runSession{}
	m.steerQ.push("next question")
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		if prompt != "next question" {
			t.Fatalf("started prompt = %q, want the queued message", prompt)
		}
		return make(chan tea.Msg, 1), nil
	}

	got, cmd := m.Update(rebuildDoneMsg{label: "compact", summary: "compacted"})
	m = got.(Model)
	if !m.running {
		t.Fatal("the queued message must be started as the next run")
	}
	if cmd == nil {
		t.Fatal("starting the queued message must return a command")
	}
}

// TestBeginRunResetsToolCards: a new run must start with a fresh tool-card
// index. A provider that recycles tool-call ids across runs would otherwise
// resolve the new call against a finished card — the new call's transcript
// block silently never appears and the old card's aborted state stays on
// screen.
func TestBeginRunResetsToolCards(t *testing.T) {
	m := NewModel(Options{})
	old := &toolCard{id: "call_1", name: "bash", state: cardWarn}
	m.toolCards["call_1"] = old
	m.lastToolCard = old

	ch := make(chan tea.Msg, 1)
	m.beginRun(ch)
	if len(m.toolCards) != 0 || m.lastToolCard != nil {
		t.Fatal("beginRun must start a fresh tool-card index")
	}
	if !m.running || m.runCh != ch {
		t.Fatal("beginRun must arm the run channel and the running state")
	}
}

// TestRunEndClearsApprovalChannel: the approval channel must not outlive its
// run. While it points at a dead run channel, a gate firing outside a run
// would park forever waiting for a UI that is no longer pumping; nil makes
// confirmApproval fail closed instead.
func TestRunEndClearsApprovalChannel(t *testing.T) {
	s := &runSession{approvalCh: make(chan tea.Msg, 1)}
	m := NewModel(Options{})
	m.session = s
	m.running = true
	m.runCh = make(chan tea.Msg, 1)
	m.sideRun = true // the side-run path skips persistence on this bare session

	got, _ := m.Update(runEndMsg{})
	m = got.(Model)
	if m.running {
		t.Fatal("runEndMsg must leave the running state")
	}
	if s.approvalCh != nil {
		t.Error("runEndMsg must clear the session's approval channel")
	}
}

// TestNewRunChannelWiresNotesAndApproval locks the shared starter: every run
// type (/prompt, /btw, /goal) must build its channel through newRunChannel so
// notes and approval dialogs target the channel that is actually being pumped.
// A run that skipped the wiring would send its first confirmation to a
// previous run's dead channel and stall the tool call forever.
func TestNewRunChannelWiresNotesAndApproval(t *testing.T) {
	s := &runSession{notes: run.NewReviewNotes()}
	ch := s.newRunChannel()
	if s.approvalCh != ch {
		t.Fatal("newRunChannel must register the channel for approval dialogs")
	}
	s.notes.Emit(judge.Note{Kind: judge.NoteApproved})
	select {
	case msg := <-ch:
		if _, ok := msg.(judgeNoteMsg); !ok {
			t.Fatalf("note handler published %T, want judgeNoteMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("newRunChannel must wire the review-note handler")
	}
}
