package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/session"
)

// resumeState is the in-flight /resume load. gen drops a result for a request
// that was canceled or superseded; pending keeps a prompt from starting on the
// old session while the new one is still being read.
type resumeState struct {
	gen     uint64
	id      string
	pending bool
}

// resumeLoadedMsg is the background read's answer. Header and entries are the
// new session; nothing is adopted until the UI validates the generation.
type resumeLoadedMsg struct {
	gen     uint64
	id      string
	header  session.SessionHeader
	entries []session.Entry
	skipped int
	err     error
}

// startResume persists the current session and reads the target off the update
// loop. The persist stays here: it writes the current conversation's state and
// is serialized with the run loop by persistMu. The load is the expensive part
// after redaction and runs in the returned command.
func (m Model) startResume(id string) (tea.Model, tea.Cmd) {
	id = strings.TrimSpace(id)
	if m.session == nil {
		m.transcript.addSystem("No active session to switch from.")
		return m, nil
	}
	if id == m.session.header.ID {
		m.transcript.addSystem(fmt.Sprintf("Already on session %s.", id))
		return m, nil
	}
	m.transcript.addSystem(fmt.Sprintf("Resuming session %s… (esc cancels)", id))
	m.relayout()
	if err := m.session.persist(); err != nil {
		m.transcript.addSystem(fmt.Sprintf("resume: save current session: %v", err))
		m.relayout()
		return m, nil
	}
	m.resume.gen++
	gen := m.resume.gen
	m.resume.pending = true
	m.resume.id = id
	store := m.session.store
	return m, func() tea.Msg {
		h, entries, skipped, err := store.LoadEntriesLenient(id)
		return resumeLoadedMsg{gen: gen, id: id, header: h, entries: entries, skipped: skipped, err: err}
	}
}

// finishResume commits a load that still matches the pending generation. A
// failed or stale load leaves the old session and its transcript untouched.
func (m Model) finishResume(msg resumeLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.resume.pending || msg.gen != m.resume.gen {
		return m, nil
	}
	m.resume.pending = false
	m.resume.id = ""
	if msg.err != nil {
		m.transcript.addSystem(fmt.Sprintf("resume: %v", msg.err))
		m.relayout()
		return m, nil
	}
	msgs, err := m.session.adopt(msg.header, msg.entries)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("resume: %v", err))
		m.relayout()
		return m, nil
	}
	m.syncLiveDisplay()
	m.transcript.reset()
	m.clearSelection()
	m.addSessionBanner(logoFrames)
	seedTranscript(&m.transcript, msgs)
	m.refreshContextUsage(msgs)
	m.transcript.addSystem(fmt.Sprintf("Resumed session %s (%s).", msg.id, m.live.Model))
	if msg.skipped > 0 {
		m.transcript.addSystem(fmt.Sprintf(
			"Skipped %d oversized message(s) while loading; the file was not changed. Run `golder session repair %s` to compact it.",
			msg.skipped, msg.id))
	}
	m.logoRunning = false
	m.relayout()
	return m, m.startStatusReveal()
}

// cancelResume abandons an in-flight load. The read itself cannot be
// interrupted, so its result is dropped by the generation check; the old
// session remains the active one.
func (m Model) cancelResume() Model {
	if !m.resume.pending {
		return m
	}
	m.resume.pending = false
	m.resume.gen++
	m.transcript.addSystem("Resume canceled.")
	m.relayout()
	return m
}
