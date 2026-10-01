package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/smallnest/pigo/internal/cli/ui"
)

// ctrlKey builds a Ctrl+<letter> key press matching String()=="ctrl+<letter>".
func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// TestParseToolResult verifies depth inference from leading spaces and trailing
// blank-line trimming.
func TestParseToolResult(t *testing.T) {
	nodes := parseToolResult("root\n  child\n    grandchild\n\n")
	if len(nodes) != 3 {
		t.Fatalf("node count = %d, want 3 (trailing blank trimmed)", len(nodes))
	}
	want := []respNode{
		{text: "root", depth: 0},
		{text: "child", depth: 1},
		{text: "grandchild", depth: 2},
	}
	for i, w := range want {
		if nodes[i] != w {
			t.Errorf("node[%d] = %+v, want %+v", i, nodes[i], w)
		}
	}
}

func TestToolCardRender(t *testing.T) {
	theme := DefaultTheme()
	card := toolCard{
		id:       "1",
		name:     "read",
		input:    map[string]any{"path": "/tmp/x"},
		response: parseToolResult("line one\n  nested"),
		state:    cardSuccess,
	}
	collapsed := card.render(theme, 60)
	for _, want := range []string{"•", "read", "/tmp/x"} {
		if !strings.Contains(collapsed, want) {
			t.Errorf("collapsed render missing %q\n%s", want, collapsed)
		}
	}
	// The collapsed card previews the response but keeps the argument section
	// (and any old bordered layout) hidden.
	for _, nope := range []string{"Input arguments", "Response", "│ path:", "RoundedBorder"} {
		if strings.Contains(collapsed, nope) {
			t.Errorf("collapsed render should not contain %q\n%s", nope, collapsed)
		}
	}
	if !strings.Contains(collapsed, "line one") {
		t.Errorf("collapsed render should preview the first response line\n%s", collapsed)
	}
	if lines := strings.Count(collapsed, "\n"); lines != 2 {
		t.Errorf("collapsed render = %d newlines, want header + two preview lines", lines)
	}
	card.expanded = true
	expanded := card.render(theme, 60)
	for _, want := range []string{"path: /tmp/x", "line one", "nested"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded render missing %q\n%s", want, expanded)
		}
	}
}

// todoCallInput is the decoded shape argsToMap produces for a todo call:
// {"todos": []any of {"content","status"}}.
func todoCallInput() map[string]any {
	return map[string]any{"todos": []any{
		map[string]any{"content": "run tests", "status": "completed"},
		map[string]any{"content": "commit", "status": "in_progress"},
		map[string]any{"content": "push", "status": "pending"},
	}}
}

// TestToolCardTodoHeadline verifies the header summarizes the checklist as a
// count instead of dumping the decoded array as Go map syntax.
func TestToolCardTodoHeadline(t *testing.T) {
	card := toolCard{name: "todo", input: todoCallInput(), state: cardSuccess}
	if got := card.title(); got != "Ran todo 3 tasks" {
		t.Fatalf("title = %q, want Ran todo 3 tasks", got)
	}
}

// TestToolCardTodoDetail verifies the expanded card renders one checkbox row
// per item (mirroring the result block's marks) and never leaks `map[...]`.
func TestToolCardTodoDetail(t *testing.T) {
	card := toolCard{name: "todo", input: todoCallInput(), state: cardSuccess, expanded: true}
	got := card.render(DefaultTheme(), 80)
	for _, want := range []string{"│ [x] run tests", "│ [~] commit", "│ [ ] push"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "map[") {
		t.Fatalf("render leaked raw map syntax\n%s", got)
	}
}

// TestToolCardTodoMalformedFallsBack verifies a todos value that is not a
// checklist keeps the generic argument rendering and the bare header.
func TestToolCardTodoMalformedFallsBack(t *testing.T) {
	card := toolCard{name: "todo", input: map[string]any{"todos": "oops"}, state: cardSuccess}
	if got := card.title(); got != "Ran todo" {
		t.Fatalf("title = %q, want bare Ran todo", got)
	}
	if got := card.renderDetail(DefaultTheme(), 80); !strings.Contains(got, "todos: oops") {
		t.Fatalf("renderDetail = %q, want the generic argument line", got)
	}
}

