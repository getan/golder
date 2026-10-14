package judge

// Reading files through bash: which paths can be seen in a command line.
//
// The read-scope gate asks about a path argument (read, grep, find, ls,
// view_image). bash takes no path argument — the paths are words inside the
// command — so the gate could not see them, and a command like
// `cat ~/.config/foo` read whatever it liked with no question asked. This file
// recovers what a static reader can: the file operands of the commands that
// actually open files.
//
// # What it can and cannot do
//
// Shell parsing here is the same deliberate approximation as the credential
// floor's (see static.go): quotes group words, segments split on control
// operators, transparent prefixes unwrap. On top of that:
//
//   - Only commands that take file operands are scanned. A path-shaped string
//     handed to `echo`, `git commit -m`, or `jq` is not a file being read, and
//     asking about it would train people to approve without reading.
//   - $HOME (and ${HOME}) is substituted, because rc files and dotfiles are the
//     common case and $HOME's value is knowable. Other variables are left
//     alone: their values are not, and treating an unknown variable as
//     suspicious would ask about every `cat $LOG` in a build script.
//   - An interpreter's inline program is scanned for embedded absolute paths
//     (`python3 -c 'open("/x")'`), because that path is a real read.
//
// It is best-effort by construction. `cat $UNKNOWN`, a script file that reads
// other files, and a path assembled at runtime are all invisible to it. The
// sandbox is the enforcement that does not depend on reading the command; this
// is the part that can ask before the command runs.

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// fileOperandCommands are the commands whose arguments name files to open.
// Printers are deliberately absent: `echo /etc/hosts` renders a string, it does
// not read a file.
//
// It must cover every whitelisted program that opens a path it is handed, and
// readOnlyProgramsWithoutPathOperands records the decision for the rest: the two
// tables together are the statement "for each command the recognizer can call
// read-only, whether its operands are scanned".
// TestOperandCoverageIsDeclaredForEveryWhitelistedProgram holds that statement,
// because the two tables live in different files and nothing else keeps them in
// step — a whitelisted program missing from both is exactly how `cksum
// /outside/x` came to be neither asked about nor reviewed.
var fileOperandCommands = map[string]bool{
	// readers
	"cat": true, "tac": true, "head": true, "tail": true, "less": true,
	"more": true, "bat": true, "nl": true, "od": true, "xxd": true,
	"strings": true, "base64": true, "file": true, "stat": true,
	// date is here for its file-reading flags (`date -r FILE`, GNU's
	// `date -f DATESFILE`), declared in fileArgFlags below. A bare date operand
	// (BSD's `date MMDDhhmm`, which sets the clock) is not path-shaped, so the
	// scanner reports nothing for it — the whitelist is what refuses it.
	"date": true,
	// searchers
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"ack": true, "find": true, "fd": true, "ls": true, "tree": true,
	// du and which look at named paths too, and the readers below were missing
	// from this table while the whitelist recognized them: for `cksum
	// /outside/x` the consent gate stayed silent AND the review was skipped, so
	// the read went through unasked. The table has to cover every whitelisted
	// program that opens a path it is handed.
	"du": true, "which": true, "cksum": true, "base32": true,
	"readlink": true, "realpath": true,
	// cd's operand is a directory the shell enters and everything after it reads
	// from, so it names a place the boundary applies to. Without this the
	// dominant shape of a real command line — `cd /outside && ls` — carried no
	// path the scanner could see, so the gate never asked about the directory
	// being entered; the whitelist recognizes `cd`, which would leave that read
	// unreviewed as well as unasked.
	"cd": true,
	// stream editors
	"sed": true, "awk": true, "gawk": true, "sort": true, "uniq": true,
	"cut": true, "tr": true, "wc": true, "diff": true, "cmp": true,
	// copiers / archivers
	"cp": true, "mv": true, "install": true, "rsync": true, "scp": true,
	"sftp": true, "tar": true, "zip": true, "unzip": true, "gzip": true,
	"gunzip": true,
	// hashers
	"shasum": true, "sha1sum": true, "sha256sum": true, "md5": true,
	// interpreters: a script file argument, and an inline program
	"python": true, "python3": true, "node": true, "nodejs": true,
	"perl": true, "ruby": true, "php": true, "bash": true, "sh": true,
	"zsh": true, "dash": true, "source": true,
}

