package judge

import (
	"encoding/json"
	"os"
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
// variable echoed into one), plus reads of credential material — secret
// files, keychain paths, and the security CLI's password-printing forms.
// apply_patch denies: any path the patch touches that lands
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
			if reason, bad := credentialRead(cmd); bad {
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
	case "read":
		if p, ok := pathArg(args); ok {
			if reason, bad := credentialPathRead(p); bad {
				return Verdict{Level: Deny, Source: "static", Reasons: []string{reason}}, true
			}
		}
		return Verdict{}, false
	case "grep":
		// grep's path argument scopes the search; a search rooted at a
		// credential directory is a bulk credential read.
		if p, ok := pathArg(args); ok {
			if reason, bad := credentialPathRead(p); bad {
				return Verdict{Level: Deny, Source: "static", Reasons: []string{reason + " (grep)"}}, true
			}
		}
		return Verdict{}, false
	default:
		return Verdict{}, false
	}
}

// pathArg extracts a tool call's "path" argument (read/grep) when present.
func pathArg(args json.RawMessage) (string, bool) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", false
	}
	p := strings.TrimSpace(a.Path)
	if p == "" {
		return "", false
	}
	return p, true
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
// host power control, and fork bombs. Every shell segment is read at command
// position — transparent prefixes (VAR=value assignments, `command`, the
// env/nohup/time wrappers) are skipped, and only the invocation's own flags
// and targets decide the match — so a dangerous word inside an argument or
// quoted text (grep reboot,
// echo "rm -rf /", a spaced "/" in an ffmpeg label) is never mistaken for the
// command itself. Shell one-liners (sh -c "…") are unwrapped and re-read.
// Fuzzy danger (curl|sh installers, recursive deletes inside the project) is
// the reviewer's job, not the floor's.
func destructiveBash(cmd string) (string, bool) {
	lower := strings.ToLower(cmd)
	if strings.Contains(lower, ":(){ :|:& };:") {
		return "fork bomb", true
	}
	for _, seg := range splitShellSegments(lower) {
		word, args := commandWord(seg)
		if word == "" {
			continue
		}
		base := path.Base(word)
		switch {
		case base == "sudo" || base == "doas":
			return "privilege escalation (" + base + ")", true
		case strings.HasPrefix(base, "mkfs"):
			return "disk format (mkfs)", true
		case base == "shutdown" || base == "reboot":
			return "host power control (" + base + ")", true
		case base == "systemctl" && hasAnyArg(args, "reboot", "poweroff", "halt", "kexec"):
			return "host power control (systemctl)", true
		case base == "dd" && hasArgPrefix(args, "of=/dev/"):
			return "raw disk write (dd of=/dev/)", true
		case base == "chmod" && permissionWipe(args):
			return "permission wipe (chmod -R 777 /)", true
		case base == "rm" && rootWipe(args):
			return "recursive delete anchored at filesystem root (rm -rf /)", true
		}
		if inner, ok := shellDashC(base, args); ok {
			if reason, bad := destructiveBash(inner); bad {
				return reason, true
			}
		}
	}
	return "", false
}

