package mygoui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestTranscriptSelectionAcrossCollapsedCards(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "first", Kind: appsession.ItemAssistant, Text: "First response alpha.", Done: true},
		{ID: "thinking", Kind: appsession.ItemThinking, Text: "hidden thinking", Done: true},
		exploreTestTool("shell", "bash", map[string]any{"command": "echo hello"}, "hidden output"),
		{ID: "last", Kind: appsession.ItemAssistant, Text: "Last response omega.", Done: true},
	}
	tt := ui.NewTester(a.view, 1200, 900)
	first, ok := tt.Find("First response alpha.")
	if !ok {
		t.Fatalf("first response missing: %q", tt.Texts())
	}
	last, ok := tt.Find("Last response omega.")
	if !ok {
		t.Fatalf("last response missing: %q", tt.Texts())
	}
	tt.Press(first.X+1, first.Y+first.H/2)
	tt.Move(last.X+last.W-1, last.Y+last.H/2)
	tt.Release(last.X+last.W-1, last.Y+last.H/2)
	tt.Command("copy")
	if got := tt.Clipboard(); !strings.Contains(got, "First response alpha.") || !strings.Contains(got, "Last response omega.") || strings.Contains(got, "hidden thinking") || strings.Contains(got, "hidden output") {
		t.Fatalf("cross-card selection copied %q", got)
	}
	if a.thinkingOpen["thinking"] || a.toolOpen["shell"] {
		t.Fatal("dragging across cards activated a control")
	}
	if err := tt.Click("Thinking"); err != nil {
		t.Fatal(err)
	}
	if !a.thinkingOpen["thinking"] {
		t.Fatal("thinking card no longer opens after selecting")
	}
}

func TestTranscriptCanCopyFullResponseAsMarkdown(t *testing.T) {
	a := testApp()
	source := "A **bold** response with `code`."
	a.items = []appsession.Item{{ID: "reply", Kind: appsession.ItemAssistant, Text: source, Done: true}}
	tt := ui.NewTester(a.view, 1200, 600)
	if err := tt.RightClick("A bold response with code."); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy response as Markdown"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != source {
		t.Fatalf("copied markdown %q, want %q", got, source)
	}
}

func TestTranscriptRightClickCopiesCrossCardSelection(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "first", Kind: appsession.ItemUser, Text: "First message."},
		{ID: "last", Kind: appsession.ItemAssistant, Text: "Second message.", Done: true},
	}
	tt := ui.NewTester(a.view, 1200, 600)
	first, _ := tt.Find("First message.")
	last, _ := tt.Find("Second message.")
	tt.Press(first.X+1, first.Y+first.H/2)
	tt.Move(last.X+last.W-1, last.Y+last.H/2)
	tt.Release(last.X+last.W-1, last.Y+last.H/2)
	if err := tt.RightClick("Second message."); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "First message.\nSecond message." {
		t.Fatalf("context-menu copy lost the selection: %q", got)
	}
}

func TestTranscriptSelectionSignalsInlineFormula(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{{ID: "math", Kind: appsession.ItemAssistant, Text: "Before $x^2$ after.", Done: true}}
	tt := ui.NewTester(a.view, 1200, 600)
	text := "Before \ufffc after."
	row, ok := tt.Find(text)
	if !ok {
		t.Fatalf("inline math paragraph missing: %q", tt.Texts())
	}
	tt.Press(row.X+1, row.Y+row.H/2)
	tt.Move(row.X+row.W-1, row.Y+row.H/2)
	tt.Release(row.X+row.W-1, row.Y+row.H/2)
	tt.Command("copy")
	if got := tt.Clipboard(); got != "Before [formula] after." {
		t.Fatalf("inline formula copied as %q", got)
	}
	if !strings.Contains(a.banner, "[formula]") {
		t.Fatalf("missing formula-copy notice: %q", a.banner)
	}
	if !tt.HasText("Inline formulas are represented as [formula] in copied text.") {
		t.Fatalf("formula-copy notice was not displayed: %q", tt.Texts())
	}
}

