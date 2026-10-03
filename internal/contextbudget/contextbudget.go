// Package contextbudget is a leaf package (standard library only) holding the
// live context-window budget of one run: how many tokens the conversation has
// used against the model's window, whether a low-budget reminder has been
// delivered, and whether the model has asked to continue in a fresh window.
//
// The agent loop owns the state's lifecycle: it creates one per run (or adopts
// the session-scoped one the driver passes in), publishes it into the run
// context, and observes the conversation size as the run progresses. The
// context-sensitive tools (get_context_remaining, new_context) and the budget
// reminder provider read the same object back from the context, so every
// surface sees one consistent figure without per-driver wiring — the same
// pattern as the judge's message snapshot.
package contextbudget

import (
	"context"
	"sync"
)

// Reminder thresholds, as percentages of the window still remaining: a notice
// at <=10% and an escalated warning at <=3%. Fractions rather than absolute
// token counts so one rule fits a 32k and a 1M window.
const (
	noticePercent   = 10
	criticalPercent = 3
)

// Tier identifies which low-budget reminder fired.
type Tier int

const (
	// TierNone means no reminder is due.
	TierNone Tier = 0
	// TierNotice is the first low-budget reminder.
	TierNotice Tier = 1
	// TierCritical is the escalated reminder as the budget nears exhaustion.
	TierCritical Tier = 2
)

// Notice is one due reminder: the tier plus the figures it should carry.
type Notice struct {
	Tier      Tier
	Remaining int
	Used      int
	Window    int
}

// State is the concurrency-safe context budget of one run. Use New; the zero
// value is not usable.
type State struct {
	mu sync.Mutex

	window   int
	used     int
	haveUsed bool

	// rolloverEnabled records whether starting a fresh window is possible this
	// run (compaction enabled and the window known). The new_context tool
	// refuses politely when it is false.
	rolloverEnabled bool
	// rolloverRequested is set by new_context and consumed by the loop at the
	// next turn boundary.
	rolloverRequested bool

	// noticeTier is the highest tier already delivered since the budget last
	// recovered, so each crossing reminds once instead of every turn.
	noticeTier Tier
}

// New returns an empty budget state.
func New() *State { return &State{} }

// SetWindow records the model's context window in tokens (<=0 means unknown).
func (s *State) SetWindow(window int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.window = window
}

// SetRolloverEnabled records whether new_context can be honored this run.
func (s *State) SetRolloverEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloverEnabled = enabled
}

// RolloverEnabled reports whether new_context can be honored.
func (s *State) RolloverEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rolloverEnabled
}

// Observe records the current conversation size in tokens.
func (s *State) Observe(used int) {
	if s == nil {
		return
	}
	if used < 0 {
		used = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used = used
	s.haveUsed = true
	// Recovery above the notice threshold (compaction, rebuild, a new run with a
	// smaller context) re-arms the reminder ladder so a later crossing reminds
	// again instead of staying silent forever.
	if s.window > 0 && (s.window-s.used)*100 > s.window*noticePercent {
		s.noticeTier = TierNone
	}
}

// Usage returns the last observed usage and whether a figure exists yet.
func (s *State) Usage() (used, window int, ok bool) {
	if s == nil {
		return 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.window, s.haveUsed && s.window > 0
}

// Remaining returns the tokens left in the window, or ok=false while unknown.
func (s *State) Remaining() (int, bool) {
	used, window, ok := s.Usage()
	if !ok {
		return 0, false
	}
	remaining := window - used
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// RequestRollover asks the loop to start a fresh context window at the next
// turn boundary. It is unconditional; callers check RolloverEnabled first to
// explain why the request cannot be honored.
func (s *State) RequestRollover() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloverRequested = true
}

// TakeRolloverRequest consumes and reports a pending rollover request.
func (s *State) TakeRolloverRequest() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	requested := s.rolloverRequested
	s.rolloverRequested = false
	return requested
}

// TakeNotice returns the next due low-budget reminder. A reminder fires once
// per tier per crossing; the escalation from notice to critical fires once
// more, and the ladder resets after the budget recovers (e.g. after
// compaction), so a later crossing reminds again.
func (s *State) TakeNotice() (Notice, bool) {
	if s == nil {
		return Notice{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.haveUsed || s.window <= 0 {
		return Notice{}, false
	}
	remaining := s.window - s.used
	if remaining < 0 {
		remaining = 0
	}
	tier := TierNone
	switch {
	case remaining*100 <= s.window*criticalPercent:
		tier = TierCritical
	case remaining*100 <= s.window*noticePercent:
		tier = TierNotice
	}
	if tier == TierNone {
		// The budget recovered (compaction): allow a future crossing to remind.
		s.noticeTier = TierNone
		return Notice{}, false
	}
	if tier <= s.noticeTier {
		return Notice{}, false
	}
	s.noticeTier = tier
	return Notice{Tier: tier, Remaining: remaining, Used: s.used, Window: s.window}, true
}

// stateKey is the unexported context key under which a run's budget state is
// carried for tools and reminder providers.
type stateKey struct{}

// WithState returns a child context carrying the run's budget state.
func WithState(ctx context.Context, s *State) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, stateKey{}, s)
}

// FromContext returns the budget state carried by ctx, or nil when the caller
// runs outside a budget-aware loop.
func FromContext(ctx context.Context) *State {
	s, _ := ctx.Value(stateKey{}).(*State)
	return s
}
