package judge

// Read-only command recognition: the fast path that keeps a provably read-only
// shell command out of the model reviewer.
//
// Why it exists. In auto mode every bash call is graded by the reviewer, which
// is a whole model round trip. Measured against one real session, bash calls
// took a 2.65s median end to end while the reviewer-free file tools took 0.03s,
// and ~98% of the bash calls in a session are distinct, so the verdict cache
// cannot amortize it: a session spent reading a repository pays that round trip
// on nearly every step. codex has the same shape and answers it the same way
// (codex-rs/shell-command/src/command_safety/is_safe_command.rs): a closed
// whitelist of read-only programs, a deliberately crippled command-sequence
// parser, and everything else left to the reviewer.
//
// The safety argument is the whitelist, not the parser. The parser's only job
// is to guarantee that what will run is exactly the argv vectors it returns: no
// redirection, no command substitution, no subshell, no background job, no
// variable that could expand into a different program. Given that guarantee, a
// command reads only if every argv in it names a whitelisted program used with
// arguments that cannot write or execute. So the parser is strict to the point
// of being useless on its own ($VAR, globs in command position, assignments and
// env/nohup/time prefixes are all rejected), and the whitelist is what makes
// the answer safe.
//
// # What this deliberately does not recognize
//
// These keep going to the reviewer, because a pattern cannot grade them as well
// as the model can, and being slow is better than being wrong:
//
//   - Anything that executes: awk, python/node/perl/ruby, tar, tee, xargs.
//   - Anything that writes: go build/test (the build cache), gofmt, cp, mv,
//     git add/commit/checkout/fetch/pull/push. A build or a test run is the
//     shape of command this most wants to cover — it is slow, so a review on
//     top hurts most — and it is exactly the shape a pattern must not wave
//     through, because it writes to the tree and runs arbitrary project code.
//   - A whitelisted program with a flag that runs or writes: rg --pre,
//     find -exec/-delete/-fprint*, sed beyond `-n Np`, sort/base64/tree -o,
//     git --output/--textconv/--ext-diff/--filters.
//   - Every shell construct the scanner cannot see through: '>' '<' '&' '!',
//     '$( )', backticks, braces, parentheses, backslash escapes, and variable
//     references.
//   - Assignments (`LD_PRELOAD=… ls` is code execution, not a read) and the
//     wrapper prefixes (`env`, `nohup`, `time`).
//   - A program in command position by path (`/bin/ls`, `./ls`): a path can be
//     shadowed inside the repository, a bare name cannot be without the
//     workspace already being on PATH.
//   - `printenv`, `env`, `git config --list`: they read, but what they read is
//     the environment, and the transcript goes to the model provider.
//
// Inert printers are recognized even though they read nothing — `echo` and
// `printf` touch no file and run nothing, and they dominate real command lines
// as separators (`echo "==="; ls`). Only their literal forms qualify: a `$`
// anywhere is rejected, so a printer cannot disclose a variable, and `printf
// -v` (which assigns) is refused by name.
//
// Credential material is not this layer's business and is not weakened by it:
// the static floor runs first and denies reads of ~/.ssh, ~/.zshrc and friends,
// and the read-scope gate has already asked about anything outside the
// workspace before the permission gate is reached. Both stay in front of the
// fast path, so a recognized read still cannot reach a file the user did not
// approve.

import (
	"encoding/json"
	"os"
	"strings"
)

// readOnlyCommand reports whether cmd is provably read-only: a sequence of
// commands joined by `&&`, `||`, `;` or `|`, each a whitelisted read-only
// program used with read-only arguments.
//
// A false answer means "not recognized", not "dangerous": the caller's behavior
// is unchanged for those, so the only cost of a miss is the review the command
// would have had anyway.
func readOnlyCommand(cmd string) bool {
	_, ok := readOnlyPlan(cmd)
	return ok
}

// readOnlySummary names a recognized read-only command for a transcript
// summary: the verb to lead with ("List", "Read", "Search", "Find"), or the
// program's own name for the rest of the whitelist, plus what it acts on.
// ok=false means the command is not recognized (or names nothing worth showing),
// and the caller should render it as its own card.
func readOnlySummary(cmd string) (verb, detail string, ok bool) {
	segs, recognized := readOnlyPlan(cmd)
	if !recognized {
		return "", "", false
	}
	return summarizePlan(segs)
}

