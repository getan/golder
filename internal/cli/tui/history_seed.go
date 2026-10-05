package tui

// One-time backfill of the global prompt-history file from the session store.
// The history file only records prompts submitted after the feature shipped, so
// a long-time user would otherwise open a new session, press ↑, and see nothing
// even though every earlier prompt is sitting in ~/.golder/sessions. The first
// browse (↑ / Ctrl+R) merges those past user messages in, deduplicates by exact
// text, orders by timestamp, and cuts to the same window the reader uses. A
// marker file records that the merge ran; a failure leaves the marker unwritten
// so the next browse retries.

import (
	"os"
	"sort"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/history"
	"github.com/getan/golder/internal/session"
)

// maxSeedSessions bounds how many recent sessions the backfill scans, so a
// store with years of history still seeds in one quick pass.
const maxSeedSessions = 50

// seedMarkerSuffix names the file that records a completed backfill, stored
// next to history.jsonl.
const seedMarkerSuffix = ".seeded"

// seedPromptHistoryFromSessions merges the store's user messages into the
// history file at path. It is idempotent and cheap to call: once the marker
// exists it returns immediately, and the merge itself deduplicates by text.
func seedPromptHistoryFromSessions(store *session.Store, path string) {
	if store == nil || path == "" {
		return
	}
	marker := path + seedMarkerSuffix
	if _, err := os.Stat(marker); err == nil {
		return
	}

	// Start from whatever the file already holds (a user may have submitted a
	// prompt before the first browse), then add the sessions' prompts.
	existing, err := history.Load(path, 0)
	if err != nil {
		return
	}
	byText := make(map[string]history.Entry, len(existing))
	for _, e := range existing {
		if e.Text = strings.TrimSpace(e.Text); e.Text != "" {
			byText[e.Text] = e
		}
	}

	headers, err := store.List()
	if err == nil {
		if len(headers) > maxSeedSessions {
			headers = headers[:maxSeedSessions]
		}
		for _, h := range headers {
			_, entries, err := store.LoadEntries(h.ID)
			if err != nil {
				continue
			}
			for _, e := range entries {
				um, ok := e.Message.(agentcore.UserMessage)
				if !ok {
					continue
				}
				text := strings.TrimSpace(agentcore.ContentToText(um.Content))
				if text == "" {
					continue
				}
				candidate := history.Entry{
					TS:        e.Timestamp.Unix(),
					SessionID: h.ID,
					Text:      text,
				}
				if prev, ok := byText[text]; !ok || candidate.TS >= prev.TS {
					byText[text] = candidate
				}
			}
		}
	}

	merged := make([]history.Entry, 0, len(byText))
	for _, e := range byText {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].TS < merged[j].TS })
	if len(merged) > history.MaxEntries {
		merged = merged[len(merged)-history.MaxEntries:]
	}

	if err := history.WriteAll(path, merged); err != nil {
		return
	}
	_ = os.WriteFile(marker, nil, 0o600)
}