// readOnlyProgramsWithoutPathOperands names the whitelisted programs whose bare
// operands are NOT paths, with the reason, so "every whitelisted program has
// been decided about" is a checkable statement rather than an assumption. The
// whitelist (readonly.go) and the operand table above live in different files,
// and nothing else keeps them in step — a program missing from both is how
// `cksum /outside/x` came to be neither asked about by the consent gate nor
// reviewed by the model.
//
// A program here may STILL read a file through a flag; that half is declared in
// fileArgFlags, which is where `date -f`, `grep -f` and the like are listed.
var readOnlyProgramsWithoutPathOperands = map[string]string{
	// Nothing to open: the program reports on the shell or the machine.
	"pwd":      "prints the shell's own working directory",
	"whoami":   "prints the current user name",
	"id":       "prints the current user's ids",
	"uname":    "prints kernel information",
	"sw_vers":  "prints the macOS version; it takes no operands",
	"hostname": "prints the machine name (a bare operand would set it, which rejectsNonFlags refuses)",
	"true":     "does nothing",
	"false":    "does nothing but exit non-zero",

	// Prints its arguments; opening them is what the shell would have to do, and
	// the scanner has already refused the expansions that could turn one into a
	// read (`echo "$(cat x)"` is not recognized at all).
	"echo":   "prints its arguments, it does not open them",
	"printf": "prints its arguments, it does not open them",

	// Transforms a string: the path is never opened, only split.
	"basename": "splits a string, it never opens the path",
	"dirname":  "splits a string, it never opens the path",

	// The exception that proves the point: git's operands ARE paths, but they
	// are pathspecs inside the repository, and git itself refuses to leave it
	// (`git show HEAD:../../etc/passwd` → "outside repository"). The flags that
	// would escape that (--no-index, --git-dir, -C, --work-tree, -c, …) are
	// refused by gitReadOnly, which is why this entry is safe without a scanned
	// operand.
	"git": "operands are repository-relative pathspecs; the flags that escape the repository are refused",
}

// interpreterProgramFlags maps an interpreter to the flag whose argument is an
// inline program. A path inside that program is a real read, so the program
// string is scanned in addition to the argument list. A shell's `-c` is here
// for the same reason: `bash -c 'cat /outside/x'` reads /outside/x.
var interpreterProgramFlags = map[string]byte{
	"python": 'c', "python3": 'c', "node": 'e', "nodejs": 'e',
	"perl": 'e', "ruby": 'e', "php": 'r',
	"bash": 'c', "sh": 'c', "zsh": 'c', "dash": 'c',
}

// fileArgFlags maps a command to the flags whose argument names a file to READ
// rather than the file being operated on. Two things go wrong without it: a
// search's pattern-position skip swallows the flag's value (`grep -f /outside/p
// /outside/data` reads a pattern file outside the workspace and the scanner
// treated it as the pattern), and the `--flag=value` form is invisible because
// the whole word starts with `-`.
var fileArgFlags = map[string][]string{
	"grep": {"-f", "--file"}, "egrep": {"-f", "--file"}, "fgrep": {"-f", "--file"},
	"rg":   {"-f", "--file", "--ignore-file"},
	"sort": {"--files0-from", "--random-source"},
	"wc":   {"--files0-from"},
	"diff": {"--from-file", "--to-file"},
	"file": {"-m", "--magic-file", "-f", "--files-from"},
	// date's -f is a dates FILE on GNU and a format string on BSD, and -r names
	// a file whose mtime is reported. Declaring both is the fail-safe reading:
	// a BSD format string is not path-shaped and adds no candidate, while a
	// GNU path does — and a path is a read either way.
	"date": {"-f", "--file", "-r", "--reference"},
}

