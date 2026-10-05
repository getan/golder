package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/getan/golder/internal/cli/ui"
)

// This file implements the transient bottom-right "N new lines · Ctrl+E to
// jump" notice shown while the user reads history and fresh output lands below
// the fold. It deliberately lives outside the status bar — that row is
// reserved for the persistent readout — and floats over the transcript's
// bottom-right corner, expiring on its own after unseenToastTTL. Each
// scroll-away period announces at most once (unseenNotified), so a long stream
// never turns the notice into a flickering nag.

// unseenWatchInterval is how often the watch chain re-checks the transcript
// while the user is scrolled away from the bottom: the poll that turns "new
// lines arrived unseen" into the toast, and the clock that expires it. The
// chain runs only while scrolled up, so a bottom-pinned idle screen pays no
// wakeups.
const unseenWatchInterval = 250 * time.Millisecond

// unseenToastTTL is how long the toast stays on screen once popped.
const unseenToastTTL = 3 * time.Second

// unseenWatchMsg is one heartbeat of the unseen-content watch. gen lets a
// superseded chain (cleared notice, jump to bottom, fresh arm) drop its ticks.
type unseenWatchMsg struct{ gen int }

// unseenWatchTick schedules the next heartbeat for chain gen.
func unseenWatchTick(gen int) tea.Cmd {
	return tea.Tick(unseenWatchInterval, func(time.Time) tea.Msg { return unseenWatchMsg{gen: gen} })
}

// armUnseenWatch starts the watch chain when it is not already running and the
// transcript is scrolled away from the bottom. Scroll paths call it after
// updating the viewport; it is a no-op once the chain is live and never fires
// while the view is pinned, so an idle bottom screen does not tick.
func (m *Model) armUnseenWatch() tea.Cmd {
	if m.transcript.follow || m.unseenWatchActive {
		return nil
	}
	m.unseenWatchActive = true
	m.unseenWatchGen++
	return unseenWatchTick(m.unseenWatchGen)
}

// clearUnseenNotice removes the toast and stops the watch chain. Jumping to
// the bottom takes this path so the notice can never linger over a settled
// view; the render-side checks in toastBlock back it up for the scroll paths.
func (m *Model) clearUnseenNotice() {
	m.unseenToastUntil = time.Time{}
	m.unseenNotified = false
	if m.unseenWatchActive {
		m.unseenWatchActive = false
		m.unseenWatchGen++
	}
}

// stepUnseenWatch advances the watch by one heartbeat: it pops the toast when
// new lines arrived unseen (at most once per scroll-away period), lets it
// expire after its TTL, and stops the chain once the user is back at the
// bottom.
func (m *Model) stepUnseenWatch(msg unseenWatchMsg) tea.Cmd {
	if msg.gen != m.unseenWatchGen {
		return nil // stale heartbeat from a superseded chain
	}
	if m.transcript.follow {
		m.clearUnseenNotice()
		return nil
	}
	if n := m.transcript.unseen; n > 0 && !m.unseenNotified {
		m.unseenNotified = true
		m.unseenToastUntil = time.Now().Add(unseenToastTTL)
	}
	if !m.unseenToastUntil.IsZero() && !time.Now().Before(m.unseenToastUntil) {
		m.unseenToastUntil = time.Time{}
	}
	return unseenWatchTick(m.unseenWatchGen)
}

// toastVisible reports whether the toast should paint right now: a live
// deadline, actual unseen content, and a viewport that is not at the bottom.
// The state fields may briefly lag a scroll-back, so this render-side check
// keeps the notice off screen the moment the user returns.
func (m Model) toastVisible() bool {
	return !m.unseenToastUntil.IsZero() &&
		time.Now().Before(m.unseenToastUntil) &&
		m.transcript.unseen > 0 &&
		!m.transcript.follow
}

// toastBlock renders the toast as a styled block, or "" when it should not
// paint (see toastVisible).
func (m Model) toastBlock() string {
	if !m.toastVisible() {
		return ""
	}
	n := m.transcript.unseen
	word := "lines"
	if n == 1 {
		word = "line"
	}
	return m.theme.Toast.Render(fmt.Sprintf(" ↓ %d new %s · Ctrl+E to jump ", n, word))
}

// overlayToast paints block onto the right edge of view's bottom row, keeping
// the row's existing prefix up to the space the block needs. The prefix is
// truncated on a cell boundary with ANSI state preserved (ansi.Truncate), and
// a reset separates it from the block so a dangling style can never bleed into
// the toast. When the terminal is too narrow to hold the block beside at least
// one column of content, the toast is dropped rather than wrapped (wrapping
// would break the fixed row budget).
func overlayToast(view string, block string, width int) string {
	w := ui.Width(block)
	if w <= 0 || w >= width {
		return view
	}
	rows := strings.Split(view, "\n")
	i := len(rows) - 1
	if i < 0 {
		return view
	}
	left := ansi.Truncate(rows[i], width-w, "")
	if pad := width - w - ui.Width(left); pad > 0 {
		left += strings.Repeat(" ", pad)
	}
	rows[i] = left + "\x1b[0m" + block
	return strings.Join(rows, "\n")
}
