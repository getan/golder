package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

// ResumeListN is how many recent sessions a bare /resume view lists.
const ResumeListN = 10

// AllSessionsFlag lifts the default project filter on a resume view, listing
// every project's sessions — the shape `codex resume --all` established.
const AllSessionsFlag = "--all"

// ResumeScope is the project filter a resume view applies. The zero value
// matches nothing: a caller that does not know its working directory gets an
// empty view rather than another project's history.
type ResumeScope struct {
	// All lifts the filter (from AllSessionsFlag).
	All bool
	// Dir is the directory the view is anchored at; a session is in scope when
	// it ran in the same project as Dir (same repository root, or the same
	// directory outside any repository).
	Dir string
}

// ProjectScope returns the default scope for a launch directory: the sessions
// that ran in the same project.
func ProjectScope(dir string) ResumeScope { return ResumeScope{Dir: dir} }

// AllSessionsScope returns the unfiltered scope.
func AllSessionsScope() ResumeScope { return ResumeScope{All: true} }

// Match reports whether h belongs to the scope. A session with no recorded Cwd
// (written before the field existed) is unattributed and only matches --all.
// A scope with no anchor directory degrades to --all: with no project to
// compare against, hiding every session would be strictly worse than showing
// them.
func (s ResumeScope) Match(h session.SessionHeader) bool {
	if s.All || s.Dir == "" {
		return true
	}
	if h.Cwd == "" {
		return false
	}
	return session.SameProject(h.Cwd, s.Dir)
}

// ParseResumeArg splits a /resume argument into the scope it selects and the
// selector left over (a 1-based number or a literal id; "" for the bare list).
// --all (or -a) anywhere in the argument lifts the project filter, so both
// "/resume --all" and "/resume --all 3" work.
func ParseResumeArg(arg, projectDir string) (ResumeScope, string) {
	scope := ProjectScope(projectDir)
	var sel []string
	for _, f := range strings.Fields(arg) {
		if f == AllSessionsFlag || f == "-a" {
			scope.All = true
			continue
		}
		sel = append(sel, f)
	}
	return scope, strings.Join(sel, " ")
}

