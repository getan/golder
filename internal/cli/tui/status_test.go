package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
)

// typeCommand feeds "/name" into the model and presses Enter, mirroring how a
// user runs a slash command from the composer (the popup is open at Enter, so it
// routes through submitSlashSelected, exactly like the REPL path).
func typeCommand(t *testing.T, m Model, cmd string) Model {
	t.Helper()
	m = typeInto(t, m, cmd).(Model)
	got, c := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if c != nil {
		if msg := c(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("%s should not quit", cmd)
			}
		}
	}
	return got.(Model)
}

// TestStatusWithSessionRendersSections drives /status on a session-bound model
// and asserts every report section appears in the transcript, with the model
// staying idle (no run is started).
func TestStatusWithSessionRendersSections(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{
		Model:         "status-model",
		ProviderName:  "status-provider",
		ThinkingLevel: agentcore.ThinkingLevel("low"),
	})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	// The /status environment section keys off the launch directory; keep it
	// deterministic rather than depending on the test's cwd.
	s.cwd = "/tmp/tui-status"

	m := NewModel(Options{}).withSession(s, nil)
	m = typeCommand(t, m, "/status")

	if m.running {
		t.Error("/status is an action command; model should stay idle")
	}
	if m.input.Value() != "" {
		t.Errorf("after executing /status, input = %q, want cleared", m.input.Value())
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	for _, want := range []string{
		"runtime config:",
		"model: status-model",
		"provider: status-provider",
		"context:",
		"project & environment:",
		"cwd: /tmp/tui-status",
		"credentials & connectivity:",
		"telemetry:",
		"no telemetry yet",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("/status output missing %q; transcript:\n%s", want, joined)
		}
	}
}

// TestStatusWithoutSessionNotice verifies a session-less model reports the
// unavailable notice rather than panicking on nil collaborators, and still
// clears the input.
func TestStatusWithoutSessionNotice(t *testing.T) {
	m := NewModel(Options{})
	m = typeCommand(t, m, "/status")

	if m.running {
		t.Error("/status must not start a run on a session-less model")
	}
	if m.input.Value() != "" {
		t.Errorf("after executing /status, input = %q, want cleared", m.input.Value())
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	if !strings.Contains(joined, "status unavailable: no active session") {
		t.Errorf("expected an unavailable notice in transcript, got:\n%s", joined)
	}
}

// TestStatusIncludesSessionSummary drives /status on a session-bound model and
// asserts the session section (id, message count, creation time) — the facts
// absorbed from the retired /session command — appears alongside the other
// sections.
func TestStatusIncludesSessionSummary(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{
		Model:        "session-model",
		ProviderName: "session-provider",
	})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	// Simulate two user/assistant turns in the live context (unsaved messages are
	// counted too, mirroring the REPL's in-memory source of truth).
	s.agentCtx.Messages = agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q1")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a1")}},
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("q2")}},
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("a2")}},
	}

	m := NewModel(Options{}).withSession(s, nil)
	m = typeCommand(t, m, "/status")

	if m.running {
		t.Error("/status is an action command; model should stay idle")
	}
	joined := strings.Join(blockTexts(m.transcript), "\n")
	for _, want := range []string{
		"session:",
		"id: " + s.header.ID,
		"messages: 4",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("/status output missing %q; transcript:\n%s", want, joined)
		}
	}
}

// TestTelemetryFoldingFeedsStatus verifies a telemetryMsg from the bridge is
// folded into the session's telemetry holder, so /status renders the cumulative
// and last-run telemetry blocks instead of "no telemetry yet".
func TestTelemetryFoldingFeedsStatus(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	if s.telemetry == nil {
		t.Fatal("session telemetry holder should be initialized")
	}

	m := NewModel(Options{}).withSession(s, nil)
	// A telemetryMsg is bridged while a run is pumping; set up that state so
	// Update keeps the pump running (pumpNext) exactly as during a real run.
	m.running = true
	m.runCh = make(chan tea.Msg)
	next, cmd := m.Update(telemetryMsg{ev: agentcore.TelemetryEvent{
		Turns:              3,
		TruncationCount:    1,
		CompactionCount:    1,
		ContextTokens:      1000,
		ContextWindow:      200000,
		ContextUtilization: 0.005,
	}})
	if cmd == nil {
		t.Fatal("telemetryMsg should return a pump cmd while a run is in flight")
	}
	m = next.(Model)

	// The event must be retained on the session's holder (not just the status
	// bar), otherwise /status could not render the telemetry section.
	if !s.telemetry.HasTelemetry() {
		t.Fatal("telemetry event should be folded into the session holder")
	}
	if s.telemetry.CumulativeTurns() != 3 {
		t.Errorf("CumulativeTurns = %d, want 3", s.telemetry.CumulativeTurns())
	}

	// The run has since ended, returning the model to idle so the user can
	// issue /status (key presses are dropped while a run is in flight).
	m.running = false
	m.runCh = nil

	m = typeCommand(t, m, "/status")
	joined := strings.Join(blockTexts(m.transcript), "\n")
	for _, want := range []string{
		"telemetry:",
		"since session start:",
		"turns: 3",
		"truncations: 1",
		"last run:",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("/status output missing %q after telemetry fold; transcript:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "no telemetry yet") {
		t.Error("/status should render real telemetry after a fold, not 'no telemetry yet'")
	}
}

// TestStatusNotInterceptedForStatusFoo verifies "/statusfoo" is NOT intercepted
// as /status (mirroring the REPL's guard), so it resolves as an unknown command
// rather than rendering the status report.
func TestStatusNotInterceptedForStatusFoo(t *testing.T) {
	m := NewModel(Options{})
	m = typeCommand(t, m, "/statusfoo")

	joined := strings.Join(blockTexts(m.transcript), "\n")
	if strings.Contains(joined, "runtime config:") {
		t.Errorf("/statusfoo must not run the status command; transcript:\n%s", joined)
	}
}
