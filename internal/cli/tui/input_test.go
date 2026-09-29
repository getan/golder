package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// runeKey builds a printable-character key press carrying r, mirroring what a
// terminal sends for a typed rune: Code is the rune and Text is its UTF-8
// encoding (textarea inserts from Text). This is how CJK / emoji reach the
// component.
func runeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// TestInputCJKByRune drives the input with the runes of "你好" and asserts the
// buffer holds the full multi-byte string with the cursor left on a rune
// boundary. This guards against the old REPL bug that keyed on byte length
// (len==1) and dropped the trailing bytes of every multi-byte rune.
func TestInputCJKByRune(t *testing.T) {
	in := newInput()
	for _, r := range "你好" {
		var cmd tea.Cmd
		in, cmd = in.Update(runeKey(r))
		_ = cmd
	}

	got := in.Value()
	if got != "你好" {
		t.Fatalf("Value() = %q, want %q", got, "你好")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("Value() is not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 2 {
		t.Fatalf("rune count = %d, want 2 (no dropped chars)", n)
	}
	// The cursor column is a rune index into the line; after two runes it must be
	// 2, proving textarea advanced by whole runes rather than bytes.
	if col := in.ta.Column(); col != 2 {
		t.Errorf("cursor column = %d, want 2 (rune boundary)", col)
	}
}

// TestInputEmojiByRune confirms a multi-byte emoji is inserted whole.
func TestInputEmojiByRune(t *testing.T) {
	in := newInput()
	in, _ = in.Update(runeKey('🚀'))
	if got := in.Value(); got != "🚀" {
		t.Fatalf("Value() = %q, want %q", got, "🚀")
	}
}

// TestInputNewlineKeys verifies each newline binding — Shift+Enter (primary),
// Ctrl+J and Alt+Enter (fallbacks) — inserts a newline into the buffer while
// plain runes fill each line.
func TestInputNewlineKeys(t *testing.T) {
	cases := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := newInput()
			in, _ = in.Update(runeKey('你'))
			in, _ = in.Update(tc.key)
			in, _ = in.Update(runeKey('好'))
			if got := in.Value(); got != "你\n好" {
				t.Fatalf("Value() = %q, want %q", got, "你\n好")
			}
		})
	}
}

// TestInputEnterIsNotNewline confirms plain Enter does NOT insert a newline in
// the editor: the model intercepts it as submit, so the editor must leave it
// alone (only Shift+Enter breaks a line).
func TestInputEnterIsNotNewline(t *testing.T) {
	in := newInput()
	in, _ = in.Update(runeKey('a'))
	in, _ = in.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := in.Value(); got != "a" {
		t.Fatalf("Value() = %q, want %q (Enter must not add a newline)", got, "a")
	}
}

// TestInputNewlineRendersBothLines guards the viewport-offset regression: after
// 你 + Shift+Enter + 好 the buffer is "你\n好", but the editor once rendered two
// blank rows because a manual SetHeight left the textarea's viewport scrolled
// past the first line. DynamicHeight now resets the scroll offset in the same
// pass, so both runes must appear in the rendered View.
func TestInputNewlineRendersBothLines(t *testing.T) {
	in := newInput()
	in.SetWidth(40)
	in, _ = in.Update(runeKey('你'))
	in, _ = in.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	in, _ = in.Update(runeKey('好'))

	view := in.View()
	if !strings.Contains(view, "你") || !strings.Contains(view, "好") {
		t.Fatalf("rendered view missing content, want both 你 and 好:\n%s", view)
	}
}

// TestInputClearBlurFocus exercises the lifecycle methods the model relies on
// while gating input during a run.
func TestInputClearBlurFocus(t *testing.T) {
	in := newInput()
	in, _ = in.Update(runeKey('x'))
	in.Clear()
	if got := in.Value(); got != "" {
		t.Errorf("after Clear, Value() = %q, want empty", got)
	}
	if !in.Focused() {
		t.Error("newInput should start focused")
	}
	in.Blur()
	if in.Focused() {
		t.Error("after Blur, Focused() should be false")
	}
	in.Focus()
	if !in.Focused() {
		t.Error("after Focus, Focused() should be true")
	}
}

// TestModelEnterSubmits feeds a typed line and Enter to the model and asserts
// the prompt is submitted: the user turn lands in the transcript and the editor
// is cleared. Enter submits; Shift+Enter is the newline key in the multi-line
// composer. With no startRunFn wired the model stays idle and records the
// pre-#392 system note.
func TestModelEnterSubmits(t *testing.T) {
	m := NewModel(Options{})
	var model tea.Model = m
	for _, r := range "你好世界" {
		model, _ = model.Update(runeKey(r))
	}
	model, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	got := model.(Model)
	if got.input.Value() != "" {
		t.Errorf("after submit, input = %q, want cleared", got.input.Value())
	}
	joined := strings.Join(blockTexts(got.transcript), "\n")
	if !strings.Contains(joined, "你好世界") {
		t.Errorf("submitted prompt missing from transcript: %q", joined)
	}
}

// TestModelTwoStageInterrupt verifies FR-14: while running, Esc / Ctrl+C
// interrupts the in-flight run (calls interruptFn) and does NOT quit; while
// idle, the same keys quit the program.
func TestModelTwoStageInterrupt(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		// Running: first press interrupts, no quit.
		interrupted := false
		running := NewModel(Options{})
		running.running = true
		running.interruptFn = func() { interrupted = true }
		next, cmd := running.Update(key)
		if !interrupted {
			t.Errorf("%s while running: interruptFn was not called", key.String())
		}
		if next.(Model).quitting {
			t.Errorf("%s while running: model should not be quitting", key.String())
		}
		if cmd != nil {
			if _, isQuit := cmd().(tea.QuitMsg); isQuit {
				t.Errorf("%s while running: should not quit", key.String())
			}
		}

		// Idle: first press only arms, second press quits.
		idle := NewModel(Options{})
		armed, cmd := idle.Update(key)
		if cmd != nil {
			t.Fatalf("%s while idle: first press should only arm, got %T", key.String(), cmd())
		}
		if armed.(Model).quitting {
			t.Fatalf("%s while idle: first press must not quit", key.String())
		}
		got, cmd := armed.Update(key)
		if cmd == nil {
			t.Fatalf("%s while idle: second press should quit", key.String())
		}
		if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
			t.Errorf("%s while idle: cmd should be tea.Quit", key.String())
		}
		if !got.(Model).quitting {
			t.Errorf("%s while idle: model should be marked quitting", key.String())
		}
	}
}

// blockTexts extracts the raw text of every transcript block for assertions.
func blockTexts(t transcript) []string {
	out := make([]string, len(t.blocks))
	for i, b := range t.blocks {
		out[i] = b.text
	}
	return out
}
