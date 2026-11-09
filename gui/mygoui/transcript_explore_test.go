package mygoui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/judge"
)

func exploreTestTool(id, name string, input map[string]any, output string) appsession.Item {
	return appsession.Item{ID: id, Kind: appsession.ItemTool, Done: true,
		Tool: &appsession.ToolState{ID: id, Name: name, Input: input, Output: output}}
}

func TestExploreGroupsConsecutiveReadToolsAndKeepsBoundaries(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		exploreTestTool("read-a", "read", map[string]any{"path": "a.go"}, "contents a"),
		exploreTestTool("read-b", "read", map[string]any{"path": "b.go"}, "contents b"),
		exploreTestTool("search", "grep", map[string]any{"pattern": "TODO", "path": "internal"}, "search results"),
		exploreTestTool("find", "find", map[string]any{"glob": "*.go"}, "found files"),
		exploreTestTool("image", "view_image", map[string]any{"path": "diagram.png"}, "image details"),
		exploreTestTool("shell", "bash", map[string]any{"command": "ls -la"}, "shell output"),
		exploreTestTool("web", "websearch", map[string]any{"query": "reference"}, "web results"),
		exploreTestTool("list", "ls", map[string]any{"path": "docs"}, "listed files"),
		{ID: "assistant", Kind: appsession.ItemAssistant, Text: "result", Done: true},
		exploreTestTool("read-c", "read", map[string]any{"path": "c.go"}, "contents c"),
	}
	rows := a.projectedTranscriptRows()
	if len(rows) != 6 || len(rows[0].members) != 5 || rows[1].key != "shell" || rows[2].key != "web" || len(rows[3].members) != 1 || rows[4].key != "assistant" || len(rows[5].members) != 1 {
		t.Fatalf("wrong Explore boundaries: %+v", rows)
	}
	tt := ui.NewTester(a.view, 1200, 900)
	for _, want := range []string{"Explored · 5 calls", "Read a.go, b.go", "Search TODO in internal", "Find *.go", "View diagram.png", "bash · ls -la · done", "websearch · reference · done"} {
		if !tt.HasText(want) {
			t.Fatalf("collapsed transcript missing %q: %q", want, tt.Texts())
		}
	}
	if tt.HasText("contents a") || tt.HasText("read · a.go · done") {
		t.Fatalf("read cards should start folded: %q", tt.Texts())
	}
	if err := tt.Click("Explored · 5 calls"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	for _, want := range []string{"read · a.go · done", "contents a", "read · b.go · done", "contents b", "search results", "found files", "image details"} {
		if !tt.HasText(want) {
			t.Fatalf("expanded Explore missing %q: %q", want, tt.Texts())
		}
	}
}

func TestExploreStreamingUpdatesAndStableGroupKey(t *testing.T) {
	a := testApp()
	first := exploreTestTool("one", "read", nil, "")
	first.Done, first.Tool.Running = false, true
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &first})
	rows := a.projectedTranscriptRows()
	if len(rows) != 1 || len(rows[0].members) != 1 || !a.explorePresentation(rows[0]).running {
		t.Fatalf("running read was not grouped: %+v", rows)
	}
	first.Tool = &appsession.ToolState{ID: "one", Name: "read", Running: true, Input: map[string]any{"path": "streamed.go"}}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &first})
	if got := a.explorePresentation(a.projectedTranscriptRows()[0]); len(got.summary) != 1 || got.summary[0].Text != "Read streamed.go" {
		t.Fatalf("streamed arguments were not reflected: %+v", got)
	}
	second := exploreTestTool("two", "ls", map[string]any{"path": "docs"}, "files")
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &second})
	rows = a.projectedTranscriptRows()
	if len(rows) != 1 || rows[0].key != "one" || len(rows[0].members) != 2 {
		t.Fatalf("late read did not extend stable group: %+v", rows)
	}
	first.Tool = &appsession.ToolState{ID: "one", Name: "read", Input: map[string]any{"path": "streamed.go"}, Output: "failed", Error: true}
	first.Done, first.Failed = true, true
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &first})
	if got := a.explorePresentation(a.projectedTranscriptRows()[0]); got.running || !got.failed {
		t.Fatalf("finished failure was hidden: %+v", got)
	}
}

