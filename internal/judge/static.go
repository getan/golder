package judge

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/getan/golder/internal/patch"
)

// StaticFloor is the tiny hard-deny list for unambiguously catastrophic
// calls. It never allows, it only denies; everything else returns false so
// the chain falls through to the model reviewer. Keep this list short on
// purpose: broad heuristics belong in the reviewer's policy prompt
// (maintainable as criteria text), only irreversible damage lives here.
//
// bash denies: privilege escalation, bare-metal destructive commands,
// host power control, fork bombs, and one-command environment exfiltration
// (whole-environment dump piped into a network sink, or a sensitive-named
// variable echoed into one). apply_patch denies: any path the patch touches that lands
// in a live secret directory — every path in the patch is checked, so a
// multi-file patch cannot smuggle one through. The agenttool layer already
// rejects Root escapes; this is the extra secret-material floor above it.
func staticDeny(tool string, args json.RawMessage) (Verdict, bool) {
	name := strings.ToLower(strings.TrimSpace(tool))
	switch name {
	case "bash":
		if cmd, ok := bashCommand(args); ok {
			if reason, bad := destructiveBash(cmd); bad {
				return Verdict{Level: Deny, Source: "static", Reasons: []string{reason}}, true
			}
			if reason, bad := secretExfil(cmd); bad {
				return Verdict{Level: Deny, Source: "static", Reasons: []string{reason}}, true
			}
		}
		return Verdict{}, false
	case "apply_patch":
		if text, ok := patchText(args); ok {
			for _, p := range patch.Paths(text) {
				if reason, bad := secretPath(p); bad {
					return Verdict{Level: Deny, Source: "static", Reasons: []string{reason + " (in apply_patch)"}}, true
				}
			}
		}
		return Verdict{}, false
	default:
		return Verdict{}, false
	}
}

func bashCommand(args json.RawMessage) (string, bool) {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", false
	}
	cmd := strings.TrimSpace(a.Command)
	if cmd == "" {
		return "", false
	}
	return cmd, true
}

// patchText extracts the apply_patch call's patch text. A patch that does not
// parse yields false: the static floor stays silent and the tool itself will
// reject it, so a malformed patch is never silently allowed through grading
// with a path set the regex layer never saw.
func patchText(args json.RawMessage) (string, bool) {
	var a struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", false
	}
	if strings.TrimSpace(a.Patch) == "" {
		return "", false
	}
	return a.Patch, true
}

// destructiveBash matches commands that are destructive beyond the working
// tree regardless of cwd: privilege escalation, disk-level destruction,
// host power control, and fork bombs. Matching is substring-based and
// deliberately narrow; fuzzy danger (curl|sh installers, recursive deletes
// inside the project) is the reviewer's job, not the floor's.
func destructiveBash(cmd string) (string, bool) {
	lower := strings.ToLower(cmd)
	for _, prefix := range []string{"sudo ", "sudo\t", "doas "} {
		if strings.HasPrefix(lower, prefix) {
			return "privilege escalation (" + strings.TrimSpace(prefix) + ")", true
		}
	}
	for _, pat := range []struct{ sub, reason string }{
		{"mkfs", "disk format (mkfs)"},
		{"shutdown", "host power control (shutdown)"},
		{"reboot", "host power control (reboot)"},
		{":(){ :|:& };:", "fork bomb"},
		{"chmod -r 777 /", "permission wipe (chmod -R 777 /)"},
		{"chmod -rf 777 /", "permission wipe (chmod -R 777 /)"},
	} {
		if strings.Contains(lower, pat.sub) {
			return pat.reason, true
		}
	}
	if strings.Contains(lower, "dd ") && strings.Contains(lower, "of=/dev/") {
		return "raw disk write (dd of=/dev/)", true
	}
	if isBareRootWipe(lower) {
		return "recursive delete anchored at filesystem root (rm -rf /)", true
	}
	return "", false
}

// isBareRootWipe matches rm -rf variants whose target is literally /,
// possibly with --no-preserve-root. Project-scoped deletes (rm -rf
// ./build, rm -rf $TMPDIR/x) intentionally do NOT match: the reviewer grades those.
func isBareRootWipe(lower string) bool {
	idx := strings.Index(lower, "rm ")
	if idx < 0 {
		return false
	}
	rest := lower[idx+3:]
	if !strings.Contains(rest, "-") || !strings.Contains(rest, "r") || !strings.Contains(rest, "f") {
		return false
	}
	for _, tok := range strings.Fields(rest) {
		if tok == "/" || tok == "/*" {
			return true
		}
	}
	return false
}

// secretPath matches writes into live secret material: SSH/GPG identity
// dirs and the trust store itself. Matching is on slash-separated path
// segments so only the path argument is graded, never file prose.
func secretPath(p string) (string, bool) {
	cleaned := path.Clean("/" + strings.TrimSpace(p))
	segs := strings.Split(cleaned, "/")
	for _, s := range segs {
		switch s {
		case ".ssh", ".gnupg":
			return "write into live secret directory (" + s + ")", true
		case "trust.json":
			return "write into the trust store (trust.json)", true
		}
	}
	return "", false
}