// TestToolCardTodoChecklistAlwaysVisible is the regression for "Ran todo 3
// tasks shows no details": the checklist renders in the collapsed card (no
// expand needed) and while the call is still running, with no hidden-line hint
// because the rows are the payload, not a preview.
func TestToolCardTodoChecklistAlwaysVisible(t *testing.T) {
	for _, state := range []cardState{cardRunning, cardSuccess} {
		card := toolCard{name: "todo", input: todoCallInput(), state: state}
		got := card.render(DefaultTheme(), 80)
		for _, want := range []string{"│ [x] run tests", "│ [~] commit", "│ [ ] push"} {
			if !strings.Contains(got, want) {
				t.Errorf("state %v: render missing %q\n%s", state, want, got)
			}
		}
		if strings.Contains(got, "click to expand") {
			t.Errorf("state %v: checklist is not a preview, no hint expected\n%s", state, got)
		}
	}
}

// TestToolCardTodoEchoSuppressed verifies the tool's own result — the same
// checklist under a "Todos:" header — is not rendered a second time beneath
// the rows, while a non-echo response (a validation error) still shows.
func TestToolCardTodoEchoSuppressed(t *testing.T) {
	card := toolCard{
		name:     "todo",
		input:    todoCallInput(),
		response: parseToolResult("Todos:\n  [x] run tests\n  [~] commit\n  [ ] push\n(1/3 completed)"),
		state:    cardSuccess,
	}
	got := card.render(DefaultTheme(), 80)
	if strings.Contains(got, "(1/3 completed)") {
		t.Errorf("echo response should be suppressed\n%s", got)
	}
	if n := strings.Count(got, "run tests"); n != 1 {
		t.Errorf("checklist rendered %d times, want 1\n%s", n, got)
	}

	card.response = parseToolResult("todo: item 2 has invalid status \"nope\"")
	card.state = cardWarn
	if got := card.render(DefaultTheme(), 80); !strings.Contains(got, "invalid status") {
		t.Errorf("a non-echo (error) response must still render\n%s", got)
	}
}

// TestToolCardExpandTruncation verifies the collapsed card shows the header,
// the first few response lines, and a "… +N lines" hint for the rest, while the
// expanded card reveals every response line with no hint.
func TestToolCardExpandTruncation(t *testing.T) {
	theme := DefaultTheme()
	var b strings.Builder
	const n = 8
	for i := 0; i < n; i++ {
		b.WriteString("resp-line-")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	card := toolCard{name: "grep", response: parseToolResult(b.String()), state: cardSuccess}

	collapsed := card.render(theme, 60)
	hidden := n - collapsedPreviewLines
	if !strings.Contains(collapsed, fmt.Sprintf("\u2026 +%d lines (ctrl+t or click to expand)", hidden)) {
		t.Errorf("collapsed card should hint %d hidden lines\n%s", hidden, collapsed)
	}
	for i := 0; i < collapsedPreviewLines; i++ {
		want := "resp-line-" + string(byte('a'+i))
		if !strings.Contains(collapsed, want) {
			t.Errorf("collapsed card should preview %q\n%s", want, collapsed)
		}
	}
	if strings.Contains(collapsed, "resp-line-d") {
		t.Errorf("collapsed card should hide lines past the preview\n%s", collapsed)
	}

	card.expanded = true
	expanded := card.render(theme, 60)
	if strings.Contains(expanded, "click to expand") {
		t.Errorf("expanded card should not show the hint\n%s", expanded)
	}
	for i := 0; i < n; i++ {
		want := "resp-line-" + string(byte('a'+i))
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded card should show %q\n%s", want, expanded)
		}
	}
}

// TestModelToolCardFlow drives the model through a tool start/end and asserts the
// card is created, transitions running→success, and that a failed tool yields
// warn.
func TestModelToolCardFlow(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(toolStartMsg{id: "t1", name: "read_file", input: map[string]any{"path": "a.go"}})
	mm := next.(Model)
	card, ok := mm.toolCards["t1"]
	if !ok {
		t.Fatalf("toolStartMsg should create a card")
	}
	if card.state != cardRunning {
		t.Errorf("new card state = %v, want cardRunning", card.state)
	}

	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: "done\n  detail"})
	mm = next.(Model)
	if mm.toolCards["t1"].state != cardSuccess {
		t.Errorf("state after ok end = %v, want cardSuccess", mm.toolCards["t1"].state)
	}
	if len(mm.toolCards["t1"].response) != 2 {
		t.Errorf("response nodes = %d, want 2", len(mm.toolCards["t1"].response))
	}

	// A failed tool flips the same card to warn.
	next, _ = m.Update(toolStartMsg{id: "t2", name: "bash"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "t2", ok: false, result: "boom"})
	mm = next.(Model)
	if mm.toolCards["t2"].state != cardWarn {
		t.Errorf("state after failed end = %v, want cardWarn", mm.toolCards["t2"].state)
	}
}

