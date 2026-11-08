package mygoui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/history"
	"github.com/getan/golder/internal/session"
)

func TestComposerHistoryNavigation(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	a := testApp()
	a.history = promptHistory{entries: []string{"first", "second"}, loaded: true}
	a.status.Running = true
	tt := ui.NewTester(a.composer, 900, 300)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("draft")
	for _, tc := range []struct {
		key  ui.Key
		want string
	}{
		{ui.KeyUp, "second"}, {ui.KeyUp, "first"}, {ui.KeyUp, "first"},
		{ui.KeyDown, "second"}, {ui.KeyDown, "draft"}, {ui.KeyDown, "draft"},
	} {
		tt.Key(0, tc.key)
		if a.draft != tc.want {
			t.Fatalf("key %v: got %q, want %q", tc.key, a.draft, tc.want)
		}
	}
}

func TestComposerHistoryKeepsMultilineCaretMovement(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	a := testApp()
	a.history = promptHistory{entries: []string{"older"}, loaded: true}
	tt := ui.NewTester(a.composer, 900, 300)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("第一行")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("second")
	draft := "第一行\nsecond"
	tt.Key(0, ui.KeyUp)
	if a.draft != draft || a.historyNav.browsing {
		t.Fatalf("up inside draft recalled history: %q", a.draft)
	}
	tt.Key(0, ui.KeyUp)
	if a.draft != "older" {
		t.Fatalf("up at first line = %q", a.draft)
	}
	tt.Key(0, ui.KeyDown)
	if a.draft != draft {
		t.Fatalf("down did not restore multiline draft: %q", a.draft)
	}
}

func TestComposerHistoryRecalledMultiline(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	a := testApp()
	a.history = promptHistory{entries: []string{"older", "first\nsecond"}, loaded: true}
	tt := ui.NewTester(a.composer, 900, 300)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Type("draft")
	tt.Key(0, ui.KeyUp)
	if a.draft != "first\nsecond" {
		t.Fatalf("up did not recall multiline entry: %q", a.draft)
	}
	tt.Key(0, ui.KeyUp)
	if a.draft != "first\nsecond" {
		t.Fatalf("up on the last line skipped caret movement: %q", a.draft)
	}
	tt.Key(0, ui.KeyDown)
	if a.draft != "first\nsecond" {
		t.Fatalf("down on the first line skipped caret movement: %q", a.draft)
	}
	tt.Key(0, ui.KeyDown)
	if a.draft != "draft" {
		t.Fatalf("down at the last line did not restore draft: %q", a.draft)
	}
}

func TestPromptHistoryPersistenceAndDedup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	var h promptHistory
	var cursor promptHistoryCursor
	h.record(&cursor, " first ", "session", "/project")
	h.record(&cursor, "first", "session", "/project")
	h.record(&cursor, "second", "session", "/project")
	if len(h.entries) != 2 || h.entries[0] != "first" || h.entries[1] != "second" {
		t.Fatalf("session history = %q", h.entries)
	}
	entries, err := history.Load(filepath.Join(dir, history.FileName), history.MaxEntries)
	if err != nil || len(entries) != 3 || entries[2].SessionID != "session" || entries[2].Cwd != "/project" {
		t.Fatalf("persisted history = %+v, %v", entries, err)
	}
	var reopened promptHistory
	reopened.load(nil)
	if len(reopened.entries) != 2 || reopened.entries[0] != "first" || reopened.entries[1] != "second" {
		t.Fatalf("reloaded history = %q", reopened.entries)
	}
}

func TestSubmitRecordsPromptHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	p := &guiBlockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	ctrl := appsession.New(appsession.Options{Model: "test", ProviderName: "fake", Provider: p, Cwd: project, Store: store})
	if err := ctrl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := New(ctrl)
	t.Cleanup(func() { _ = a.hub.Close() })
	if !a.submit("hello from GUI") {
		t.Fatalf("submit failed: %s", a.banner)
	}
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	close(p.release)
	entries, err := history.Load(filepath.Join(dir, history.FileName), history.MaxEntries)
	if err != nil || len(entries) != 1 || entries[0].Text != "hello from GUI" || entries[0].SessionID != ctrl.SessionID() || entries[0].Cwd != project {
		t.Fatalf("submitted history = %+v, %v", entries, err)
	}
	tt := ui.NewTester(a.composer, 900, 300)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyUp)
	if a.draft != "hello from GUI" {
		t.Fatalf("submitted prompt not recalled: %q", a.draft)
	}
}

func TestRejectedSubmitDoesNotEnterHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	a := testApp()
	a.ctrl = appsession.New(appsession.Options{}) // not open
	if a.submit("not sent") {
		t.Fatal("submission to an unopened controller succeeded")
	}
	entries, err := history.Load(filepath.Join(dir, history.FileName), history.MaxEntries)
	if err != nil || len(entries) != 0 || len(a.history.entries) != 0 {
		t.Fatalf("rejected prompt entered history: %+v, %v", entries, err)
	}
}

func TestPromptHistorySeedsOlderSessions(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	header := session.SessionHeader{ID: "old-session", CreatedAt: now, UpdatedAt: now, Model: "test", Provider: "test"}
	entries := []session.Entry{{ID: "old-entry", Timestamp: now, Message: agentcore.UserMessage{
		RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("earlier prompt")},
	}}}
	if err := store.SaveEntries(header, entries); err != nil {
		t.Fatal(err)
	}
	ctrl := appsession.New(appsession.Options{Model: "test", ProviderName: "test", Cwd: t.TempDir(), Store: store})
	if err := ctrl.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })
	var h promptHistory
	h.load(ctrl)
	if len(h.entries) != 1 || h.entries[0] != "earlier prompt" {
		t.Fatalf("seeded history = %q", h.entries)
	}
}

func TestHistoryEdgeUsesRuneOffsets(t *testing.T) {
	text := "中文\nlast"
	if !historyEdge(text, 2, true) || historyEdge(text, 3, true) || historyEdge(text, 2, false) || !historyEdge(text, 3, false) {
		t.Fatal("incorrect first or last line for Unicode caret")
	}
}