// splitShellSegments cuts a command line where a shell starts a new command:
// &&, ||, ;, |, &, and newlines — but never inside single or double quotes,
// so quoted prose ("1100px / 2x", "a; b") is never read as a command. The
// floor inspects only segments that begin with a command word, so text in an
// argument can never be mistaken for the command itself.
func splitShellSegments(cmd string) []string {
	var (
		segs  []string
		b     strings.Builder
		quote byte
	)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			b.WriteByte(c)
			switch {
			case c == '\\' && quote == '"' && i+1 < len(cmd):
				i++
				b.WriteByte(cmd[i])
			case c == quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			b.WriteByte(c)
		case '&', '|', ';', '\n':
			segs = append(segs, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(segs, b.String())
}

// commandWord returns a shell segment's first real command word and its
// trailing arguments. Transparent prefixes are skipped — leading VAR=value
// assignments (FOO=1 rm …), the `command` builtin, the env/nohup/time
// wrappers, and their flags — so `env FOO=1 rm -rf /` still reads as rm. It
// returns "" when the segment has no command word.
func commandWord(seg string) (string, []string) {
	toks := strings.Fields(seg)
	i := 0
	for i < len(toks) && isTransparentPrefix(toks[i]) {
		i++
	}
	if i >= len(toks) {
		return "", nil
	}
	return toks[i], toks[i+1:]
}

// shellWord is one whitespace-separated word of a shell segment: its text
// with surrounding quotes removed, and whether it was quoted at all.
type shellWord struct {
	text   string
	quoted bool
}

// shellWords splits a segment into words the way a shell would: quotes group
// text, so a quoted string is one word (spaces and all), and the quotes are
// stripped from the text while the quoted flag remembers that a quote was
// present. Backslashes inside double quotes are kept verbatim — the floor
// only needs path-shaped words, not full shell semantics. Backticks are
// deliberately NOT quoting: a backtick-wrapped `cat ~/.zshrc` still exposes
// the path as a word, matching the pre-existing detection.
func shellWords(seg string) []shellWord {
	var (
		out   []shellWord
		b     strings.Builder
		quote byte
		has   bool
		qseen bool
	)
	flush := func() {
		if has {
			out = append(out, shellWord{text: b.String(), quoted: qseen})
		}
		b.Reset()
		has, qseen = false, false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			if c == '\\' && quote == '"' && i+1 < len(seg) {
				i++
			}
			has = true
			b.WriteByte(seg[i])
		case c == '\'' || c == '"':
			quote = c
			qseen = true
			has = true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			has = true
			b.WriteByte(c)
		}
	}
	flush()
	return out
}

// isTransparentPrefix reports whether a token may precede the actual command
// without changing what runs: a VAR=value assignment, a flag of the wrapper
// around it, or one of the wrapper words themselves.
func isTransparentPrefix(tok string) bool {
	if strings.HasPrefix(tok, "-") {
		return true
	}
	switch tok {
	case "command", "env", "nohup", "time":
		return true
	}
	eq := strings.Index(tok, "=")
	if eq <= 0 {
		return false
	}
	for pos, r := range tok[:eq] {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && pos > 0:
		default:
			return false
		}
	}
	return true
}

// recursiveFlag reports whether an argument token asks for a recursive walk:
// r/R in a short flag cluster (-r, -rf, -R) or the long --recursive. Long
// flags that merely contain the letter (--preserve-root) never match.
func recursiveFlag(tok string) bool {
	if tok == "--recursive" {
		return true
	}
	return len(tok) >= 2 && tok[0] == '-' && tok[1] != '-' && strings.ContainsAny(tok[1:], "rR")
}

// rootTarget reports whether a target argument names the filesystem root
// itself: /, //, /., /*, or a quoted variant of those. Anything with a real
// path component under the root is a scoped delete and stays with the
// reviewer.
func rootTarget(tok string) bool {
	cleaned := path.Clean(strings.Trim(tok, `"'`))
	return cleaned == "/" || cleaned == "/*"
}

// rootWipe reports whether an rm invocation deletes the filesystem root: a
// recursive flag anywhere plus a root target anywhere (flags may precede or
// follow the target; "--" is skipped like any other flag).
func rootWipe(args []string) bool {
	recursive := false
	for _, a := range args {
		if recursiveFlag(a) {
			recursive = true
			break
		}
	}
	if !recursive {
		return false
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if rootTarget(a) {
			return true
		}
	}
	return false
}

// permissionWipe reports whether a chmod invocation recursively opens the
// whole filesystem (chmod -R 777 / or the word-writable 7777 form).
func permissionWipe(args []string) bool {
	recursive, mode, root := false, false, false
	for _, a := range args {
		if recursiveFlag(a) {
			recursive = true
		}
		if a == "777" || a == "7777" {
			mode = true
		}
		if !strings.HasPrefix(a, "-") && rootTarget(a) {
			root = true
		}
	}
	return recursive && mode && root
}