// fileFlagValue returns the path a word carries as a file-reading flag's value,
// for the spellings that keep it inside the word: `--file=/x` and `-f/x`. The
// separated form (`-f /x`) is an ordinary following word, and its index is
// reported by fileFlagValueIndex instead.
func fileFlagValue(cmdName, word string) (string, bool) {
	for _, f := range fileArgFlags[cmdName] {
		if strings.HasPrefix(f, "--") {
			if v, ok := strings.CutPrefix(word, f+"="); ok {
				return v, true
			}
			continue
		}
		if len(word) > len(f) && strings.HasPrefix(word, f) {
			return word[len(f):], true
		}
	}
	return "", false
}

// fileFlagValueIndices marks the word indices that are a separate file-reading
// flag's value (`-f /x`, `--file /x`), so the pattern-position skip cannot
// mistake one for a search pattern.
func fileFlagValueIndices(cmdName string, words []shellWord, from int) map[int]bool {
	out := map[int]bool{}
	for j := from; j+1 < len(words); j++ {
		if isFileFlag(cmdName, words[j].text) {
			out[j+1] = true
		}
	}
	return out
}

// isFileFlag reports whether one word is, exactly, a file-reading flag of this
// program: the separated spelling whose VALUE is the next word (`date -r FILE`,
// `grep --file PATTERNS`). The attached spelling is fileFlagValue's business.
func isFileFlag(cmdName, word string) bool {
	for _, f := range fileArgFlags[cmdName] {
		if word == f {
			return true
		}
	}
	return false
}

// patternFromFile reports whether a search command takes its patterns from a
// file (`grep -f patterns.txt data.txt`). With no pattern operand, every
// non-flag word is a file to read, so the pattern-position skip must not drop
// the first one.
func patternFromFile(cmdName string, words []shellWord, from int) bool {
	for j := from; j < len(words); j++ {
		if isFileFlag(cmdName, words[j].text) {
			return true
		}
		if _, ok := fileFlagValue(cmdName, words[j].text); ok {
			return true
		}
	}
	return false
}

// bashPathCandidates returns the path-shaped words a bash command may read.
// Callers resolve each one and ask about the ones that land outside the
// workspace; the list is ordered as the command writes it, so the first
// outside path is also the most prominent one.
func bashPathCandidates(cmd string) []string {
	var out []string
	for _, seg := range splitShellSegments(cmd) {
		words := shellWords(seg)
		i := 0
		for i < len(words) && (isTransparentPrefix(words[i].text) ||
			words[i].text == "sudo" || words[i].text == "doas") {
			i++
		}
		if i >= len(words) {
			continue
		}
		cmdName := path.Base(words[i].text)
		if !fileOperandCommands[cmdName] {
			continue
		}
		// A separated file-reading flag's value (`-f /outside/pat`) is a file
		// operand, not the search pattern.
		fileValues := fileFlagValueIndices(cmdName, words, i+1)
		// A pattern command's first non-flag argument is a pattern, not a path:
		// `rg '/usr/local' internal` searches for that text. When the pattern
		// itself comes from a file (`grep -f pat.txt data.txt`) there is no
		// pattern operand, so every non-flag word names a file to read.
		skip := -1
		if patternCommands[cmdName] && !patternFromFile(cmdName, words, i+1) {
			for j := i + 1; j < len(words); j++ {
				if strings.HasPrefix(words[j].text, "-") || fileValues[j] {
					continue
				}
				skip = j
				break
			}
		}
		progWord := -1
		if letter, ok := interpreterProgramFlags[cmdName]; ok {
			for j := i + 1; j < len(words); j++ {
				if flagCarriesProgram(words[j].text, letter) && j+1 < len(words) {
					progWord = j + 1
					break
				}
			}
		}
		for j, w := range words {
			switch {
			case j == skip:
				continue
			case j == progWord:
				for _, frag := range embeddedPaths(w.text) {
					if shellProgramInterpreters[cmdName] {
						frag = expandTilde(frag)
					}
					out = append(out, frag)
				}
			default:
				// The attached spellings of a file-reading flag carry their path
				// inside the word, where the flag-prefix test below would hide it.
				if v, ok := fileFlagValue(cmdName, w.text); ok {
					v = expandKnownVars(v)
					if !w.leadQuoted {
						v = expandTilde(v)
					}
					if pathShapedWord(v) {
						out = append(out, v)
					}
					continue
				}
				text := expandKnownVars(w.text)
				// The shell expands a leading ~ only when the tilde itself was
				// written unquoted: `~/x` and `~/"x"` expand, `"~/x"` does not.
				if !w.leadQuoted {
					text = expandTilde(text)
				}
				if pathShapedWord(text) {
					out = append(out, text)
				}
			}
		}
	}
	return out
}

