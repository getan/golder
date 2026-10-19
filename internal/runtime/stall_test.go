package runtime

// A turn that produces nothing the person can act on — the model reasoned and
// then ended its turn without an answer and without a tool call — used to end the
// RUN: the person saw the last tool card and nothing else, with no error to
// explain it. These tests drive that shape through the real loop (the faux
// provider is the project's core test seam) and pin both halves of the answer:
// the loop asks the model to carry on, and when the budget is spent it stops with
// the reason stated instead of going quiet.

import (
	"context"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/provider"
)

// thinkingTurn scripts a turn whose only content is a reasoning block.
func thinkingTurn(thinking string) fauxTurn {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	withThinking := partial
	withThinking.Content = agentcore.ContentList{agentcore.NewThinkingContent(thinking)}
	final := withThinking
	final.StopReason = agentcore.StopReasonEndTurn
	return fauxTurn{
		provider.StreamStartEvent{Partial: partial},
		provider.StreamThinkingEvent{Partial: withThinking},
		provider.StreamDoneEvent{Message: final},
	}
}

// emptyTurn scripts a turn with no content at all: the shape the provider layer
// already retries once of its own accord, and which the loop must therefore NOT
// treat as a stall — continuing a completion that never arrived would double the
// retry rather than ask the model to finish a thought.
func emptyTurn() fauxTurn {
	partial := agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}
	final := partial
	final.StopReason = agentcore.StopReasonEndTurn
	return fauxTurn{
		provider.StreamStartEvent{Partial: partial},
		provider.StreamDoneEvent{Message: final},
	}
}

// stallRun drives one run to completion and returns its turn_end events in order
// plus the messages the run produced (the run's own additions, not the seeded
// prompt, matching every other seam test).
func stallRun(t *testing.T, cfg RunConfig) ([]agentcore.TurnEndEvent, []agentcore.AgentMessage) {
	t.Helper()
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("start")}},
	}}
	s := agentLoop(context.Background(), agentCtx, cfg)
	var turns []agentcore.TurnEndEvent
	for ev := range s.Events() {
		if e, ok := ev.(agentcore.TurnEndEvent); ok {
			turns = append(turns, e)
		}
	}
	msgs, err := s.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	return turns, msgs
}

// TestStalledTurnCarriesOn: one reasoning-only turn does not end the run. The
// model is told what its previous turn was missing and gets another chance, which
// is the difference between a task that stalls and a task that finishes.
func TestStalledTurnCarriesOn(t *testing.T) {
	p := &fauxProvider{
		name: "faux",
		turns: []fauxTurn{
			thinkingTurn("let me read one more file before answering"),
			textTurn("here is the answer"),
		},
	}
	turns, msgs := stallRun(t, newFauxRunCfg(p))

	if p.callCount() != 2 {
		t.Fatalf("provider called %d times, want 2 (the stalled turn plus one continuation)", p.callCount())
	}
	if len(turns) != 2 {
		t.Fatalf("turn_end events = %d, want 2", len(turns))
	}
	if turns[0].Stalled != 1 || turns[0].StallLimit != stallContinueMax {
		t.Errorf("first turn_end = stalled %d/%d, want 1/%d", turns[0].Stalled, turns[0].StallLimit, stallContinueMax)
	}
	if turns[1].Stalled != 0 {
		t.Errorf("the recovered turn must be an ordinary turn, got stalled %d", turns[1].Stalled)
	}

	// The context carries the stall and the nudge, in order: the model cannot see
	// that its reasoning never became an answer, so the loop has to say so.
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (stalled turn, continuation, answer)", len(msgs))
	}
	if _, ok := msgs[1].(agentcore.UserMessage); !ok {
		t.Fatalf("msgs[1] should be the continuation user message, got %T", msgs[1])
	}
	if a, ok := msgs[2].(agentcore.AssistantMessage); !ok || textContentOf(a.Content) != "here is the answer" {
		t.Errorf("msgs[2] should be the recovered answer, got %T %+v", msgs[2], msgs[2])
	}
}