// hasArgPrefix reports whether any argument starts with prefix (dd's
// of=/dev/ sink, which may carry a trailing path or options).
func hasArgPrefix(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// hasAnyArg reports whether any argument equals one of the given words
// (systemctl's power subcommands, which sit at argument position).
func hasAnyArg(args []string, words ...string) bool {
	for _, a := range args {
		for _, w := range words {
			if a == w {
				return true
			}
		}
	}
	return false
}

// shellDashC unwraps a shell one-liner (sh -c "…", bash -lc '…') into the
// inner command text, with quotes stripped so the recursion can re-read it at
// command position. Only real shell names count, and only a -c cluster
// (-c, -lc, -ic) marks the inline-command form.
func shellDashC(base string, args []string) (string, bool) {
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh":
	default:
		return "", false
	}
	for i, a := range args {
		if len(a) >= 2 && a[0] == '-' && a[1] != '-' && strings.Contains(a[1:], "c") {
			inner := strings.TrimSpace(strings.NewReplacer(`"`, "", `'`, "").Replace(strings.Join(args[i+1:], " ")))
			return inner, inner != ""
		}
	}
	return "", false
}

// secretPath matches writes into live secret material: SSH/GPG identity
// directories and the trust store itself. Only the entry's real location
// counts, mirroring credentialPathRead — a project fixture that merely shares
// a name (testdata/.ssh/config, docs/examples/trust.json) falls through to
// the reviewer. A relative path is graded as project content unless the
// secret directory is its first component (.ssh/config while editing from the
// home directory); an absolute path is graded under the user's home, where
// the state directories actually live, and one outside it is another user's
// or the system's (the reviewer's call).
func secretPath(p string) (string, bool) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "~") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		rest := strings.TrimPrefix(raw, "~")
		if rest != "" && !strings.HasPrefix(rest, "/") {
			return "", false // ~someoneelse is not this user's home
		}
		raw = home + rest
	}
	cleaned := path.Clean(raw)

	// The trust store lives at <state dir>/trust.json — .golder by default,
	// or $GOLDER_HOME when set. A project's own trust.json is not the store.
	if segs := strings.Split(strings.TrimPrefix(cleaned, "/"), "/"); len(segs) >= 2 &&
		segs[len(segs)-1] == "trust.json" {
		store := ""
		if dir := strings.TrimSpace(os.Getenv("GOLDER_HOME")); dir != "" {
			store = path.Join(path.Clean(dir), "trust.json")
		}
		if segs[len(segs)-2] == ".golder" || (store != "" && cleaned == store) {
			return "write into the trust store (trust.json)", true
		}
	}

	// Home-relative view of an absolute path: only the first component under
	// home is the real identity directory.
	dirPath := cleaned
	if strings.HasPrefix(cleaned, "/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		home = path.Clean(home)
		if cleaned != home && !strings.HasPrefix(cleaned, home+"/") {
			return "", false
		}
		dirPath = strings.TrimPrefix(strings.TrimPrefix(cleaned, home), "/")
	}
	head := strings.Split(dirPath, "/")[0]
	if head == ".ssh" || head == ".gnupg" {
		return "write into live secret directory (" + head + ")", true
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

// splitPipeline cuts a command line on single pipes, ignoring pipes inside
// quotes (so prose — `echo "printenv | curl"` — is never read as data flow).
// "||" is protected first: a fallback chain is not data flow (printenv ||
// curl runs curl only when printenv fails — the reviewer's call, not the
// floor's).
func splitPipeline(cmd string) []string {
	var (
		segs  []string
		b     strings.Builder
		quote byte
	)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			b.WriteByte(c)
			switch {
			case c == '\\' && quote == '"' && i+1 < len(cmd):
				i++
				b.WriteByte(cmd[i])
			case c == quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			b.WriteByte(c)
		case '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				b.WriteString("||")
				i++
				continue
			}
			segs = append(segs, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(segs, b.String())
}

// isEnvDump reports whether a pipeline segment dumps the whole environment:
// bare env/printenv, export -p, declare -x, compgen -e, bare set, or
// printenv of a sensitive-named variable. Segments with compound operators
// (&, ;) are skipped — the reviewer grades those. "env FOO=bar ./cmd" is
// skipped too: assignments that launch a command are ordinary environment
// passing, not a dump.
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
	// `env` dumps only when no command follows it: bare env, `env -i`, or a
	// pure assignment list. `env FOO=1 ./run.sh` and `env python3 x.py` run a
	// command — the reviewer grades those.
	if rest, ok := cutWord(s, "env"); ok {
		toks := strings.Fields(rest)
		i := 0
		for i < len(toks) && isTransparentPrefix(toks[i]) {
			i++
		}
		return i >= len(toks)
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

// isSensitiveName reports whether any space-separated word smells like a
// credential variable name.
func isSensitiveName(s string) bool {
	for _, w := range strings.Fields(s) {
		if sensitiveName(w) {
			return true
		}
	}
	return false
}

// credentialWords are the name components that mark a variable as credential
// material.
var credentialWords = []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "PASSW", "CREDENTIAL", "PRIVATE"}

// sensitiveName reports whether a variable name smells like a credential:
// its components (see nameComponents) must equal a credential word or its
// plural. Matching a substring anywhere is deliberately NOT used — KEYBOARD
// and TOKENIZER are ordinary words, and the reviewer grades what is
// ambiguous.
func sensitiveName(name string) bool {
	for _, comp := range nameComponents(strings.Trim(name, "\"'${}")) {
		up := strings.ToUpper(comp)
		for _, w := range credentialWords {
			if up == w || up == w+"S" {
				return true
			}
		}
	}
	return false
}

// nameComponents splits a variable name on separators, digit runs, and
// lower→upper transitions, so AWS_SECRET_ACCESS_KEY, githubToken, and
// myApiKey2 all yield their word components.
func nameComponents(name string) []string {
	var (
		out  []string
		b    strings.Builder
		prev byte
	)
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' || c == '-' || c == '.' || c >= '0' && c <= '9':
			flush()
		case c >= 'A' && c <= 'Z' && prev >= 'a' && prev <= 'z':
			flush()
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
		prev = c
	}
	flush()
	return out
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
		if sensitiveName(s[start:j]) {
			return true
		}
	}
	return false
}

