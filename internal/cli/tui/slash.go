// This file implements slash-commands and their autocomplete popup for the
// full-screen TUI (US-008, FR-15). It is the TUI counterpart to the REPL's
// slash handling (internal/cli/repl/repl.go): both front-ends consult the SAME
// shared registry assembled by internal/cli/prompts.BuildSlashRegistry (#383),
// so /model, /help, user-declared templates (~/.golder/{commands,prompts}),
// config/CLI prompt templates, plugin commands and ~/.agents/skills /skill-name
// commands are identical across the two surfaces.
//
// tui deliberately imports prompts/runtime/cli (the shared lower layers), never
// repl: prompts sits below both front-ends, so there is no import cycle.
//
// The autocomplete popup (slashMenu) activates while the input buffer is a
// "/name" being typed (a leading "/" with no whitespace yet). It filters the
// registry by the typed prefix, is navigated with the arrow keys, completed with
// Tab, and run with Enter — the model intercepts those keys before delegating to
// the textarea (see model.handleKey).
package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// maxMenuRows caps how many candidate rows the popup shows at once; a longer
// filtered list scrolls a window around the selection so the overlay stays a few
// lines tall regardless of how many commands are registered.
const maxMenuRows = 8

// newSlashRegistry assembles the shared slash-command registry for the TUI the
// same way the REPL does: built-ins seeded from runtime, the live-state /model
// and /help commands bound to live (so a /model switch mutates the very config
// the run loop reads), user/plugin/skill/template commands from disk. A load
// error is non-fatal — BuildSlashRegistry still returns a registry with the
// built-ins, so the TUI stays usable and the failure is surfaced on stderr.
func newSlashRegistry(opts Options, live *cli.LiveConfig) *runtime.SlashRegistry {
	// A credentials store resolved from the same flags the run uses, so
	// "/model" live catalog listing (issue #566) can authenticate against the live endpoint.
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(opts.ProviderName, opts.APIKey)
	reg, err := prompts.BuildSlashRegistry(live, creds, opts.Skills, opts.Plugins, prompts.PromptTemplateSources{
		Settings: opts.ConfigPrompts,
		CLI:      opts.CliPrompts,
		Disable:  opts.NoPromptTemplates,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "golder: slash-commands: %v\n", err)
	}
	return reg
}

// slashMenu is the autocomplete popup state. It holds the candidates matching
// the current "/prefix" and the highlighted row; it is inactive (rendered as
// nothing) whenever the buffer is not a slash-command being typed or no command
// matches the prefix.
type slashMenu struct {
	theme    Theme
	active   bool
	filtered []runtime.SlashCommand
	selected int
	// pick, when non-nil, puts the menu in picker mode: rows are plain items
	// (no "/" prefix) and Enter confirms the highlight through the pick path
	// instead of slash resolution. Used by the /model and /resume pickers.
	pick []pickItem
	// pickMark annotates the row whose Value matches (e.g. the current model
	// or session) in picker mode.
	pickMark string
	// pickKind selects the confirm action: "model" re-runs /model, "resume"
	// re-runs /resume with the picked Value.
	pickKind string
}

// pickItem is one picker row: Title renders first, Detail (dimmer context)
// second, Value is handed to the confirm action.
type pickItem struct {
	Title  string
	Detail string
	Value  string
}

// newSlashMenu builds an inactive menu bound to the theme used for its rows.
func newSlashMenu(theme Theme) slashMenu { return slashMenu{theme: theme} }