// summarizePlan names the read a recognized plan performs. `cd /x && rg -n pat
// .` leads with the interesting half: a leading cd is the shell moving into
// position, not the read the user is watching for.
func summarizePlan(segs [][]string) (verb, detail string, ok bool) {
	// `cd /x && rg -n pat .` leads with the interesting half: a leading cd is
	// the shell moving into position, not the read the user is watching for.
	for i, argv := range segs {
		if i == 0 && argv[0] == "cd" {
			continue
		}
		return summarizeArgv(argv)
	}
	return "", "", false
}

// summarizeArgv renders one recognized segment as verb + detail.
func summarizeArgv(argv []string) (verb, detail string, ok bool) {
	name, args := argv[0], argv[1:]
	firstOperand := func() string {
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			return a
		}
		return ""
	}
	switch name {
	case "ls", "tree", "du":
		if p := firstOperand(); p != "" {
			return "List", p, true
		}
		return "List", ".", true
	case "cat", "head", "tail", "nl", "tac", "od", "strings", "wc", "file",
		"stat", "sort", "uniq", "cut", "tr", "diff", "cmp", "shasum",
		"sha1sum", "sha256sum", "md5", "cksum", "base64", "base32",
		"basename", "dirname", "realpath", "readlink":
		return "Read", firstOperand(), true
	case "grep", "egrep", "fgrep", "rg":
		// The pattern is the first non-flag word; the scope, if any, follows.
		pattern, scope, seen := "", "", false
		for _, a := range args {
			if !seen {
				if strings.HasPrefix(a, "-") {
					continue
				}
				pattern, seen = a, true
				continue
			}
			if !strings.HasPrefix(a, "-") {
				scope = a
				break
			}
		}
		if scope != "" {
			return "Search", pattern + " in " + scope, true
		}
		return "Search", pattern, true
	case "find", "fd":
		return "Find", firstOperand(), true
	}
	// The rest of the whitelist (git, cd, pwd, …) leads with its own name, which
	// is what the user would recognize at a glance.
	if len(args) == 0 {
		return name, "", true
	}
	shown := args
	if len(shown) > 4 {
		shown = shown[:4]
	}
	return name, strings.Join(shown, " "), true
}

// readOnlyPlan parses cmd and returns its recognized argv sequence. The plan is
// empty and ok=false for anything the scanner cannot prove.
func readOnlyPlan(cmd string) ([][]string, bool) {
	segs, ok := readOnlySegments(cmd)
	if !ok || len(segs) == 0 {
		return nil, false
	}
	for _, argv := range segs {
		if !readOnlyArgv(argv) {
			return nil, false
		}
	}
	return segs, true
}

