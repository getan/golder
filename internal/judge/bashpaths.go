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
	"strings"
	"unicode"
)

// fileOperandCommands are the commands whose arguments name files to open.
// Printers are deliberately absent: `echo /etc/hosts` renders a string, it does
// not read a file.
var fileOperandCommands = map[string]bool{
	// readers
	"cat": true, "tac": true, "head": true, "tail": true, "less": true,
	"more": true, "bat": true, "nl": true, "od": true, "xxd": true,
	"strings": true, "base64": true, "file": true, "stat": true,
	// searchers
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"ack": true, "find": true, "fd": true, "ls": true, "tree": true,
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

// interpreterProgramFlags maps an interpreter to the flag whose argument is an
// inline program. A path inside that program is a real read, so the program
// string is scanned in addition to the argument list. A shell's `-c` is here
// for the same reason: `bash -c 'cat /outside/x'` reads /outside/x.
var interpreterProgramFlags = map[string]byte{
	"python": 'c', "python3": 'c', "node": 'e', "nodejs": 'e',
	"perl": 'e', "ruby": 'e', "php": 'r',
	"bash": 'c', "sh": 'c', "zsh": 'c', "dash": 'c',
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
		// A pattern command's first non-flag argument is a pattern, not a path:
		// `rg '/usr/local' internal` searches for that text.
		skip := -1
		if patternCommands[cmdName] {
			for j := i + 1; j < len(words); j++ {
				if strings.HasPrefix(words[j].text, "-") {
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
				out = append(out, embeddedPaths(w.text)...)
			default:
				if text := expandKnownVars(w.text); pathShapedWord(text) {
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
// answer on every build script.
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
