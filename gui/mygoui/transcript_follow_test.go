package mygoui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func followTestApp() *App {
	a := testApp()
	for i := range 40 {
		item := appsession.Item{ID: fmt.Sprintf("entry-%02d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Entry %02d", i), Done: true}
		a.index[item.ID] = len(a.items)
		a.items = append(a.items, item)
	}
	return a
}

func TestTranscriptFollowSurvivesIncreasedScrollRange(t *testing.T) {
	s := &transcriptWindow{}
	s.sync([]transcriptRow{{key: "first"}, {key: "last"}}, false)
	s.started, s.follow = true, true
	s.scroll.Y, s.scroll.MaxY = 500, 500
	s.plan(400, nil)
	// Layout can grow the scroll range before the tracked offset catches up.
	// That is not an operator scroll and must not turn following off.
	s.scroll.Y, s.scroll.MaxY = 500, 650
	s.plan(400, nil)
	if !s.follow {
		t.Fatal("content growth was mistaken for a user scroll")
	}
}

func TestTranscriptFollowRearmsWhenContentFits(t *testing.T) {
	s := &transcriptWindow{}
	s.sync([]transcriptRow{{key: "only"}}, false)
	s.started, s.follow = true, false
	s.scroll.Y, s.scroll.MaxY = 0, 0
	s.plan(400, nil)
	if !s.follow {
		t.Fatal("a view that fits without a selection must follow new output")
	}
}

func TestTranscriptKeepsFollowingStreamingGrowthAndResize(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	item := a.items[len(a.items)-1]
	item.Text += "\n\n" + strings.Repeat("A growing response wraps across several lines. ", 60)
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	tt.Frame()
	if !a.list.follow || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatalf("streaming growth lost follow: follow=%v scroll=%+v", a.list.follow, a.list.scroll)
	}
	tt.SetSize(900, 400)
	tt.Frame()
	if !a.list.follow || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatalf("resize lost follow: follow=%v scroll=%+v", a.list.follow, a.list.scroll)
	}
	for i := range 30 {
		item.Text += fmt.Sprintf("\nstreamed line %d", i)
		a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
		tt.Frame()
		if !a.list.follow || a.list.scroll.Y < a.list.scroll.MaxY-1 {
			t.Fatalf("stream update %d lost follow: follow=%v scroll=%+v", i, a.list.follow, a.list.scroll)
		}
	}
}

func TestTranscriptNewLinesAndCtrlE(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	tt.Frame()
	if a.list.follow || a.list.scroll.Y >= a.list.scroll.MaxY-1 {
		t.Fatalf("scrolling up did not pause following: follow=%v scroll=%+v", a.list.follow, a.list.scroll)
	}
	first, y := a.list.first, a.list.scroll.Y
	item := appsession.Item{ID: "stream-new", Kind: appsession.ItemAssistant, Text: "first new line\nsecond new line"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	if a.list.follow || a.list.first != first || a.list.scroll.Y != y || a.list.unseen != 2 {
		t.Fatalf("new output moved the reader or was not counted: first=%d/%d y=%v/%v unseen=%d", a.list.first, first, a.list.scroll.Y, y, a.list.unseen)
	}
	if !tt.HasText("↓ 2 new lines · Ctrl+E to jump") {
		t.Fatalf("missing unseen-lines notice: %q", tt.Texts())
	}
	item.Text += "\nthird new line"
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &item})
	tt.Frame()
	if a.list.unseen != 3 || !tt.HasText("↓ 3 new lines · Ctrl+E to jump") {
		t.Fatalf("streaming lines were not counted: unseen=%d texts=%q", a.list.unseen, tt.Texts())
	}
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Frame()
	if !a.list.follow || a.list.unseen != 0 || a.list.scroll.Y < a.list.scroll.MaxY-1 || tt.HasText("↓ 3 new lines · Ctrl+E to jump") {
		t.Fatalf("Ctrl+E did not return to the live tail: follow=%v unseen=%d scroll=%+v", a.list.follow, a.list.unseen, a.list.scroll)
	}
}

func TestTranscriptNewLinesNoticeCanBeClicked(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	item := appsession.Item{ID: "latest", Kind: appsession.ItemAssistant, Text: "new output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	if err := tt.Click("Jump to latest (1 new line)"); err != nil {
		t.Fatalf("new-output notice is not clickable: %v; texts=%q", err, tt.Texts())
	}
	tt.Frame()
	if !a.list.follow || a.list.unseen != 0 || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatalf("click did not return to the live tail: follow=%v unseen=%d scroll=%+v", a.list.follow, a.list.unseen, a.list.scroll)
	}
}