func TestTranscriptCopyWaitsForMaterialization(t *testing.T) {
	s := transcriptWindow{keys: []string{"a", "b", "c"}, startMarker: 1, endMarker: 2,
		ranges: []transcriptRange{{1, 3}}}
	all := []ui.SelectedText{{ID: 1}, {ID: 3, Text: "text", End: 4}, {ID: 2}}
	if s.copyReady(all) {
		t.Fatal("select-all copy accepted an unbuilt row")
	}
	s.ranges = []transcriptRange{{0, 3}}
	if !s.copyReady(all) {
		t.Fatal("select-all copy rejected materialized rows")
	}
	s.ranges = []transcriptRange{{0, 1}, {2, 3}}
	if s.copyReady(all[1:2]) {
		t.Fatal("copy accepted changed selection across an unbuilt gap")
	}
	s.selection = transcriptSelection(all[1:2])
	if !s.copyReady(all[1:2]) {
		t.Fatal("copy rejected an unchanged pinned selection")
	}
}

func TestTranscriptSelectionAcrossVirtualizedScroll(t *testing.T) {
	a := testApp()
	for i := range 40 {
		text := fmt.Sprintf("Entry %02d distinct text.", i)
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%02d", i), Kind: appsession.ItemAssistant, Text: text, Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	last, ok := tt.Find("Entry 39 distinct text.")
	if !ok {
		t.Fatalf("last response missing: %q", tt.Texts())
	}
	firstVisible, _ := a.list.Visible()
	visible, ok := tt.Find(fmt.Sprintf("Entry %02d distinct text.", firstVisible))
	if !ok {
		t.Fatalf("first visible row %d missing: %q", firstVisible, tt.Texts())
	}
	tt.Press(last.X+last.W-1, last.Y+last.H/2)
	tt.Move(visible.X+1, visible.Y-20)
	for range 300 {
		tt.Frame()
		firstVisible, _ = a.list.Visible()
		if firstVisible == 0 {
			break
		}
	}
	first, ok := tt.Find("Entry 00 distinct text.")
	if !ok || firstVisible != 0 {
		t.Fatalf("drag did not reach first row: first=%d texts=%q", firstVisible, tt.Texts())
	}
	tt.Move(first.X+1, first.Y+first.H/2)
	tt.Release(first.X+1, first.Y+first.H/2)
	tt.Command("copy")
	got := tt.Clipboard()
	var wantLines []string
	for i := range 40 {
		want := fmt.Sprintf("Entry %02d distinct text.", i)
		wantLines = append(wantLines, want)
		if !strings.Contains(got, want) {
			t.Fatalf("selection lost %q while scrolling: %q", want, got)
		}
	}
	if want := strings.Join(wantLines, "\n"); got != want {
		t.Fatalf("selection order or boundaries changed: %q, want %q", got, want)
	}
}

func TestTranscriptSelectionForwardAcrossVirtualizedScroll(t *testing.T) {
	a := testApp()
	for i := range 40 {
		text := fmt.Sprintf("Entry %02d distinct text.", i)
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%02d", i), Kind: appsession.ItemAssistant, Text: text, Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	a.list.scroll.Y = 0
	tt.Frame()
	first, ok := tt.Find("Entry 00 distinct text.")
	if !ok {
		t.Fatalf("first response missing: %q", tt.Texts())
	}
	_, lastVisible := a.list.Visible()
	visible, ok := tt.Find(fmt.Sprintf("Entry %02d distinct text.", lastVisible))
	if !ok {
		t.Fatalf("last visible row %d missing: %q", lastVisible, tt.Texts())
	}
	tt.Press(first.X+1, first.Y+first.H/2)
	tt.Move(visible.X+visible.W-1, visible.Y+visible.H+20)
	for range 300 {
		tt.Frame()
		_, lastVisible = a.list.Visible()
		if lastVisible == 39 {
			break
		}
	}
	last, ok := tt.Find("Entry 39 distinct text.")
	if !ok || lastVisible != 39 {
		t.Fatalf("drag did not reach last row: last=%d texts=%q", lastVisible, tt.Texts())
	}
	tt.Move(last.X+last.W-1, last.Y+last.H/2)
	tt.Release(last.X+last.W-1, last.Y+last.H/2)
	tt.Command("copy")
	var want []string
	for i := range 40 {
		want = append(want, fmt.Sprintf("Entry %02d distinct text.", i))
	}
	if got, expected := tt.Clipboard(), strings.Join(want, "\n"); got != expected {
		t.Fatalf("forward selection lost or reordered text: %q, want %q", got, expected)
	}
}

func TestTranscriptSelectionIncludesOnlyExpandedCardContent(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "first", Kind: appsession.ItemAssistant, Text: "First alpha.", Done: true},
		{ID: "thinking", Kind: appsession.ItemThinking, Text: "visible thinking details", Done: true},
		exploreTestTool("shell", "bash", map[string]any{"command": "echo hello"}, "visible output details"),
		{ID: "last", Kind: appsession.ItemAssistant, Text: "Last omega.", Done: true},
	}
	a.thinkingOpen["thinking"], a.toolOpen["shell"] = true, true
	tt := ui.NewTester(a.view, 1200, 900)
	first, ok := tt.Find("First alpha.")
	if !ok {
		t.Fatalf("first response missing: %q", tt.Texts())
	}
	last, ok := tt.Find("Last omega.")
	if !ok {
		t.Fatalf("last response missing: %q", tt.Texts())
	}
	tt.Press(first.X+1, first.Y+first.H/2)
	tt.Move(last.X+last.W-1, last.Y+last.H/2)
	tt.Release(last.X+last.W-1, last.Y+last.H/2)
	tt.Command("copy")
	got := tt.Clipboard()
	for _, want := range []string{"First alpha.", "visible thinking details", "visible output details", "Last omega."} {
		if !strings.Contains(got, want) {
			t.Fatalf("selection omitted visible %q: %q", want, got)
		}
	}
}

