package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
