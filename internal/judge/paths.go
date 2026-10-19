package judge

// Which path a sandbox question is about: the directory that made the sandbox
// refuse — an escape request's target, or the path a contained command just
// failed on — so a prompt can offer to re-admit just that path instead of
// unisolating the whole command.
//
// This lives in judge (not in one of the dialogs) because every front-end asks
// the same question from it: the TUI builds its "allow writes to <path>" rows
// from it, the REPL's stdin prompt offers the same grant on `w`/`s`, and the
// bash tool uses it to decide whether a denial has anything to offer at all. A
// second copy of the parsing would let the rows disagree about what the user is
// granting.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/shellfacts"
)

// SandboxDenialMarkers are the failure signatures that indicate the OS sandbox
// refused an operation, as opposed to the command itself failing. They mirror
// codex's detection list (sandboxing/src/denial.rs). One list answers both
// questions about a denial — whether it happened (LooksSandboxDenied) and which
// path it named (PathsFromDenial) — so the two can never disagree.
var SandboxDenialMarkers = []string{
	"operation not permitted",
	"permission denied",
	"read-only file system",
	"sandbox",
	"landlock",
	"seccomp",
}

// LooksSandboxDenied reports whether a sandboxed command's output looks like a
// sandbox denial rather than an ordinary command failure. The caller only
// consults this for failed sandboxed runs.
func LooksSandboxDenied(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range SandboxDenialMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// PathsFromDenial returns the first path a sandbox denial named, or "". The OS
// message is the ground truth about what was refused: it prints the path the
// syscall failed on — read or write — including paths no static reading of the
// command can derive (variables, loops, generated names) and paths it can get
// wrong (a shell metacharacter in a sed expression once offered "/\1"). Only
// lines carrying a denial marker are read, and every candidate must survive
// NormalizePath and PlausibleWritableRoot, so a denial naming nothing grantable
// yields "" and the caller falls back to the command text.
func PathsFromDenial(output, cwd string) string {
	for _, line := range strings.Split(output, "\n") {
		if !LooksSandboxDenied(line) {
			continue
		}
		for _, raw := range denialLinePaths(line) {
			p, err := permissions.NormalizePath(raw)
			if err != nil {
				continue
			}
			if _, bad := CredentialPathReason(p); bad {
				// SSH keys, known_hosts and shell rc files are credential
				// material. A path re-admission is not the right answer for
				// them (and the static floor would refuse it anyway); skip so a
				// later, grantable path can still be offered.
				continue
			}
			if PlausibleWritableRoot(p, cwd) {
				return p
			}
		}
	}
	return ""
}

// DenialExplanation explains a sandbox denial that has no grantable path. It is
// the other half of PathsFromDenial: when every path the OS named is system
// protected or credential material, saying so is more useful than silently
// falling back to the command text and offering a path that cannot fix it.
//
// A protected system path is often not a sandbox decision at all: macOS SIP
// reports /private/var/select/sh as "Operation not permitted" even outside the
// sandbox. A credential path is one the static floor deliberately refuses to
// hand over by a keystroke. Both need a one-call unisolated approval, not a
// path grant.
func DenialExplanation(output, cwd string) string {
	credential, system := "", ""
	for _, line := range strings.Split(output, "\n") {
		if !LooksSandboxDenied(line) {
			continue
		}
		for _, raw := range denialLinePaths(line) {
			p, err := permissions.NormalizePath(raw)
			if err != nil {
				continue
			}
			if _, bad := CredentialPathReason(p); bad {
				if credential == "" {
					credential = p
				}
				continue
			}
			if systemProtectedPath(p) {
				if system == "" {
					system = p
				}
				continue
			}
			if PlausibleWritableRoot(p, cwd) {
				// There is a grantable path; the normal re-admission dialog is
				// the right explanation, so this function has nothing to add.
				return ""
			}
		}
	}
	switch {
	case credential != "":
		return "the sandbox denied " + credential + ", which is credential material and cannot be granted by a path re-admission. If the command needs it, ask the user to approve one unisolated run of this exact command, or point the program at a copy of that file outside ~/.ssh"
	case system != "":
		return "the only path the OS named is the system-protected " + system + "; a sandbox path grant would not fix it (macOS reports SIP-protected paths as Operation not permitted). If the command is legitimate, the user can approve one unisolated run"
	default:
		return ""
	}
}

// systemProtectedPath reports whether p is in a system-owned tree whose
// "Operation not permitted" is not a sandbox decision. It is intentionally
// lexical and short: the point is to stop offering a grant for an OS path, not
// to enumerate every protected file.
func systemProtectedPath(p string) bool {
	for _, root := range []string{
		"/private/var", "/private/etc", "/System", "/Library", "/usr",
		"/bin", "/sbin", "/lib", "/lib64", "/opt", "/dev", "/proc",
	} {
		if permissions.Inside(root, p) {
			return true
		}
	}
	return false
}

// denialLinePaths pulls path candidates out of one denial line. The commonest
// shape is "<prog>: <path>: <message>"; the create-shaped messages wrap the
// path in quotes ("cp: cannot create regular file '/x/y': Operation not
// permitted", "touch: cannot touch '/x/y'", "dd: failed to open '/x/y'"). The
// line's plain tokens are the last resort — the line itself is the denial, so
// its tokens are better evidence than the command's — and every candidate is
// validated by the caller.
func denialLinePaths(line string) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if i := strings.Index(line, ": "); i >= 0 {
		rest := line[i+2:]
		if j := strings.Index(rest, ": "); j >= 0 {
			add(rest[:j])
		} else {
			add(rest)
		}
	}
	for _, quoted := range strings.Split(line, "'") {
		quoted = strings.TrimSpace(quoted)
		if strings.HasPrefix(quoted, "/") || strings.HasPrefix(quoted, "~/") {
			add(quoted)
		}
	}
	return append(out, pathTokens(line)...)
}

