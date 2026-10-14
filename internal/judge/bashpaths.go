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
// # How it is read
//
// One reader, two halves, and neither guesses at the other's job:
//
//   - The SHELL PARSE (internal/shellfacts, which is mvdan) answers everything
//     lexical: where a word begins and ends, what quoting did to it, which of
//     its parts is a variable, which redirection writes where, and what runs
//     inside a substitution, a loop or a function. This file does not do shell
//     syntax; it reads the parse.
//   - THIS FILE answers everything the grammar cannot: which programs open a
//     file they are handed (so `echo /etc/hosts` is quiet and `cat /etc/hosts`
//     is not), which argument positions hold a pattern or a script rather than
//     a file, how a word's parts become a value (`$HOME` and a leading `~` are
//     knowable, a glob is a location with a pattern, anything else is not), and
//     what an interpreter's inline program means.
//
// It used to be two readers with a union: the scanner walked the text with its
// own tokenizer while the parse reported path-shaped words, and whichever was
// less careful won. A sed script arrived as a path because the parse calls it an
// argument, and `/60` arrived because the scanner read a program the parse had
// already identified as one word. Those were not missing cases; they were the
// shape of two disagreeing readers.
//
// It is still best-effort by construction. `cat $UNKNOWN`, a path assembled at
// runtime, and a program that computes its own filename are invisible here. The
// sandbox is the enforcement that does not depend on reading the command; this
// is the part that can ask before the command runs.

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/getan/golder/internal/shellfacts"
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
	// sed's and awk's -f names a script FILE, which is a real read — and, with
	// it declared, the pattern-position rule below knows the script is not
	// among the operands, so `sed -f /outside/p.sed f.go` is asked about
	// instead of being skipped as "the script".
	"sed": {"-f", "--file"}, "awk": {"-f", "--file"}, "gawk": {"-f", "--file"},
	// date's -f is a dates FILE on GNU and a format string on BSD, and -r names
	// a file whose mtime is reported. Declaring both is the fail-safe reading:
	// a BSD format string is not path-shaped and adds no candidate, while a
	// GNU path does — and a path is a read either way.
	"date": {"-f", "--file", "-r", "--reference"},
}

// inlinePatternFlags maps a pattern command to the flags that carry the pattern
// or script inside the command line. Their values are never files, and there can
// be several (`sed -e '/a/p' -e '/b/p' f`, `grep -e '/usr/local' -e '/etc' f`).
var inlinePatternFlags = map[string][]string{
	"grep": {"-e", "--regexp"}, "egrep": {"-e", "--regexp"}, "fgrep": {"-e", "--regexp"},
	"rg": {"-e", "--regexp"}, "ag": {"-e"}, "ack": {"-e"},
	"sed": {"-e", "--expression"},
	"awk": {"-e", "--source"}, "gawk": {"-e", "--source"},
}

