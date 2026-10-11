package judge

// Which path an escalation is about: the directory that made the sandbox
// refuse, so a prompt can offer to re-admit just that path instead of
// unisolating the whole command.
//
// This lives in judge (not in one of the two dialogs) because both front-ends
// ask the same question: the TUI's approval dialog builds a "allow writes to
// <path>" row from it, and the REPL's stdin prompt offers the same grant on
// `w`/`s`. A second copy of the parsing would let the two rows disagree about
// what the user is granting.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/permissions"
)

// EscalationPathFromText extracts the first plausible path token from an
// escalation's summary and the model's justification, resolved against cwd.
// "" means no grant is offered — the dialog then offers only allow/deny, which
// is the fail-closed direction: a missed path hides a row, it never grants one.
func EscalationPathFromText(summary, justification, cwd string) string {
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
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if inside(filepath.Join(home, ".golder")) || p == home {
			return false
		}
	}
	if gh := strings.TrimSpace(os.Getenv("GOLDER_HOME")); gh != "" && inside(gh) {
		return false
	}
	return true
}