// GrantablePathFromText extracts the first plausible path from a call's command
// and its justification, resolved against cwd. The command goes through
// commandPaths — the boundary gate's own reader: shellfacts (mvdan) for the
// grammar, plus the program tables for what the grammar cannot say — so a
// script's words are never mistaken for paths ("sed -E 's#x#y#g'" has no path
// in it, and its "/g" flag once reached a dialog). The justification is prose
// written by the model, so it is read with the lexical scan. "" means no grant
// is offered — a dialog then shows only allow/deny and a denial has nothing to
// re-admit — which is the fail-closed direction: a missed path hides a row, it
// never grants one.
func GrantablePathFromText(summary, justification, cwd string) string {
	candidates := append(commandPaths(strings.TrimPrefix(summary, "command: ")), flagValuePaths(summary)...)
	candidates = append(candidates, writeTargetPaths(summary)...)
	candidates = append(candidates, pathTokens(justification)...)
	for _, raw := range candidates {
		p, err := permissions.NormalizePath(raw)
		if err != nil {
			// A candidate may be a RELATIVE spelling, and one of them is the
			// everyday case this function exists for: `git clone <url> ../repo`
			// names its destination one level above the workspace. NormalizePath
			// refuses relatives by design — they resolve differently for every
			// process — so the run's own directory, which is what the shell would
			// resolve the word against, is applied here instead.
			if cwd == "" {
				continue
			}
			abs, aerr := permissions.AbsAgainst(cwd, raw)
			if aerr != nil {
				continue
			}
			if p, err = permissions.NormalizePath(abs); err != nil {
				continue
			}
		}
		if PlausibleWritableRoot(p, cwd) {
			return p
		}
	}
	return ""
}

// commandPlan parses a call's command text once. The dialog's summary carries a
// "command: " prefix, so that is stripped before the parse; the words the shell
// would build are what the scans below read.
func commandPlan(summary string) *shellfacts.Plan {
	return shellfacts.Parse(strings.TrimPrefix(strings.TrimSpace(summary), "command: "))
}

