package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/runtime"
)

// typeInto feeds each rune of s to the model as a key press, returning the
// evolved model. It mirrors how a terminal delivers typed characters (Code +
// Text), including the leading "/" of a slash-command.
func typeInto(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// menuNames returns the "/name" of every candidate currently in the popup.
func menuNames(m Model) []string {
	out := make([]string, len(m.menu.filtered))
	for i, c := range m.menu.filtered {
		out[i] = "/" + c.Name
	}
	return out
}

func containsAll(hay []string, needles ...string) bool {
	set := make(map[string]bool, len(hay))
	for _, h := range hay {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

// TestSlashMenuOpensOnSlash verifies that typing a bare "/" opens the popup with
// the built-in commands present (/model, /help among them).
func TestSlashMenuOpensOnSlash(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/").(Model)
	if !m.menu.active {
		t.Fatalf("menu should be active after typing '/'")
	}
	names := menuNames(m)
	if !containsAll(names, "/model", "/help") {
		t.Errorf("candidate set %v missing /model or /help", names)
	}
}

// TestSlashMenuFiltersByPrefix verifies the popup narrows to the typed prefix:
// "/mo" keeps /model but drops /help (and the deleted /models).
func TestSlashMenuFiltersByPrefix(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/mo").(Model)
	if !m.menu.active {
		t.Fatalf("menu should be active for '/mo'")
	}
	names := menuNames(m)
	if !containsAll(names, "/model") {
		t.Errorf("candidate set %v missing /model", names)
	}
	for _, n := range names {
		if n == "/models" {
			t.Errorf("candidate set %v should not contain deleted /models", names)
		}
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "/mo") {
			t.Errorf("candidate %q does not match prefix /mo (set %v)", n, names)
		}
	}
}

// TestSlashMenuClosesOnSpace verifies name-completion stops once the buffer moves
// on to arguments (a space after the command name closes the popup).
func TestSlashMenuClosesOnSpace(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/model ").(Model)
	if m.menu.active {
		t.Errorf("menu should close once the command name is complete (buffer %q)", m.input.Value())
	}
}

// TestSlashMenuNavigation verifies arrow keys move the highlighted candidate and
// wrap at the ends.
func TestSlashMenuNavigation(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/").(Model)
	n := len(m.menu.filtered)
	if n < 2 {
		t.Fatalf("need at least two candidates to test navigation, got %d", n)
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := next.(Model).menu.selected; got != 1 {
		t.Errorf("after Down, selected = %d, want 1", got)
	}
	// Up from index 0 wraps to the last candidate.
	back, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := back.(Model).menu.selected; got != n-1 {
		t.Errorf("after Up from 0, selected = %d, want %d (wrap)", got, n-1)
	}
}

// TestSlashTabCompletes verifies Tab fills the buffer with the highlighted
// command and closes the popup (ready for arguments).
func TestSlashTabCompletes(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/hel").(Model)
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	gm := got.(Model)
	if gm.input.Value() != "/help " {
		t.Errorf("after Tab, buffer = %q, want %q", gm.input.Value(), "/help ")
	}
	if gm.menu.active {
		t.Errorf("menu should close after Tab completion")
	}
}

// TestSlashHelpExecutesIntoTranscript verifies executing a built-in action
// command (/help) renders its output into the transcript as a system block —
// listing the available commands — without requiring a live provider.
func TestSlashHelpExecutesIntoTranscript(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/help").(Model)
	got, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	gm := got.(Model)
	// No run is started for an action command.
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("/help should not quit")
			}
		}
	}
	if gm.running {
		t.Errorf("/help is an action command; model should stay idle")
	}
	joined := strings.Join(blockTexts(gm.transcript), "\n")
	if !strings.Contains(joined, "/help") || !strings.Contains(joined, "/model") {
		t.Errorf("/help output should list commands (/help, /model); transcript:\n%s", joined)
	}
	if gm.input.Value() != "" {
		t.Errorf("after executing /help, input = %q, want cleared", gm.input.Value())
	}
}

// TestSlashUnknownCommandReported verifies an unknown "/name" surfaces the
// resolver error into the transcript rather than being sent to the agent.
func TestSlashUnknownCommandReported(t *testing.T) {
	m := typeInto(t, NewModel(Options{}), "/definitelynotacommand").(Model)
	// The popup filters to nothing, so it is inactive; Enter routes through submit.
	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	gm := got.(Model)
	if gm.running {
		t.Errorf("an unknown command must not start a run")
	}
	joined := strings.Join(blockTexts(gm.transcript), "\n")
	if !strings.Contains(joined, "unknown command") {
		t.Errorf("expected an unknown-command notice in transcript, got:\n%s", joined)
	}
}

