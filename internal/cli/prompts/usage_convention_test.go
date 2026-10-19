package prompts

// The built-in slash commands follow one convention for how they describe
// themselves, and these tests are what hold it: a command's USAGE lives in
// ArgumentHint, and its DESCRIPTION is a sentence about what it does.
//
// The rule exists because three surfaces render the same two fields (the popup,
// /help, the REPL's completion label), so usage written into a description is
// usage that can only be shown in the one place there is room for it — and once
// it is prose, nothing keeps it true. That is how /permissions came to carry a
// hand-written "…" abbreviation that users read as a rendering bug, and how
// /trust came to have no discoverable arguments at all once its description was
// trimmed back to a sentence.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/trust"
)

// builtinRegistry assembles every built-in a session can have, in the same order
// the front-ends do: the shared registry, then the instance built-ins whose
// closures need state (/permissions carries the live approval mode, /trust the
// manager, /boundary the conversation language).
func builtinRegistry(t *testing.T) *runtime.SlashRegistry {
	t.Helper()
	reg, err := BuildSlashRegistry(nil, nil, nil, nil, PromptTemplateSources{Disable: true})
	if err != nil {
		t.Fatalf("BuildSlashRegistry: %v", err)
	}
	RegisterPermissionCommand(reg, permissions.New(permissions.Auto), nil)
	RegisterBoundaryCommand(reg, nil)
	mgr, err := trust.NewManager("")
	if err != nil {
		t.Fatalf("trust.NewManager: %v", err)
	}
	trust.RegisterCommand(reg, mgr, "/tmp")
	return reg
}

// TestBuiltinDescriptionsDoNotCarryUsage is the smell detector. A description
// that names its own command, or spells a --flag, is usage that has leaked out of
// the hint: it will read as a run-on sentence everywhere the description is
// shown, and it cannot be kept in sync with the code that parses the arguments.
func TestBuiltinDescriptionsDoNotCarryUsage(t *testing.T) {
	reg := builtinRegistry(t)
	for _, c := range reg.List() {
		if c.Source != runtime.SourceBuiltin {
			continue
		}
		if strings.Contains(c.Description, "/"+c.Name) {
			t.Errorf("/%s description names itself (%q): usage belongs in ArgumentHint",
				c.Name, c.Description)
		}
		for _, field := range strings.Fields(c.Description) {
			if strings.HasPrefix(field, "--") {
				t.Errorf("/%s description carries the flag %q: flags are usage and belong in ArgumentHint",
					c.Name, field)
			}
		}
	}
}

// TestBuiltinHintsAreUsageShape: a hint is the signature the user types, so it is
// bracketed like the frontmatter convention documents (`<angle>` required,
// `[square]` optional). Prose in the hint field is the same bug pointing the
// other way — it would be printed where the arguments go.
func TestBuiltinHintsAreUsageShape(t *testing.T) {
	reg := builtinRegistry(t)
	for _, c := range reg.List() {
		if c.Source != runtime.SourceBuiltin || c.ArgumentHint == "" {
			continue
		}
		if !strings.HasPrefix(c.ArgumentHint, "[") && !strings.HasPrefix(c.ArgumentHint, "<") {
			t.Errorf("/%s hint %q should open with [ or <: it is the signature, not a sentence",
				c.Name, c.ArgumentHint)
		}
		if strings.ContainsAny(c.ArgumentHint, "\n") {
			t.Errorf("/%s hint spans lines: %q", c.Name, c.ArgumentHint)
		}
	}
}

// TestBuiltinUsageInventory pins which built-ins take arguments. It is the
// forcing function for the convention: a command that grows an argument must
// declare it in the hint AND appear here, so forgetting the hint fails a test
// instead of shipping an undiscoverable command (which is exactly how /trust lost
// its arguments). Commands absent from this map are pinned as argument-less.
func TestBuiltinUsageInventory(t *testing.T) {
	want := map[string]string{
		"proxy":          "[<provider> on|off | url <url>]",
		"model":          "[n|id [level]|think level]",
		"provider":       "[name | default [name|off]]",
		"permissions":    "[list | read-only|ask|auto|full-access]",
		"boundary":       "[list | readable [list|add|rm] <path> | writable [list|add|rm] <path>]",
		"trust":          "[on|off|once|status]",
		"grants":         "[list | add <path> <r|w|e> | rm <path>]",
		"memory":         "[recall [on|off]]",
		"fork":           "[n]",
		"tree":           "[n]",
		"rewind":         "[n]",
		"export":         "[path.jsonl|path.html]",
		"import":         "<path.jsonl>",
		"resume":         "[--all] [n|id]",
		"dream":          "[--dry-run]",
		"goal":           "[--tokens N] <objective> | pause | resume | clear",
		"btw":            "[question]",
		"remote-control": "[stop|status]",
	}
	reg := builtinRegistry(t)
	got := map[string]string{}
	for _, c := range reg.List() {
		if c.Source != runtime.SourceBuiltin {
			continue
		}
		if c.ArgumentHint != "" {
			got[c.Name] = c.ArgumentHint
		}
	}
	for name, hint := range want {
		switch g, ok := got[name]; {
		case !ok:
			t.Errorf("/%s should declare usage %q but has no hint", name, hint)
		case g != hint:
			t.Errorf("/%s hint = %q, want %q", name, g, hint)
		}
	}
	for name, hint := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("/%s declares usage %q but is not in the inventory: add it here (and to the command's own docs) if it really takes arguments",
				name, hint)
		}
	}
}

