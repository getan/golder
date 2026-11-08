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

func TestTranscriptProjectionAssociatesExactCallsOnly(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "user", Kind: appsession.ItemUser, Text: "start"},
		{ID: "note-a", Kind: appsession.ItemNote, Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash"}},
		{ID: "child-note", Kind: appsession.ItemNote, Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash", Origin: "child"}},
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "bash"}},
		{ID: "run-1-tool-b", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "b", Name: "bash"}},
		{ID: "orphan-note", Kind: appsession.ItemNote, Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "missing", Tool: "bash"}},
		{ID: "note-other-run", Kind: appsession.ItemNote, Note: &appsession.NoteState{RunID: "run-2", ToolCallID: "a", Tool: "bash"}},
		{ID: "run-2-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "bash"}},
	}
	a.pending = []appsession.ApprovalView{
		{ID: "approval-a", RunID: "run-1", ToolCallID: "a", Tool: "bash"},
		{ID: "approval-b", RunID: "run-1", ToolCallID: "b", Tool: "bash"},
		{ID: "approval-child", RunID: "run-1", ToolCallID: "a", Tool: "bash", Origin: "child"},
		{ID: "approval-unknown", Tool: "bash"},
	}
	rows := a.projectedTranscriptRows()
	byKey := make(map[string]transcriptRow, len(rows))
	for _, row := range rows {
		if _, exists := byKey[row.key]; exists {
			t.Fatalf("duplicate row key %q", row.key)
		}
		byKey[row.key] = row
	}
	if row := byKey["run-1-tool-a"]; row.itemIndex != 3 || len(row.notes) != 1 || row.notes[0] != 1 || len(row.approvals) != 1 || row.approvals[0] != 0 {
		t.Fatalf("call a grouped incorrectly: %+v", row)
	}
	if row := byKey["run-1-tool-b"]; row.itemIndex != 4 || len(row.notes) != 0 || len(row.approvals) != 1 || row.approvals[0] != 1 {
		t.Fatalf("call b grouped incorrectly: %+v", row)
	}
	if row := byKey["run-2-tool-a"]; row.itemIndex != 7 || len(row.notes) != 1 || row.notes[0] != 6 || len(row.approvals) != 0 {
		t.Fatalf("another run was conflated: %+v", row)
	}
	for _, key := range []string{"child-note", "orphan-note", "approval-child", "approval-unknown"} {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("unmatched or child review %q was hidden: %+v", key, rows)
		}
	}
	if index := approvalRowIndex(rows, a.pending, "approval-a"); index < 0 || rows[index].key != "run-1-tool-a" {
		t.Fatalf("approval should reveal its own tool, index=%d", index)
	}
}

func TestTranscriptProjectionNeverBindsMismatchedToolIdentity(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "other", Name: "read"}},
		{ID: "note", Kind: appsession.ItemNote, Text: "review", Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash"}},
	}
	a.pending = []appsession.ApprovalView{{ID: "approval", RunID: "run-1", ToolCallID: "a", Tool: "bash", Summary: "exact bash action"}}
	rows := a.projectedTranscriptRows()
	if len(rows) != 3 || rows[0].key != "run-1-tool-a" || len(rows[0].notes) != 0 || len(rows[0].approvals) != 0 || rows[1].key != "note" || rows[2].key != "approval" {
		t.Fatalf("mismatched identity was grouped or lost: %+v", rows)
	}
	if index := approvalRowIndex(rows, a.pending, "approval"); index != 2 {
		t.Fatalf("standalone approval was not independently reachable: %d", index)
	}
}

func TestTranscriptProjectionRechecksLateToolName(t *testing.T) {
	a := testApp()
	tool := appsession.Item{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a"}}
	a.items = []appsession.Item{tool}
	a.index[tool.ID] = 0
	a.pending = []appsession.ApprovalView{{ID: "approval", RunID: "run-1", ToolCallID: "a", Tool: "bash"}}
	if rows := a.projectedTranscriptRows(); len(rows) != 1 || len(rows[0].approvals) != 1 {
		t.Fatalf("incomplete tool did not get its approval: %+v", rows)
	}
	tool.Tool = &appsession.ToolState{ID: "a", Name: "read"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &tool})
	rows := a.projectedTranscriptRows()
	if len(rows) != 2 || len(rows[0].approvals) != 0 || rows[1].key != "approval" {
		t.Fatalf("late conflicting name did not separate the approval: %+v", rows)
	}
}

func TestApprovalAndReviewFoldInsideToolCard(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "bash", Running: true, Input: map[string]any{"command": "echo one"}}},
		{ID: "review-a", Kind: appsession.ItemNote, Text: "private review rationale", Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash", Kind: int(judge.NoteSandboxed)}},
	}
	a.pending = []appsession.ApprovalView{{ID: "approval-a", RunID: "run-1", ToolCallID: "a", Tool: "bash", Summary: "echo one", Rationale: "needs exact review"}}
	tt := ui.NewTester(a.view, 1200, 800)
	head := "bash · echo one · running · approval required · sandboxed"
	for _, want := range []string{head, "!  Approval required · bash", "needs exact review", "Allow once", "Review · sandboxed"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in expanded approval: %q", want, tt.Texts())
		}
	}
	if tt.HasText("private review rationale") {
		t.Fatal("review details should start collapsed")
	}
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Advance(250 * time.Millisecond)
	}
	click("Review · sandboxed")
	if !tt.HasText("private review rationale") {
		t.Fatalf("review did not expand: %q", tt.Texts())
	}
	click("Review · sandboxed")
	if tt.HasText("private review rationale") {
		t.Fatalf("review did not collapse: %q", tt.Texts())
	}
	click("!  Approval required · bash")
	if tt.HasText("Allow once") {
		t.Fatalf("approval did not collapse: %q", tt.Texts())
	}
	click("!  Approval required · bash")
	if !tt.HasText("Allow once") {
		t.Fatalf("approval did not reopen: %q", tt.Texts())
	}
	click(head)
	if tt.HasText("Allow once") {
		t.Fatalf("tool card did not collapse: %q", tt.Texts())
	}
	click(head)
	if !tt.HasText("Allow once") {
		t.Fatalf("tool card did not reopen: %q", tt.Texts())
	}
}

