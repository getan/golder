package contextbudget

import (
	"context"
	"testing"
)

func TestUsageAndRemaining(t *testing.T) {
	s := New()
	if _, ok := s.Remaining(); ok {
		t.Fatal("remaining must be unknown before any observation")
	}
	s.SetWindow(100000)
	if _, ok := s.Remaining(); ok {
		t.Fatal("remaining must be unknown before any observation")
	}
	s.Observe(60000)
	remaining, ok := s.Remaining()
	if !ok || remaining != 40000 {
		t.Fatalf("remaining = %d, ok=%v, want 40000", remaining, ok)
	}
	// Overshoot clamps at zero instead of going negative.
	s.Observe(120000)
	if remaining, _ := s.Remaining(); remaining != 0 {
		t.Fatalf("remaining = %d, want 0 when overshot", remaining)
	}
}

func TestTakeNoticeTiersFireOnceAndEscalate(t *testing.T) {
	s := New()
	s.SetWindow(100000)

	// 50% used: silent.
	s.Observe(50000)
	if _, ok := s.TakeNotice(); ok {
		t.Fatal("no reminder expected at 50% used")
	}

	// 91% used (9% remaining): notice fires once, not twice.
	s.Observe(91000)
	n, ok := s.TakeNotice()
	if !ok || n.Tier != TierNotice {
		t.Fatalf("notice = %+v, ok=%v; want tier notice", n, ok)
	}
	if n.Remaining != 9000 || n.Used != 91000 || n.Window != 100000 {
		t.Fatalf("notice figures = %+v", n)
	}
	if _, ok := s.TakeNotice(); ok {
		t.Fatal("notice must fire only once per crossing")
	}

	// 98% used (2% remaining): escalates to critical once.
	s.Observe(98000)
	n, ok = s.TakeNotice()
	if !ok || n.Tier != TierCritical {
		t.Fatalf("notice = %+v, ok=%v; want tier critical", n, ok)
	}
	if _, ok := s.TakeNotice(); ok {
		t.Fatal("critical must fire only once per crossing")
	}

	// Recovery above the notice threshold (compaction) resets the ladder: the
	// next crossing reminds again.
	s.Observe(50000)
	if _, ok := s.TakeNotice(); ok {
		t.Fatal("no reminder expected after recovery")
	}
	s.Observe(95000)
	n, ok = s.TakeNotice()
	if !ok || n.Tier != TierNotice {
		t.Fatalf("post-recovery notice = %+v, ok=%v; want tier notice", n, ok)
	}
}

func TestNoticeSkippedWhenWindowUnknown(t *testing.T) {
	s := New()
	s.Observe(500)
	if _, ok := s.TakeNotice(); ok {
		t.Fatal("no reminder expected without a window")
	}
}

func TestRolloverRequestConsumedOnce(t *testing.T) {
	s := New()
	if s.RolloverEnabled() {
		t.Fatal("rollover must default to disabled")
	}
	s.SetRolloverEnabled(true)
	if !s.RolloverEnabled() {
		t.Fatal("rollover should be enabled")
	}
	if s.TakeRolloverRequest() {
		t.Fatal("no request expected before one is made")
	}
	s.RequestRollover()
	if !s.TakeRolloverRequest() {
		t.Fatal("request expected")
	}
	if s.TakeRolloverRequest() {
		t.Fatal("request must be consumed exactly once")
	}
}

func TestContextPlumbing(t *testing.T) {
	s := New()
	if FromContext(context.Background()) != nil {
		t.Fatal("no state expected on a bare context")
	}
	ctx := WithState(context.Background(), s)
	if FromContext(ctx) != s {
		t.Fatal("state must round-trip through the context")
	}
	if WithState(context.Background(), nil) == nil {
		t.Fatal("WithState(nil) must not produce a nil context")
	}
}

func TestNilStateIsInert(t *testing.T) {
	var s *State
	s.SetWindow(1)
	s.Observe(1)
	s.SetRolloverEnabled(true)
	s.RequestRollover()
	if s.RolloverEnabled() || s.TakeRolloverRequest() {
		t.Fatal("nil state must stay inert")
	}
	if _, ok := s.Remaining(); ok {
		t.Fatal("nil state has no remaining budget")
	}
	if _, ok := s.TakeNotice(); ok {
		t.Fatal("nil state never reminds")
	}
}
