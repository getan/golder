package judge

// What a tool call touches: the path spellings the workspace boundary asks
// about.
//
// This extraction used to live in internal/accessgate, a package below this one,
// and it carried its two collaborators — the read-only recognizer and the
// program-aware operand scanner — as injected function fields, because
// accessgate could not import judge without a cycle. Both collaborators live
// here, so folding the extraction in removes the cycle and the injection with
// it, and what is left is the part that was ever about paths.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/patch"
	"github.com/getan/golder/internal/shellfacts"
)

// pathArgs reads a `path` argument in either declared shape: one string (read,
// view_image) or a list (grep, find, ls). Both shapes are read because a list
// the gate could not see would let a search reach outside the workspace with no
// question asked.
func pathArgs(args json.RawMessage) []string {
	var raw struct {
		Path json.RawMessage `json:"path"`
	}
	if err := json.Unmarshal(args, &raw); err != nil || len(raw.Path) == 0 {
		return nil
	}
	if raw.Path[0] == '"' {
		var one string
		if err := json.Unmarshal(raw.Path, &one); err != nil {
			return nil
		}
		if p := strings.TrimSpace(one); p != "" {
			return []string{p}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw.Path, &many); err != nil {
		return nil
	}
	out := make([]string, 0, len(many))
	for _, p := range many {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// commandPaths returns the paths a command line names, from ONE answer: the
// shell parse (shellfacts, which is mvdan) for everything lexical, and the
// program-semantics tables in bashpaths.go for what the grammar cannot say
// (`sort --files0-from=/outside/list` names a file to read, and nothing about the
// shell says so).
//
// It used to be the UNION of that parse and a second, hand-rolled reading of the
// text: the parser's list of path-shaped words, narrowed by the scanner's rules,
// plus the scanner's own list. Two readers meant two answers, and a word that one
// of them misread arrived as a consent dialog — `sed -n '/func X/,/^}/p' f.go`
// asked about `/func X/,/^}/p` because the parse reports it as an argument and
// only the scanner knew it was a script, and `python3 -c "…((n*med)/60)"` asked
// about `/60` because the scanner scanned a program the parse had already said
// was one word. Neither was a missing case; they were the shape of the union.
// There is one reader now, and the rules it applies are the ones that used to be
// the scanner's.
func commandPaths(cmd string) []string { return commandPathsAt(cmd, 0) }

// commandPathsAt is commandPaths at one level of inline-program recursion: a
// `sh -c "…"` program is a shell command line, so it goes through this same
// function rather than through a second reading of the text.
func commandPathsAt(cmd string, depth int) []string {
	plan := shellfacts.Parse(cmd)
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if !plan.Parsed {
		// The shell could not parse this either, so nothing here will run — but
		// "nothing here" is not a claim this layer should make on the parser's
		// behalf. The fallback reads the text lexically and keeps only what
		// EXISTS: the same verifiable predicate the interpreter programs use (see
		// programPaths), on the same reasoning — a candidate that names nothing
		// is not a read.
		for _, p := range pathShapedRuns(cmd) {
			if _, err := os.Stat(p); err == nil {
				add(p)
			}
		}
		return out
	}
	// AllCommands, not Commands: the latter holds the top level alone, and a
	// command inside a substitution, a loop body or a function runs too.
	for _, c := range plan.AllCommands() {
		for _, p := range commandCandidates(c, depth) {
			add(p)
		}
		// A write verb's operands are places this command changes. They are not
		// in fileOperandCommands (that list answers "does this program READ its
		// argument"), so without this pass `rm /outside/f` and `mv /outside/a b`
		// never reached the boundary gate at all — and a bare (envelope) shell
		// has no sandbox left to catch them.
		for _, p := range writeOperandCandidates(c) {
			add(p)
		}
	}
	return out
}

// writeOperandCandidates lists the operand paths of a command that writes them.
// The verb set is the same one PathWriteNeed uses, so the question asked and the
// grant recorded agree about what counts as a write.
func writeOperandCandidates(cmd shellfacts.Command) []string {
	i, ok := unwrapProgram(cmd.Argv)
	if !ok {
		return nil
	}
	prog := filepath.Base(cmd.Argv[i].Text)
	// git clone creates the directory it is handed, and that write belongs to the
	// SUBCOMMAND rather than to the program: `git` is in neither table, and the
	// destination is not an operand of git itself. It is collected before the verb
	// test, because the answer for `git clone … ../repo` would otherwise be "this
	// command writes nothing" — the gap gitclone.go documents at length.
	var out []string
	for _, p := range cloneTargetPaths(cmd) {
		if pathShapedWord(p) {
			out = append(out, p)
		}
	}
	writes := writeVerbs[prog]
	if prog == "sed" || prog == "awk" || prog == "gawk" {
		writes = inPlaceFlag(cmd.Args())
	}
	if !writes {
		return out
	}
	for _, w := range cmd.Argv[i+1:] {
		if v, ok := wordValue(w); ok && pathShapedWord(v) {
			out = append(out, v)
		}
	}
	return out
}

// bashPaths reads the command out of a bash call's arguments and returns the
// paths it names. It is the table's reader for bash: the paths a shell call
// touches are words in its command line rather than an argument, so the reading
// happens one step later than for the file tools.
func bashPaths(args json.RawMessage) []string {
	cmd, ok := bashCommand(args)
	if !ok {
		return nil
	}
	return commandPaths(cmd)
}

// patchArgs returns the paths an apply_patch call touches, read from the patch
// text. Every path counts, including a move destination: a rename out of the
// workspace writes outside it just as surely as an edit there.
func patchArgs(args json.RawMessage) []string {
	text, ok := patchText(args)
	if !ok {
		return nil
	}
	return patch.Paths(text)
}