func TestExploreKeepsApprovalsAndReviewVisibleWhenFolded(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "read", Running: true, Input: map[string]any{"path": "/outside/a"}}},
		{ID: "review", Kind: appsession.ItemNote, Text: "sandbox reason", Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "read", Kind: int(judge.NoteSandboxed)}},
		{ID: "run-1-tool-b", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "b", Name: "read", Running: true, Input: map[string]any{"path": "/outside/b"}}},
	}
	approval := appsession.ApprovalView{ID: "approval-b", RunID: "run-1", ToolCallID: "b", Tool: "read", Summary: "read /outside/b", Rationale: "outside access"}
	a.applyEvent(appsession.Event{Kind: appsession.EventApproval, Approval: &approval})
	rows := a.projectedTranscriptRows()
	if len(rows) != 1 || len(rows[0].members) != 2 || approvalRowIndex(rows, a.pending, "approval-b") != 0 {
		t.Fatalf("approval was not grouped with the right call: %+v", rows)
	}
	tt := ui.NewTester(a.view, 1200, 900)
	for _, want := range []string{"Exploring · 2 calls · approval required", "Review · sandboxed", "read /outside/b", "outside access", "Allow once", "Deny"} {
		if !tt.HasText(want) {
			t.Fatalf("folded Explore hid %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Exploring · 2 calls · approval required"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if !tt.HasText("read · /outside/b · running · approval required") || !tt.HasText("Allow once") || !tt.HasText("Review · sandboxed") {
		t.Fatalf("expanded call lost its exact approval or review: %q", tt.Texts())
	}
}

func TestExploreSummariesMarkMixedEnvelopes(t *testing.T) {
	a := testApp()
	first := exploreTestTool("one", "read", map[string]any{"path": "a.go"}, "a")
	first.Tool.Details = map[string]any{"envelope": "sandbox"}
	second := exploreTestTool("two", "read", map[string]any{"path": "b.go"}, "b")
	second.Tool.Details = map[string]any{"envelope": "bare"}
	a.items = []appsession.Item{first, second}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{"Read a.go", "·sandbox", "Read b.go", "·unconfined", "mixed sandbox/unconfined"} {
		if !tt.HasText(want) {
			t.Fatalf("mixed group omitted %q: %q", want, tt.Texts())
		}
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "Explored") && (strings.Contains(text, "·sandbox") || strings.Contains(text, "·unconfined")) {
			t.Fatalf("mixed group made a group-wide envelope claim: %q", text)
		}
	}
	if err := tt.Click("Explored · 2 calls · mixed sandbox/unconfined"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	for _, want := range []string{"read · a.go · done ·sandbox", "read · b.go · done ·unconfined"} {
		if !tt.HasText(want) {
			t.Fatalf("expanded call omitted its envelope %q: %q", want, tt.Texts())
		}
	}
}

func TestExploreBoundsCollapsedSummaryAndRevealsLateApproval(t *testing.T) {
	a := testApp()
	for i := 0; i < 12; i++ {
		a.items = append(a.items, exploreTestTool(fmt.Sprintf("run-1-tool-%d", i), "grep", map[string]any{"pattern": fmt.Sprintf("q%d", i)}, "result"))
		a.items[i].Tool.ID = fmt.Sprint(i)
	}
	for i := 0; i < 100; i++ {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("notice-%d", i), Kind: appsession.ItemNotice, Text: fmt.Sprintf("other item %d", i)})
	}
	approval := appsession.ApprovalView{ID: "approval", RunID: "run-1", ToolCallID: "8", Tool: "grep", Summary: "exact search request"}
	a.applyEvent(appsession.Event{Kind: appsession.EventApproval, Approval: &approval})
	tt := ui.NewTester(a.view, 1200, 650)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("… 8 more steps") || !tt.HasText("exact search request") || !tt.HasText("Allow once") {
		t.Fatalf("long group did not reveal approval or limit summary: %q", tt.Texts())
	}
}