// writeTargetPaths returns the path operands of a command whose effect is a
// write or a delete — "rm -rf <dir>", "mkdir -p <dir>", "cp a <dst>" — which
// the boundary reader does not always collect: it reads what a command READS,
// and rm deletes. sed and awk only count when they write in place; their other
// flags take scripts, and a script's words are not paths.
func writeTargetPaths(s string) []string {
	plan := commandPlan(s)
	if !plan.Parsed {
		return nil
	}
	var out []string
	for _, c := range plan.AllCommands() {
		// git clone's destination is a write no verb table covers: the directory
		// is created by the subcommand, and the target is the second positional
		// word rather than an operand of the program (see gitclone.go).
		for _, p := range cloneTargetPaths(c) {
			if pathShapedWord(p) {
				out = append(out, p)
			}
		}
		prog, ok := c.Program()
		if !ok {
			continue
		}
		verb := filepath.Base(prog)
		switch verb {
		case "sed", "awk", "gawk":
			if !inPlaceFlag(c.Args()) {
				continue
			}
		default:
			if !writeVerbs[verb] {
				continue
			}
		}
		for _, a := range c.Args() {
			if v, ok := wordValue(a); ok && literalPath(v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// inPlaceFlag reports whether an argument list carries sed/awk's in-place flag
// (-i, -i.bak, --in-place, --in-place=…).
func inPlaceFlag(args []shellfacts.Word) bool {
	for _, a := range args {
		v, ok := wordValue(a)
		if !ok {
			continue
		}
		if v == "--in-place" || strings.HasPrefix(v, "--in-place=") ||
			v == "-i" || (strings.HasPrefix(v, "-i") && !strings.HasPrefix(v, "--")) {
			return true
		}
	}
	return false
}

// pathFlagNames are the flags whose value is a write target in practice, for
// the tools the program tables in bashpaths.go do not describe ("cargo build
// --target-dir /x", "go build -o /x"). Deliberately short and conservative: an
// unlisted flag hides a row, it never grants one — and flags that take scripts
// (sed -n/-e, awk -e) must stay out, or a script's words come back as paths.
var pathFlagNames = map[string]bool{
	"-o": true, "--output": true, "--out": true, "--out-dir": true, "--output-dir": true,
	"-C": true, "--directory": true, "--target-dir": true, "--prefix": true,
	"--cache-dir": true, "--store": true, "--root": true, "--dest": true,
	"--destination": true, "--tmpdir": true, "--workdir": true, "--work-tree": true,
}

// flagValuePaths returns the values of path-taking flags, read from the shell's
// own words. Adjacency is the evidence here: a path-shaped word after an
// unrelated flag is a script's argument ("sed -n '/func X/,/^}/p'"), not a
// target — and the parse is what tells the two apart.
func flagValuePaths(s string) []string {
	plan := commandPlan(s)
	if !plan.Parsed {
		return nil
	}
	var out []string
	for _, c := range plan.AllCommands() {
		args := c.Args()
		for i, a := range args {
			name, ok := wordValue(a)
			if !ok || !strings.HasPrefix(name, "-") {
				continue
			}
			if eq := strings.IndexByte(name, '='); eq > 0 {
				if pathFlagNames[name[:eq]] && literalPath(name[eq+1:]) {
					out = append(out, name[eq+1:])
				}
				continue
			}
			if !pathFlagNames[name] || i+1 >= len(args) {
				continue
			}
			if v, ok := wordValue(args[i+1]); ok && literalPath(v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// literalPath reports whether s is a path spelling that carries no shell
// expression syntax.
func literalPath(s string) bool {
	if !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~/") {
		return false
	}
	return !strings.ContainsAny(s, `\*?${}#%`)
}

// OnceSet answers "is this the first time?" for the path-scoped questions the
// harness puts to a person. One implementation for one rule: a question that was
// asked and answered must not be asked again in the same run, or a retrying
// model turns a refusal into a nag. Safe for concurrent use — tool batches run
// in parallel and the gate's closure is shared across those calls.
type OnceSet struct {
	mu   sync.Mutex
	seen map[string]bool
}

// First records key and reports whether this was its first sighting. An empty
// key is never a question worth asking, so it reports false without recording.
func (s *OnceSet) First(key string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if s.seen[key] {
		return false
	}
	s.seen[key] = true
	return true
}

// PathWriteNeed reports whether a denied path is needed for WRITING rather than
// reading, so the question can say which it is and lead with the row that
// actually unblocks the command. Three signals, cheapest first: a path that does
// not exist can only have been needed for writing (there is nothing to read out
// of it); a redirection that writes to the path (or under it); and a command
// whose verb, or path-taking flag, makes the path a write target. The parse is
// what keeps a stderr redirect (`2>/dev/null`) from counting as a write. It
// stays a heuristic — the dialog still offers both sides — and it is biased to
// "read" when unsure, which is the narrower grant.
func PathWriteNeed(path, summary string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true
	}
	return PathWritten(path, summary)
}

// PathWritten reports whether the command's own text writes path: a redirect
// target, a write verb's operand, or a flag value naming it. Unlike
// PathWriteNeed it makes no guess from the path's absence, so it is the right
// question for a gate deciding the SIDE of a question it is about to ask —
// reading a file that does not exist is still a read.
func PathWritten(path, summary string) bool {
	if path == "" {
		return false
	}
	plan := commandPlan(summary)
	if !plan.Parsed {
		return false
	}
	for _, c := range plan.AllCommands() {
		for _, r := range c.Redirs {
			if r.Dup || r.Heredoc || !isWriteOp(r.Op) {
				continue
			}
			if target, ok := wordValue(r.Target); ok && pathCovers(path, target) {
				return true
			}
		}
		prog, ok := c.Program()
		if !ok {
			continue
		}
		verb := filepath.Base(prog)
		writes := writeVerbs[verb]
		if verb == "sed" || verb == "awk" || verb == "gawk" {
			writes = inPlaceFlag(c.Args())
		}
		if !writes {
			continue
		}
		for _, a := range c.Args() {
			if v, ok := wordValue(a); ok && pathCovers(path, v) {
				return true
			}
		}
	}
	for _, v := range flagValuePaths(summary) {
		if pathCovers(path, v) {
			return true
		}
	}
	return false
}

// pathCovers reports whether root is target itself or contains it.
func pathCovers(root, target string) bool {
	nr, err := permissions.NormalizePath(root)
	if err != nil {
		return false
	}
	nt, err := permissions.NormalizePath(target)
	if err != nil {
		return false
	}
	return nt == nr || permissions.Inside(nr, nt)
}

// isWriteOp reports whether a redirection operator writes to its target.
func isWriteOp(op string) bool {
	switch op {
	case ">", ">>", ">|", "&>", "&>>", "<>":
		return true
	}
	return false
}

// writeVerbs are the command names that make their path argument a write target.
// Deliberately short: the missing-path signal carries the common cases, and a
// wrong "false" here only reorders two rows.
var writeVerbs = map[string]bool{
	"mkdir": true, "touch": true, "rm": true, "rmdir": true, "mv": true, "cp": true,
	"tee": true, "install": true, "chmod": true, "chown": true, "ln": true, "dd": true,
	"truncate": true, "sed": true,
}

// pathTokens splits s into shell-ish tokens that name an absolute or ~ path.
// It is deliberately lexical: quotes, =, ':' and shell punctuation break
// tokens, and a surviving token must start with "/" or "~/". Commands are
// free-form, so a missed path only hides the row (fail closed), never grants.
//
// CJK punctuation is in the delimiter set on purpose. Chinese prose carries no
// spaces between clauses, so a justification reading "…代替 ~/.ssh，避免读取
// 凭据目录…" used to yield the single token "~/.ssh，避免读取凭据目录…" — a
// nonexistent path that is not recognised as credential material either (its
// first component is ".ssh，…", not ".ssh"), and a dialog offered to grant it.
func pathTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\'', '"', '`', '=', ':', '(', ')', ',', ';':
			return true
		}
		// Full-width punctuation (and the ideographic space): the same break in
		// prose written in Chinese, which has no ASCII commas to split on.
		switch r {
		case '，', '。', '；', '：', '、', '！', '？', '（', '）', '【', '】',
			'“', '”', '‘', '’', '《', '》', '\u3000':
			return true
		}
		return false
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimLeft(f, ">|<")
		f = strings.TrimRight(f, ".,;:)]}，。；：、！？）】”’")
		if strings.HasPrefix(f, "~/") || (strings.HasPrefix(f, "/") && f != "/") {
			// A shell metacharacter means this token is an expression, not a
			// literal path — the sed backreference "\1" in a s#…#…# command
			// reached a dialog once. Skipping it hides a row; keeping it grants
			// something that does not exist.
			if strings.ContainsAny(f, `\*?${}#%`) {
				continue
			}
			out = append(out, f)
		}
	}
	return out
}

// PlausibleWritableRoot filters the candidates whose grant would be
// meaningless or dangerous: paths the command can already write (the
// workspace, the temp roots), the system runtime (read-only by design), and
// golder's own state. Exported because the TUI checks the same predicate when
// it decides whether to offer the grant.
func PlausibleWritableRoot(p, cwd string) bool {
	inside := func(dir string) bool {
		return dir != "" && permissions.Inside(dir, p)
	}
	if p == "/" || p == "//" {
		return false
	}
	// A direct child of the filesystem root that does not exist is a token, not
	// a path: sed's s///g flag reaches here as "/g", and a backreference as
	// "/\1". Real top-level directories exist (a mounted volume) or are already
	// refused above; nested candidates ("…/build") are untouched, so a volume
	// root that is not mounted this minute still gets its row.
	if filepath.Dir(p) == "/" {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	if inside(cwd) {
		return false
	}
	for _, dir := range []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp"} {
		if inside(dir) {
			return false
		}
	}
	for _, dir := range []string{
		"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/etc",
		"/System", "/Library", "/Applications", "/dev", "/proc", "/var",
		// macOS's canonical spellings: /var and /etc are symlinks into
		// /private, so a denial reports the /private form. Without these the
		// dialog offered a grant for SIP-protected /private/var/select/sh.
		"/private/var", "/private/etc",
	} {
		if inside(dir) {
			return false
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && p == home {
		return false
	}
	// golder's own state is never a grant: the authorization table lives there,
	// so a writable root over it would let the agent rewrite the rules it is
	// being held to.
	if gh := golderhome.Dir(); gh != "" && inside(gh) {
		return false
	}
	return true
}