// TestHelpRendersHintAndDescriptionOnce each: the two fields are printed side by
// side, so a hint that repeats the description (or a description that repeats the
// hint) is visible as duplication in the one listing users read to learn the
// commands. This is the assertion that would have caught usage written into both.
func TestHelpRendersHintAndDescriptionOnce(t *testing.T) {
	reg := builtinRegistry(t)
	out, err := reg.ResolveOutcome("/help")
	if err != nil {
		t.Fatalf("ResolveOutcome /help: %v", err)
	}
	for _, c := range reg.List() {
		if c.Source != runtime.SourceBuiltin || c.ArgumentHint == "" {
			continue
		}
		// The line for this command must mention each field exactly once.
		var line string
		for _, ln := range strings.Split(out.Message, "\n") {
			if strings.Contains(ln, "/"+c.Name+" ") {
				line = ln
				break
			}
		}
		if line == "" {
			t.Errorf("/help does not list /%s", c.Name)
			continue
		}
		for _, field := range []string{c.ArgumentHint, c.Description} {
			if n := strings.Count(line, field); n != 1 {
				t.Errorf("/help line for /%s prints %q %d times, want once:\n%s", c.Name, field, n, line)
			}
		}
	}
}

// TestUsageLinesAgreeWithHints: the line a command answers a bad argument with is
// the same grammar the popup and /help advertise. These were typed twice for
// every command that has both, and they drifted the moment a subcommand was added
// to one and not the other — the hint learned `list` while the error line still
// taught the old list. The commands whose usage line lives in this package are
// asserted here; the ones whose parse lives in a front-end (which owns its own
// error text) are covered by TestBuiltinUsageInventory.
func TestUsageLinesAgreeWithHints(t *testing.T) {
	reg := builtinRegistry(t)
	for _, tc := range []struct{ name, badArg string }{
		{"permissions", "bogus-mode"},
		{"boundary", "bogus-verb"},
		{"trust", "bogus-verb"},
		{"grants", "bogus-verb"},
		{"proxy", "one-arg-only"},
	} {
		cmd, ok := reg.Lookup(tc.name)
		if !ok {
			t.Fatalf("/%s not registered", tc.name)
		}
		msg := cmd.Action(tc.badArg)
		if !strings.Contains(msg, cmd.ArgumentHint) {
			t.Errorf("/%s usage line should print the hint %q, got %q", tc.name, cmd.ArgumentHint, msg)
		}
	}
}

// TestSubcommandVerbsAreAccepted pins the *other* direction of drift: every verb
// the grammar advertises must actually work. A hint that offers a verb the parser
// rejects (or a sub-usage that teaches a subcommand that was renamed) is the same
// class of lie as a usage line that disagrees with the hint, and only a test that
// calls the command can see it.
//
// The verbs are listed here rather than extracted from the hint on purpose: what is
// being pinned is that the documented surface works, so this list is the
// expectation — a hint that stops mentioning one is a separate failure, caught by
// the inventory test.
func TestSubcommandVerbsAreAccepted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))

	reg := builtinRegistry(t)
	bd, ok := reg.Lookup("boundary")
	if !ok {
		t.Fatal("/boundary not registered")
	}
	for _, kw := range []string{"writable", "readable"} {
		for _, verb := range []string{"list", "add", "rm"} {
			arg := kw + " " + verb
			if verb != "list" {
				// add/rm need a path; the store is a temp dir, so these are real
				// round trips against an empty store.
				arg += " " + filepath.Join(dir, kw+"-"+verb)
			}
			if msg := bd.Action(arg); strings.Contains(msg, "usage:") {
				t.Errorf("/boundary %s was answered with usage; the grammar advertises that verb:\n%s", arg, msg)
			}
		}
	}
	// The hint must keep advertising each one too, or the two lists have split.
	for _, verb := range []string{"list", "add", "rm"} {
		if !strings.Contains(bd.ArgumentHint, verb) {
			t.Errorf("/boundary hint %q no longer names %q", bd.ArgumentHint, verb)
		}
	}
	// /proxy's provider form: both verbs, and the address form, are accepted.
	proxy, ok := reg.Lookup("proxy")
	if !ok {
		t.Fatal("/proxy not registered")
	}
	for _, arg := range []string{"url http://127.0.0.1:9", "openai on", "openai off"} {
		if msg := proxy.Action(arg); strings.Contains(msg, "usage:") {
			t.Errorf("/proxy %s was answered with usage:\n%s", arg, msg)
		}
	}
}