// TestToolCardDiffSection verifies a card carrying a diff renders a dedicated
// Diff section whose lines carry the per-line theme colors: red removals,
// green additions, cyan @@ markers, dim headers and context (#560).
func TestToolCardDiffSection(t *testing.T) {
	theme := DefaultTheme()
	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n alpha\n-beta\n+BETA\n gamma\n"
	card := toolCard{
		name:     "edit",
		input:    map[string]any{"path": "f.txt"},
		response: parseToolResult("Edited f.txt (1 replacement(s))"),
		diff:     diff,
		state:    cardSuccess,
		expanded: true,
	}
	out := card.render(theme, 60)

	// The headline styles the name and command separately, so assert on the
	// color-stripped text for the plain-content checks.
	plain := stripTCardANSI(out)
	for _, want := range []string{
		"edit f.txt",
		"Edited f.txt (1 replacement(s))",
		"--- a/f.txt",
		"@@ -1,3 +1,3 @@",
		"-beta",
		"+BETA",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("render missing %q\n%s", want, plain)
		}
	}
	// The diff lines are styled, not plain body text.
	for _, styled := range []string{
		theme.DiffDel.Render("    -beta"),
		theme.DiffAdd.Render("    +BETA"),
		theme.DiffHunk.Render("    @@ -1,3 +1,3 @@"),
		theme.DiffCtx.Render("    --- a/f.txt"),
		theme.DiffCtx.Render("     alpha"),
	} {
		if !strings.Contains(out, styled) {
			t.Errorf("render missing styled diff line %q\n%s", styled, out)
		}
	}
}

// TestToolCardDiffCollapseExpand verifies a diff hides behind the hint when
// collapsed and renders fully once expanded.
func TestToolCardDiffCollapseExpand(t *testing.T) {
	theme := DefaultTheme()
	var b strings.Builder
	b.WriteString("--- a/f.txt\n+++ b/f.txt\n")
	const adds = 4
	for i := 0; i < adds; i++ {
		b.WriteString("+line\n")
	}
	card := toolCard{
		name:     "edit",
		response: parseToolResult("Edited f.txt (1 replacement(s))"),
		diff:     b.String(),
		state:    cardSuccess,
	}

	// The summary line is previewed; the 2 headers + 4 additions stay hidden
	// behind the hint.
	collapsed := card.render(theme, 60)
	if !strings.Contains(collapsed, "\u2026 +6 lines (ctrl+t or click to expand)") {
		t.Errorf("collapsed card should hint 6 hidden lines\n%s", collapsed)
	}
	if strings.Contains(collapsed, "+line") {
		t.Errorf("collapsed card should not show diff lines\n%s", collapsed)
	}

	card.expanded = true
	expanded := card.render(theme, 60)
	if strings.Contains(expanded, "click to expand") {
		t.Errorf("expanded card should not show the hint\n%s", expanded)
	}
	if got := strings.Count(expanded, "+line"); got != adds {
		t.Errorf("expanded card shows %d additions, want %d\n%s", got, adds, expanded)
	}
}

// TestStripDiffTail verifies the response text keeps only its summary once the
// diff moves to its own section: the cut happens at the diff header, and a
// text without a diff passes through untouched.
func TestStripDiffTail(t *testing.T) {
	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n"
	text := "Edited f.txt (1 replacement(s))\n" + diff
	if got := stripDiffTail(text); got != "Edited f.txt (1 replacement(s))\n" {
		t.Errorf("stripDiffTail = %q, want the summary line only", got)
	}
	// A result clipped mid-diff still splits at the header.
	if got := stripDiffTail(text[:len(text)-5]); got != "Edited f.txt (1 replacement(s))\n" {
		t.Errorf("stripDiffTail on clipped text = %q, want the summary line only", got)
	}
	// Text without a diff header is left alone.
	plain := "done\nsome output\n"
	if got := stripDiffTail(plain); got != plain {
		t.Errorf("stripDiffTail on plain text = %q, want unchanged", got)
	}
}