// readOnlySegments splits a command line into the argv of each command in it, or
// reports false for every construct the scanner will not vouch for. It is the
// strict counterpart of splitShellSegments/shellWords (static.go), which exist
// to READ a command line and stay permissive; this one exists to RUN one, so it
// rejects rather than approximates.
//
// Rejected outright: backslash escapes, `$` in every form, backticks, `<`, `>`,
// `&` (background), parentheses, braces, `!`, and a glob metacharacter in
// command position (a glob there expands to a filename, which then decides what
// runs).
//
// One redirect form is accepted, because without it most real read commands
// would fall through to the reviewer for no safety gain: a redirect whose
// target is literally /dev/null (`2>/dev/null`, `>/dev/null`, `&>/dev/null`,
// `< /dev/null`), and a file-descriptor duplication (`2>&1`). Both discard or
// move a stream; neither opens a file for writing. Every other redirect — and
// any heredoc, whose body is parsed as commands — is rejected.
func readOnlySegments(cmd string) ([][]string, bool) {
	var (
		segs        [][]string
		argv        []string
		word        strings.Builder
		wordStarted bool
		quote       byte
	)
	flushWord := func() {
		if wordStarted {
			argv = append(argv, word.String())
			word.Reset()
			wordStarted = false
		}
	}
	flushSeg := func() {
		flushWord()
		if len(argv) > 0 {
			segs = append(segs, argv)
			argv = nil
		}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		// Redirects are recognized before the general switch: `&>` is a
		// redirect, while a bare `&` is a background job, and the fd digits of
		// `2>` are already sitting in the word buffer. Quoted text is not read
		// for redirects: a pattern like `'a > b'` is data the shell hands over
		// literally, so the redirect check must sit behind the quote handling
		// rather than in front of it.
		if quote == 0 && (c == '<' || c == '>' || (c == '&' && i+1 < len(cmd) && cmd[i+1] == '>')) {
			fd := word.String()
			if wordStarted && !allDigits(fd) {
				return nil, false // a word interrupted by a redirect
			}
			next, ok := consumeRedirect(cmd, i)
			if !ok {
				return nil, false
			}
			word.Reset()
			wordStarted = false
			i = next - 1
			continue
		}
		switch {
		case quote == '\'':
			// Single quotes are fully literal: nothing inside can execute.
			if c == '\'' {
				quote = 0
				continue
			}
			word.WriteByte(c)
			wordStarted = true
		case quote == '"':
			switch c {
			case '"':
				quote = 0
			case '$', '`':
				return nil, false
			case '\\':
				// Inside double quotes a backslash is special only before $, `,
				// ", \ and a newline; before anything else both characters are
				// literal. Resolving the escape is what lets a pattern like
				// `"a\|b"` — the shape real greps are written in — be read
				// instead of rejected, and it cannot smuggle an expansion: an
				// escaped $ or backtick is exactly the case that does NOT expand.
				if i+1 >= len(cmd) {
					return nil, false
				}
				switch next := cmd[i+1]; next {
				case '$', '`', '"', '\\':
					word.WriteByte(next)
					i++
				case '\n':
					return nil, false // line continuation joins two commands
				default:
					word.WriteByte('\\')
					word.WriteByte(next)
					i++
				}
				wordStarted = true
			default:
				word.WriteByte(c)
				wordStarted = true
			}
		case c == '\'' || c == '"':
			quote = c
			wordStarted = true
		case c == '\\' || c == '$' || c == '`' ||
			c == '(' || c == ')' || c == '{' || c == '}' || c == '!':
			return nil, false
		case c == ' ' || c == '\t':
			flushWord()
		case c == '\n' || c == ';':
			flushSeg()
		case c == '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				i++
				flushSeg()
				continue
			}
			return nil, false // background job
		case c == '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				i++
			}
			flushSeg()
		case c == '*' || c == '?':
			if len(argv) == 0 && !wordStarted {
				return nil, false // a glob that decides what runs
			}
			word.WriteByte(c)
			wordStarted = true
		default:
			word.WriteByte(c)
			wordStarted = true
		}
	}
	if quote != 0 {
		return nil, false // unterminated quote: the shell would keep reading
	}
	flushSeg()
	return segs, true
}

// consumeRedirect reads one redirect starting at index i (which points at `<`,
// `>`, or the `&` of `&>`), returning the index just past it. Only the two
// forms that cannot create a file are accepted: a target of /dev/null, and an
// fd duplication (`2>&1`). A heredoc is rejected because its body arrives as
// further shell input.
func consumeRedirect(cmd string, i int) (int, bool) {
	op := cmd[i]
	j := i + 1
	if op == '&' {
		// `&>file` / `&>>file`: both streams, same target rules as `>`.
		op, j = '>', i+1
		if j < len(cmd) && cmd[j] == '>' {
			j++
		}
	} else if j < len(cmd) && cmd[j] == op {
		if op == '<' {
			return 0, false // heredoc (or here-string): the body is more input
		}
		j++ // >>
	}
	dup := false
	if j < len(cmd) && cmd[j] == '&' {
		dup, j = true, j+1
	}
	for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	target, next, ok := readRedirectTarget(cmd, j)
	if !ok {
		return 0, false
	}
	if dup {
		// `2>&1`, `>&2`: a descriptor duplication moves a stream, it does not
		// open anything.
		if !allDigits(target) {
			return 0, false
		}
		return next, true
	}
	if target != "/dev/null" {
		return 0, false
	}
	return next, true
}

