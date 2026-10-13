package hostcompat

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// took reads the next report and returns the state it named, so a test can
// assert step by step. Reading after each step is what makes the assertions
// deterministic: the reporter coalesces whatever is queued when it drains, and
// a test that reported two changes before looking would be asserting on the
// scheduler rather than on the driver's contract.
func took(t *testing.T, rec *recorder) string {
	t.Helper()
	return stateOfArgv(t, rec.next(t))
}

// stateOfArgv pulls the state out of a tuios set-agent-state command line.
func stateOfArgv(t *testing.T, argv []string) string {
	t.Helper()
	for i, a := range argv {
		if a == "set-agent-state" && i+1 < len(argv) {
			return argv[i+1]
		}
		if a == "set-agent-session" {
			return "(session)"
		}
	}
	t.Fatalf("no state in %q", argv)
	return ""
}

// sessionOfArgv pulls the conversation id out of an argv, "" when it carries
// none.
func sessionOfArgv(argv []string) string {
	for i, a := range argv {
		if a == "--agent-session-id" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// openSessionFor builds a *Session over a fake runner inside a tuios pane.
func openSessionFor(t *testing.T, rec *recorder) *Session {
	t.Helper()
	s := OpenSession(Options{
		Getenv:   env(tuiosPane()),
		LookPath: fakeLookPath,
		Run:      rec.run,
	})
	if s == nil {
		t.Fatal("OpenSession returned nil inside a host pane")
	}
	return s
}

// TestSessionLifecycle walks the ordinary shape of a session: start at rest,
// take a prompt, finish, and release on the way out.
func TestSessionLifecycle(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)

	const id = "20261011-231500-000123"
	s.Start(id, ResumeArgv(id))
	// The conversation rides the state report rather than being a call of its
	// own: that is what `set-agent-state --agent-session-id` is for, and one
	// call per change is one fewer round trip through the daemon.
	idle := rec.next(t)
	if got := stateOfArgv(t, idle); got != "idle" {
		t.Errorf("after Start, state = %q, want idle", got)
	}
	if got := sessionOfArgv(idle); got != id {
		t.Errorf("after Start, --agent-session-id = %q, want %q", got, id)
	}

	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Errorf("after TurnStart, state = %q, want working", got)
	}
	s.TurnEnd(false)
	if got := took(t, rec); got != "done" {
		t.Errorf("after TurnEnd, state = %q, want done", got)
	}
	s.Close()
	if got := took(t, rec); got != "none" {
		t.Errorf("after Close, state = %q, want none (the release)", got)
	}
}

// TestSessionFailedTurn: a run that stopped on an error is not a finished turn.
func TestSessionFailedTurn(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Fatalf("after TurnStart, state = %q, want working", got)
	}
	s.TurnEnd(true)
	if got := took(t, rec); got != "errored" {
		t.Errorf("after a failed TurnEnd, state = %q, want errored", got)
	}
}

// TestBlockedPromptOwnsTheState is the one that matters most: a pane waiting
// for a person must not be overridden by a turn boundary. Reporting "done" over
// an open prompt would take the pane off the rail's needs-attention list while
// the person is still being waited for.
func TestBlockedPromptOwnsTheState(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Fatalf("after TurnStart, state = %q, want working", got)
	}
	unblock := s.Block(KindApproval, "approve Bash: rm -rf build")
	blocked := rec.next(t)
	if got := stateOfArgv(t, blocked); got != "needs_input" {
		t.Fatalf("after Block, state = %q, want needs_input", got)
	}
	if !slices.Contains(blocked, "approve Bash: rm -rf build") {
		t.Errorf("the report does not carry the question: %q", blocked)
	}
	// The turn ends while the prompt is still open (the run was cancelled, or
	// the answer arrived after the loop gave up): the pane stays blocked.
	s.TurnEnd(false)
	unblock()
	if got := took(t, rec); got != "idle" {
		t.Errorf("after the answer, state = %q, want idle", got)
	}
}

// TestBlockedPromptThenWorkResumes: answering a prompt mid-turn returns the
// pane to working, not to rest — the turn is still running.
func TestBlockedPromptThenWorkResumes(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Fatalf("after TurnStart, state = %q, want working", got)
	}
	unblock := s.Block(KindApproval, "approve Bash: make")
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after Block, state = %q, want needs_input", got)
	}
	unblock()
	// The turn is still running, so the pane goes back to working rather than
	// to rest.
	if got := took(t, rec); got != "working" {
		t.Fatalf("after the answer, state = %q, want working", got)
	}
	s.TurnEnd(false)
	if got := took(t, rec); got != "done" {
		t.Errorf("after TurnEnd, state = %q, want done", got)
	}
}

// TestConcurrentPrompts is the case the bookkeeping exists for: parallel tool
// calls can each need an answer, so the pane stays blocked until the LAST one
// is answered. A boolean flag here leaves the pane reported as idle while a
// dialog is still open — the exact failure that makes a person miss a prompt.
func TestConcurrentPrompts(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Fatalf("after TurnStart, state = %q, want working", got)
	}
	first := s.Block(KindApproval, "approve Bash: go test ./...")
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after the first Block, state = %q, want needs_input", got)
	}
	second := s.Block(KindApproval, "approve Edit: main.go")
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after the second Block, state = %q, want needs_input", got)
	}
	// Answer the first: still one dialog open, so still blocked.
	first()
	assertNoReport(t, rec, "answering one of two open prompts")
	// Answer the second: now the pane is back to work.
	second()
	if got := took(t, rec); got != "working" {
		t.Errorf("after the last answer, state = %q, want working", got)
	}
}