func TestTranscriptCtrlEWorksWithTranscriptFocus(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	tt.Frame()
	row := fmt.Sprintf("Entry %02d", (a.list.first+a.list.last)/2)
	if err := tt.Click(row); err != nil {
		t.Fatalf("focus transcript row %q: %v", row, err)
	}
	item := appsession.Item{ID: "focus-test", Kind: appsession.ItemAssistant, Text: "new output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Frame()
	if !a.list.follow || a.list.unseen != 0 || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatalf("Ctrl+E with transcript focus did not jump: follow=%v unseen=%d scroll=%+v", a.list.follow, a.list.unseen, a.list.scroll)
	}
}

func TestTranscriptCtrlEWorksAfterMouseScrollWithoutFocus(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	item := appsession.Item{ID: "mouse-scroll", Kind: appsession.ItemAssistant, Text: "new output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Frame()
	if !a.list.follow || a.list.unseen != 0 || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatalf("Ctrl+E after scrolling did not jump: follow=%v unseen=%d scroll=%+v", a.list.follow, a.list.unseen, a.list.scroll)
	}
}

func TestTranscriptScrollingBackToBottomClearsNotice(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	item := appsession.Item{ID: "latest", Kind: appsession.ItemAssistant, Text: "new output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	if a.list.unseen == 0 {
		t.Fatal("new output was not counted")
	}
	tt.Scroll(700, 200, 0, 10000)
	tt.Frame()
	if !a.list.follow || a.list.unseen != 0 || tt.HasText("↓ 1 new line · Ctrl+E to jump") {
		t.Fatalf("scrolling to the end did not clear the notice: follow=%v unseen=%d", a.list.follow, a.list.unseen)
	}
}

func TestTranscriptCountsOnlyNewTextBelowViewport(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	tt.Frame()
	old := a.items[0]
	old.Text += "\nnot new at the tail"
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &old})
	if a.list.unseen != 0 {
		t.Fatalf("an update above the viewport counted as new: %d", a.list.unseen)
	}
	last := a.items[len(a.items)-1]
	last.Text += " continued"
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &last})
	if a.list.unseen != 1 {
		t.Fatalf("an offscreen streaming fragment was not counted: %d", a.list.unseen)
	}
	last.Text += " more"
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &last})
	if a.list.unseen != 1 {
		t.Fatalf("one growing line was counted twice: %d", a.list.unseen)
	}
	last.Text += "\nnext line"
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &last})
	if a.list.unseen != 2 {
		t.Fatalf("new logical line was not counted: %d", a.list.unseen)
	}
}

func TestTranscriptCountsExpandedToolOutputButNotCollapsedOutput(t *testing.T) {
	a := followTestApp()
	item := exploreTestTool("running-tool", "bash", nil, "first")
	a.index[item.ID] = len(a.items)
	a.items = append(a.items, item)
	a.toolOpen[item.ID] = true
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	updated := item
	state := *item.Tool
	state.Output += "\nsecond\nthird"
	updated.Tool = &state
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &updated})
	if a.list.unseen != 2 {
		t.Fatalf("expanded tool output was not counted: %d", a.list.unseen)
	}
	a.toolOpen[item.ID] = false
	state2 := state
	state2.Output += "\nhidden"
	updated.Tool = &state2
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &updated})
	if a.list.unseen != 2 {
		t.Fatalf("collapsed tool output was counted: %d", a.list.unseen)
	}
}

func TestTranscriptDoesNotCountOutputInsideCollapsedExploreGroup(t *testing.T) {
	a := followTestApp()
	item := exploreTestTool("read-tool", "read", nil, "first")
	a.index[item.ID] = len(a.items)
	a.items = append(a.items, item)
	a.toolOpen[item.ID] = true // A previously expanded member can remain marked open.
	a.toolOpen["explore:"+item.ID] = false
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	updated := item
	state := *item.Tool
	state.Output += "\nhidden"
	updated.Tool = &state
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &updated})
	if a.list.unseen != 0 {
		t.Fatalf("output inside a collapsed group was counted: %d", a.list.unseen)
	}
}

func TestTranscriptWheelScrollPausesFollowing(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Wheel(700, 200, 0, -400)
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if a.list.follow || a.list.scroll.Y >= a.list.scroll.MaxY-1 {
		t.Fatalf("mouse wheel failed to pause follow: follow=%v scroll=%+v", a.list.follow, a.list.scroll)
	}
}

func TestTranscriptJumpCancelsWheelMotion(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Wheel(700, 200, 0, -400)
	tt.Advance(30 * time.Millisecond)
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	if !a.list.follow || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatalf("wheel momentum moved away after Ctrl+E: follow=%v scroll=%+v", a.list.follow, a.list.scroll)
	}
}