// readRedirectTarget reads the word a redirect points at, starting at index i.
// Quotes are honored and stripped; a target holding an expansion or a glob is
// rejected, since what it names is not knowable here.
func readRedirectTarget(cmd string, i int) (string, int, bool) {
	if i >= len(cmd) {
		return "", 0, false
	}
	var (
		b     strings.Builder
		quote byte
	)
	for ; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			if c == '\\' || c == '$' || c == '`' {
				return "", 0, false
			}
			b.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '|' || c == '&':
			return b.String(), i, b.Len() > 0
		case c == '\\' || c == '$' || c == '`' || c == '*' || c == '?' || c == '[':
			return "", 0, false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), i, b.Len() > 0
}

// allDigits reports whether s is a non-empty run of digits (an fd number).
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// readOnlyArgv reports whether one command is a whitelisted program with
// read-only arguments.
func readOnlyArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	name := argv[0]
	// Command position must be a bare, literal program name: a path can be
	// shadowed by the repository, and a name holding a path separator, a glob or
	// a quote is not the program it looks like.
	if name == "" || strings.ContainsAny(name, "/=\"'*?[") {
		return false
	}
	check, ok := readOnlyPrograms[name]
	if !ok {
		return false
	}
	return check(argv[1:])
}

// readOnlyPrograms is the whitelist: a program reads only if it is here and its
// argument check passes. It is closed on purpose, and every entry is a program
// that does not write or execute; the entries with a check are the ones that can
// do both through a flag.
var readOnlyPrograms = map[string]func([]string) bool{
	// Listing and inspection.
	"ls": readsAlways, "pwd": readsAlways, "tree": rejectsFlags("-o", "--output"),
	"du": readsAlways, "stat": readsAlways,
	"basename": readsAlways, "dirname": readsAlways, "realpath": readsAlways,
	"readlink": readsAlways, "which": readsAlways, "whoami": readsAlways,
	"id": readsAlways, "uname": readsAlways, "true": readsAlways,
	"false": readsAlways, "sw_vers": readsAlways,
	// cd writes nothing; it moves the shell. The argument is required and must
	// not be `-` (OLDPWD, whose value is not knowable here). Without cd in the
	// whitelist the dominant shape of a real read — `cd <dir> && <read>` — would
	// still be reviewed, which is the cost this file exists to remove.
	"cd": cdArgOnly,
	// Inert printers: no file is touched and nothing runs. The scanner has
	// already refused every expansion, so only literal text gets here.
	"echo": readsAlways, "printf": printfArgs,
	// date and hostname both take a bare operand that CHANGES the host: BSD
	// `date MMDDhhmm` sets the clock with no flag at all, and `hostname NAME`
	// renames it. Only the query forms are reads, so a date operand must be a
	// `+FORMAT` string and a hostname operand is not allowed.
	"date": dateArgs, "hostname": rejectsNonFlags,

	// Reading.
	"cat": readsAlways, "tac": readsAlways, "head": readsAlways, "tail": readsAlways,
	"nl": readsAlways, "od": readsAlways, "strings": readsAlways, "wc": readsAlways,
	// sort's --compress-program runs a program on the data; diff's --paginate
	// pipes the result through `pr`; file's -C compiles a magic file in place.
	// sort's -T/--temporary-directory picks where its temp files go, which is a
	// write path into a directory the caller names.
	"sort": rejectsFlags("-o", "--output", "--compress-program",
		"-T", "--temporary-directory"),
	// uniq takes [INPUT [OUTPUT]]: the second operand is a file it WRITES, so at
	// most one operand may be present.
	"uniq": atMostOneOperand,
	"cut":  readsAlways, "tr": readsAlways, "cmp": readsAlways,
	"diff":   rejectsFlags("-l", "--paginate"),
	"file":   rejectsFlags("-C", "--compile"),
	"shasum": readsAlways, "sha1sum": readsAlways, "sha256sum": readsAlways,
	"md5": readsAlways, "cksum": readsAlways, "base32": rejectsFlags("-o", "--output"),
	"base64": rejectsFlags("-o", "--output"),

	// Searching. rg's --pre and --hostname-bin run a command per file (or to
	// find the hostname) and -z shells out to a decompressor; find's -exec/-ok
	// run commands and -delete/-fls/-fprint* write; fd's -x/-X are find's -exec.
	"grep": readsAlways, "egrep": readsAlways, "fgrep": readsAlways,
	"rg": rejectsFlags("--pre", "--hostname-bin", "-z", "--search-zip"),
	"fd": rejectsFlags("-x", "-X", "--exec", "--exec-batch"),
	"find": rejectsFlags("-exec", "-execdir", "-ok", "-okdir", "-delete",
		"-fls", "-fprint", "-fprint0", "-fprintf"),

	// A reader with an interpreter attached: sed runs `e` (a command), `w` (a
	// write) and `-i` as a script, so only the print form is recognized —
	// exactly the form codex recognizes.
	"sed": sedPrintOnly,

	// Read-only git; the subcommand and its flags are checked below.
	"git": gitReadOnly,
}