// TestStalledTurnStopsAfterTheLimit: the loop is bounded. A model that stalls
// every time stops the run after the budget, and the last turn_end says so — the
// run must not spin on a model that will not finish the turn.
func TestStalledTurnStopsAfterTheLimit(t *testing.T) {
	p := &fauxProvider{
		name: "faux",
		turns: []fauxTurn{
			thinkingTurn("one"), thinkingTurn("two"),
			thinkingTurn("three"), thinkingTurn("four"),
		},
	}
	turns, _ := stallRun(t, newFauxRunCfg(p))

	if want := stallContinueMax + 1; p.callCount() != want {
		t.Fatalf("provider called %d times, want %d (one turn plus the continuation budget)", p.callCount(), want)
	}
	if want := stallContinueMax + 1; len(turns) != want {
		t.Fatalf("turn_end events = %d, want %d", len(turns), want)
	}
	last := turns[len(turns)-1]
	if last.Stalled != stallContinueMax+1 || last.StallLimit != stallContinueMax {
		t.Fatalf("last turn_end = stalled %d/%d, want %d/%d", last.Stalled, last.StallLimit, stallContinueMax+1, stallContinueMax)
	}
	if note := StallNote(TurnStall{Count: last.Stalled, Limit: last.StallLimit}); !strings.Contains(note, "stopping here") {
		t.Errorf("note for the spent budget = %q, want it to say the run stopped", note)
	}
}

// TestStallBudgetResetsForNewInput: the continuation budget is per pass of the
// outer loop. Material the person (or a hook) adds after a stall begins a fresh
// pass, so one bad turn cannot leave every later turn unable to be continued.
func TestStallBudgetResetsForNewInput(t *testing.T) {
	p := &fauxProvider{
		name: "faux",
		turns: []fauxTurn{
			thinkingTurn("one"), thinkingTurn("two"), thinkingTurn("three"),
			thinkingTurn("four"), textTurn("here is the answer"),
		},
	}
	cfg := newFauxRunCfg(p)
	served := false
	cfg.GetFollowUpMessages = func(context.Context, *agentcore.AgentContext) []agentcore.AgentMessage {
		if served {
			return nil
		}
		served = true
		return []agentcore.AgentMessage{agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent("still there?")},
		}}
	}
	turns, msgs := stallRun(t, cfg)

	if p.callCount() != 5 {
		t.Fatalf("provider called %d times, want 5", p.callCount())
	}
	if got := turns[2].Stalled; got != stallContinueMax+1 {
		t.Errorf("the turn that spent the budget reported stalled = %d, want %d", got, stallContinueMax+1)
	}
	if got := turns[3].Stalled; got != 1 {
		t.Errorf("the first turn after new input reported stalled = %d, want a fresh budget (1)", got)
	}
	last := msgs[len(msgs)-1]
	if a, ok := last.(agentcore.AssistantMessage); !ok || textContentOf(a.Content) != "here is the answer" {
		t.Errorf("the run must end with the answer, got %T %+v", last, last)
	}
}

// TestEmptyTurnIsNotAStall pins the scope of the decision: only a turn that
// reasoned and then stopped is continued. A completion with nothing in it at all
// is the provider's to retry (it does), and a blank text turn is an ordinary end.
func TestEmptyTurnIsNotAStall(t *testing.T) {
	for _, c := range []struct {
		name string
		turn fauxTurn
	}{
		{"no content at all", emptyTurn()},
		{"blank text", textTurn("")},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &fauxProvider{name: "faux", turns: []fauxTurn{c.turn}}
			turns, _ := stallRun(t, newFauxRunCfg(p))
			if p.callCount() != 1 {
				t.Errorf("provider called %d times, want 1: this is not a stall", p.callCount())
			}
			if len(turns) != 1 || turns[0].Stalled != 0 {
				t.Errorf("turn_end = %+v, want an ordinary turn end", turns)
			}
		})
	}
}

// TestStallNoteNamesTheOutcome pins the two sentences a front-end shows, because
// they are the whole user-visible difference between "it is carrying on" and
// "it stopped".
func TestStallNoteNamesTheOutcome(t *testing.T) {
	carrying := StallNote(TurnStall{Count: 1, Limit: 2})
	if !strings.Contains(carrying, "carrying on automatically (attempt 1 of 2)") {
		t.Errorf("carrying note = %q", carrying)
	}
	if strings.Contains(carrying, "Send another message") {
		t.Errorf("carrying note = %q, want no instruction to do what the loop is doing", carrying)
	}
	stopped := StallNote(TurnStall{Count: 3, Limit: 2})
	if !strings.Contains(stopped, "stopping here") || !strings.Contains(stopped, "3 turns in a row") {
		t.Errorf("stopped note = %q", stopped)
	}
}