// slashToken reports whether buffer is a slash-command name still being typed
// and returns the text after the leading "/". It is true only for a leading "/"
// with no whitespace yet: once the user types a space the name is complete and
// the buffer has moved on to arguments, so name-completion stops.
func slashToken(buffer string) (token string, ok bool) {
	trimmed := strings.TrimLeft(buffer, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	rest := trimmed[1:]
	if strings.ContainsAny(rest, " \t\n") {
		return "", false
	}
	return rest, true
}

// refresh recomputes the menu from the current buffer and registry. It activates
// only when the buffer is a "/name" prefix that matches at least one command;
// otherwise it deactivates and clears its candidates. The selection is clamped
// so it stays in range as the filtered set shrinks.
func (mn *slashMenu) refresh(buffer string, reg *runtime.SlashRegistry) {
	if mn.picking() {
		return
	}
	token, ok := slashToken(buffer)
	if !ok || reg == nil {
		mn.close()
		return
	}
	var out []runtime.SlashCommand
	for _, c := range reg.List() {
		if strings.HasPrefix(c.Name, token) {
			out = append(out, c)
		}
	}
	mn.filtered = out
	mn.active = len(out) > 0
	if mn.selected >= len(out) || mn.selected < 0 {
		mn.selected = 0
	}
}

// rows reports how many terminal rows the popup occupies when rendered, so the
// model can reserve that space above the input line during relayout. It is zero
// while inactive and otherwise the visible window height (min of the candidate
// count and maxMenuRows).
// row renders one candidate: a plain item in picker mode (with the mark
// annotated), otherwise "/name  description".
func (mn slashMenu) row(i int) string {
	if mn.picking() {
		it := mn.pick[i]
		line := it.Title
		if it.Detail != "" {
			line += "  " + it.Detail
		}
		v := it.Value
		if v == "" {
			v = it.Title
		}
		if v != "" && v == mn.pickMark {
			line += "  (current)"
		}
		return line
	}
	c := mn.filtered[i]
	line := "/" + c.Name
	if c.Description != "" {
		line += "  " + c.Description
	}
	return line
}

func (mn slashMenu) rows() int {
	n := mn.count()
	if !mn.active || n == 0 {
		return 0
	}
	if n > maxMenuRows {
		return maxMenuRows
	}
	return n
}

// close deactivates the menu and drops its candidates.
func (mn *slashMenu) close() {
	mn.active = false
	mn.filtered = nil
	mn.selected = 0
	mn.pick = nil
	mn.pickMark = ""
	mn.pickKind = ""
}

// openPicker shows the menu as an item picker (arrow keys + Enter, Esc
// cancels) for plain string items confirmed as /model. mark annotates the
// matching row, e.g. the current model.
func (mn *slashMenu) openPicker(items []string, mark string) {
	its := make([]pickItem, 0, len(items))
	for _, it := range items {
		its = append(its, pickItem{Title: it, Value: it})
	}
	mn.openPickerDetailed(its, mark, "model")
}

// openPickerDetailed shows the menu as an item picker with titled rows,
// confirmed through the kind path ("model" or "resume").
func (mn *slashMenu) openPickerDetailed(items []pickItem, mark, kind string) {
	mn.pick = items
	mn.pickMark = mark
	mn.pickKind = kind
	mn.active = len(items) > 0
	mn.filtered = nil
	mn.selected = 0
}

// picking reports whether the menu is in picker mode.
func (mn slashMenu) picking() bool { return mn.pick != nil }

// pickCurrent returns the highlighted pick item's Value.
func (mn slashMenu) pickCurrent() (string, bool) {
	if !mn.picking() || mn.selected < 0 || mn.selected >= len(mn.pick) {
		return "", false
	}
	v := mn.pick[mn.selected].Value
	if v == "" {
		v = mn.pick[mn.selected].Title
	}
	return v, true
}

// pickMove moves the picker highlight, wrapping at the ends.
func (mn *slashMenu) pickMove(d int) {
	if len(mn.pick) == 0 {
		return
	}
	mn.selected = (mn.selected + d + len(mn.pick)) % len(mn.pick)
}

// moveUp / moveDown cycle the highlighted candidate, wrapping at the ends so
// arrow navigation is continuous. Both work in picker mode via count().
func (mn *slashMenu) moveUp() {
	if n := mn.count(); n > 0 {
		mn.selected--
		if mn.selected < 0 {
			mn.selected = n - 1
		}
	}
}

func (mn *slashMenu) moveDown() {
	if n := mn.count(); n > 0 {
		mn.selected++
		if mn.selected >= n {
			mn.selected = 0
		}
	}
}

// current returns the highlighted candidate, or ok=false when the menu is
// inactive / empty.
func (mn slashMenu) current() (runtime.SlashCommand, bool) {
	if !mn.active || mn.selected < 0 || mn.selected >= len(mn.filtered) {
		return runtime.SlashCommand{}, false
	}
	return mn.filtered[mn.selected], true
}

// view renders the popup as a block of up to maxMenuRows lines, the highlighted
// row marked with a "›" caret and accented. Each row is "/name  description",
// truncated to the width so it never wraps. Returns "" when inactive so the
// model omits the overlay entirely (and its row) while idle.
func (mn slashMenu) view(width int) string {
	if !mn.active || mn.rows() == 0 {
		return ""
	}
	start, end := mn.window()
	rowWidth := width - 2 // reserve the caret / indent column
	if rowWidth < 1 {
		rowWidth = width
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		line := TruncateToWidth(mn.row(i), rowWidth)
		if i == mn.selected {
			b.WriteString(mn.theme.Accent.Render("› " + line))
		} else {
			b.WriteString(mn.theme.System.Render("  " + line))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// window returns the [start,end) slice of filtered candidates to display,
// scrolled to keep the selection visible when the list is taller than
// maxMenuRows.
// count is the candidate count in either mode.
func (mn slashMenu) count() int {
	if mn.picking() {
		return len(mn.pick)
	}
	return len(mn.filtered)
}

func (mn slashMenu) window() (int, int) {
	n := mn.count()
	if n <= maxMenuRows {
		return 0, n
	}
	start := mn.selected - maxMenuRows + 1
	if start < 0 {
		start = 0
	}
	if start > n-maxMenuRows {
		start = n - maxMenuRows
	}
	return start, start + maxMenuRows
}