func TestExploreKeepsConcurrentApprovalsIndependent(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "read", Running: true}},
		{ID: "run-1-tool-b", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "b", Name: "read", Running: true}},
	}
	for _, p := range []appsession.ApprovalView{
		{ID: "approval-a", RunID: "run-1", ToolCallID: "a", Tool: "read", Summary: "first exact read"},
		{ID: "approval-b", RunID: "run-1", ToolCallID: "b", Tool: "read", Summary: "second exact read"},
	} {
		p := p
		a.applyEvent(appsession.Event{Kind: appsession.EventApproval, Approval: &p})
	}
	rows := a.projectedTranscriptRows()
	if len(rows) != 1 || len(rows[0].members) != 2 || len(rows[0].members[0].approvals) != 1 || rows[0].members[0].approvals[0] != 0 || len(rows[0].members[1].approvals) != 1 || rows[0].members[1].approvals[0] != 1 {
		t.Fatalf("approvals were misassociated: %+v", rows)
	}
	tt := ui.NewTester(a.view, 1200, 900)
	if !tt.HasText("first exact read") || !tt.HasText("second exact read") {
		t.Fatalf("both approvals must be reachable: %q", tt.Texts())
	}
	a.removeApproval("approval-a")
	rows = a.projectedTranscriptRows()
	if len(rows) != 1 || len(rows[0].members[0].approvals) != 0 || len(rows[0].members[1].approvals) != 1 || a.pending[rows[0].members[1].approvals[0]].ID != "approval-b" {
		t.Fatalf("remaining approval shifted to the wrong call: %+v", rows)
	}
	tt.Frame()
	if tt.HasText("first exact read") || !tt.HasText("second exact read") || !tt.HasText("Allow once") {
		t.Fatalf("wrong approval remained after resolution: %q", tt.Texts())
	}
}

func TestExploreLongReadBurstBoundsPreviewButChecksAllStates(t *testing.T) {
	a := testApp()
	for i := 0; i < 500; i++ {
		item := exploreTestTool(fmt.Sprintf("read-%d", i), "read", map[string]any{"path": fmt.Sprintf("file-%d.go", i)}, "content")
		if i == 499 {
			item.Tool.Error, item.Failed = true, true
			item.Tool.Details = map[string]any{"envelope": "bare"}
		} else {
			item.Tool.Details = map[string]any{"envelope": "sandbox"}
		}
		a.items = append(a.items, item)
	}
	rows := a.projectedTranscriptRows()
	if len(rows) != 1 || len(rows[0].members) != 500 {
		t.Fatalf("large read burst was not compacted: %d rows", len(rows))
	}
	group := a.explorePresentation(rows[0])
	if group.moreCalls != 372 || !group.failed || !group.mixed || len(group.summary) != 1 || strings.Contains(group.summary[0].Text, "file-128.go") {
		t.Fatalf("preview was unbounded or failed to scan later call status: %+v", group)
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Explored · 500 calls · failed") || !tt.HasText("… 372 more calls") {
		t.Fatalf("large group did not show bounded preview and failure: %q", tt.Texts())
	}
}

func TestExploreProjectionInvalidatesAcrossSnapshots(t *testing.T) {
	a := testApp()
	first := exploreTestTool("same-id", "read", map[string]any{"path": "first.go"}, "first")
	second := exploreTestTool("same-id", "read", map[string]any{"path": "second.go"}, "second")
	a.applySnapshot(appsession.Snapshot{SessionID: "session-1", Items: []appsession.Item{first}})
	if got := a.explorePresentation(a.projectedTranscriptRows()[0]).summary[0].Text; got != "Read first.go" {
		t.Fatalf("first session summary = %q", got)
	}
	a.applySnapshot(appsession.Snapshot{SessionID: "session-2", Items: []appsession.Item{second}})
	if got := a.explorePresentation(a.projectedTranscriptRows()[0]).summary[0].Text; got != "Read second.go" {
		t.Fatalf("stale first session summary in second session: %q", got)
	}
}
