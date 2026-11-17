package provider

import "testing"

// TestSingleWireProviderReportsItsWire pins the status bar contract for a
// provider that has one declared wire: withWireFallback adds no wrapper, so the
// driver itself must answer CurrentWire. Without it the stream-retry wrapper's
// initial guess (Chat) was displayed for an auto-resolved DeepSeek run.
func TestSingleWireProviderReportsItsWire(t *testing.T) {
	env := func(string) string { return "" }
	p, name, err := ResolveProvider("deepseek-v4-flash", "", "", "deepseek", env)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if name != "deepseek" {
		t.Fatalf("name = %q", name)
	}
	wr, ok := p.(WireReporter)
	if !ok {
		t.Fatal("resolved provider does not report its wire")
	}
	if got := wr.CurrentWire(); got != WireResponses {
		t.Fatalf("CurrentWire = %q, want %q", got, WireResponses)
	}
}
