package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const gitRefreshInterval = 10 * time.Second

// gitRefreshState belongs to one TUI, not to a workspace or its other panes.
// A workspace pane starts inactive because tuios reports focus-in to the
// selected pane only. A standalone terminal starts active so terminals that do
// not support focus reporting still get a bounded, periodic refresh.
type gitRefreshState struct {
	initialized bool
	active      bool
	tickGen     uint64
	tickDelay   time.Duration
	request     uint64
	inFlight    bool
	pending     bool
	failures    int
}

func newGitRefreshState() gitRefreshState {
	workspace := strings.TrimSpace(os.Getenv("GOLDER_WORKSPACE")) == "1" && strings.TrimSpace(os.Getenv("TUIOS_PANE_ID")) != ""
	return gitRefreshState{active: !workspace}
}

type gitRefreshStartMsg struct{}
type gitRefreshTickMsg struct{ gen uint64 }

// gitRefreshDelay backs off a missing repository or failed git without making
// an idle TUI repeatedly launch failing subprocesses.
func gitRefreshDelay(failures int) time.Duration {
	switch {
	case failures <= 0:
		return gitRefreshInterval
	case failures == 1:
		return 30 * time.Second
	case failures == 2:
		return time.Minute
	default:
		return 2 * time.Minute
	}
}

func (m *Model) scheduleGitRefresh() tea.Cmd {
	gen, delay := m.gitRefresh.tickGen, gitRefreshDelay(m.gitRefresh.failures)
	m.gitRefresh.tickDelay = delay
	return tea.Tick(delay, func(time.Time) tea.Msg { return gitRefreshTickMsg{gen: gen} })
}

func (m *Model) startGitRefresh() tea.Cmd {
	if m.gitRefresh.initialized {
		return nil
	}
	m.gitRefresh.initialized = true
	probe := m.requestGitRefresh(false) // one initial reading even in a hidden pane
	if !m.gitRefresh.active {
		return probe
	}
	m.gitRefresh.tickGen++
	return tea.Batch(probe, m.scheduleGitRefresh())
}

func (m *Model) setGitFocus(focused bool) tea.Cmd {
	if m.gitRefresh.active == focused {
		return nil
	}
	m.gitRefresh.active = focused
	m.gitRefresh.tickGen++ // invalidate the old timer chain
	if !focused || !m.gitRefresh.initialized {
		return nil
	}
	// A focus return is an explicit reason to get a fresh reading even if an
	// earlier request is still running. The two requests never overlap.
	return tea.Batch(m.requestGitRefresh(true), m.scheduleGitRefresh())
}

func (m *Model) tickGitRefresh(msg gitRefreshTickMsg) tea.Cmd {
	if !m.gitRefresh.initialized || !m.gitRefresh.active || msg.gen != m.gitRefresh.tickGen {
		return nil
	}
	return tea.Batch(m.requestGitRefresh(false), m.scheduleGitRefresh())
}

// requestGitRefresh coalesces periodic reads. A turn end or focus return that
// overlaps a read requests one trailing read, so the older snapshot cannot leave
// a commit made during that read invisible until the next timer.
func (m *Model) requestGitRefresh(force bool) tea.Cmd {
	s := &m.gitRefresh
	if s.inFlight {
		s.pending = s.pending || force
		return nil
	}
	s.request++
	s.inFlight = true
	id, dir := s.request, m.cwd
	probe := fetchGitCmd(dir)
	return func() tea.Msg {
		result := probe().(gitInfoMsg)
		result.request, result.dir = id, dir
		return result
	}
}

func (m *Model) finishGitRefresh(msg gitInfoMsg) tea.Cmd {
	s := &m.gitRefresh
	if msg.request == 0 {
		// Legacy direct readings have no request identity.
		m.statusBar.SetGit(msg)
		return nil
	}
	if !s.inFlight || msg.request != s.request {
		return nil
	}
	s.inFlight = false
	if msg.dir == m.cwd {
		m.statusBar.SetGit(msg)
		if msg.ok {
			s.failures = 0
		} else if s.failures < 3 {
			s.failures++
		}
	}
	var timer tea.Cmd
	if s.initialized && s.active && s.tickDelay != gitRefreshDelay(s.failures) {
		s.tickGen++
		timer = m.scheduleGitRefresh()
	}
	if s.pending || msg.dir != m.cwd {
		s.pending = false
		return tea.Batch(m.requestGitRefresh(false), timer)
	}
	return timer
}
