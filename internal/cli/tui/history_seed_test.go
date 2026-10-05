package tui

// Tests for the one-time backfill that seeds history.jsonl from the session
// store: past sessions' user messages must merge in order, deduplicate by text
// keeping the newest copy, ignore non-user messages, and never re-run after the
// marker lands.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/history"
	"github.com/getan/golder/internal/session"
)

// saveTimedSession writes a session with explicit entry timestamps so merge
// ordering is deterministic.
func saveTimedSession(t *testing.T, store *session.Store, id string, entries ...session.Entry) {
	t.Helper()
	now := time.Unix(1, 0).UTC()
	h := session.SessionHeader{ID: id, CreatedAt: now, UpdatedAt: now, Model: "m", Provider: "p"}
	for i := range entries {
		if entries[i].ID == "" {
			entries[i].ID = id + "-" + string(rune('a'+i))
		}
	}
	if err := store.SaveEntries(h, entries); err != nil {
		t.Fatalf("SaveEntries(%s): %v", id, err)
	}
}

func userEntry(ts int64, text string) session.Entry {
	return session.Entry{
		Timestamp: time.Unix(ts, 0).UTC(),
		Message:   agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(text)}},
	}
}

// TestSeedPromptHistoryFromSessions covers the ordinary backfill: prompts from
// two past sessions merge oldest-first, a repeated prompt keeps its newest
// copy, an assistant message is ignored, and the marker prevents a re-run.
func TestSeedPromptHistoryFromSessions(t *testing.T) {
	store := newTestStore(t)
	saveTimedSession(t, store, "sess-a",
		userEntry(100, "old prompt"),
		userEntry(200, "shared prompt"),
		session.Entry{
			Timestamp: time.Unix(250, 0).UTC(),
			Message:   agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{agentcore.NewTextContent("assistant text")}},
		},
	)
	saveTimedSession(t, store, "sess-b",
		userEntry(300, "shared prompt"),
		userEntry(400, "newest prompt"),
	)

	path := filepath.Join(t.TempDir(), history.FileName)
	seedPromptHistoryFromSessions(store, path)

	got, err := history.Load(path, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("seeded %d entries (%+v), want 3 user prompts", len(got), got)
	}
	for i, want := range []string{"old prompt", "shared prompt", "newest prompt"} {
		if got[i].Text != want {
			t.Errorf("entry %d = %q, want %q", i, got[i].Text, want)
		}
	}
	if got[1].TS != 300 || got[1].SessionID != "sess-b" {
		t.Errorf("shared prompt kept %+v, want the newest copy (ts 300, sess-b)", got[1])
	}
	if got[0].SessionID != "sess-a" || got[0].TS != 100 {
		t.Errorf("old prompt = %+v, want ts 100 from sess-a", got[0])
	}

	// The marker makes the backfill a one-shot: a session added afterwards is
	// not merged in (its prompts flow through normal submission appends).
	if _, err := os.Stat(path + seedMarkerSuffix); err != nil {
		t.Fatalf("marker missing after seed: %v", err)
	}
	saveTimedSession(t, store, "sess-c", userEntry(500, "after marker"))
	seedPromptHistoryFromSessions(store, path)
	got, err = history.Load(path, 0)
	if err != nil {
		t.Fatalf("Load after second call: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("second seed changed the file (%d entries), want the marker to short-circuit", len(got))
	}
}

// TestSeedPromptHistoryMergesExistingFile covers the order of operations where
// the user submits a prompt before ever browsing: the backfill must merge the
// new prompt with the sessions' prompts in timestamp order rather than clobber
// the file.
func TestSeedPromptHistoryMergesExistingFile(t *testing.T) {
	store := newTestStore(t)
	saveTimedSession(t, store, "sess-a", userEntry(100, "from session"))

	path := filepath.Join(t.TempDir(), history.FileName)
	if err := history.Append(path, history.Entry{TS: 350, SessionID: "live", Text: "typed before browsing"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	seedPromptHistoryFromSessions(store, path)

	got, err := history.Load(path, 0)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("merged entries = %d (%+v), want 2", len(got), got)
	}
	if got[0].Text != "from session" || got[1].Text != "typed before browsing" {
		t.Errorf("merge order = %q then %q, want the session prompt first", got[0].Text, got[1].Text)
	}
}
