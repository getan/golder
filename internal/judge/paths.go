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
	"strings"
	"sync"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/permissions"
)

// GrantablePathFromText extracts the first plausible path token from a call's
// summary and its justification, resolved against cwd. "" means no grant is
// offered — a dialog then shows only allow/deny and a denial has nothing to
// re-admit — which is the fail-closed direction: a missed path hides a row, it
// never grants one.
func GrantablePathFromText(summary, justification, cwd string) string {
	for _, raw := range append(pathTokens(summary), pathTokens(justification)...) {
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