func TestPendingApprovalWithoutToolUsesStablePlaceholder(t *testing.T) {
	a := testApp()
	a.pending = []appsession.ApprovalView{{ID: "approval", RunID: "run-1", ToolCallID: "call-1", Tool: "bash", Summary: "echo hello"}}
	rows := a.projectedTranscriptRows()
	if len(rows) != 1 || rows[0].key != "run-1-tool-call-1" || rows[0].placeholder == nil || len(rows[0].approvals) != 1 {
		t.Fatalf("missing approval placeholder: %+v", rows)
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("bash · pending · approval required") || !tt.HasText("echo hello") || !tt.HasText("Allow once") || !tt.HasText("Waiting for approval") {
		t.Fatalf("pending approval is not visible in the tool card: %q", tt.Texts())
	}
	item := appsession.Item{ID: "run-1-tool-call-1", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "call-1", Name: "bash", Running: true}}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &item})
	rows = a.projectedTranscriptRows()
	if len(rows) != 1 || rows[0].key != item.ID || rows[0].itemIndex != 0 || rows[0].placeholder != nil || len(rows[0].approvals) != 1 {
		t.Fatalf("placeholder did not become the real card: %+v", rows)
	}
	tt.Frame()
	if !tt.HasText("echo hello") || !tt.HasText("Allow once") {
		t.Fatalf("approval disappeared when the tool arrived: %q", tt.Texts())
	}
}

func TestReviewAttachesWhenItsToolArrivesLater(t *testing.T) {
	a := testApp()
	note := appsession.Item{ID: "note", Kind: appsession.ItemNote, Text: "sandboxed review",
		Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "call-1", Tool: "bash", Kind: int(judge.NoteSandboxed)}}
	a.items = []appsession.Item{note}
	a.index[note.ID] = 0
	if rows := a.projectedTranscriptRows(); len(rows) != 1 || rows[0].key != note.ID {
		t.Fatalf("unmatched note should remain standalone: %+v", rows)
	}
	tool := appsession.Item{ID: "run-1-tool-call-1", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "call-1", Name: "bash", Running: true}}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemAdded, Item: &tool})
	rows := a.projectedTranscriptRows()
	if len(rows) != 1 || rows[0].key != tool.ID || rows[0].itemIndex != 1 || len(rows[0].notes) != 1 || rows[0].notes[0] != 0 {
		t.Fatalf("late tool did not absorb its note: %+v", rows)
	}
	first := &rows[0]
	updated := tool
	updated.Tool = &appsession.ToolState{ID: "call-1", Name: "bash", Running: true, Output: "streaming output"}
	a.applyEvent(appsession.Event{Kind: appsession.EventItemUpdated, Item: &updated})
	rows = a.projectedTranscriptRows()
	if &rows[0] != first || a.items[rows[0].itemIndex].Tool.Output != "streaming output" {
		t.Fatal("ordinary tool output should update the card without rebuilding its projection")
	}
}

func TestFoldedReviewKeepsSecurityOutcomeAndOriginVisible(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "bash"}},
		{ID: "escape-note", Kind: appsession.ItemNote, Text: "detailed escape rationale", Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash", Kind: int(judge.NoteEscaped)}},
		{ID: "child-note", Kind: appsession.ItemNote, Text: "child review details", Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash", Origin: "worker", Kind: int(judge.NoteSandboxed)}},
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("bash · done · outside sandbox") || !tt.HasText("Review · sandboxed · from worker") {
		t.Fatalf("folded review hid its outcome or provenance: %q", tt.Texts())
	}
	if tt.HasText("detailed escape rationale") || tt.HasText("child review details") {
		t.Fatalf("review details should begin folded: %q", tt.Texts())
	}
	if err := tt.Click("Review · sandboxed · from worker"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if !tt.HasText("child review details") {
		t.Fatalf("standalone child review could not be expanded: %q", tt.Texts())
	}
}

func TestPendingApprovalRevealsOffscreenTool(t *testing.T) {
	a := testApp()
	a.items = append(a.items, appsession.Item{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "bash", Running: true}})
	for i := 0; i < 100; i++ {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("notice-%d", i), Kind: appsession.ItemNotice, Text: fmt.Sprintf("other item %d", i)})
	}
	approval := appsession.ApprovalView{ID: "approval", RunID: "run-1", ToolCallID: "a", Tool: "bash", Summary: "exact request"}
	a.applyEvent(appsession.Event{Kind: appsession.EventApproval, Approval: &approval})
	tt := ui.NewTester(a.view, 1200, 600)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("exact request") || !tt.HasText("Allow once") {
		t.Fatalf("offscreen approval was not revealed: %s", strings.Join(tt.Texts(), " | "))
	}
}
