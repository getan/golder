package judge

// git clone: which directory the command is asking to have created.
//
// A clone was invisible to every table in this package. `git` is not a
// file-operand command — its arguments are not files it opens — and it is not
// in writeVerbs, because the write is a side effect of a SUBCOMMAND rather than
// of the program. So `git clone https://github.com/org/repo.git ../repo` named
// no path at all:
//
//   - the boundary gate had nothing to ask about, so the write outside the
//     workspace never met the one question the boundary exists to put;
//   - the sandbox had no writable root to widen, so the clone could only fail
//     there;
//   - the escalation dialog had no path to re-admit, so the smaller fix it
//     exists to offer (grant the directory, keep the call contained) was not on
//     the table;
//   - and the model's only remaining route to a same-level clone was to ask for
//     the WHOLE call to leave the sandbox — a request the reviewer is asked to
//     answer Direct, which is how two identical clones came to differ: the one
//     where the reviewer granted the escape ran, and the one where it did not
//     was blocked with advice naming no directory to grant.
//
// This file is that missing reader, and it is deliberately the ONLY one: the
// boundary gate, the write-side detection (PathWritten) and the escalation
// fallback (GrantablePathFromText) all ask it, so the three cannot drift about
// which word is the destination.
//
// It reads the shell parse rather than splitting on spaces, because the
// distinction it has to make is exactly the one text cannot carry: `git -c
// http.proxy=http://127.0.0.1:7897 clone … ../repo` puts a config setting, a
// repository URL and a destination on one line, and the destination is the
// second POSITIONAL word after the clone subcommand — not the second word, and
// not the last.

import (
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/shellfacts"
)

// gitGlobalValueFlags are git's global options whose argument is a separate
// word. The values are stepped over rather than read, because they are where a
// reader loses the thread: `-c <name>=<value>` carries a config setting (the
// case above carries a proxy URL), and the rest name places git is being told
// about rather than the directory this command creates.
var gitGlobalValueFlags = map[string]bool{
	"-c": true, "--config-env": true, "--exec-path": true, "--git-dir": true,
	"--work-tree": true, "--namespace": true, "--super-prefix": true,
}

// gitGlobalBareFlags are git's global options that take no separate word.
var gitGlobalBareFlags = map[string]bool{
	"-p": true, "--paginate": true, "--no-pager": true, "--bare": true,
	"-h": true, "--help": true, "--version": true, "--html-path": true,
	"--man-path": true, "--info-path": true, "--no-replace-objects": true,
	"--literal-pathspecs": true, "--no-literal-pathspecs": true,
	"--glob-pathspecs": true, "--noglob-pathspecs": true,
	"--icase-pathspecs": true, "--no-optional-locks": true, "--no-advice": true,
}

// cloneValueFlags are the clone options whose argument is a separate word. They
// have to be stepped over or the value is read as the repository: `git clone
// --depth 1 --branch main <url> <dir>` would otherwise take "main" for the
// repository and the URL for the destination.
var cloneValueFlags = map[string]bool{
	"-o": true, "--origin": true, "-b": true, "--branch": true,
	"-u": true, "--upload-pack": true, "--template": true,
	"--reference": true, "--reference-if-able": true,
	"--depth": true, "--shallow-since": true, "--shallow-exclude": true,
	"-j": true, "--jobs": true, "--separate-git-dir": true,
	"-c": true, "--config": true, "--server-option": true, "--filter": true,
	"--ref-format": true, "--bundle-uri": true, "--revision": true, "--ref": true,
}

// cloneBareFlags are the clone options that take no separate word.
var cloneBareFlags = map[string]bool{
	"-l": true, "--local": true, "--no-hardlinks": true, "-s": true,
	"--shared": true, "-n": true, "--no-checkout": true, "-q": true,
	"--quiet": true, "-v": true, "--verbose": true, "--progress": true,
	"--no-progress": true, "--dissociate": true, "--single-branch": true,
	"--no-single-branch": true, "--tags": true, "--no-tags": true,
	"--bare": true, "--mirror": true, "-4": true, "--ipv4": true, "-6": true,
	"--ipv6": true, "--sparse": true, "--no-sparse": true,
	"--also-filter-submodules": true, "--remote-submodules": true,
	"--no-remote-submodules": true, "--recurse-submodules": true,
	"--no-recurse-submodules": true, "--shallow-submodules": true,
	"--no-shallow-submodules": true, "--reject-shallow": true,
	"--no-reject-shallow": true, "--atomic": true, "--no-atomic": true,
	"--no-dissociate": true, "--no-also-filter-submodules": true,
}

// cloneShortFlags are the short clone options, by letter, so a cluster (`-qb`)
// is read as the flags it is rather than as an unknown word.
var cloneShortFlags = map[byte]bool{
	'l': true, 's': true, 'n': true, 'q': true, 'v': true, 'b': true,
	'o': true, 'u': true, 'j': true, 'c': true, '4': true, '6': true, 'h': true,
}