func TestTranscriptCtrlEDoesNotOverrideSettingsInput(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	item := appsession.Item{ID: "latest", Kind: appsession.ItemAssistant, Text: "new output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	a.settingsOpen = true
	a.settings = appsession.ProviderSettings{Provider: "openai", Providers: []string{"openai"}}
	a.settingsEdit = appsession.ProviderSettingsEdit{Provider: "openai", Model: "gpt-test"}
	tt.Frame()
	if err := tt.Click("Provider Base URL"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Frame()
	if a.list.follow || a.list.unseen != 1 {
		t.Fatalf("Ctrl+E in settings affected the transcript: follow=%v unseen=%d", a.list.follow, a.list.unseen)
	}
}

func TestTranscriptCtrlEDoesNotJumpBehindUnfocusedModal(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	item := appsession.Item{ID: "latest", Kind: appsession.ItemAssistant, Text: "new output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	tt.Frame()
	a.settingsOpen = true
	a.settings = appsession.ProviderSettings{Provider: "openai", Providers: []string{"openai"}}
	a.settingsEdit = appsession.ProviderSettingsEdit{Provider: "openai", Model: "gpt-test"}
	tt.Frame()
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Frame()
	if a.list.follow || a.list.unseen != 1 {
		t.Fatalf("Ctrl+E behind a modal affected the transcript: follow=%v unseen=%d", a.list.follow, a.list.unseen)
	}
}

func TestTranscriptJumpPreservesTextSelection(t *testing.T) {
	a := followTestApp()
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	tt.Frame()
	want := fmt.Sprintf("Entry %02d", (a.list.first+a.list.last)/2)
	row, ok := tt.Find(want)
	if !ok {
		t.Fatalf("visible row %q is missing", want)
	}
	tt.Press(row.X+1, row.Y+row.H/2)
	tt.Move(row.X+row.W-1, row.Y+row.H/2)
	tt.Release(row.X+row.W-1, row.Y+row.H/2)
	tt.Key(ui.Ctrl, ui.KeyE)
	tt.Frame()
	if !a.list.follow || a.list.scroll.Y < a.list.scroll.MaxY-1 {
		t.Fatal("Ctrl+E did not jump while text was selected")
	}
	tt.Command("copy")
	if got := tt.Clipboard(); got != want {
		t.Fatalf("jump changed the text selection: got %q, want %q", got, want)
	}
}

func TestTranscriptCountsBackgroundSessionChangesOnRestore(t *testing.T) {
	s := &transcriptWindow{started: true, follow: false, last: 3, index: map[string]int{"tail": 8}}
	s.scroll.Y, s.scroll.MaxY = 10, 100
	tail := appsession.Item{ID: "tail", Kind: appsession.ItemAssistant, Text: "old line"}
	items := []appsession.Item{
		{ID: "tail", Kind: appsession.ItemAssistant, Text: "old line\ncontinued"},
		{ID: "new", Kind: appsession.ItemAssistant, Text: "first\nsecond"},
	}
	s.noteRestoredItems(tail, items, map[string]int{"tail": 0, "new": 1})
	if s.unseen != 3 {
		t.Fatalf("background output was not counted on restore: %d", s.unseen)
	}
	s.noteRestoredItems(appsession.Item{ID: "missing"}, items, map[string]int{"tail": 0, "new": 1})
	if s.unseen != 3 {
		t.Fatal("a missing tail produced an invented count")
	}
}

func TestTranscriptSnapshotRefreshCountsNewOutputOnce(t *testing.T) {
	a := followTestApp()
	a.status.SessionID = "session-1"
	tt := ui.NewTester(a.view, 1200, 500)
	tt.Scroll(700, 200, 0, -500)
	first := a.list.first
	items := append([]appsession.Item(nil), a.items...)
	items[len(items)-1].Text += "\ncontinued"
	items = append(items, appsession.Item{ID: "new", Kind: appsession.ItemAssistant, Text: "first\nsecond"})
	snapshot := appsession.Snapshot{Status: a.status, Project: a.project, Items: items}
	a.applySnapshot(snapshot)
	tt.Frame()
	if a.list.first != first || a.list.unseen != 3 {
		t.Fatalf("snapshot lost the anchor or new lines: first=%d/%d unseen=%d", a.list.first, first, a.list.unseen)
	}
	a.applySnapshot(snapshot)
	tt.Frame()
	if a.list.unseen != 3 {
		t.Fatalf("unchanged snapshot was counted twice: %d", a.list.unseen)
	}
}
