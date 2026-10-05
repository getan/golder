package tui

// Ctrl+R reverse history search (shell-style, Codex-flavored but in-memory):
// the footer line above the composer becomes the search input. Typing a query
// restarts the scan from the newest match; Ctrl+R/↑ and Ctrl+S/↓ walk unique
// matches; Enter accepts the preview as an editable draft (it does not submit);
// Esc or Ctrl+C restores the exact draft that existed before the search. The
// composer previews the selected match, so the search line only carries the
// query, the position, and the key hints.

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// histSearchHint is the footer guidance shown while a search session is active.
const histSearchHint = "↑ older · ↓ newer · Enter accept · Esc cancel"

// histSearch is the active Ctrl+R session (nil = inactive).
type histSearch struct {
	// query is the text typed after Ctrl+R.
	query string
	// matches are indices into m.history, newest-first, deduplicated by exact
	// text within this query.
	matches []int
	// cursor indexes matches; -1 means the query has no matches (or is empty).
	cursor int
	// originalDraft is the composer content captured when the search began; it
	// is restored verbatim on cancel or a miss.
	originalDraft string
}

// beginHistorySearch opens a search session: the global history is merged in
// (lazily, like ↑ browsing), the slash popup is closed, and the current draft is
// stashed. No match is previewed until the user types, so opening Ctrl+R never
// replaces the composer by itself.
func (m Model) beginHistorySearch() (tea.Model, tea.Cmd) {
	m.ensureHistoryLoaded()
	m.histSearch = &histSearch{originalDraft: m.input.Value(), cursor: -1}
	m.menu.close()
	m.relayout()
	return m, nil
}

// handleHistorySearchKey owns every key press while the search is active. Keys
// that are not part of the search vocabulary are swallowed: a pending search
// must not leak characters into the composer or trigger other bindings.
func (m Model) handleHistorySearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+r", "up":
		m.histSearchMoveOlder()
	case "ctrl+s", "down":
		m.histSearchMoveNewer()
	case "enter":
		m.acceptHistorySearch()
	case "esc", "ctrl+c":
		m.cancelHistorySearch()
	case "backspace":
		if q := m.histSearch.query; q != "" {
			r := []rune(q)
			m.histSearchEditQuery(string(r[:len(r)-1]))
		}
	default:
		if msg.Text != "" {
			m.histSearchEditQuery(m.histSearch.query + msg.Text)
		}
	}
	m.relayout()
	return m, nil
}

// histSearchEditQuery replaces the query and rescans the history from the
// newest entry. An empty query clears the matches and restores the original
// draft (opening the search only shows matches once something is typed).
func (m *Model) histSearchEditQuery(query string) {
	s := m.histSearch
	s.query = query
	s.matches = nil
	s.cursor = -1
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		seen := map[string]bool{}
		for i := len(m.history) - 1; i >= 0; i-- {
			text := m.history[i]
			if seen[text] {
				continue
			}
			if strings.Contains(strings.ToLower(text), q) {
				seen[text] = true
				s.matches = append(s.matches, i)
			}
		}
		if len(s.matches) > 0 {
			s.cursor = 0
		}
	}
	m.histSearchPreview()
}

// histSearchMoveOlder steps to the next older unique match (Ctrl+R / ↑).
func (m *Model) histSearchMoveOlder() {
	s := m.histSearch
	if s.cursor+1 < len(s.matches) {
		s.cursor++
		m.histSearchPreview()
	}
}

// histSearchMoveNewer steps back toward the newest match (Ctrl+S / ↓). At the
// newest match it stays put: a boundary hit must not drop the preview.
func (m *Model) histSearchMoveNewer() {
	s := m.histSearch
	if s.cursor > 0 {
		s.cursor--
		m.histSearchPreview()
	}
}

// histSearchPreview shows the selected match in the composer, or the original
// draft when nothing is selected.
func (m *Model) histSearchPreview() {
	s := m.histSearch
	if s.cursor >= 0 && s.cursor < len(s.matches) {
		m.input.SetValue(m.history[s.matches[s.cursor]])
		return
	}
	m.input.SetValue(s.originalDraft)
}

// acceptHistorySearch closes the session, keeping the previewed match (or the
// original draft when there was no match) as an editable draft. It never
// submits: the user reviews and presses Enter again.
func (m *Model) acceptHistorySearch() {
	m.histSearchPreview()
	m.histSearch = nil
}

// cancelHistorySearch closes the session and restores the pre-search draft.
func (m *Model) cancelHistorySearch() {
	m.input.SetValue(m.histSearch.originalDraft)
	m.histSearch = nil
}

// histSearchRows is the number of rows the search line occupies (0 when
// inactive), reserved by relayout so the transcript shrinks to fit.
func (m Model) histSearchRows() int {
	if m.histSearch == nil {
		return 0
	}
	return 1
}

// histSearchView renders the reverse-search line above the composer, or "" when
// no session is active. The line is clipped to the terminal width so it always
// occupies exactly one row.
func (m Model) histSearchView(width int) string {
	s := m.histSearch
	if s == nil {
		return ""
	}
	var status string
	switch {
	case s.cursor >= 0:
		status = fmt.Sprintf("%d/%d", s.cursor+1, len(s.matches))
	case strings.TrimSpace(s.query) == "":
		status = "type to search"
	default:
		status = "no matches"
	}
	line := m.theme.Accent.Render("reverse-search:") + " " + s.query +
		"  " + m.theme.System.Render("["+status+"]") +
		"  " + m.theme.System.Render(histSearchHint)
	if width > 0 {
		line = ansi.Truncate(line, width, "…")
	}
	return line
}
