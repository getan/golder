package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestUsageUnsupportedProviderExplainsItself drives /usage end to end for a
// provider with no quota endpoint: the command runs off the loop, returns the
// explanation, and the transcript shows it — the point of the command is that the
// answer is never a silent no-op or a raw 404.
func TestUsageUnsupportedProviderExplainsItself(t *testing.T) {
	s := newRemoteTestSession(t)
	m := NewModel(Options{ProviderName: "openai", Model: "gpt-6-astra"}).withSession(s, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	next, cmd := m.runSlash("/usage")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("bare /usage should run a lookup command")
	}
	if !m.running {
		t.Error("/usage should arm the session op while it looks the quota up")
	}
	// The returned command is a batch (the lookup plus the spinner tick), so the
	// lookup's own message is taken from its producer rather than from cmd().
	msg, ok := m.session.usageCmd(context.Background())().(usageDoneMsg)
	if !ok {
		t.Fatal("usageCmd did not produce a usageDoneMsg")
	}
	if msg.err != nil {
		t.Fatalf("unsupported provider should not error: %v", msg.err)
	}
	if !strings.Contains(msg.text, "no quota endpoint") || !strings.Contains(msg.text, "relay") {
		t.Errorf("text = %q, want the hint", msg.text)
	}

	m = apply(t, m, msg)
	out := m.transcript.view()
	if !strings.Contains(out, "no quota endpoint") {
		t.Errorf("transcript missing the hint:\n%s", out)
	}
	if m.running {
		t.Error("the session op should have settled")
	}
}

// TestUsageRendersAKnownProviderReport: a supported provider's text (the fetch is
// stubbed with the message the command would have produced) lands verbatim.
func TestUsageRendersAKnownProviderReport(t *testing.T) {
	s := newRemoteTestSession(t)
	m := NewModel(Options{ProviderName: "deepseek", Model: "deepseek-v4-flash"}).withSession(s, nil)
	m = apply(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	next, _ := m.runSlash("/usage")
	m = next.(Model)
	m = apply(t, m, usageDoneMsg{text: "usage · deepseek (available)\n  balance  CNY 98.07"})

	out := m.transcript.view()
	for _, want := range []string{"usage · deepseek", "CNY 98.07"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
}
