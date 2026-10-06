package tui

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
)

// TestBridgeForwardsContextUsageEvent pins the wire conversion: a
// ContextUsageEvent must reach the UI as contextUsageMsg (a silent drop here
// is exactly the class of bug — a registered-but-dead action — this feature
// exists to avoid).
func TestBridgeForwardsContextUsageEvent(t *testing.T) {
	ch := newEventChan()
	h := newStreamHandler(ch, nil)

	h.OnEvent(agentcore.ContextUsageEvent{Tokens: 1234, Window: 5678})

	msg := <-ch
	u, ok := msg.(contextUsageMsg)
	if !ok {
		t.Fatalf("got %T, want contextUsageMsg", msg)
	}
	if u.tokens != 1234 || u.window != 5678 {
		t.Fatalf("contextUsageMsg = %+v, want {1234 5678}", u)
	}
}

// TestModelContextUsageMsgUpdatesStatusBar pins the mid-run gauge refresh: the
// status bar shows the reported percentage immediately, without waiting for
// the run-end telemetry summary.
func TestModelContextUsageMsgUpdatesStatusBar(t *testing.T) {
	m := NewModel(Options{Model: "test-model"})

	mm, _ := m.Update(contextUsageMsg{tokens: 50_000, window: 100_000})
	got := mm.(Model)

	if got.statusBar.contextPct != 50 {
		t.Fatalf("contextPct = %d, want 50", got.statusBar.contextPct)
	}
	if got.statusBar.tokens != 50_000 {
		t.Fatalf("tokens = %d, want 50000", got.statusBar.tokens)
	}
}

// TestRefreshContextUsageSeedsFromHistory pins the startup/resume gauge: the
// very first frame already shows how much of the window the resumed session
// inherited, using the same estimate the loop publishes at turn boundaries.
func TestRefreshContextUsageSeedsFromHistory(t *testing.T) {
	m := NewModel(Options{Model: "test-model"})
	m.live = &cli.LiveConfig{ContextWindow: 100_000}
	msgs := agentcore.MessageList{
		agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(strings.Repeat("x", 40_000))},
		},
	}

	m.refreshContextUsage(msgs)

	// 40k chars ≈ 10k tokens → 10% of a 100k window.
	if m.statusBar.contextPct != 10 {
		t.Fatalf("contextPct = %d, want 10", m.statusBar.contextPct)
	}
	if m.statusBar.tokens != 10_000 {
		t.Fatalf("tokens = %d, want 10000", m.statusBar.tokens)
	}
}

// TestRefreshContextUsageStaysHiddenWhenUnknown verifies the gauge stays blank
// when there is nothing meaningful to show: an unknown window, an empty
// context, or a session-less model must not render a fake 0%.
func TestRefreshContextUsageStaysHiddenWhenUnknown(t *testing.T) {
	msgs := agentcore.MessageList{
		agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent("hello")},
		},
	}

	unknownWindow := NewModel(Options{Model: "test-model"})
	unknownWindow.live = &cli.LiveConfig{ContextWindow: 0}
	unknownWindow.refreshContextUsage(msgs)
	if unknownWindow.statusBar.contextPct != -1 {
		t.Errorf("unknown window: contextPct = %d, want hidden (-1)", unknownWindow.statusBar.contextPct)
	}

	emptyContext := NewModel(Options{Model: "test-model"})
	emptyContext.live = &cli.LiveConfig{ContextWindow: 100_000}
	emptyContext.refreshContextUsage(nil)
	if emptyContext.statusBar.contextPct != -1 {
		t.Errorf("empty context: contextPct = %d, want hidden (-1)", emptyContext.statusBar.contextPct)
	}

	sessionless := NewModel(Options{Model: "test-model"})
	sessionless.live = nil
	sessionless.refreshContextUsage(msgs)
	if sessionless.statusBar.contextPct != -1 {
		t.Errorf("nil live: contextPct = %d, want hidden (-1)", sessionless.statusBar.contextPct)
	}
}

// TestContextUsageMsgHidesOnUnknownWindow verifies a window-less update clears
// the gauge instead of rendering a stale percentage.
func TestContextUsageMsgHidesOnUnknownWindow(t *testing.T) {
	m := NewModel(Options{Model: "test-model"})
	mm, _ := m.Update(contextUsageMsg{tokens: 50_000, window: 100_000})
	shown := mm.(Model)

	mm, _ = shown.Update(contextUsageMsg{tokens: 60_000, window: 0})
	hidden := mm.(Model)
	if hidden.statusBar.contextPct != -1 {
		t.Fatalf("contextPct = %d, want hidden (-1)", hidden.statusBar.contextPct)
	}
}
