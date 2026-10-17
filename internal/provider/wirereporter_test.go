package provider

import "testing"

// A wire fallback reports the wire serving now: its resolved primary until a
// fallback succeeds, then the promoted one. The status bar reads this live, so a
// mid-turn fallback updates the protocol segment without a restart.
func TestWireFallbackReportsTheCurrentWire(t *testing.T) {
	inner := &scriptedProvider{}
	p := &wireFallbackProvider{primaryWire: WireResponses, primary: inner}
	if got := p.CurrentWire(); got != WireResponses {
		t.Fatalf("CurrentWire = %q, want responses before any fallback", got)
	}
	p.promote(WireChat, inner)
	if got := p.CurrentWire(); got != WireChat {
		t.Errorf("CurrentWire = %q, want chat after the fallback promoted it", got)
	}
}

// The retry wrapper delegates to an inner reporter, and falls back to the
// resolver's initial wire for a direct driver that cannot report one.
func TestStreamRetryReportsTheProviderWire(t *testing.T) {
	direct := withStreamRetry(&scriptedProvider{}, WireMessages)
	if got := direct.(WireReporter).CurrentWire(); got != WireMessages {
		t.Errorf("direct CurrentWire = %q, want the initial messages", got)
	}

	fallback := &wireFallbackProvider{primaryWire: WireResponses, primary: &scriptedProvider{}}
	wrapped := withStreamRetry(fallback, WireChat)
	if got := wrapped.(WireReporter).CurrentWire(); got != WireResponses {
		t.Errorf("wrapped CurrentWire = %q, want the inner responses", got)
	}
	fallback.promote(WireChat, fallback.primary)
	if got := wrapped.(WireReporter).CurrentWire(); got != WireChat {
		t.Errorf("wrapped CurrentWire = %q, want chat after promotion", got)
	}
}