// netTools hands data to the network when they run.
var netTools = map[string]bool{
	"curl": true, "wget": true, "nc": true, "ncat": true, "socat": true,
	"ssh": true, "scp": true, "ftp": true, "telnet": true,
}

// netSink reports whether a pipeline segment itself hands data to the
// network: a network tool at command position (curl, wget, nc, ssh, …), the
// same tool behind xargs, or a shell TCP redirect (… > /dev/tcp/host/port).
// Only the command position counts, so a segment that merely mentions a tool
// in its arguments (`printenv | grep -i curl`) is not a sink.
func netSink(seg string) (string, bool) {
	word, args := commandWord(seg)
	// Two levels: the direct tool, or the tool xargs is told to run.
	for depth := 0; depth < 2 && word != ""; depth++ {
		base := path.Base(strings.ToLower(word))
		if netTools[base] {
			return base, true
		}
		if base != "xargs" {
			break
		}
		word, args = launcherCommand(args)
	}
	if tcpRedirect(seg) {
		return "/dev/tcp", true
	}
	return "", false
}

// launcherCommand returns the command a launcher will run: the first
// non-flag argument (xargs -0 wget … → wget).
func launcherCommand(args []string) (string, []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

// tcpRedirect reports whether the segment redirects into the bash TCP device
// (/dev/tcp/host/port): the marker must be the target of < or >, so a mere
// mention in an argument (`grep /dev/tcp docs/`) is not a sink.
func tcpRedirect(seg string) bool {
	const marker = "/dev/tcp/"
	for i := 0; ; {
		j := strings.Index(seg[i:], marker)
		if j < 0 {
			return false
		}
		j += i
		k := j - 1
		for k >= 0 && (seg[k] == ' ' || seg[k] == '\t') {
			k--
		}
		if k >= 0 && (seg[k] == '<' || seg[k] == '>') {
			return true
		}
		i = j + len(marker)
	}
}