// nonPathFlagValues maps a command to the flags whose separate argument is
// neither a file nor the pattern: awk's -v assignment and -F separator. They
// have to be stepped over when looking for the script, or the script itself
// keeps looking like a path — `awk -F: '/x/{print}' f` would report `/x/{print}`.
var nonPathFlagValues = map[string][]string{
	"awk": {"-v", "-F"}, "gawk": {"-v", "-F"},
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
func fileFlagValueIndices(cmdName string, texts []string, from int) map[int]bool {
	out := map[int]bool{}
	for j := from; j+1 < len(texts); j++ {
		if isFileFlag(cmdName, texts[j]) {
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
func patternFromFile(cmdName string, texts []string, from int) bool {
	for j := from; j < len(texts); j++ {
		if isFileFlag(cmdName, texts[j]) {
			return true
		}
		if _, ok := fileFlagValue(cmdName, texts[j]); ok {
			return true
		}
	}
	return false
}

// nonPathOperandIndices returns the indices of the words that are NOT files even
// when they are path-shaped: the pattern of a search command, the script of sed
// or awk, and the value of a flag that carries a script or a setting rather than
// a file.
//
// This is the knowledge that a path-SHAPED word is not necessarily a path.
// `/func X/,/^}/p` is a sed address range and `/usr/local` is a search string,
// and both read exactly like an absolute path to anything that only looks at the
// text. Two readers need this rule and must not drift: the scanner below, which
// walks its own words, and commandPaths (extract.go), which narrows the facts
// reader's list of path-shaped ARGUMENTS with it — shellfacts reports those as
// facts and says so ("the caller must narrow it with knowledge this package does
// not have", see Access.Op), and this is that narrowing.
func nonPathOperandIndices(cmdName string, texts []string, from int) map[int]bool {
	if !patternCommands[cmdName] {
		return nil
	}
	out := map[int]bool{}
	// A flag can carry the pattern or script inline, and there may be several of
	// them: every one is a non-path operand. A flag merely HOLDING a setting
	// (`awk -v`, `awk -F`) is stepped over the same way but does not mean the
	// script is inline — awk still takes one as its first bare operand.
	sawInline := false
	for j := from; j+1 < len(texts); j++ {
		switch {
		case carriesInlinePattern(cmdName, texts[j]):
			out[j+1] = true
			sawInline = true
		case isNonPathFlagValue(cmdName, texts[j]):
			out[j+1] = true
		}
	}
	// The bare operand is a pattern too, but only when the command has no inline
	// pattern of its own and does not read one from a file: `sed -e '/x/p' f`
	// has no positional script, so `f` is a file rather than the rule.
	if !sawInline && !patternFromFile(cmdName, texts, from) {
		fileValues := fileFlagValueIndices(cmdName, texts, from)
		for j := from; j < len(texts); j++ {
			// An empty word (a bare '' argument) is not an operand either: it is
			// how a flag that takes an optional value is spelled with none.
			if strings.HasPrefix(texts[j], "-") || texts[j] == "" || fileValues[j] || out[j] {
				continue
			}
			out[j] = true
			break
		}
	}
	return out
}

// carriesInlinePattern reports whether one word is a flag this command reads its
// pattern or script from, in the separated or attached spelling.
func carriesInlinePattern(cmdName, word string) bool {
	for _, f := range inlinePatternFlags[cmdName] {
		if word == f || strings.HasPrefix(word, f) && len(word) > len(f) &&
			(strings.HasPrefix(f, "--") && word[len(f)] == '=' || !strings.HasPrefix(f, "--")) {
			return true
		}
	}
	return false
}

// isNonPathFlagValue reports whether one word is a flag whose separate argument
// is a setting rather than a file (`awk -v`, `awk -F`).
func isNonPathFlagValue(cmdName, word string) bool {
	for _, f := range nonPathFlagValues[cmdName] {
		if word == f {
			return true
		}
	}
	return false
}

// commandCandidates returns the paths one parsed command may read: the targets
// of its redirections, the file operands of its program, and the programs of any
// interpreter it runs inline.
//
// This function is the whole of the split this file documents: the PARSER
// supplies everything lexical — words with quotes removed, which part of a word
// is a variable, which redirection writes — and this function supplies
// everything semantic: which programs open a file they are handed, which
// argument positions hold a pattern or a script instead, and how a word's parts
// become a value. Neither half guesses at the other's job.
func commandCandidates(cmd shellfacts.Command, depth int) []string {
	var out []string

	// Redirect targets name files by construction, whatever the program is: the
	// shell itself opens them. (`2>&1` names no path, and a heredoc's body is
	// further shell input rather than a target.)
	for _, r := range cmd.Redirs {
		if r.Dup || r.Heredoc {
			continue
		}
		if v, ok := wordValue(r.Target); ok && pathShapedWord(v) {
			out = append(out, v)
		}
	}

	i, ok := unwrapProgram(cmd.Argv)
	if !ok {
		return out
	}
	prog := path.Base(cmd.Argv[i].Text)
	if !fileOperandCommands[prog] {
		// The program does not open a path it is handed — it prints it, splits
		// it, or never sees one — so its arguments are nobody's boundary
		// question. This is the rule that keeps `echo /etc/hosts` quiet, and it
		// is why the operand tables live in this layer rather than in the
		// parser: "does this program open its argument" is knowledge about the
		// PROGRAM, not about the shell.
		return out
	}
	args := cmd.Argv[i+1:]
	texts := make([]string, len(args))
	for j, w := range args {
		texts[j] = w.Text
	}
	// A search command's pattern and a script are path-SHAPED often enough to
	// matter and are never files (see nonPathOperandIndices).
	skip := nonPathOperandIndices(prog, texts, 0)
	progWord := interpreterProgramIndex(prog, texts)
	for j, w := range args {
		switch {
		case skip[j]:
			continue
		case j == progWord:
			out = append(out, programPaths(prog, w.Text, depth)...)
		default:
			// The attached spelling of a file-reading flag carries its path
			// inside the word (`--file=/x`), where the flag test would hide it.
			if v, ok := fileFlagValue(prog, w.Text); ok {
				v = expandTilde(expandKnownVars(v))
				if pathShapedWord(v) {
					out = append(out, v)
				}
				continue
			}
			if v, ok := wordValue(w); ok && pathShapedWord(v) {
				out = append(out, v)
			}
			// `cat $(echo /outside/x)` reads /outside/x: the substitution's
			// OUTPUT is the operand, and only a printer's words can be read back
			// as a path.
			out = append(out, substitutionOperands(w)...)
		}
	}
	return out
}

// unwrapProgram steps over the programs that only decorate the command they run
// — `env FOO=1`, `nohup`, `time`, `command`, `sudo`, `doas` — and returns the
// index of the real program word. ok=false when there is none, which is what a
// line of nothing but assignments looks like.
//
// The parser has already separated leading assignments (cmd.Assigns) from the
// argument vector, so only wrappers appearing as PROGRAMS are stepped over; each
// one's flags and assignments are skipped by the same isTransparentPrefix rule
// the rest of the package uses.
func unwrapProgram(argv []shellfacts.Word) (int, bool) {
	i := 0
	for i < len(argv) {
		text := argv[i].Text
		if !isTransparentPrefix(text) && text != "sudo" && text != "doas" {
			return i, true
		}
		i++
		for i < len(argv) && isTransparentPrefix(argv[i].Text) {
			i++
		}
	}
	return 0, false
}

// interpreterProgramIndex returns the index, within prog's arguments, of the
// word holding an inline program, or -1 when the program has no such flag or
// none is present.
func interpreterProgramIndex(prog string, texts []string) int {
	letter, ok := interpreterProgramFlags[prog]
	if !ok {
		return -1
	}
	for j := 0; j+1 < len(texts); j++ {
		if flagCarriesProgram(texts[j], letter) {
			return j + 1
		}
	}
	return -1
}

// substitutionOperands returns the paths a command substitution hands to the
// command it feeds.
//
// `cat $(echo /outside/x)` reads /outside/x, and neither half of the line says
// so on its own: the substitution's OUTPUT is the operand. Only a printer passes
// its words through unchanged (`echo`, `printf`), so only those are read back —
// anything else produces a value this layer cannot know, which is the same limit
// that applies to an unknown variable.
func substitutionOperands(w shellfacts.Word) []string {
	var out []string
	for _, p := range w.Parts {
		if p.Kind != shellfacts.CommandSubstPart || p.Nested == nil {
			continue
		}
		for _, c := range p.Nested.AllCommands() {
			idx, ok := unwrapProgram(c.Argv)
			if !ok {
				continue
			}
			switch path.Base(c.Argv[idx].Text) {
			case "echo", "printf":
			default:
				continue
			}
			for _, a := range c.Argv[idx+1:] {
				if v, ok := wordValue(a); ok && pathShapedWord(v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// wordValue renders one word the way the shell would hand it to the program, or
// reports that the value cannot be known here.
//
// It is where the parser's structure becomes a spelling, and it replaces the
// two approximations the old scanner used (a leadquoted flag and a textual
// $HOME substitution):
//
//   - a leading unquoted `~` is a TildePart, so `~/x` and `~/"x"` expand while
//     `"~/x"` stays literal — the shell's own rule, read from the parse rather
//     than guessed from the first byte;
//   - `$HOME` is a VariablePart with a known name, so it expands; any OTHER
//     variable, and any substitution or arithmetic, makes the value unknowable
//     and the word is skipped. Skipping is the honest answer: the old scanner
//     passed `$UNKNOWN/x` through as a "path" that then resolved against the
//     workspace and was silently dropped further down.
//
// A glob is not unknowable in that sense: the literal part still carries the
// pattern, and `cat ~/logs/*` names a location even though the shell decides
// which files live there.
func wordValue(w shellfacts.Word) (string, bool) {
	if !w.Dynamic {
		return w.Text, true
	}
	home, _ := os.UserHomeDir()
	var b strings.Builder
	for _, p := range w.Parts {
		switch p.Kind {
		case shellfacts.LiteralPart, shellfacts.QuotedPart:
			b.WriteString(p.Text)
		case shellfacts.GlobPart, shellfacts.ExtGlobPart:
			// The pattern's text already came through the literal part above.
		case shellfacts.TildePart:
			rest := strings.TrimPrefix(p.Text, "~")
			if home == "" || (rest != "" && !strings.HasPrefix(rest, "/")) {
				// `~other` is another user's home: the shell expands it, and this
				// layer cannot know to what.
				return "", false
			}
			b.WriteString(home + rest)
		case shellfacts.VariablePart:
			if p.Text != "HOME" || home == "" {
				return "", false
			}
			b.WriteString(home)
		default: // command substitution, process substitution, arithmetic
			return "", false
		}
	}
	return b.String(), true
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

// maxProgramDepth bounds the recursion into shell one-liners. `sh -c "bash -c
// '…'"` is two levels and appears in practice; past the third the text is data
// the reviewer reads rather than a boundary question.
const maxProgramDepth = 3

// programPaths returns the paths an interpreter's inline program names.
//
// Two kinds of program, answered differently on purpose:
//
//   - A SHELL program (`bash -c '…'`) IS a shell command line, so it is parsed
//     as one, recursively. The answer is exact — quoting, substitutions and
//     redirections are read the way the outer command's were, because the same
//     parser reads both. `sh -c 'cat /outside/x'` is seen without any lexical
//     guessing, and `sh -c 'echo a / b'` produces nothing.
//
//   - Any other language is one this package does not parse and never will: a
//     Python program is not a shell command, and `/60` in `(n*med)/60` is
//     arithmetic. So the text is scanned for path-shaped runs, and a run counts
//     ONLY IF IT EXISTS on disk.
//
// The existence test is the point, not a heuristic layered on top of one. This
// gate asks a READ question — bash's paths are read-scoped, see the tool table
// in tools.go — and a path that does not exist cannot be read, so existence is
// precisely the property that separates a path from a string that resembles
// one. It is a VERIFIABLE predicate rather than a longer list of shapes:
// `(n*med)/60` yields the run `/60`, `/60` does not exist, so it is not a path;
// and no arithmetic, however unusual, can produce a false candidate, because a
// candidate has to name something real.
//
// The hole is the mirror image, and it is design rather than oversight: a
// program that CREATES a path (`open("/new", "w")`) names a file that does not
// exist yet and is not reported. Writes by a shell command are not this gate's
// business — the sandbox and the escalation dialog are their mechanism — which
// is the same reason bash's own entry in the tool table asks about reads.
func programPaths(interpreter, prog string, depth int) []string {
	if shellProgramInterpreters[interpreter] {
		if depth >= maxProgramDepth {
			return nil
		}
		return commandPathsAt(prog, depth+1)
	}
	var out []string
	for _, frag := range pathShapedRuns(prog) {
		// No tilde expansion here: `open("~/x")` in Python opens a directory
		// literally named `~`, and the stat below refuses it on its own. Refusing
		// is right — nothing expands a tilde in that language.
		if _, err := os.Stat(frag); err == nil {
			out = append(out, frag)
		}
	}
	return out
}

// pathShapedRuns pulls absolute-path-shaped runs out of an interpreter program,
// keeping the ones that appear inside a quoted string.
//
// Two conditions, both of them structural rather than a list of shapes:
//
//   - The run must sit inside a quote. In every language this scans a path is a
//     string literal, and the same characters outside one are arithmetic
//     (`(n*med)/60` divides; `"/x/y"` names a file). Quoting is the one
//     convention every language here shares.
//   - programPaths then keeps only runs that EXIST, because this gate asks a
//     read question and a path that does not exist cannot be read.
//
// It is a RUN extractor, not an answer: its output is a candidate list. The name
// says so deliberately — a function called `embeddedPaths` invited callers to
// treat what it returned as paths, which is how a consent dialog came to be
// raised for `/60`.
func pathShapedRuns(prog string) []string {
	var out []string
	start := -1
	startQuoted := false
	var quote rune
	flush := func(end int) {
		if start < 0 {
			return
		}
		if frag := prog[start:end]; startQuoted && (strings.HasPrefix(frag, "/") || strings.HasPrefix(frag, "~/")) {
			out = append(out, expandKnownVars(frag))
		}
		start = -1
	}
	for i, r := range prog {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'' || r == '`':
			quote = r
		}
		if pathRune(r) {
			if start < 0 {
				start = i
				startQuoted = quote != 0
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