// readsAlways is the check for a program with no write or execute flag.
func readsAlways([]string) bool { return true }

// cdArgOnly accepts `cd <dir>`: exactly one argument, not a flag and not `-`.
// A bare `cd` would move to $HOME, which the read-scope scanner cannot see as a
// path operand.
func cdArgOnly(args []string) bool {
	return len(args) == 1 && args[0] != "" && !strings.HasPrefix(args[0], "-")
}

// atMostOneOperand accepts an invocation with zero or one non-flag argument.
// It is the check for a program that reads one file and WRITES a second when
// given two, like `uniq [INPUT [OUTPUT]]`.
func atMostOneOperand(args []string) bool {
	operands := 0
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		operands++
		if operands > 1 {
			return false
		}
	}
	return true
}

// dateArgs accepts the query forms of date. Both spellings of a clock change
// are refused: GNU's -s/--set, and BSD's bare operand (`date 010112002026` sets
// the clock with no flag at all). A query may still carry a `+FORMAT` operand
// and the flags that select what is shown (-u, -r, -j).
func dateArgs(args []string) bool {
	// rejectsFlags returns true when the invocation is ACCEPTABLE, so the
	// negation is what refuses a clock-setting form.
	if !rejectsFlags("-s", "--set")(args) {
		return false
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			continue
		}
		return false // a bare operand is a clock setting, not a query
	}
	return true
}

// printfArgs accepts the printing forms of printf. Two of its own features are
// refused: -v, which assigns to a named variable, and the %n directive, which
// is the same assignment written inside the format — bash's printf writes the
// character count into the variable `%n` names (`printf '%n' PATH` sets PATH),
// so a format carrying an unescaped %n is not a read-only call. The check is on
// the format string, and it deliberately does not try to parse printf's format
// language: an argument reached through %s/%b cannot assign, so only the format
// itself is examined, and a `%%n` (an escaped percent) is inert.
func printfArgs(args []string) bool {
	if !rejectsFlags("-v")(args) {
		return false
	}
	format := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			format = a
			break
		}
	}
	return !formatAssigns(format)
}

// formatAssigns reports whether a printf format can write to a variable: an
// unescaped %n. %% is a literal percent and does not introduce a directive, so
// it never counts.
func formatAssigns(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		j := i + 1
		if j < len(format) && format[j] == '%' {
			i = j // %%: the next character is literal text
			continue
		}
		// Skip flags, width and precision to find the conversion character.
		for j < len(format) && strings.ContainsRune("-+ #0123456789.*", rune(format[j])) {
			j++
		}
		if j < len(format) && format[j] == 'n' {
			return true
		}
	}
	return false
}

// rejectsNonFlags rejects any non-flag argument, the shape a command takes when
// it sets something rather than asks.
func rejectsNonFlags(args []string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return true
}

// rejectsFlags returns a check that rejects an invocation carrying any of the
// given flags, in the spellings a shell can produce: `-o`/`-ofile` for a short
// flag, `--output`/`--output=file` for a long one.
func rejectsFlags(flags ...string) func([]string) bool {
	return func(args []string) bool {
		for _, a := range args {
			for _, f := range flags {
				if flagMatches(a, f) {
					return false
				}
			}
		}
		return true
	}
}