// cloneShortValueLetters are the short clone options whose value is a separate
// word when the option stands alone or ends a cluster: -b <branch>, -o <name>,
// -u <pack>, -j <n>, -c <key=value>.
var cloneShortValueLetters = map[byte]bool{
	'b': true, 'o': true, 'u': true, 'j': true, 'c': true,
}

// cloneTargetPaths returns the destination directory of every `git clone` in
// one parsed command, spelled as the command spells it: a relative destination
// stays relative, because the caller that resolves paths is the one that knows
// the directory the command runs in. A `git -C <dir>` is folded in first, since
// it is what makes a relative destination mean something else.
//
// The result is empty whenever the destination cannot be named with certainty —
// no explicit destination, an option this table does not know, a value the shell
// computes at runtime, or a clone this reader cannot follow. Empty is the
// fail-closed direction, and it is the direction every caller wants: nothing is
// what they had before, and a wrong answer here would be a dialog about a path
// the command never touches.
func cloneTargetPaths(cmd shellfacts.Command) []string {
	i, ok := unwrapProgram(cmd.Argv)
	if !ok {
		return nil
	}
	argv := cmd.Argv[i:]
	prog, ok := pinnedWordValue(argv[0])
	if !ok || filepath.Base(prog) != "git" {
		return nil
	}
	rest, workDir, ok := gitSubcommand(argv[1:])
	if !ok || len(rest) == 0 {
		return nil
	}
	if sub, ok := wordValue(rest[0]); !ok || sub != "clone" {
		return nil
	}
	target, ok := cloneTargetOperand(rest[1:])
	if !ok {
		return nil
	}
	// An absolute destination is not relative to the directory `-C` entered, so
	// folding it would invent a path (`/elsewhere/src` + `/Volumes/x`). A
	// `~/repos/x` arrives here already expanded to an absolute spelling by
	// wordValue, and is covered by the same test.
	if workDir != "" && !filepath.IsAbs(target) {
		target = filepath.Join(workDir, target)
	}
	return []string{target}
}

// gitSubcommand steps over git's global options and returns the words from the
// subcommand onward, plus the working directory the `-C` options established
// (git applies them left to right, each relative to the previous).
//
// ok=false means the line must not be read this way, and there are two reasons:
// an option this table does not know, and a value the shell decides at runtime.
// Guessing past either is how a reader ends up taking an option's value for the
// subcommand and then treating whatever follows as a destination.
func gitSubcommand(args []shellfacts.Word) (rest []shellfacts.Word, workDir string, ok bool) {
	for j := 0; j < len(args); j++ {
		text, ok := pinnedWordValue(args[j])
		if !ok {
			return nil, "", false
		}
		switch {
		case text == "--":
			// Everything after this is an operand, by the shell's own rule.
			return args[j+1:], workDir, true
		case text == "-C":
			if j+1 >= len(args) {
				return nil, "", false
			}
			dir, ok := pinnedWordValue(args[j+1])
			if !ok {
				return nil, "", false
			}
			workDir = joinWorkDir(workDir, dir)
			j++
		case strings.HasPrefix(text, "-C") && !strings.HasPrefix(text, "--"):
			workDir = joinWorkDir(workDir, text[2:])
		case gitGlobalValueFlags[text]:
			j++
		case gitGlobalBareFlags[text]:
		case strings.HasPrefix(text, "-c") && len(text) > 2:
			// `-cname=value`: the setting is attached, nothing to step over.
		case strings.HasPrefix(text, "--") && strings.Contains(text, "="):
			if name := text[:strings.IndexByte(text, '=')]; !gitGlobalValueFlags[name] {
				return nil, "", false
			}
		case strings.HasPrefix(text, "-") && text != "-":
			return nil, "", false
		default:
			return args[j:], workDir, true
		}
	}
	return nil, "", false
}

// joinWorkDir folds one `-C` into the working directory it is relative to.
func joinWorkDir(dir, next string) string {
	if dir == "" {
		return next
	}
	return filepath.Join(dir, next)
}

// cloneTargetOperand returns the destination directory of one clone's argument
// list, or ok=false when it cannot be named.
//
// The destination is the SECOND positional word. The first is the repository —
// a URL, or a local directory the clone reads — and reporting it as a
// destination would raise a question about a path this command does not create.
// With no second word git derives the directory from the repository name, which
// this layer cannot compute, so the answer is "no destination" and never the
// parent directory: that parent is the tree holding every sibling of the
// workspace, and handing it over to write one new repository is precisely the
// over-grant the boundary gate exists to prevent.
//
// A third positional word is not a destination either: git takes exactly two,
// and a command this reader cannot follow is one it declines to describe.
func cloneTargetOperand(args []shellfacts.Word) (string, bool) {
	var positional []shellfacts.Word
	endOfOptions := false
	for j := 0; j < len(args); j++ {
		text, ok := pinnedWordValue(args[j])
		if !ok {
			return "", false
		}
		if endOfOptions {
			positional = append(positional, args[j])
			continue
		}
		switch {
		case text == "--":
			endOfOptions = true
		case text == "-" || !strings.HasPrefix(text, "-"):
			positional = append(positional, args[j])
		default:
			takesValue, known := cloneOptionValue(text)
			if !known {
				return "", false
			}
			if takesValue {
				if j+1 >= len(args) {
					return "", false
				}
				j++
			}
		}
	}
	if len(positional) != 2 {
		return "", false
	}
	return pinnedWordValue(positional[1])
}

