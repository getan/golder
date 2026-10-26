package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/getan/golder/internal/judge"
)

func TestSpinnerPublishesAccountingAndClockOnlyOnTicks(t *testing.T) {
	for _, animate := range []bool{false, true} {
		s := newSpinner(DefaultTheme())
		start := time.Unix(100, 0) // deliberately unrelated to View's wall clock
		s.begin(start, "medium")
		before := s.view(120)
		s.addTokens("中文ab") // four RUNES, not eight UTF-8 bytes
		s.addTokenChars(8)
		if got := s.view(120); got != before {
			t.Fatal("pending accounting changed the visible spinner")
		}
		s.tick(start.Add(2*time.Second), animate)
		if s.chars != 12 || s.pendingChars != 0 || s.elapsed != 2*time.Second {
			t.Fatalf("published stats = %+v", s)
		}
		if got := stripANSI(s.view(120)); !strings.Contains(got, "2s · ↓ 3 tokens") {
			t.Fatalf("snapshot was not published: %q", got)
		}
		if (s.frame != 0) != animate {
			t.Fatal("animation must be independent from the accounting tick")
		}
		s.addTokenChars(4)
		s.tick(start.Add(3*time.Second), animate)
		if s.chars != 16 {
			t.Fatalf("tick lost or double-counted pending output: %d", s.chars)
		}
		s.begin(start.Add(time.Minute), "")
		if s.chars != 0 || s.pendingChars != 0 || s.elapsed != 0 {
			t.Fatal("a new run inherited old display/accounting state")
		}
	}
}

func TestSSHCoalescesBodyWithoutDelayingInputOrCompletion(t *testing.T) {
	clearMotionEnv(t)
	local := NewModel(Options{})
	if local.streamRenderDelay() != 50*time.Millisecond {
		t.Fatal("ordinary local stream cadence changed")
	}
	t.Setenv("SSH_CONNECTION", "client 123 server 22")
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.streamRenderDelay() != 150*time.Millisecond {
		t.Fatal("SSH must use the lower-frequency body cadence")
	}
	m.running = true
	m.spinner.begin(time.Now(), "")
	m.relayout()
	m = apply(t, m, textDeltaMsg{delta: "unflushed final text"})
	m = apply(t, m, runeKey('x'))
	if m.input.Value() != "x" {
		t.Fatal("typing waited behind a stream render tick")
	}
	m = apply(t, m, runEndMsg{})
	if m.running || m.streamRenderArmed || !strings.Contains(m.View().Content, "unflushed final text") {
		t.Fatal("completion must flush the final text without waiting for a timer")
	}
}

// Bubble Tea asks for View after every message, not only after timer messages.
// A provider burst must not turn its accounting events into terminal frames.
func TestTokenBurstsDoNotRepaintBetweenTicks(t *testing.T) {
	for _, noAnimations := range []bool{false, true} {
		for _, visible := range []bool{false, true} {
			t.Run(fmt.Sprintf("reduced=%v/visible=%v", noAnimations, visible), func(t *testing.T) {
				clearMotionEnv(t)
				m := apply(t, NewModel(Options{NoAnimations: noAnimations}), tea.WindowSizeMsg{Width: 80, Height: 24})
				m.running = true
				m.spinner.begin(time.Now(), "medium")
				m.relayout()
				previous := m.View().Content
				changed := 0
				for range 100 {
					if visible {
						m = apply(t, m, textDeltaMsg{delta: "word "})
					} else {
						m = apply(t, m, tokenDeltaMsg{chars: 8})
					}
					current := m.View().Content
					if current != previous {
						changed++
					}
					previous = current
				}
				if changed != 0 {
					t.Fatalf("100 delta messages changed %d frames before any render/spinner tick", changed)
				}
				if visible {
					m = apply(t, m, streamRenderTickMsg{})
					if !strings.Contains(stripANSI(m.View().Content), "word word") {
						t.Fatal("coalescing tick must publish the accumulated text")
					}
				}
				// A key is not deferred behind the streaming cadence.
				m = apply(t, m, runeKey('x'))
				if m.input.Value() != "x" || m.View().Cursor == nil {
					t.Fatal("streaming must retain immediate input and the real IME cursor")
				}
			})
		}
	}
}

func TestApprovalReplacesSlashMenuWithoutShrinkingFrame(t *testing.T) {
	for _, width := range []int{40, 80} {
		m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: width, Height: 30})
		m = apply(t, m, runeKey('/'))
		if m.menu.rows(width) == 0 {
			t.Fatal("fixture must have an open slash menu")
		}
		reply := make(chan judge.ApprovalAnswer, 1)
		m = apply(t, m, approvalRequestMsg{req: testApprovalRequest(), reply: reply})
		if m.menu.active {
			t.Error("approval must close the hidden slash menu")
		}
		if got := strings.Count(m.View().Content, "\n") + 1; got != m.height {
			t.Errorf("width %d: approval frame has %d rows, want %d", width, got, m.height)
		}
	}
}

func TestQueuedPreviewStaysWithinNarrowTerminal(t *testing.T) {
	for _, width := range []int{4, 8, 16, 24, 40, 80} {
		m := NewModel(Options{})
		m.steerQ.push(strings.Repeat("中文 text 🌍 ", 20))
		for i, line := range m.queuedPreviewLines(width) {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d, preview row %d has %d columns: %q", width, i, got, stripANSI(line))
			}
		}
	}
}

func TestApprovalRowsIncludeShortcutWidth(t *testing.T) {
	for _, width := range []int{4, 8, 16, 24, 40, 80} {
		m := NewModel(Options{})
		m.openApproval(testApprovalRequest(), make(chan judge.ApprovalAnswer, 1))
		for _, line := range strings.Split(m.approvalView(width), "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d: approval row wraps to %d columns: %q", width, got, stripANSI(line))
			}
		}
	}
}
