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
			if PlausibleWritableRoot(p, cwd) {
				return p
			}
		}
	}
	return ""
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

// GrantablePathFromText extracts the first plausible path token from a call's
// justification and its summary, resolved against cwd. The justification is
// read first because it is prose written by the model — its tokens are paths,
// while a command's tokens are shell expressions — and the summary still backs
// it up for the commands that name their path plainly. "" means no grant is
// offered — a dialog then shows only allow/deny and a denial has nothing to
// re-admit — which is the fail-closed direction: a missed path hides a row, it
// never grants one.
func GrantablePathFromText(summary, justification, cwd string) string {
	for _, raw := range append(pathTokens(justification), pathTokens(summary)...) {
		p, err := permissions.NormalizePath(raw)
		if err != nil {
			continue
		}
		if PlausibleWritableRoot(p, cwd) {
			return p
		}
	}
	return ""
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
// actually unblocks the command. Two signals, cheapest first: a path that does
// not exist can only have been needed for writing (there is nothing to read out
// of it), and otherwise the command is scanned for the verbs that make a path a
// write target. It stays a heuristic — the dialog still offers both sides — and
// it is biased to "read" when unsure, which is the narrower grant.
func PathWriteNeed(path, summary string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true
	}
	for _, tok := range strings.Fields(summary) {
		if strings.Contains(tok, ">") {
			return true
		}
		if writeVerbs[tok] {
			return true
		}
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
func pathTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\'', '"', '`', '=', ':', '(', ')', ',', ';':
			return true
		}
		return false
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimLeft(f, ">|<")
		f = strings.TrimRight(f, ".,;:)]}")
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