// sedPrintOnly accepts only `sed -n <Np|M,Np> [file]`: the print form. Every
// other sed invocation is a script that can write (`w`, `-i`) or execute (`e`),
// and a pattern cannot tell those apart from a substitution.
func sedPrintOnly(args []string) bool {
	if len(args) < 2 || len(args) > 3 || args[0] != "-n" {
		return false
	}
	core, ok := strings.CutSuffix(args[1], "p")
	if !ok || core == "" {
		return false
	}
	for _, part := range strings.Split(core, ",") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return len(args) != 3 || !strings.HasPrefix(args[2], "-")
}

// gitUnsafeGlobals are the global options that change what git runs or reads:
// -c and --config-env inject configuration (core.pager, core.fsmonitor,
// uploadpack…), -C/--git-dir/--work-tree/--namespace/--super-prefix point the
// command at another tree, --exec-path and --paginate run something else.
var gitUnsafeGlobals = []string{
	"-C", "-c", "-p", "--config-env", "--exec-path", "--git-dir", "--namespace",
	"--paginate", "--super-prefix", "--work-tree",
}

// gitUnsafeSubcommandArgs are the per-subcommand flags that write or execute:
// --output redirects to a file, --ext-diff/--textconv/--filters/--exec run
// helpers declared in configuration.
var gitUnsafeSubcommandArgs = []string{
	"--output", "--ext-diff", "--textconv", "--filters", "--exec",
	// --no-index drops the repository entirely and diffs two arbitrary paths, so
	// it reads files the read-scope scanner never sees (git's operands are
	// pathspecs, checked by its own rules above).
	"--no-index",
}

// gitReadOnly reports whether a git invocation only reads.
func gitReadOnly(args []string) bool {
	// The subcommand is git's first non-flag word; anything before it is a
	// global option.
	idx := -1
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		idx = i
		break
	}
	if idx < 0 {
		return false
	}
	for _, a := range args[:idx] {
		for _, bad := range gitUnsafeGlobals {
			if flagMatches(a, bad) {
				return false
			}
		}
	}
	sub, rest := args[idx], args[idx+1:]
	switch sub {
	case "status", "log", "diff", "show":
		return !carriesAny(rest, gitUnsafeSubcommandArgs)
	case "branch":
		return gitBranchReadOnly(rest)
	case "rev-parse", "ls-files", "describe", "blame", "shortlog", "cat-file",
		"show-ref", "for-each-ref", "rev-list", "merge-base", "name-rev",
		"symbolic-ref", "diff-tree", "diff-index", "diff-files", "whatchanged",
		"count-objects", "check-ignore", "check-attr", "check-ref-format", "var":
		return !carriesAny(rest, gitUnsafeSubcommandArgs)
	case "stash":
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show")
	case "reflog":
		// `git reflog` prints; `git reflog expire|delete` rewrites history.
		return len(rest) == 0 || rest[0] == "show"
	case "remote":
		return len(rest) == 1 && rest[0] == "-v"
	case "worktree":
		return len(rest) == 1 && rest[0] == "list"
	}
	return false
}

// gitBranchReadOnly accepts only the listing forms: no arguments at all, or
// flags that ask for a list. Anything else can create, rename or delete.
func gitBranchReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}
	sawReadOnly := false
	for _, a := range args {
		switch {
		case a == "--list" || a == "-l" || a == "--show-current" || a == "-a" ||
			a == "--all" || a == "-r" || a == "--remotes" || a == "-v" ||
			a == "-vv" || a == "--verbose":
			sawReadOnly = true
		case strings.HasPrefix(a, "--format="):
			sawReadOnly = true
		default:
			return false
		}
	}
	return sawReadOnly
}

// carriesAny reports whether any argument names one of the given flags.
func carriesAny(args, flags []string) bool {
	for _, a := range args {
		for _, f := range flags {
			if flagMatches(a, f) {
				return true
			}
		}
	}
	return false
}

