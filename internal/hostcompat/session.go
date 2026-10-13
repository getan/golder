package hostcompat

import (
	"sync"
)

// Session is one golder session's view of the multiplexer around it: the
// lifecycle half of this package, where Reporter is the transport half.
//
// A driver calls it at the four moments it knows about — a session starts, a
// turn starts, a turn ends, golder is going away — and at the one moment only
// it can recognise: a prompt is now waiting for the person. The bookkeeping
// that matters is what happens when those overlap, and it lives here so every
// driver gets it:
//
//   - Prompts can overlap. Parallel tool calls can each need an answer, and
//     the driver may open the second dialog before the first is answered, so
//     the open-prompt count is a counter, not a flag. The pane is blocked
//     while it is above zero and returns to work when it drops to zero.
//   - A turn that ends while a prompt is open must not report "done" over the
//     top of "waiting for you": the person is still being waited for, and a
//     host that saw "done" would stop showing the pane as needing attention.
//   - A prompt answered after the turn it belonged to has been cancelled
//     reports rest, not work: there is nothing left running.
//
// A nil *Session is a valid no-op, so a driver can hold one unconditionally
// and never test for a multiplexer again.
type Session struct {
	rep *Reporter

	mu sync.Mutex
	// turn is whether a run is in flight.
	turn bool
	// open is how many prompts are waiting for the person.
	open int
	// closed is set by Close.
	closed bool
}

// OpenSession returns the session for the multiplexer this process runs in, or
// nil when it runs in a plain terminal. It is Open plus the lifecycle
// bookkeeping, which is what a driver wants.
func OpenSession(opts Options) *Session {
	rep := Open(opts)
	if rep == nil {
		return nil
	}
	return &Session{rep: rep}
}

// Reporter returns the underlying reporter, for a caller that wants to send
// something this type does not model.
func (s *Session) Reporter() *Reporter {
	if s == nil {
		return nil
	}
	return s.rep
}

// HostName is the multiplexer's id, "" outside one.
func (s *Session) HostName() string {
	if s == nil {
		return ""
	}
	return s.rep.HostName()
}

// Conversation tells the host which conversation the pane holds, and how to
// reopen it. It is what makes a restored pane come back on the same
// conversation rather than on an empty prompt, so it is reported at session
// start, when the pane's shell is planted, and again whenever the session
// changes under it (/resume, /clone, /fork).
//
// It does not move the pane's state: a report that names a conversation is not
// a report about what the agent is doing.
func (s *Session) Conversation(id string, resumeArgv []string) {
	if s == nil {
		return
	}
	if !ValidSessionID(id) {
		// An id the hosts cannot carry safely is not reported at all: a host
		// that resumed this pane would type the id into a shell.
		return
	}
	s.rep.Session(id, resumeArgv)
}

// Start reports that a session is up and at rest: the pane holds a conversation
// and golder waits at its prompt.
func (s *Session) Start(id string, resumeArgv []string) {
	if s == nil {
		return
	}
	s.Conversation(id, resumeArgv)
	s.mu.Lock()
	s.turn = false
	s.open = 0
	s.mu.Unlock()
	s.rep.State(StateIdle, KindNone, "")
}

// TurnStart reports that golder took a prompt and is working on it.
func (s *Session) TurnStart() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turn = true
	open := s.open
	s.mu.Unlock()
	if open > 0 {
		// A prompt is already waiting; the pane is blocked, not working. Saying
		// otherwise would drop the person's one signal that they are needed.
		return
	}
	s.rep.Working("")
}

// TurnEnd reports a finished turn. failed says it stopped on an error rather
// than finishing its work, which the hosts that can tell the two apart show
// differently.
func (s *Session) TurnEnd(failed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turn = false
	open := s.open
	s.mu.Unlock()
	if open > 0 {
		// Still being waited for: the open prompt owns the pane's state until
		// it is answered, whatever the run did.
		return
	}
	s.rep.State(turnEndState(failed), KindNone, "")
}

// turnEndState is the state a finished turn reports.
func turnEndState(failed bool) State {
	if failed {
		return StateErrored
	}
	return StateDone
}

// Block reports that golder is waiting for the person, and returns the call
// that says the wait is over. A driver defers it right where it opens the
// prompt:
//
//	unblock := s.host.Block(hostcompat.KindApproval, "approve Bash: go test ./...")
//	defer unblock()
//
// The returned function is safe to call more than once and on a nil Session,
// so the defer cannot go wrong on an early return.
func (s *Session) Block(kind Kind, message string) func() {
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.open++
	s.mu.Unlock()
	s.rep.Blocked(kind, message)

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.open > 0 {
				s.open--
			}
			open := s.open
			turn := s.turn
			s.mu.Unlock()
			if open > 0 {
				// Another prompt is still waiting, and the pane stays blocked.
				return
			}
			if turn {
				s.rep.Working("")
				return
			}
			s.rep.State(StateIdle, KindNone, "")
		})
	}
}

// Meta reports display metadata: the model, how full the context is, and what
// the agent is doing. It is display only — no host reads it as state.
func (s *Session) Meta(kv map[string]string) {
	if s == nil || len(kv) == 0 {
		return
	}
	s.rep.Meta(kv)
}

// MetaModel is the metadata key holding the model's name.
const MetaModel = "model"

// MetaContext is the metadata key holding how full the context window is, as a
// percentage.
const MetaContext = "context"

// ContextMeta renders a context reading for Session.Meta: the percentage both
// hosts show, and nothing when the window is unknown.
func ContextMeta(tokens, window int) map[string]string {
	pct := metaContext(tokens, window)
	if pct == "" {
		return nil
	}
	return map[string]string{MetaContext: pct}
}

// Close releases the pane and stops reporting. It is what golder runs on the
// way out: the pane's agent is cleared so the host stops drawing a golder that
// has quit, and the release is flushed before the process exits.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.rep.Release()
	s.rep.Close()
}