func TestTranscriptSelectionAcrossApprovalAndReviewCards(t *testing.T) {
	a := testApp()
	a.items = []appsession.Item{
		{ID: "user", Kind: appsession.ItemUser, Text: "User prompt begins."},
		{ID: "thinking", Kind: appsession.ItemThinking, Text: "Thought details.", Done: true},
		{ID: "run-1-tool-a", Kind: appsession.ItemTool, Tool: &appsession.ToolState{ID: "a", Name: "bash", Input: map[string]any{"command": "echo ok"}, Output: "Tool output."}},
		{ID: "review", Kind: appsession.ItemNote, Text: "Review details.", Note: &appsession.NoteState{RunID: "run-1", ToolCallID: "a", Tool: "bash"}},
		{ID: "last", Kind: appsession.ItemAssistant, Text: "Assistant response ends.", Done: true},
	}
	a.pending = []appsession.ApprovalView{{ID: "approval", RunID: "run-1", ToolCallID: "a", Tool: "bash", Summary: "Approval request details."}}
	a.thinkingOpen["thinking"], a.toolOpen["run-1-tool-a"], a.approvalOpen["review"] = true, true, true
	tt := ui.NewTester(a.view, 1200, 1200)
	first, ok := tt.Find("User prompt begins.")
	if !ok {
		t.Fatalf("user message missing: %q", tt.Texts())
	}
	last, ok := tt.Find("Assistant response ends.")
	if !ok {
		t.Fatalf("assistant message missing: %q", tt.Texts())
	}
	tt.Press(first.X+1, first.Y+first.H/2)
	tt.Move(last.X+last.W-1, last.Y+last.H/2)
	tt.Release(last.X+last.W-1, last.Y+last.H/2)
	tt.Command("copy")
	got := tt.Clipboard()
	for _, want := range []string{"User prompt begins.", "Thought details.", "Approval request details.", "Review details.", "Tool output.", "Assistant response ends."} {
		if !strings.Contains(got, want) {
			t.Fatalf("selection omitted %q: %q", want, got)
		}
	}
	for _, control := range []string{"Allow once", "Deny", "Always this session"} {
		if strings.Contains(got, control) {
			t.Fatalf("selection copied button %q: %q", control, got)
		}
	}
}

func TestTranscriptWindowVirtualizesWithoutSelection(t *testing.T) {
	a := testApp()
	for i := range 1000 {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%04d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Message %04d", i), Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	if !tt.HasText("Message 0999") {
		t.Fatalf("latest message is missing: %q", tt.Texts())
	}
	if count := a.list.builtHi - a.list.builtLo; count > 50 {
		t.Fatalf("built %d rows for a 1000-row unselected session", count)
	}
	a.list.scroll.Y = 0
	tt.Frame()
	if !tt.HasText("Message 0000") || a.list.first != 0 {
		t.Fatalf("scrolling to the start missed its rows: first=%d texts=%q", a.list.first, tt.Texts())
	}
	if count := a.list.builtHi - a.list.builtLo; count > 50 {
		t.Fatalf("scrolling built %d rows for a 1000-row unselected session", count)
	}
}