// cloneOptionValue reports whether one clone option consumes the following
// word, and whether the option is one this table knows. An unknown option is
// reported as unknown so the caller can decline to read the line at all: an
// option whose value takes a word shifts every positional after it.
func cloneOptionValue(text string) (takesValue, known bool) {
	if strings.HasPrefix(text, "--") {
		name := text
		attached := false
		if eq := strings.IndexByte(text, '='); eq > 0 {
			name, attached = text[:eq], true
		}
		switch {
		case cloneValueFlags[name]:
			// The long forms take their value attached (`--depth=1`) or as the
			// next word (`--depth 1`).
			return !attached, true
		case cloneBareFlags[name]:
			return false, true
		}
		return false, false
	}
	body := strings.TrimPrefix(text, "-")
	if body == "" {
		return false, false
	}
	if cloneShortValueLetters[body[0]] {
		// `-b main` takes the next word; `-bmain` carries the value on the flag.
		return len(body) == 1, true
	}
	// A cluster (`-qb`): every letter must be a flag this table knows, and a
	// value-taking letter in last position takes the next word.
	for i := 0; i < len(body); i++ {
		if !cloneShortFlags[body[i]] {
			return false, false
		}
	}
	return cloneShortValueLetters[body[len(body)-1]], true
}

// pinnedWordValue renders a word whose value the text fixes: literal and quoted
// runs, a leading `~` or `$HOME`, and nothing the shell computes at runtime.
//
// It is wordValue plus the two shapes wordValue deliberately keeps as text: a
// glob (whose text is a PATTERN — `../repo*` names as many places as there are
// matches) and a quoted tilde (which the shell does not expand, so `"~/x"` is a
// directory named `~` in the current directory, not the user's home). A grant
// built from either would name a place the command may never touch, and the
// dialog would offer a path the person did not ask about.
func pinnedWordValue(w shellfacts.Word) (string, bool) {
	for _, p := range w.Parts {
		switch p.Kind {
		case shellfacts.GlobPart, shellfacts.ExtGlobPart:
			return "", false
		}
	}
	if strings.HasPrefix(w.Text, "~") {
		expanded := false
		for _, p := range w.Parts {
			if p.Kind == shellfacts.TildePart {
				expanded = true
			}
		}
		if !expanded {
			return "", false
		}
	}
	return wordValue(w)
}

// cloneCreates reports whether raw is the destination directory of a git clone
// in this command text, which is how the boundary gate learns that a path it was
// handed is a write rather than a read.
//
// The comparison is between two spellings produced by the SAME reader from the
// same text, so it is exact — which matters because the gate holds the
// destination as written (a relative `../repo` resolved against the workspace),
// and a looser match would call an unrelated path a write.
func cloneCreates(command, raw string) bool {
	want := filepath.Clean(strings.TrimSpace(raw))
	if want == "" || want == "." {
		return false
	}
	return cloneCreatesAt(command, want, 0)
}

// cloneCreatesAt is cloneCreates at one level of inline-program recursion, and it
// exists for the same reason commandPathsAt does: `sh -c 'git clone <url> ../repo'`
// is a shell command line whose clone the boundary reader DOES see (it recurses
// into the program), so the write-side test has to look in the same place. A
// reader that stopped at the outer command would report the destination as a
// path and then call it a read.
func cloneCreatesAt(command, want string, depth int) bool {
	plan := commandPlan(command)
	if !plan.Parsed {
		return false
	}
	for _, c := range plan.AllCommands() {
		for _, target := range cloneTargetPaths(c) {
			if filepath.Clean(target) == want {
				return true
			}
		}
		if depth >= maxProgramDepth {
			continue
		}
		i, ok := unwrapProgram(c.Argv)
		if !ok {
			continue
		}
		prog := filepath.Base(c.Argv[i].Text)
		if !shellProgramInterpreters[prog] {
			continue
		}
		args := c.Argv[i+1:]
		texts := make([]string, len(args))
		for j, w := range args {
			texts[j] = w.Text
		}
		if idx := interpreterProgramIndex(prog, texts); idx >= 0 {
			if cloneCreatesAt(texts[idx], want, depth+1) {
				return true
			}
		}
	}
	return false
}