// flagCarriesProgram reports whether a token is the interpreter's inline-program
// flag, allowing a short-flag cluster (`sh -lc`, `bash -eu -c`): the letter
// must be the last of a `-xyz` run, and the token must not be a long flag.
func flagCarriesProgram(tok string, letter byte) bool {
	if len(tok) < 2 || tok[0] != '-' || tok[1] == '-' {
		return false
	}
	return tok[len(tok)-1] == letter
}

// pathShapedWord reports whether a word names a place outside the workspace: an
// absolute path, a home-anchored one, or a parent-relative escape. A bare
// relative word is excluded on purpose — it resolves inside the workspace,
// which is not a boundary question. A URL is never a local path.
func pathShapedWord(w string) bool {
	if w == "" || strings.HasPrefix(w, "-") || strings.Contains(w, "://") {
		return false
	}
	switch {
	case strings.HasPrefix(w, "/"):
		return true
	case w == "~" || strings.HasPrefix(w, "~/"):
		return true
	case w == ".." || strings.HasPrefix(w, "../"):
		return true
	}
	return false
}

// expandKnownVars substitutes the variables whose value is knowable here and
// whose referent is a real place on disk. $HOME is the one that matters (rc
// files, dotfiles, configs); an unknown variable is left as written, because a
// value this layer cannot know must not become a question the user has to
// answer on every build script. The shell's own tilde expansion is the other
// knowable spelling; it lives in expandTilde, next to this, because it applies
// to unquoted words only.
func expandKnownVars(w string) string {
	if !strings.Contains(w, "$HOME") && !strings.Contains(w, "${HOME}") {
		return w
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return w
	}
	// The braced form first, so "${HOME}" is not half-eaten by "$HOME".
	w = strings.ReplaceAll(w, "${HOME}", home)
	return strings.ReplaceAll(w, "$HOME", home)
}

// shellProgramInterpreters are the commands whose inline program (-c) is itself
// a shell command line: the inner shell expands a leading ~ there exactly as
// the outer one does, so a ~ fragment inside the program names the user's home.
// Other interpreters (python3 -c 'open("~/x")') never expand it.
var shellProgramInterpreters = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true,
}

// expandTilde substitutes the shell's own leading-tilde expansion. The shell,
// not the tool, resolves a bare `~` or `~/…` operand, so a gate that read the
// word literally would place it inside the workspace — and the read that
// actually happens, of a file under $HOME, would never be asked about. Only
// the plain form is expanded: `~someoneelse` is another user's home, whose
// value this layer cannot know.
func expandTilde(w string) string {
	if w != "~" && !strings.HasPrefix(w, "~/") {
		return w
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return w
	}
	if w == "~" {
		return home
	}
	return filepath.Join(home, w[2:])
}

// embeddedPaths pulls absolute-path-shaped runs out of an interpreter program.
// It splits on every character a path cannot hold, so `open("/x/y")` yields
// /x/y — and a regex or route string that happens to look like a path yields a
// false candidate, which is the accepted cost of seeing the real ones.
func embeddedPaths(prog string) []string {
	var out []string
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		if frag := prog[start:end]; strings.HasPrefix(frag, "/") || strings.HasPrefix(frag, "~/") {
			out = append(out, expandKnownVars(frag))
		}
		start = -1
	}
	for i, r := range prog {
		if pathRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(prog))
	return out
}

// pathRune reports whether r can appear inside a path.
func pathRune(r rune) bool {
	switch r {
	case '/', '~', '.', '_', '-', '+', '@', '%':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