// TestModelToolEndDiff drives a tool start/end pair whose end event carries
// edit-style Details, and verifies the model stores the diff on the card and
// keeps only the summary in the response (no duplicated diff).
func TestModelToolEndDiff(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(toolStartMsg{id: "e1", name: "edit", input: map[string]any{"path": "f.txt"}})
	mm := next.(Model)

	diff := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+BETA\n"
	details := map[string]any{"path": "f.txt", "replacements": 1, "diff": diff}
	next, _ = mm.Update(toolEndMsg{
		id:      "e1",
		ok:      true,
		result:  "Edited f.txt (1 replacement(s))\n" + diff,
		details: details,
	})
	mm = next.(Model)

	card, ok := mm.toolCards["e1"]
	if !ok {
		t.Fatalf("toolEndMsg should keep the card")
	}
	if card.diff != diff {
		t.Errorf("card.diff = %q, want the diff from Details", card.diff)
	}
	if len(card.response) != 1 || card.response[0].text != "Edited f.txt (1 replacement(s))" {
		t.Errorf("card.response = %+v, want only the summary line", card.response)
	}

	// Without Details the card renders as before: full text, no diff section.
	next, _ = m.Update(toolStartMsg{id: "e2", name: "edit"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "e2", ok: true, result: "Edited g.txt (1 replacement(s))\n" + diff})
	mm = next.(Model)
	card2 := mm.toolCards["e2"]
	if card2.diff != "" {
		t.Errorf("card without Details should carry no diff, got %q", card2.diff)
	}
	if len(card2.response) == 1 && card2.response[0].text == "Edited g.txt (1 replacement(s))" {
		// The response keeps the embedded diff text when there is no Details to
		// split on, so more than the summary should be present.
		t.Errorf("response should keep the embedded diff when Details is absent: %+v", card2.response)
	}
}

// TestModelCtrlTTogglesExpanded verifies Ctrl+T flips the most-recent card's
// expanded flag so the full detail becomes visible (codex parity).
func TestModelCtrlTTogglesExpanded(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	mm := next.(Model)

	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteString("row")
		b.WriteByte(byte('0' + i))
		b.WriteByte('\n')
	}
	next, _ = mm.Update(toolStartMsg{id: "t1", name: "grep"})
	mm = next.(Model)
	next, _ = mm.Update(toolEndMsg{id: "t1", ok: true, result: b.String()})
	mm = next.(Model)

	if mm.lastToolCard.expanded {
		t.Fatalf("card should start collapsed")
	}
	next, _ = mm.Update(ctrlKey('t'))
	mm = next.(Model)
	if !mm.lastToolCard.expanded {
		t.Errorf("Ctrl+T should expand the most-recent card")
	}
	// Toggling again collapses it.
	next, _ = mm.Update(ctrlKey('t'))
	mm = next.(Model)
	if mm.lastToolCard.expanded {
		t.Errorf("second Ctrl+T should collapse the card")
	}
}

// TestToolCardRunningRanTitle verifies the codex-style verb: Running while the
// tool executes, Ran once finished, plus the empty-result note.
func TestToolCardRunningRanTitle(t *testing.T) {
	theme := DefaultTheme()
	running := toolCard{name: "bash", input: map[string]any{"command": "ls"}, state: cardRunning}
	if got := running.render(theme, 60); !strings.Contains(stripTCardANSI(got), "Running bash") {
		t.Errorf("running card should show Running verb\n%s", got)
	}
	done := toolCard{name: "bash", input: map[string]any{"command": "ls"}, state: cardSuccess, expanded: true}
	out := done.render(theme, 60)
	if !strings.Contains(stripTCardANSI(out), "Ran bash") {
		t.Errorf("finished card should show Ran verb\n%s", out)
	}
	if !strings.Contains(out, "(no output)") {
		t.Errorf("empty result should render (no output)\n%s", out)
	}
}

// stripTCardANSI drops SGR escapes for verb assertions independent of color.
func stripTCardANSI(s string) string { return ansi.Strip(s) }

// TestToolCardWebSearchHeadline verifies a hosted search card shows what was
// searched: the query for search actions, the page URL for open_page.
func TestToolCardWebSearchHeadline(t *testing.T) {
	theme := DefaultTheme()
	search := toolCard{name: "web_search", input: map[string]any{"query": "muse docs"}, state: cardSuccess}
	if got := stripTCardANSI(search.render(theme, 60)); !strings.Contains(got, "muse docs") {
		t.Errorf("search card should show the query, got:\n%s", got)
	}
	open := toolCard{
		name:  "web_search",
		input: map[string]any{"action": "open_page", "url": "https://dev.meta.ai/docs/tool-calling"},
		state: cardSuccess,
	}
	if got := stripTCardANSI(open.render(theme, 60)); !strings.Contains(got, "https://dev.meta.ai/docs/tool-calling") {
		t.Errorf("open_page card should show the URL, got:\n%s", got)
	}
}