// TestUnblockIsIdempotent: a driver defers the unblock function, so a second
// call must not decrement the count twice — that would report rest while a
// dialog is still open.
func TestUnblockIsIdempotent(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Fatalf("after TurnStart, state = %q, want working", got)
	}
	first := s.Block(KindApproval, "first")
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after the first Block, state = %q, want needs_input", got)
	}
	second := s.Block(KindApproval, "second")
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after the second Block, state = %q, want needs_input", got)
	}
	first()
	first() // a stray second call, e.g. a defer plus an explicit call
	assertNoReport(t, rec, "a repeated unblock with one prompt still open")
	second()
	if got := took(t, rec); got != "working" {
		t.Errorf("after the last answer, state = %q, want working", got)
	}
}

// assertNoReport fails when the reporter sends anything within a short window.
// It is the other half of "the pane stays in this state": not just that the
// next report says the right thing, but that no report was sent at all.
func assertNoReport(t *testing.T, rec *recorder, what string) {
	t.Helper()
	select {
	case argv := <-rec.ch:
		t.Fatalf("%s sent a report anyway: %q", what, argv)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestBlockedWithNoTurnReportsRest: a prompt answered after the turn it
// belonged to is gone leaves nothing running, so the pane reports rest rather
// than working.
func TestBlockedWithNoTurnReportsRest(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	unblock := s.Block(KindApproval, "approve Bash: make")
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after Block, state = %q, want needs_input", got)
	}
	unblock()
	if got := took(t, rec); got != "idle" {
		t.Errorf("after the answer with no turn running, state = %q, want idle", got)
	}
}

// TestConcurrentBlocksAndTurnEndAreOrdered runs the reporters from several
// goroutines at once: the counters are the shared state between the run
// goroutine and the UI goroutine, so a race here is a real one. Run with -race.
func TestConcurrentBlocksAndTurnEndAreOrdered(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	// Drain the recorder's channel in the background. This test asserts that
	// nothing raced, not what the individual reports said, and the reporter
	// sends on its own goroutine — leaving the channel to fill up would block
	// that goroutine rather than test it.
	stop := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-rec.ch:
			case <-stop:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unblock := s.Block(KindApproval, "approve Bash: make")
			unblock()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.TurnStart()
		s.TurnEnd(false)
	}()
	wg.Wait()
	// Wait for the reporter itself, not only for the callers: the reports are
	// sent asynchronously by design, so a check that ran now would be asserting
	// on the scheduler.
	waitIdle(t, s.Reporter())
	close(stop)
	<-drained

	// No assertion on the exact interleaving (that is the scheduler's), only
	// that nothing raced or deadlocked and that every report left the pane in a
	// state a host can render. `go test -race` is what actually checks this.
	if len(rec.all()) == 0 {
		t.Error("no reports were sent at all")
	}
}

// TestSessionCloseReleasesFlushes: a release queued as the process exits must
// be sent, or the host keeps drawing a golder that has quit.
func TestSessionCloseReleasesFlushes(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.TurnStart()
	if got := took(t, rec); got != "working" {
		t.Fatalf("after TurnStart, state = %q, want working", got)
	}
	s.Close()
	if got := took(t, rec); got != "none" {
		t.Errorf("after Close, state = %q, want none (the release)", got)
	}
	// And Close is safe to call twice (a deferred close and an explicit one).
	s.Close()
}

// TestSessionNilIsNoOp: outside a host pane every call is a no-op, so no driver
// needs a guard.
func TestSessionNilIsNoOp(t *testing.T) {
	var s *Session
	s.Start("id", nil)
	s.TurnStart()
	s.TurnEnd(false)
	unblock := s.Block(KindApproval, "x")
	unblock()
	unblock()
	s.Conversation("id", nil)
	s.Meta(map[string]string{"model": "m"})
	s.Close()
	if got := s.HostName(); got != "" {
		t.Errorf("HostName of a nil session = %q, want empty", got)
	}
	if s.Reporter() != nil {
		t.Error("Reporter of a nil session should be nil")
	}
}

// TestConversationRefusesAnUnsafeId: the id is typed into a shell by tuios's
// resume command and carried on herdr's command line, so an id that is not one
// plain token is dropped rather than reported.
func TestConversationRefusesAnUnsafeId(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.Conversation("has space", ResumeArgv("has space"))
	select {
	case argv := <-rec.ch:
		t.Fatalf("an unsafe session id was reported: %q", argv)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestStartResetsOpenPrompts: a new session under the same pane must not
// inherit the previous one's open-prompt count, which would pin the pane on
// needs_input for good.
func TestStartResetsOpenPrompts(t *testing.T) {
	rec := newRecorder()
	s := openSessionFor(t, rec)
	s.Block(KindApproval, "approve Bash: make") // never answered: the session moved on
	if got := took(t, rec); got != "needs_input" {
		t.Fatalf("after Block, state = %q, want needs_input", got)
	}
	s.Start("20261011-231500-000123", nil)
	if got := took(t, rec); got != "idle" {
		t.Fatalf("after Start, state = %q, want idle", got)
	}
	s.TurnEnd(false)
	// done, not needs_input: the open prompt belonged to the session that is
	// gone, and inheriting its count would pin the pane on needs_input for good.
	if got := took(t, rec); got != "done" {
		t.Errorf("after TurnEnd, state = %q, want done", got)
	}
}
