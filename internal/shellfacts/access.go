package shellfacts

// Access: which paths a command touches, and what it does to them.
//
// This is the fact a multi-path command needs, and the reason a list beats a
// single path. `cp a b c /dest` names four paths with two different operations
// (three reads, one write); `ls /x /y /z` names three reads; a redirect adds a
// write the argv never mentions. Reducing that to one path — the common parent,
// say — would be a lie in one direction or the other: too wide (granting / when
// the command touches /x/y) or too narrow (/x/y when it also writes /z).
//
// So the access list is kept whole, and the caller decides. Facts here, policy
// above.

import "path/filepath"

// Access is one path a command touches, with what it does there and why.
type Access struct {
	// Path is the path as written, with quotes removed. Empty for a target that
	// cannot be named (a dynamic redirect target like `> $OUT`).
	Path string
	// Op is what the command does to it. UnknownOp means the operation depends
	// on the program's semantics — an argument — and the caller must narrow it
	// with knowledge this package does not have.
	Op Op
	// Dynamic reports that the path is not fully known: it contains a variable,
	// a glob, or a substitution. A caller must not treat Path as the whole
	// story, and must not hand out a grant wider than the written text.
	Dynamic bool
	// CommandIndex is the command that touches it, so a dialog can name the
	// step and a reviewer can see them together.
	CommandIndex int
	// Origin says where in the command the path came from: "redirect",
	// "command", "argument", or "nested" for one inside a substitution.
	Origin string
	// Nested reports that this path is inside a command substitution or process
	// substitution: it is touched by a command that runs first, and the reason
	// substitutions deserve their own line in a dialog.
	Nested bool
}

// PathAccesses returns every path this plan touches, in execution order.
//
// The walk covers all three places a path can hide, because a guard that reads
// only the argv misses two of them:
//
//   - redirection targets, where the operation is fixed by shell semantics
//     (`>` writes, `<` reads) and never appears among the arguments;
//   - the command word, when it is path-shaped — that is an execute;
//   - arguments, where the operation is the program's business.
//
// Substitutions are descended into, so `cat $(echo /etc/passwd)` reports the
// inner command's own access rather than only the outer `cat`. Their entries
// carry Nested so a caller can present them as what they are: commands that
// will run before this one does.
func (p *Plan) PathAccesses() []Access {
	if p == nil {
		return nil
	}
	var out []Access
	out = append(out, p.accessesOf(false)...)
	return out
}

func (p *Plan) accessesOf(nested bool) []Access {
	var out []Access
	for _, c := range p.Commands {
		origin := "argument"
		if nested {
			origin = "nested"
		}
		// Redirects first: they are the accesses that never appear as words, so
		// a dialog that lists only arguments would hide them entirely.
		for _, r := range c.Redirs {
			if acc, ok := redirectAccess(r, c.Index, origin, nested); ok {
				out = append(out, acc)
			}
		}
		for i, w := range c.Argv {
			if i == 0 {
				// Command position: running a path is an execute, and a path
				// there is exactly where a shadowed binary hides.
				if isPathShaped(w) {
					out = append(out, Access{
						Path: w.Text, Op: Execute, Dynamic: w.Dynamic,
						CommandIndex: c.Index, Origin: "command", Nested: nested,
					})
				}
				continue
			}
			if !isPathShaped(w) {
				continue
			}
			out = append(out, Access{
				Path: w.Text, Op: UnknownOp, Dynamic: w.Dynamic,
				CommandIndex: c.Index, Origin: origin, Nested: nested,
			})
		}
		// Descend into substitutions so their own commands are visible. This is
		// the GuardFall "Class C" gap: the dangerous command is inside a word,
		// and a listing that stops at the outer argv never shows it.
		for _, n := range c.Nested {
			out = append(out, n.accessesOf(true)...)
		}
	}
	return out
}

// redirectAccess describes one redirection's path, when it names one.
func redirectAccess(r Redirect, cmdIndex int, origin string, nested bool) (Access, bool) {
	if r.Dup || r.Heredoc {
		// A descriptor duplication names no path; a heredoc's body is further
		// shell input, which is a fact the Plan reports rather than a path.
		return Access{}, false
	}
	op := Write
	switch r.Op {
	case "<", "<>":
		// `<` reads; `<>` opens for both, which writes as surely as it reads.
		op = Read
		if r.Op == "<>" {
			op = Read | Write
		}
	}
	return Access{
		Path: r.Target.Text, Op: op, Dynamic: r.Target.Dynamic,
		CommandIndex: cmdIndex, Origin: "redirect", Nested: nested,
	}, true
}

// isPathShaped reports whether a word names a path, as opposed to a literal
// argument like a flag or a search pattern. It is deliberately conservative: a
// word that merely COULD be a path is not reported, because a false positive
// asks the user a question about something they never touched.
//
// A dynamic word still counts when its literal text is path-shaped, so
// `"$HOME"/x` and `$DIR/x` are reported (with Dynamic set) while `$MODE` and
// `--flag` are not.
func isPathShaped(w Word) bool {
	text := w.Text
	if text == "" {
		return false
	}
	if text[0] == '-' && len(text) > 1 {
		// A flag. `-` alone is a stdin/stdout convention, not a path.
		return false
	}
	if len(text) >= 3 && (text[0] == 'h' && text[1] == 't' && text[2] == 't') {
		// http://, https:// — a URL is not a local path.
		return false
	}
	switch {
	case filepath.IsAbs(text):
		return true
	case text == ".." || len(text) > 2 && text[:3] == "../":
		return true
	case text == "~" || len(text) > 1 && text[0] == '~' && text[1] == '/':
		return true
	}
	// A relative path with a separator, e.g. `internal/judge/readonly.go`.
	// A bare word (`main.go`, `readonly`) is NOT reported: it may equally be a
	// search pattern, a branch name or a package name, and asking about it
	// would train the user to dismiss the dialog.
	for i := 0; i < len(text); i++ {
		if text[i] == '/' {
			return true
		}
	}
	return false
}

// AccessSummary groups the accesses by path, keeping the operation set. A
// dialog needs "golder wants to read /x and write /y", not two rows for the
// same path when a command both reads and writes it.
type AccessSummary struct {
	Path string
	Op   Op
	// Dynamic reports that at least one occurrence was dynamic.
	Dynamic bool
}

// Summary folds the access list into one entry per path, in first-seen order.
func (p *Plan) Summary() []AccessSummary {
	var out []AccessSummary
	index := map[string]int{}
	for _, a := range p.PathAccesses() {
		if a.Path == "" {
			continue
		}
		if i, ok := index[a.Path]; ok {
			out[i].Op |= a.Op
			out[i].Dynamic = out[i].Dynamic || a.Dynamic
			continue
		}
		index[a.Path] = len(out)
		out = append(out, AccessSummary{Path: a.Path, Op: a.Op, Dynamic: a.Dynamic})
	}
	return out
}