// TestToolCardHeadlineWraps verifies a long command wraps onto `  │ `
// continuation lines (codex parity) instead of being cut with an ellipsis, that
// every rendered line fits the width, and that no piece of the command is
// dropped by the wrap.
func TestToolCardHeadlineWraps(t *testing.T) {
	theme := DefaultTheme()
	const width = 48
	cmd := `rg -n "persist_cache|try_load_cache" codex-rs/models-manager/src/ | head; ` +
		`rg -n "cache.*path" codex-rs/models-manager/src/cache.rs | head -n 15`
	card := toolCard{name: "bash", input: map[string]any{"command": cmd}, state: cardSuccess}

	out := card.render(theme, width)
	plain := stripTCardANSI(out)
	if strings.Contains(plain, "\u2026") {
		t.Fatalf("headline must wrap, not truncate:\n%s", plain)
	}
	if !strings.Contains(plain, "\n  \u2502 ") {
		t.Fatalf("wrapped headline should use the \u2502 continuation gutter:\n%s", plain)
	}
	// Drop the continuation gutters before flattening: a word-wrap break can
	// land between two words of a command, putting a │ column between them.
	flat := strings.Join(strings.Fields(strings.ReplaceAll(plain, "\u2502", " ")), " ")
	if !strings.Contains(flat, "head -n 15") {
		t.Fatalf("wrapped headline dropped the command tail:\n%s", plain)
	}
	for _, ln := range strings.Split(plain, "\n") {
		if got := ui.Width(ln); got > width {
			t.Errorf("line width %d exceeds %d: %q", got, width, ln)
		}
	}
}

// TestModelClickTogglesAnyCard verifies the mouse affordance: a bare click
// toggles the card under the cursor — including an older card that Ctrl+T no
// longer reaches — while a drag across a card stays a text selection.
func TestModelClickTogglesAnyCard(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 60, Height: 12})
	for _, id := range []string{"t1", "t2"} {
		m = apply(t, m, toolStartMsg{id: id, name: "bash", input: map[string]any{"command": "echo " + id}})
		m = apply(t, m, toolEndMsg{id: id, ok: true, result: "out-" + id})
	}
	first, second := m.toolCards["t1"], m.toolCards["t2"]
	if first.expanded || second.expanded {
		t.Fatal("cards should start collapsed")
	}

	// A bare click on the older card's first row expands it and leaves the
	// newer card alone.
	m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if !first.expanded {
		t.Error("clicking the older card should expand it")
	}
	if second.expanded {
		t.Error("a click must not touch a different card")
	}

	// Clicking the same card again collapses it.
	m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	if first.expanded {
		t.Error("a second click should collapse the card")
	}

	// A drag that starts on a card selects text instead of toggling it.
	m = apply(t, m, tea.MouseClickMsg{X: 3, Y: 0, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseMotionMsg{X: 6, Y: 1, Button: tea.MouseLeft})
	m = apply(t, m, tea.MouseReleaseMsg{X: 6, Y: 1, Button: tea.MouseLeft})
	if first.expanded {
		t.Error("a drag across the card must not toggle it")
	}
	if m.sel.empty() {
		t.Error("a drag across the card should still select text")
	}
}

// TestToolCardHeadlineColors verifies the codex-parity headline palette: the
// verb is bold in the default color, the tool name is cyan, the command is
// syntax-highlighted (chroma/Catppuccin), and nothing renders the old all-blue
// ToolHeader blob (blue 39 on the whole line read like a hyperlink).
func TestToolCardHeadlineColors(t *testing.T) {
	theme := DefaultTheme()
	card := toolCard{
		name:  "bash",
		input: map[string]any{"command": "go test ./..."},
		state: cardSuccess,
	}
	out := card.render(theme, 60)
	if !strings.Contains(out, theme.ToolVerb.Render("Ran")) {
		t.Errorf("headline missing bold default verb\n%q", out)
	}
	// The space after the name belongs to the following command token (word
	// wrapping re-inserts separators in the next word's style), so only the
	// name itself is asserted.
	if !strings.Contains(out, theme.ToolName.Render("bash")) {
		t.Errorf("headline missing cyan tool name\n%q", out)
	}
	if plain := stripTCardANSI(out); !strings.Contains(plain, "go test ./...") {
		t.Errorf("headline lost command text\n%q", plain)
	}
	if n := distinctFgColors(out); n < 2 {
		t.Errorf("command rendered with %d foreground colors, want syntax highlighting\n%q", n, out)
	}
	if strings.Contains(out, "38;5;39m") {
		t.Errorf("headline still paints a segment in 256-color 39 (blue)\n%q", out)
	}
}