// flagMatches reports whether arg is flag or a value-carrying spelling of it:
// `--output=x` for a long flag, `-cfoo` for a short one.
func flagMatches(arg, flag string) bool {
	if arg == flag {
		return true
	}
	if !strings.HasPrefix(arg, flag) {
		return false
	}
	if strings.HasPrefix(flag, "--") {
		return strings.HasPrefix(arg[len(flag):], "=")
	}
	return len(arg) > len(flag)
}

// readonlyFastPathEnabled reads the kill switch for the fast path: an operator
// who suspects the whitelist can send every bash call back to the reviewer
// without a rebuild. Unset means enabled.
func readonlyFastPathEnabled(getenv func(string) string) bool {
	if getenv == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv("GOLDER_READONLY_FASTPATH"))) {
	case "off", "0", "false", "no", "disabled":
		return false
	}
	return true
}

// readOnlyFastPathAllow is the verdict the fast path produces. It carries a
// distinct Source so a note, a log line or a test can tell it from a reviewer's
// allow — the two run the same call but one of them skipped a model round trip.
func readOnlyFastPathAllow() Verdict {
	return Verdict{
		Level:   Allow,
		Source:  "readonly",
		Reasons: []string{"provably read-only command"},
	}
}

// fastPathEnabled reports whether the fast path is on in this process.
func fastPathEnabled() bool { return readonlyFastPathEnabled(os.Getenv) }

// ReadOnlyShellCommand reports whether this shell command is one the fast path
// recognizes — the same predicate the permission gate uses to skip the reviewer,
// exposed so the execution layer can contain what it allows. It carries the
// kill switch and the static floor with it, so a caller cannot forget either.
func ReadOnlyShellCommand(command string) bool {
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return false
	}
	_, ok := readOnlyFastPath("bash", raw)
	return ok
}

// ReadOnlyShellSummary answers a display caller's whole question about one bash
// call: how to summarize it in a transcript, and whether it is a fast-path read
// at all. It is deliberately one call taking the CALL's arguments rather than a
// command string, because the answer is the gate's answer: an escalation request
// (or the kill switch, or the static floor) makes a command non-fast-path, and a
// renderer that folded such a call would hide one the reviewer in fact judged.
func ReadOnlyShellSummary(args json.RawMessage) (verb, detail string, ok bool) {
	if _, ok := readOnlyFastPath("bash", args); !ok {
		return "", "", false
	}
	cmd, ok := bashCommand(args)
	if !ok {
		return "", "", false
	}
	segs, ok := readOnlyPlan(cmd)
	if !ok {
		return "", "", false
	}
	return summarizePlan(segs)
}

// readOnlyFastPath returns the verdict that skips the reviewer for a provably
// read-only bash command, and whether it applies. It is the whole decision in
// one place, so the gate and the bash tool cannot disagree about which commands
// are exempt.
//
// The static floor is consulted HERE, not merely ahead of this call by
// convention: `cat ~/.zshrc` has the shape of a read and is recognized as one,
// so a call site that forgot to run the floor first would let the recognizer
// hand over credential material on its own. Checking it inside makes that
// impossible by construction rather than by every caller remembering the order.
//
// An unrecognized command returns false and keeps the caller's normal path (the
// reviewer), which is why a miss costs nothing but the review it would have had.
func readOnlyFastPath(tool string, args json.RawMessage) (Verdict, bool) {
	if !fastPathEnabled() {
		return Verdict{}, false
	}
	if _, denied := staticDeny(tool, args); denied {
		return Verdict{}, false
	}
	// A require_escalated request is about CONTAINMENT ("run me outside the
	// sandbox"), which the fast path must not decide: the fast path exists to
	// skip the risk review, and the escalation contract is that the reviewer (or
	// the user) weighs the request. Letting a read-only command take the fast
	// path here would silently ignore the request, and the model would retry it
	// forever — the loop the escalation path is built to avoid.
	if escalationRequested(args) {
		return Verdict{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(tool), "bash") {
		return Verdict{}, false
	}
	cmd, ok := bashCommand(args)
	if !ok || !readOnlyCommand(cmd) {
		return Verdict{}, false
	}
	return readOnlyFastPathAllow(), true
}