// TestSlashMenuRendersAboveInput verifies the popup appears in the View while
// active, so the candidate list is visible above the input line.
func TestSlashMenuRendersAboveInput(t *testing.T) {
	m := NewModel(Options{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeInto(t, next, "/mo").(Model)
	view := m.View()
	if !strings.Contains(view.Content, "/model") {
		t.Errorf("active popup should render /model in the view content")
	}
}

// TestSlashMenuWorksMidRun pins the popup as a mid-run affordance: the composer
// is a live editor while a run streams, so typing a "/prefix" must suggest
// commands there like it does when idle (the popup, its arrow navigation and Tab
// completion are all keyed on the buffer, not on the run state). Pressing Enter
// then dispatches through the same runSlash a typed line uses, so the command
// classes that must not run mid-run are still refused there.
func TestSlashMenuWorksMidRun(t *testing.T) {
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m.running = true

	// A bare "/" matches the whole registry, so the arrow assertion has more
	// than one candidate to move between.
	m = typeInto(t, m, "/").(Model)
	if !m.menu.active {
		t.Fatal("a /prefix typed mid-run should open the popup")
	}
	if got := strings.Join(menuNames(m), ","); !strings.Contains(got, "/model") {
		t.Errorf("mid-run candidates = %v, want /model among them", got)
	}
	// Arrows drive the popup rather than the transcript while it is open.
	before := m.menu.selected
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.menu.selected == before {
		t.Error("↓ mid-run should move the popup selection while it is open")
	}
	// Tab completes the highlighted command and closes the popup.
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.menu.active {
		t.Error("Tab should close the popup after completing")
	}
	if got := m.input.Value(); !strings.HasPrefix(got, "/") || !strings.HasSuffix(got, " ") {
		t.Errorf("mid-run Tab completion = %q, want a completed command name", got)
	}
}

// TestSlashPromptCommandStartsRun verifies a prompt (Expand) command's expanded
// text is fed to the run seam, not shown as a bare status. A stub startRunFn
// stands in for the live provider.
func TestSlashPromptCommandStartsRun(t *testing.T) {
	m := NewModel(Options{})
	// Inject a user prompt command directly into the registry.
	m.slash.AddUser(runtime.SlashCommand{
		Name:   "greet",
		Expand: func(args string) string { return "hello " + args },
	})
	var ran string
	m.startRunFn = func(prompt string) (chan tea.Msg, tea.Cmd) {
		ran = prompt
		ch := make(chan tea.Msg, 1)
		return ch, func() tea.Msg { return nil }
	}
	got, _ := m.runSlash("/greet world")
	gm := got.(Model)
	if ran != "hello world" {
		t.Errorf("prompt command should start a run with expanded text; got %q", ran)
	}
	if !gm.running {
		t.Errorf("model should be running after a prompt command")
	}
}

// TestSlashExitQuits verifies that /exit typed in the TUI input box terminates
// the program (tea.Quit + quitting flag), mirroring the REPL loop which
// intercepts it before slash resolution.
func TestSlashExitQuits(t *testing.T) {
	got, teaCmd := NewModel(Options{}).runSlash("/exit")
	gm := got.(Model)
	if !gm.quitting {
		t.Error("/exit: model should be marked quitting")
	}
	if teaCmd == nil {
		t.Fatal("/exit: expected a tea.Quit command, got nil")
	}
	if _, isQuit := teaCmd().(tea.QuitMsg); !isQuit {
		t.Error("/exit: cmd should be tea.Quit")
	}
}

// TestSlashMenuPickerMode verifies the generic picker overlay used by /model:
// openPicker shows plain items with the mark annotated, arrows wrap, refresh
// never clobbers picks, and close resets picker state.
func TestSlashMenuPickerMode(t *testing.T) {
	mn := slashMenu{theme: DefaultTheme()}
	mn.openPicker([]string{"m-a", "m-b", "m-c"}, "m-b")
	if !mn.picking() || !mn.active {
		t.Fatal("picker should be active after openPicker")
	}
	if got, _ := mn.pickCurrent(); got != "m-a" {
		t.Errorf("initial pick = %q, want m-a", got)
	}
	mn.moveUp()
	if got, _ := mn.pickCurrent(); got != "m-c" {
		t.Errorf("moveUp wraps to %q, want m-c", got)
	}
	mn.moveDown()
	if got, _ := mn.pickCurrent(); got != "m-a" {
		t.Errorf("moveDown wraps to %q, want m-a", got)
	}
	mn.refresh("/model x", nil)
	if !mn.picking() {
		t.Error("refresh must not clobber picker candidates")
	}
	// Rows are styled per segment, so compare on the visible text.
	view := ansi.Strip(mn.view(40))
	for _, want := range []string{"m-a", "m-b  (current)", "m-c"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "/m-a") {
		t.Errorf("picker rows must not carry a slash prefix:\n%s", view)
	}
	mn.close()
	if mn.picking() || mn.active {
		t.Error("close must reset picker state")
	}
}

// TestSlashMenuPickerDetailed verifies titled picker rows: Title renders
// first, Detail second, and the mark annotates the matching Value.
func TestSlashMenuPickerDetailed(t *testing.T) {
	mn := slashMenu{theme: DefaultTheme()}
	mn.openPickerDetailed([]pickItem{
		{Title: "fix bug", Detail: "m1 · gpt", Value: "id-1"},
		{Title: "write docs", Detail: "m2 · gpt", Value: "id-2"},
	}, "id-2", "resume")
	if !mn.picking() {
		t.Fatal("detailed picker should be active")
	}
	if mn.pickKind != "resume" {
		t.Errorf("pickKind = %q, want resume", mn.pickKind)
	}
	if got, _ := mn.pickCurrent(); got != "id-1" {
		t.Errorf("initial pick = %q, want id-1", got)
	}
	view := ansi.Strip(mn.view(60))
	for _, want := range []string{"fix bug", "m1 · gpt", "write docs  m2 · gpt  (current)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	mn.close()
	if mn.picking() || mn.pickKind != "" {
		t.Error("close must reset detailed picker state")
	}
}

// TestSlashMenuPickerRowStructure locks the provider-picker redesign: rows are
// scannable (title + colored state badge + current tag), and the highlighted
// row's labeled detail block renders indented under it with a "└" marker, so
// explanations can never be mistaken for more selectable rows.
func TestSlashMenuPickerRowStructure(t *testing.T) {
	mn := slashMenu{theme: DefaultTheme()}
	mn.openPickerDetailed([]pickItem{
		{
			Title: "openai", Status: "key set", StatusTone: pickOK, Value: "openai", Current: true,
			Info: []pickInfoLine{
				{Label: "key", Value: "OPENAI_API_KEY", Note: "set", Tone: pickOK},
				{Label: "url (optional)", Value: "OPENAI_BASE_URL", Note: "https://api.openai.com/v1 · default", Tone: pickMuted},
				{Label: "proxy", Note: "direct", Tone: pickMuted},
			},
		},
		{Title: "deepseek", Status: "key needed", StatusTone: pickWarn, Value: "deepseek"},
	}, "openai", "provider")

	raw := mn.view(80)
	plain := ansi.Strip(raw)

	// The detail block must sit between the selected row and the next row.
	selRow := strings.Index(plain, "› openai")
	infoBlock := strings.Index(plain, "└ key")
	nextRow := strings.Index(plain, "deepseek")
	if selRow < 0 || infoBlock < 0 || nextRow < 0 {
		t.Fatalf("missing row/detail pieces:\n%s", plain)
	}
	if !(selRow < infoBlock && infoBlock < nextRow) {
		t.Errorf("detail block must render with its selected row, got order sel=%d info=%d next=%d:\n%s",
			selRow, infoBlock, nextRow, plain)
	}
	// Labels name what can be set; notes state where it stands.
	for _, want := range []string{"key set", "(current)", "key ", "OPENAI_API_KEY", "set",
		"url (optional)", "OPENAI_BASE_URL", "https://api.openai.com/v1 · default", "proxy", "direct"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q:\n%s", want, plain)
		}
	}
	// Badges carry their tone: green for configured, yellow for missing.
	if !strings.Contains(raw, DefaultTheme().Success.Render("key set")) {
		t.Errorf("configured badge should render in the success color:\n%q", raw)
	}
	if !strings.Contains(raw, DefaultTheme().Warn.Render("key needed")) {
		t.Errorf("missing-credential badge should render in the warn color:\n%q", raw)
	}
	// Non-selected rows render in the terminal default, not the gray System
	// style that made the old list read as one dim blob.
	if strings.Contains(raw, DefaultTheme().System.Render("deepseek")) {
		t.Errorf("non-selected rows must not render in the dim system style:\n%q", raw)
	}
}

// longDescText is the kind of description the report was about: a built-in that
// names its own subcommands and therefore runs well past one row.
const longDescText = "show or switch the approval mode, or manage the remembered path grants: " +
	"/permissions [read-only|ask|auto|full-access|writable|readable …]"

// skillDescText is the other population: a paragraph, of the length an extension's
// frontmatter description legitimately has.
const skillDescText = "Use when the user asks for a diagram, a map of a system, or any visual " +
	"explanation: renders it as standalone HTML with inline SVG, picks a dark palette, and " +
	"writes the file next to the docs it documents."

// descCommand builds a built-in candidate (the 4-line tier).
func descCommand(name, desc string) runtime.SlashCommand {
	return runtime.SlashCommand{Name: name, Description: desc, Action: func(string) string { return "" }}
}

// TestSlashMenuWrapsHighlightedDescription is the fix for "the comment is too
// long and just gets an ellipsis": the highlighted candidate shows its whole
// description, wrapped onto continuation lines that hang under its description
// column, so the row being considered is readable where it sits.
func TestSlashMenuWrapsHighlightedDescription(t *testing.T) {
	m := NewModel(Options{})
	m.slash.AddBuiltin(descCommand("permissions", longDescText))
	m = typeInto(t, m, "/perm").(Model)

	plain := stripANSI(m.menu.view(80))
	// Reassembling the lines must give back the description word for word — the
	// proof that nothing was cut. (An ellipsis check would prove nothing here: this
	// description ends with one of its own, in the usage hint.)
	flat := strings.Join(strings.Fields(strings.ReplaceAll(plain, "\n", " ")), " ")
	if !strings.HasSuffix(flat, strings.Join(strings.Fields(longDescText), " ")) {
		t.Errorf("wrapped description is not the original text:\n%s", plain)
	}
	// Continuation lines hang under the description column (the two-column caret
	// gutter plus the "/name  " column), keeping the "/name" column clear so the
	// block still reads as one row.
	hanging := "  " + strings.Repeat(" ", ui.Width("/permissions  "))
	for i, line := range strings.Split(plain, "\n") {
		// Line 0 is the category header; the row's own first line carries the caret.
		if i == 0 || strings.HasPrefix(line, "› ") {
			continue
		}
		if !strings.HasPrefix(line, hanging) {
			t.Errorf("continuation line %d does not hang under the description column: %q", i, line)
		}
	}
}

// TestSlashMenuTruncatesUnselectedLongDescription: only the highlighted row is
// wrapped. The others keep the single ellipsized line, which is what stops a list
// of long descriptions from turning the popup into a wall of text.
func TestSlashMenuTruncatesUnselectedLongDescription(t *testing.T) {
	m := NewModel(Options{})
	m.slash.AddBuiltin(descCommand("permissions", longDescText))
	m.slash.AddBuiltin(descCommand("permalink", longDescText))
	m = typeInto(t, m, "/perm").(Model)
	if len(m.menu.filtered) < 2 {
		t.Fatalf("need two candidates, got %d", len(m.menu.filtered))
	}

	plain := stripANSI(m.menu.view(80))
	selected := m.menu.filtered[m.menu.selected].Name
	for _, c := range m.menu.filtered {
		if c.Name == selected {
			continue
		}
		var line string
		for _, ln := range strings.Split(plain, "\n") {
			if strings.Contains(ln, "/"+c.Name) {
				line = ln
			}
		}
		if line == "" {
			t.Fatalf("unselected row /%s missing from:\n%s", c.Name, plain)
		}
		if !strings.Contains(line, ellipsis) {
			t.Errorf("unselected long row should stay one ellipsized line: %q", line)
		}
	}
}

// TestSlashMenuRowHeightSurvivesWrapping pins the layout contract the wrapped row
// depends on: rows(width) counts exactly the lines view(width) draws, or the model
// would reserve the wrong height and the popup would overlap whatever sits above
// it (or leave a gap). Each drawn line must also fit the row width.
func TestSlashMenuRowHeightSurvivesWrapping(t *testing.T) {
	for _, tc := range []struct {
		name, desc string
		add        func(*runtime.SlashRegistry, runtime.SlashCommand)
	}{
		{"built-in", longDescText, (*runtime.SlashRegistry).AddBuiltin},
		{"extension", skillDescText, (*runtime.SlashRegistry).AddSkill},
	} {
		for _, w := range []int{100, 80, 60, 40, 24} {
			m := NewModel(Options{})
			tc.add(m.slash, descCommand("permissions", tc.desc))
			m = typeInto(t, m, "/perm").(Model)

			view := m.menu.view(w)
			if got := strings.Count(view, "\n") + 1; got != m.menu.rows(w) {
				t.Errorf("%s width %d: drawn lines = %d, rows() = %d", tc.name, w, got, m.menu.rows(w))
			}
			// The drawn line includes the two-column caret / indent gutter, so the
			// full terminal width is the budget — the popup may never exceed it.
			for _, line := range strings.Split(stripANSI(view), "\n") {
				if ui.Width(line) > w {
					t.Errorf("%s width %d: line exceeds the terminal width: %q", tc.name, w, line)
				}
			}
		}
	}
}

// TestSlashMenuNarrowTerminalKeepsOneLine: below the width where a hanging column
// holds readable text, the row stays the single ellipsized line rather than
// becoming a narrow well of chopped words.
func TestSlashMenuNarrowTerminalKeepsOneLine(t *testing.T) {
	m := NewModel(Options{})
	m.slash.AddBuiltin(descCommand("permissions", longDescText))
	m = typeInto(t, m, "/perm").(Model)

	view := stripANSI(m.menu.view(20))
	if !strings.Contains(view, ellipsis) {
		t.Errorf("a too-narrow terminal should keep the one-line ellipsized row:\n%s", view)
	}
	if got := strings.Count(view, "\n") + 1; got != m.menu.rows(20) {
		t.Errorf("drawn lines = %d, rows() = %d", got, m.menu.rows(20))
	}
}

// TestSlashMenuDescCapTiersBySource pins the split the two populations asked for:
// a built-in's description may fill four lines, an extension's only two — a skill
// or template description is frontmatter written for the system prompt, so the
// menu previews it rather than printing a paragraph. Both are cut with an ellipsis
// past their cap, so neither can push the composer off the screen.
func TestSlashMenuDescCapTiersBySource(t *testing.T) {
	// The same paragraph, registered both ways, so the only variable is the source.
	paragraph := strings.Repeat("word ", 200)
	read := func(add func(*runtime.SlashRegistry, runtime.SlashCommand)) string {
		m := NewModel(Options{})
		add(m.slash, descCommand("permissions", paragraph))
		m = typeInto(t, m, "/perm").(Model)
		return stripANSI(m.menu.view(80))
	}

	builtin := read((*runtime.SlashRegistry).AddBuiltin)
	if got := strings.Count(builtin, "\n") + 1; got != 1+maxBuiltinDescLines {
		t.Errorf("built-in description lines = %d, want %d:\n%s", got, 1+maxBuiltinDescLines, builtin)
	}
	if !strings.Contains(builtin, ellipsis) {
		t.Errorf("a built-in description past its cap should end with an ellipsis:\n%s", builtin)
	}

	ext := read((*runtime.SlashRegistry).AddSkill)
	if got := strings.Count(ext, "\n") + 1; got != 1+maxExtensionDescLines {
		t.Errorf("extension description lines = %d, want %d:\n%s", got, 1+maxExtensionDescLines, ext)
	}
	if !strings.Contains(ext, ellipsis) {
		t.Errorf("an extension description past its cap should end with an ellipsis:\n%s", ext)
	}

	// The cap follows the source, not the name: a user template is capped like a
	// skill, and a built-in keeps its four lines even when it shares a name.
	if got := commandDescLineCap(descCommand("permissions", longDescText)); got != maxBuiltinDescLines {
		t.Errorf("built-in cap = %d, want %d", got, maxBuiltinDescLines)
	}
	for _, add := range []func(*runtime.SlashRegistry, runtime.SlashCommand){
		(*runtime.SlashRegistry).AddUser,
		(*runtime.SlashRegistry).AddSkill,
		(*runtime.SlashRegistry).AddPlugin,
	} {
		reg := runtime.NewSlashRegistry()
		add(reg, descCommand("mine", longDescText))
		c, ok := reg.Lookup("mine")
		if !ok {
			t.Fatal("command not registered")
		}
		if got := commandDescLineCap(c); got != maxExtensionDescLines {
			t.Errorf("extension cap = %d, want %d", got, maxExtensionDescLines)
		}
	}
}
