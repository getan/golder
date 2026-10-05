package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/cli/ui"
)

// TestTranscriptStartupNoPhantomUnseen is the regression for the startup
// "↓ 8 new lines" notice: a fresh transcript is pinned to the bottom, so the
// launch banner is not counted as output waiting below the fold — and even a
// forced-unpinned transcript must not count content that fits on screen.
func TestTranscriptStartupNoPhantomUnseen(t *testing.T) {
	tr := newTranscript(DefaultTheme())
	tr.setSize(80, 24)
	tr.addBanner("line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight")
	if tr.unseen != 0 {
		t.Fatalf("startup banner counted as unseen: %d", tr.unseen)
	}

	// Even with follow forced off, content that still fits under the fold is
	// visible in full — counting it would raise a phantom notice.
	tr.follow = false
	tr.reflow()
	if tr.unseen != 0 {
		t.Fatalf("content fitting on screen counted as unseen: %d", tr.unseen)
	}
}

// fillTranscript adds n single-line blocks so the viewport overflows.
func fillTranscript(m Model, n int) Model {
	for i := 0; i < n; i++ {
		m.transcript.addSystem(fmt.Sprintf("line %d", i))
	}
	return m
}

// TestUnseenToastPopsOnceAndExpires covers the notice contract: it appears
// only after the user scrolled away with fresh output landing below, announces
// each scroll-away period at most once (no nagging while a stream continues),
// and disappears after its TTL. Jumping back to the bottom clears it.
func TestUnseenToastPopsOnceAndExpires(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 10})
	m = fillTranscript(m, 40)

	// Scroll up: follow pauses and the watch chain arms.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.transcript.follow {
		t.Fatal("pgup should pause follow")
	}
	if !m.unseenWatchActive {
		t.Fatal("scrolling away should arm the unseen watch")
	}
	if got := m.toastBlock(); got != "" {
		t.Fatalf("no unseen content yet, toast should be hidden: %q", got)
	}

	// Fresh output lands out of sight: the next heartbeat pops the toast.
	m.transcript.addSystem("brand new line")
	m = step(t, m)
	got := m.toastBlock()
	if !strings.Contains(stripANSI(got), "1 new line") || !strings.Contains(stripANSI(got), "Ctrl+E to jump") {
		t.Fatalf("toast content = %q, want the unseen-lines notice", got)
	}
	if !m.unseenNotified {
		t.Fatal("popping the toast should mark the period notified")
	}

	// More output while the notice is still up must not re-pop it.
	m.transcript.addSystem("another line")
	m = step(t, m)
	if !strings.Contains(stripANSI(m.toastBlock()), "new line") {
		t.Fatal("toast should stay up during its TTL")
	}
	if n := strings.Count(stripANSI(m.toastBlock()), "new"); n != 1 {
		t.Fatalf("toast duplicated: %d notices", n)
	}

	// Let the TTL pass: the next heartbeat drops the notice and does not
	// re-pop it even though unseen lines are still waiting.
	m.unseenToastUntil = time.Now().Add(-time.Second)
	m = step(t, m)
	if got := m.toastBlock(); got != "" {
		t.Fatalf("expired toast should not paint: %q", got)
	}
	m = step(t, m)
	if got := m.toastBlock(); got != "" {
		t.Fatalf("toast should not re-pop within the same scroll-away period: %q", got)
	}

	// Ctrl+E returns to the live tail: the watch stops and nothing lingers.
	m = apply(t, m, keyPress("ctrl+e"))
	if !m.transcript.follow {
		t.Fatal("ctrl+e should jump back to the bottom")
	}
	if m.unseenWatchActive || !m.unseenToastUntil.IsZero() || m.unseenNotified {
		t.Fatal("jumping to the bottom should clear the toast state")
	}
}

// step runs one heartbeat of the model's active watch chain.
func step(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(unseenWatchMsg{gen: m.unseenWatchGen})
	return next.(Model)
}

// TestOverlayToastBottomRight verifies the overlay paints the block flush
// against the right edge of the bottom row, pads the gap, and leaves every
// other row untouched, while keeping the visible width exact.
func TestOverlayToastBottomRight(t *testing.T) {
	const width = 40
	view := "top row\n\nshort body row"
	block := " ↓ 8 new lines · Ctrl+E to jump "
	out := overlayToast(view, block, width)

	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("overlay changed the row count: %d", len(rows))
	}
	if rows[0] != "top row" {
		t.Errorf("first row changed: %q", rows[0])
	}
	last := stripANSI(rows[2])
	if !strings.HasSuffix(last, block) {
		t.Errorf("toast not flush right: %q", last)
	}
	if w := ui.Width(rows[2]); w != width {
		t.Errorf("bottom row width = %d, want %d", w, width)
	}
	left := strings.TrimRight(strings.TrimSuffix(last, block), " ")
	if left == "" || !strings.HasPrefix("short body row", left) {
		t.Errorf("existing prefix not preserved: %q", last)
	}
}

// TestOverlayToastTooNarrowDropped verifies the block is dropped (rather than
// wrapped or clipped into garbage) when the terminal cannot hold it.
func TestOverlayToastTooNarrowDropped(t *testing.T) {
	view := "row"
	block := " ↓ 8 new lines · Ctrl+E to jump "
	if got := overlayToast(view, block, ui.Width(block)); got != view {
		t.Fatalf("narrow terminal should drop the toast, got %q", got)
	}
}