func TestTranscriptWindowReachesRowsOfDifferentHeights(t *testing.T) {
	a := testApp()
	for i := range 200 {
		text := fmt.Sprintf("Message %03d", i)
		if i%7 == 0 {
			text += "\n\n" + strings.Repeat("A long paragraph that wraps across lines. ", 20)
		}
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%03d", i), Kind: appsession.ItemAssistant, Text: text, Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	a.list.scroll.Y = 0
	tt.Frame()
	seen := make(map[string]bool)
	for range 500 {
		for _, text := range tt.Texts() {
			if strings.HasPrefix(text, "Message ") {
				seen[text] = true
			}
		}
		if a.list.scroll.Y >= a.list.scroll.MaxY-1 {
			break
		}
		a.list.scroll.Y = min(a.list.scroll.Y+180, a.list.scroll.MaxY)
		tt.Frame()
	}
	for i := range 200 {
		want := fmt.Sprintf("Message %03d", i)
		if !seen[want] {
			t.Fatalf("scrolling skipped %q (saw %d rows)", want, len(seen))
		}
	}
}

func TestTranscriptWindowMeasurementOnlyAdjustsKnownHeights(t *testing.T) {
	s := &transcriptWindow{}
	s.sync([]transcriptRow{{key: "before"}, {key: "anchor"}}, false)
	s.started, s.first, s.scroll.Y = true, 1, 1000
	if !s.measure(0, 20) || s.scroll.Y != 1000 {
		t.Fatalf("first measurement moved scroll: %v", s.scroll.Y)
	}
	if !s.measure(0, 40) || s.scroll.Y != 1020 {
		t.Fatalf("resize of measured row did not preserve anchor: %v", s.scroll.Y)
	}
}

func TestTranscriptSelectionStableOnPressAtDifferentZoom(t *testing.T) {
	for _, scale := range []float32{.85, .9, .95, 1, 1.05, 1.1, 1.2, 1.35} {
		t.Run(fmt.Sprintf("%.2f", scale), func(t *testing.T) {
			a := testApp()
			a.display.FontScale = scale
			for i := range 1000 {
				a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%04d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Message %04d", i), Done: true})
			}
			tt := ui.NewTester(a.view, 1200, 500)
			first, last := a.list.Visible()
			want := fmt.Sprintf("Message %04d", (first+last)/2)
			row, ok := tt.Find(want)
			if !ok {
				t.Fatalf("missing %q", want)
			}
			y := a.list.scroll.Y
			tt.Press(row.X+1, row.Y+row.H/2)
			after, ok := tt.Find(want)
			if !ok || after.Y < row.Y-1 || after.Y > row.Y+1 {
				t.Fatalf("press moved %q: before=%+v after=%+v scroll=%v -> %v", want, row, after, y, a.list.scroll.Y)
			}
			tt.Move(row.X+row.W-1, row.Y+row.H/2)
			moved, _ := tt.Find(want)
			moveY := a.list.scroll.Y
			if moved.Y < row.Y-1 || moved.Y > row.Y+1 {
				t.Fatalf("drag moved %q: before=%+v after=%+v scroll=%v -> %v", want, row, moved, y, moveY)
			}
			tt.Release(row.X+row.W-1, row.Y+row.H/2)
			tt.Command("copy")
			if got := tt.Clipboard(); got != want {
				t.Fatalf("copied %q, want %q: before=%+v after-press=%+v after-move=%+v scroll=%v -> %v", got, want, row, after, moved, y, moveY)
			}
		})
	}
}

func TestTranscriptSelectionKeepsVirtualizationAfterScrolling(t *testing.T) {
	a := testApp()
	for i := range 1000 {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%04d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Message %04d", i), Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	// Find reports built elements, including overscan and clipped rows. Select
	// an interior visible row rather than assuming the last row is fully visible
	// with every platform's font metrics.
	firstVisible, lastVisible := a.list.Visible()
	want := fmt.Sprintf("Message %04d", (firstVisible+lastVisible)/2)
	row, ok := tt.Find(want)
	if !ok {
		t.Fatalf("visible message %q missing: %q", want, tt.Texts())
	}
	tt.Press(row.X+1, row.Y+row.H/2)
	tt.Move(row.X+row.W-1, row.Y+row.H/2)
	tt.Release(row.X+row.W-1, row.Y+row.H/2)
	tt.Command("copy")
	if got := tt.Clipboard(); got != want {
		t.Fatalf("initial selection copied %q, want %q (row=%+v, visible=%d..%d)", got, want, row, firstVisible, lastVisible)
	}
	tt.SetClipboard("unchanged")
	a.list.scroll.Y = 0
	tt.Frame()
	if !tt.HasText("Message 0000") {
		t.Fatal("scroll did not reach the first row")
	}
	if n := len(tt.Texts()); n > 150 {
		t.Fatalf("scrolling with a short selection built %d text nodes", n)
	}
	tt.Command("copy")
	if got := tt.Clipboard(); got != want {
		t.Fatalf("scrolling changed the original selection: got %q, want %q", got, want)
	}
}

func TestTranscriptSelectAllIncludesVirtualRows(t *testing.T) {
	a := testApp()
	var want []string
	for i := range 100 {
		text := fmt.Sprintf("Message %03d", i)
		want = append(want, text)
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%03d", i), Kind: appsession.ItemAssistant, Text: text, Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	last, ok := tt.Find("Message 099")
	if !ok {
		t.Fatal("last message missing")
	}
	tt.ClickAt(last.X+2, last.Y+last.H/2)
	tt.Command("selectAll")
	tt.Command("copy")
	if got, expected := tt.Clipboard(), strings.Join(want, "\n"); got != expected {
		t.Fatalf("select all omitted or changed virtual rows: %q", got)
	}
}

func TestTranscriptWindowKeepsFocusedRowWhenScrolledAway(t *testing.T) {
	a := testApp()
	for i := range 100 {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%03d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Message %03d", i), Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	last, ok := tt.Find("Message 099")
	if !ok {
		t.Fatal("last message missing")
	}
	tt.ClickAt(last.X+2, last.Y+last.H/2)
	a.list.scroll.Y = 0
	tt.Frame()
	if !tt.HasText("Message 000") || !tt.HasText("Message 099") {
		t.Fatalf("focused row was recycled after scrolling: %q", tt.Texts())
	}
	if n := len(tt.Texts()); n > 100 {
		t.Fatalf("keeping a focused row built %d text nodes", n)
	}
	if err := tt.Click("Message input"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("Message 099") {
		t.Fatal("old focused row stayed built after focus moved to the composer")
	}
}

func TestTranscriptSelectionDoesNotCrossSessions(t *testing.T) {
	a := testApp()
	a.status.SessionID = "session-a"
	a.items = []appsession.Item{{ID: "same-row", Kind: appsession.ItemAssistant, Text: "Same response text.", Done: true}}
	tt := ui.NewTester(a.view, 1200, 600)
	row, ok := tt.Find("Same response text.")
	if !ok {
		t.Fatal("first session response missing")
	}
	tt.Press(row.X+1, row.Y+row.H/2)
	tt.Move(row.X+row.W-1, row.Y+row.H/2)
	tt.Release(row.X+row.W-1, row.Y+row.H/2)
	tt.Command("copy")
	if !strings.Contains(tt.Clipboard(), "Same response text.") {
		t.Fatalf("initial selection failed: %q", tt.Clipboard())
	}
	tt.SetClipboard("unchanged")
	a.status.SessionID = "session-b"
	a.list = nil
	a.transcriptRowsDirty = true
	tt.Frame()
	tt.Command("copy")
	if got := tt.Clipboard(); got != "unchanged" {
		t.Fatalf("selection leaked into another session: %q", got)
	}
}

func TestTranscriptWindowKeepsAnchorWhenHistoryIsPrepended(t *testing.T) {
	a := testApp()
	for i := range 100 {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%03d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Entry %03d", i), Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	a.list.scroll.Y = 1500
	tt.Frame()
	first, _ := a.list.Visible()
	key := fmt.Sprintf("Entry %03d", first)
	before, ok := tt.Find(key)
	if !ok {
		t.Fatalf("anchor %q missing: %q", key, tt.Texts())
	}
	var older []appsession.Item
	for i := range 10 {
		older = append(older, appsession.Item{ID: fmt.Sprintf("older-%03d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Older %03d", i), Done: true})
	}
	a.items = append(older, a.items...)
	a.transcriptRowsDirty = true
	tt.Frame()
	after, ok := tt.Find(key)
	if !ok || after.Y < before.Y-2 || after.Y > before.Y+2 {
		t.Fatalf("history insertion moved anchor %q: before=%+v after=%+v present=%v", key, before, after, ok)
	}
}

func TestTranscriptWindowKeepsAnchorWhenOverscanHeightChanges(t *testing.T) {
	a := testApp()
	for i := range 100 {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%03d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Entry %03d", i), Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	a.list.scroll.Y = 1500
	tt.Frame()
	first, _ := a.list.Visible()
	if first < 1 {
		t.Fatalf("expected a middle row, got %d", first)
	}
	key := fmt.Sprintf("Entry %03d", first)
	before, ok := tt.Find(key)
	if !ok {
		t.Fatalf("anchor %q missing", key)
	}
	oldY, oldFirst := a.list.scroll.Y, a.list.first
	oldHeight := a.list.height[a.list.keys[first-1]]
	updated := a.items[first-1]
	updated.Text += "\n\n" + strings.Repeat("A long paragraph that wraps across lines. ", 20)
	a.index[updated.ID] = first - 1
	a.replaceItem(updated)
	tt.Frame()
	after, ok := tt.Find(key)
	if !ok || after.Y < before.Y-2 || after.Y > before.Y+2 {
		t.Fatalf("overscan resize moved anchor %q: before=%+v after=%+v present=%v scroll=%v -> %v first=%d -> %d oldheight=%v", key, before, after, ok, oldY, a.list.scroll.Y, oldFirst, a.list.first, oldHeight)
	}
}

func TestTranscriptSelectionSurvivesStreamingAppend(t *testing.T) {
	a := testApp()
	a.status.SessionID = "streaming"
	a.items = []appsession.Item{
		{ID: "first", Kind: appsession.ItemAssistant, Text: "First response.", Done: true},
		{ID: "last", Kind: appsession.ItemAssistant, Text: "Second response.", Done: true},
	}
	tt := ui.NewTester(a.view, 1200, 600)
	first, ok := tt.Find("First response.")
	if !ok {
		t.Fatal("first response missing")
	}
	last, ok := tt.Find("Second response.")
	if !ok {
		t.Fatal("second response missing")
	}
	tt.Press(first.X+1, first.Y+first.H/2)
	tt.Move(last.X+last.W-1, last.Y+last.H/2)
	tt.Release(last.X+last.W-1, last.Y+last.H/2)
	a.items = append(a.items, appsession.Item{ID: "new", Kind: appsession.ItemAssistant, Text: "New response.", Done: true})
	a.transcriptRowsDirty = true
	tt.Frame()
	tt.Command("copy")
	if got := tt.Clipboard(); got != "First response.\nSecond response." {
		t.Fatalf("streaming changed the selection: %q", got)
	}
}

func TestTranscriptWindowFollowsNewMessagesOnlyAtEnd(t *testing.T) {
	a := testApp()
	for i := range 40 {
		a.items = append(a.items, appsession.Item{ID: fmt.Sprintf("entry-%02d", i), Kind: appsession.ItemAssistant, Text: fmt.Sprintf("Entry %02d", i), Done: true})
	}
	tt := ui.NewTester(a.view, 1200, 500)
	a.items = append(a.items, appsession.Item{ID: "entry-40", Kind: appsession.ItemAssistant, Text: "Entry 40", Done: true})
	a.transcriptRowsDirty = true
	tt.Frame()
	if !tt.HasText("Entry 40") {
		t.Fatalf("new message was not followed: %q", tt.Texts())
	}
	a.list.scroll.Y = 0
	tt.Frame()
	if !tt.HasText("Entry 00") {
		t.Fatal("could not scroll to the start")
	}
	a.items = append(a.items, appsession.Item{ID: "entry-41", Kind: appsession.ItemAssistant, Text: "Entry 41", Done: true})
	a.transcriptRowsDirty = true
	tt.Frame()
	if !tt.HasText("Entry 00") || a.list.first != 0 {
		t.Fatalf("new message pulled a reader away from history: first=%d texts=%q", a.list.first, tt.Texts())
	}
}
