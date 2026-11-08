package mygoui

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/history"
)

// promptHistory is shared across the window's sessions, just like the TUI's
// global history file. Each session keeps its own navigation cursor and draft.
type promptHistory struct {
	entries []string
	loaded  bool
}

type promptHistoryCursor struct {
	index    int
	draft    string
	browsing bool
}

func (h *promptHistory) load(ctrl *appsession.Controller) {
	if h.loaded {
		return
	}
	h.loaded = true
	path := golderhome.File(history.FileName)
	if path == "" {
		return
	}
	if ctrl != nil {
		ctrl.SeedPromptHistory(path)
	}
	entries, err := history.Load(path, history.MaxEntries)
	if err != nil {
		return
	}
	texts := make([]string, 0, len(entries)+len(h.entries))
	for _, entry := range entries {
		if text := strings.TrimSpace(entry.Text); text != "" {
			texts = append(texts, text)
		}
	}
	texts = append(texts, h.entries...)
	h.entries = texts[:0]
	for _, text := range texts {
		if len(h.entries) == 0 || h.entries[len(h.entries)-1] != text {
			h.entries = append(h.entries, text)
		}
	}
}

func (h *promptHistory) record(cursor *promptHistoryCursor, text, sessionID, cwd string) {
	text = strings.TrimSpace(text)
	if text != "" && (len(h.entries) == 0 || h.entries[len(h.entries)-1] != text) {
		h.entries = append(h.entries, text)
	}
	*cursor = promptHistoryCursor{}
	if path := golderhome.File(history.FileName); text != "" && path != "" {
		// History is a best-effort convenience, never a reason to fail a send.
		_ = history.Append(path, history.Entry{TS: time.Now().Unix(), SessionID: sessionID, Cwd: cwd, Text: text})
	}
}

func (h *promptHistory) previous(cursor *promptHistoryCursor, draft string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if !cursor.browsing {
		cursor.browsing, cursor.index, cursor.draft = true, len(h.entries), draft
	}
	if cursor.index > 0 {
		cursor.index--
	}
	return h.entries[cursor.index], true
}

func (h *promptHistory) next(cursor *promptHistoryCursor) (string, bool) {
	if !cursor.browsing || cursor.index >= len(h.entries) {
		return "", false
	}
	cursor.index++
	if cursor.index == len(h.entries) {
		cursor.browsing = false
		return cursor.draft, true
	}
	return h.entries[cursor.index], true
}

// historyEdge uses the text input's rune-indexed caret, not byte offsets.
func historyEdge(text string, caret int, up bool) bool {
	if caret < 0 || caret > utf8.RuneCountInString(text) {
		return false
	}
	runes := []rune(text)
	if up {
		return !strings.ContainsRune(string(runes[:caret]), '\n')
	}
	return !strings.ContainsRune(string(runes[caret:]), '\n')
}