// secretExfil matches one-command environment/secret exfiltration. Either
// half alone is legitimate — printing variables to debug, or downloading
// with curl — so only their combination is denied: a whole-environment dump
// (env, printenv, export -p, ...) or an echo of a sensitive-named variable
// piped into a network sink (curl, wget, nc, ssh, /dev/tcp). Single-purpose
// prints (echo $PATH, printenv HOME, set -e) and ordinary downloads fall
// through to the reviewer, which grades the recoverable middle.
func secretExfil(cmd string) (string, bool) {
	segs := splitPipeline(cmd)
	for i, seg := range segs {
		if !isEnvDump(seg) && !isSensitiveEcho(seg) {
			continue
		}
		for _, later := range segs[i+1:] {
			if sink, bad := netSink(later); bad {
				return "environment/secret piped to network (" + sink + ")", true
			}
		}
	}
	return "", false
}

// splitPipeline cuts a command line on single pipes. "||" is protected first
// so fallback chains are not mistaken for data flow (printenv || curl runs
// curl only when printenv fails — the reviewer's call, not the floor's).
func splitPipeline(cmd string) []string {
	const or = "\x00"
	protected := strings.ReplaceAll(cmd, "||", or)
	parts := strings.Split(protected, "|")
	for i := range parts {
		parts[i] = strings.ReplaceAll(parts[i], or, "||")
	}
	return parts
}

// isEnvDump reports whether a pipeline segment dumps the whole environment:
// bare env/printenv, export -p, declare -x, compgen -e, bare set, or
// printenv of a sensitive-named variable. Segments with compound operators
// (&, ;) are skipped — the reviewer grades those. "env FOO=bar ./cmd" is skipped too:
// assignments that launch a command are ordinary environment passing, not a
// dump (the rare "env FOO=1" dump-without-command is the reviewer's).
func isEnvDump(seg string) bool {
	s := strings.TrimSpace(seg)
	if s == "" || strings.ContainsAny(s, "&;") {
		return false
	}
	for _, p := range []string{"export -p", "declare -x", "compgen -e"} {
		if s == p || strings.HasPrefix(s, p+" ") || strings.HasPrefix(s, p+"\t") {
			return true
		}
	}
	if s == "set" {
		return true
	}
	if s == "env" || strings.HasPrefix(s, "env ") || strings.HasPrefix(s, "env\t") {
		return !strings.Contains(s, "=")
	}
	if s == "printenv" {
		return true
	}
	if rest, ok := cutWord(s, "printenv"); ok {
		return rest == "" || isSensitiveName(rest)
	}
	return false
}

// isSensitiveEcho reports whether a segment prints a sensitive-named
// variable (echo/printf of $...KEY.../SECRET.../TOKEN.../...). The name must
// be a real variable reference, so prose like `echo "token expired"` never
// matches — only $NAME / ${NAME} shapes.
func isSensitiveEcho(seg string) bool {
	s := strings.TrimSpace(seg)
	if s == "" || strings.ContainsAny(s, "&;") {
		return false
	}
	head := s
	if i := strings.IndexAny(head, " \t"); i >= 0 {
		head = head[:i]
	} else {
		return false
	}
	if head != "echo" && head != "printf" {
		return false
	}
	return hasSensitiveVarRef(s)
}

// cutWord splits "printenv REST" into REST, or reports false when s is not a
// printenv invocation.
func cutWord(s, word string) (string, bool) {
	if s == word {
		return "", true
	}
	for _, sep := range []string{" ", "\t"} {
		if strings.HasPrefix(s, word+sep) {
			return strings.TrimSpace(s[len(word)+1:]), true
		}
	}
	return "", false
}

// isSensitiveName reports whether space-separated words contain a
// credential-smelling token (KEY, SECRET, TOKEN, PASSW, PRIVATE).
func isSensitiveName(s string) bool {
	for _, w := range strings.Fields(s) {
		up := strings.ToUpper(strings.Trim(w, "\"'${}"))
		for _, sub := range []string{"KEY", "SECRET", "TOKEN", "PASSW", "PRIVATE"} {
			if strings.Contains(up, sub) {
				return true
			}
		}
	}
	return false
}

// hasSensitiveVarRef scans for a $NAME/「${NAME}」 reference whose NAME is
// credential-smelling. It is a tiny scanner rather than a regex so the leaf
// package stays dependency-free and the match stays tight to real
// references ($1, $@, and prose never match).
func hasSensitiveVarRef(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		j := i + 1
		braced := false
		if j < len(s) && s[j] == '{' {
			braced = true
			j++
		}
		start := j
		for j < len(s) && (s[j] == '_' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= '0' && s[j] <= '9') {
			j++
		}
		if j == start {
			continue
		}
		if braced && (j >= len(s) || s[j] != '}') {
			continue
		}
		name := strings.ToUpper(s[start:j])
		for _, sub := range []string{"KEY", "SECRET", "TOKEN", "PASSW", "PRIVATE"} {
			if strings.Contains(name, sub) {
				return true
			}
		}
	}
	return false
}

// netSink reports whether a pipeline segment hands data to the network.
// Matching is on whitespace-separated fields so `echo curl` prose in an
// earlier segment is never consulted here — only later segments are.
func netSink(seg string) (string, bool) {
	for _, f := range strings.Fields(seg) {
		w := strings.Trim(strings.ToLower(f), "\"'`")
		switch w {
		case "curl", "wget", "nc", "ncat", "socat", "ssh", "scp", "ftp", "telnet":
			return w, true
		}
		if strings.Contains(w, "/dev/tcp/") {
			return "/dev/tcp", true
		}
	}
	return "", false
}
