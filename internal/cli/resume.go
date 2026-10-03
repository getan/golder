package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/session"
)

// RecentSessions returns up to n most-recently-updated session headers (the
// store already sorts newest-first). It backs the bare-/resume picker in every
// frontend so the list is identical everywhere.
func RecentSessions(store *session.Store, n int) ([]session.SessionHeader, error) {
	headers, err := store.List()
	if err != nil {
		return nil, err
	}
	if len(headers) > n {
		headers = headers[:n]
	}
	return headers, nil
}

// ResolveResumeID maps a /resume argument to a session id: a 1-based number
// selects the nth recent session, anything else is treated as a literal id
// (existence is validated by the loader). An empty argument is a usage error;
// callers show the recent list instead.
func ResolveResumeID(store *session.Store, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("usage: /resume [n|id]")
	}
	if n, err := strconv.Atoi(arg); err == nil {
		headers, err := RecentSessions(store, max(n, 10))
		if err != nil {
			return "", err
		}
		if n < 1 || n > len(headers) {
			return "", fmt.Errorf("session %d out of range (1-%d)", n, len(headers))
		}
		return headers[n-1].ID, nil
	}
	return arg, nil
}

// previewRunes caps a session preview to this many runes (CJK-safe).
const previewRunes = 40

// ResumeItem pairs a session header with a human-readable preview.
type ResumeItem struct {
	Header  session.SessionHeader
	Preview string
}

// RecentSessionsWithPreview returns up to n recent sessions with previews,
// newest first. A session with no text yields an empty Preview.
func RecentSessionsWithPreview(store *session.Store, n int) ([]ResumeItem, error) {
	headers, err := RecentSessions(store, n)
	if err != nil {
		return nil, err
	}
	items := make([]ResumeItem, 0, len(headers))
	for _, h := range headers {
		items = append(items, ResumeItem{Header: h, Preview: SessionPreview(store, h.ID)})
	}
	return items, nil
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
			if t := previewText(agentcore.ContentToText(msg.Content)); t != "" {
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