// FilterSessions returns the headers in scope, preserving the store's
// newest-first order and capping the result at n (n <= 0 means no cap).
func FilterSessions(headers []session.SessionHeader, scope ResumeScope, n int) []session.SessionHeader {
	out := make([]session.SessionHeader, 0, len(headers))
	for _, h := range headers {
		if scope.Match(h) {
			out = append(out, h)
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// RecentSessions returns up to n most-recently-updated session headers in
// scope (the store already sorts newest-first). It backs the bare-/resume
// picker in every frontend so the list — and therefore the 1-based numbering —
// is identical everywhere.
func RecentSessions(store *session.Store, scope ResumeScope, n int) ([]session.SessionHeader, error) {
	headers, err := store.List()
	if err != nil {
		return nil, err
	}
	return FilterSessions(headers, scope, n), nil
}

// MostRecentSession returns the most recently updated session in scope; ok is
// false when the scope holds none. The caller decides what an empty project
// means: --continue falls back to the newest session of any project and names
// the directory it came from.
func MostRecentSession(store *session.Store, scope ResumeScope) (session.SessionHeader, bool, error) {
	headers, err := RecentSessions(store, scope, 1)
	if err != nil || len(headers) == 0 {
		return session.SessionHeader{}, false, err
	}
	return headers[0], true, nil
}

// ResolveResumeID maps a /resume argument to a session id: a 1-based number
// selects the nth session of the scope's recent list (the same list the caller
// displayed, so the numbering always agrees), anything else is treated as a
// literal id and resolves globally — an exact id is unambiguous, so it is never
// narrowed by the project filter. An empty argument is a usage error; callers
// show the recent list instead.
func ResolveResumeID(store *session.Store, arg, projectDir string) (string, error) {
	scope, sel := ParseResumeArg(arg, projectDir)
	if sel == "" {
		return "", fmt.Errorf("usage: /resume [--all] [n|id]")
	}
	if n, err := strconv.Atoi(sel); err == nil {
		headers, err := RecentSessions(store, scope, max(n, ResumeListN))
		if err != nil {
			return "", err
		}
		if n < 1 || n > len(headers) {
			return "", scopeRangeError(store, scope, n, len(headers))
		}
		return headers[n-1].ID, nil
	}
	return sel, nil
}

// scopeRangeError explains a session number that the scoped list cannot
// satisfy. When the project is the reason (other projects hold more sessions),
// it names the way out instead of leaving the user to guess.
func scopeRangeError(store *session.Store, scope ResumeScope, n, have int) error {
	if !scope.All {
		if headers, err := store.List(); err == nil {
			if total := len(headers); total > have {
				return fmt.Errorf("session %d out of range (this project has %d); /resume --all lists all %d", n, have, total)
			}
		}
	}
	return fmt.Errorf("session %d out of range (1-%d)", n, have)
}

// previewRunes caps a session preview to this many runes (CJK-safe).
const previewRunes = 40

// ResumeItem pairs a session header with a human-readable preview.
type ResumeItem struct {
	Header  session.SessionHeader
	Preview string
}

// RecentSessionsWithPreview returns up to n recent sessions with previews,
// newest first, filtered to scope. A session with no text yields an empty
// Preview.
func RecentSessionsWithPreview(store *session.Store, scope ResumeScope, n int) ([]ResumeItem, error) {
	headers, err := RecentSessions(store, scope, n)
	if err != nil {
		return nil, err
	}
	items := make([]ResumeItem, 0, len(headers))
	for _, h := range headers {
		items = append(items, ResumeItem{Header: h, Preview: SessionPreview(store, h.ID)})
	}
	return items, nil
}

// SessionDirDisplay renders a session's recorded directory for a list row:
// $HOME shortened to "~", and a missing Cwd (a session written before the field
// existed) as "unknown".
func SessionDirDisplay(h session.SessionHeader) string {
	if h.Cwd == "" {
		return "unknown"
	}
	return DisplayHomePath(h.Cwd)
}

// ScopedSessions reports how many recent sessions the store holds in total and
// how many of them scope selects, capped at n for the in-scope count. It powers
// the empty and short-list hints ("no sessions in ~/proj", "…--all lists 7"),
// which is what tells a user the filter — not an empty store — is hiding
// history.
func ScopedSessions(store *session.Store, scope ResumeScope, n int) (inScope, total int, err error) {
	headers, err := store.List()
	if err != nil {
		return 0, 0, err
	}
	return len(FilterSessions(headers, scope, n)), len(headers), nil
}

// ResumeScopeLabel describes a scope for a header line: the project directory
// it is anchored at, shortened for display, or "all projects" when unfiltered.
func ResumeScopeLabel(scope ResumeScope) string {
	if scope.All || scope.Dir == "" {
		return "all projects"
	}
	return DisplayHomePath(scope.Dir)
}

// ResumeEmptyMessage explains an empty scoped list: either the store really is
// empty, or the project filter is hiding other projects' sessions — in which
// case it names the flag that shows them.
func ResumeEmptyMessage(store *session.Store, scope ResumeScope) string {
	inScope, total, err := ScopedSessions(store, scope, ResumeListN)
	if err == nil && total > inScope {
		return fmt.Sprintf("No saved sessions in %s; %s lists all %d.", ResumeScopeLabel(scope), AllSessionsFlag, total)
	}
	if scope.All {
		return "No saved sessions yet."
	}
	return fmt.Sprintf("No saved sessions in %s.", ResumeScopeLabel(scope))
}

// ResumeHiddenHint reports how many sessions the project filter (or the list
// cap) is keeping out of a shown list, phrased for a dim trailing line; "" when
// nothing is hidden or the list is already unfiltered.
func ResumeHiddenHint(store *session.Store, scope ResumeScope, shown int) string {
	if scope.All {
		return ""
	}
	inScope, total, err := ScopedSessions(store, scope, ResumeListN)
	if err != nil || total <= inScope {
		return ""
	}
	hidden := total - inScope
	if inScope > shown {
		// The list is capped by ResumeListN, not by the project filter.
		return fmt.Sprintf("%d more session(s) here; %s lists all %d", hidden, AllSessionsFlag, total)
	}
	return fmt.Sprintf("%d session(s) in other projects; %s lists them", hidden, AllSessionsFlag)
}

// SessionPreview returns the first user message's excerpt for a session,
// falling back to the first assistant text. It is single-line and capped at
// previewRunes runes so CJK titles survive; "" when the session has no text.
func SessionPreview(store *session.Store, id string) string {
	_, entries, err := store.LoadEntries(id)
	if err != nil {
		return ""
	}
	fallback := ""
	for _, e := range entries {
		switch msg := e.Message.(type) {
		case agentcore.UserMessage:
			text := agentcore.ContentToText(msg.Content)
			// A `!` passthrough record previews as the original "!cmd" line,
			// not as the raw <user_shell_command> XML.
			if d, ok := ParseUserShellRecord(text); ok {
				text = "!" + d.Command
			}
			if t := previewText(text); t != "" {
				return t
			}
		case agentcore.AssistantMessage:
			if fallback == "" {
				fallback = previewText(agentcore.ContentToText(msg.Content))
			}
		}
	}
	return fallback
}

// previewText collapses s to one line and caps it at previewRunes runes.
func previewText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > previewRunes {
		return string(r[:previewRunes]) + "…"
	}
	return s
}
