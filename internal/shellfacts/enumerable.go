package shellfacts

// Enumerable: is everything this command line will touch readable from its
// text?

import "strings"

// The parser already knows what it could not resolve — a variable, a
// substitution, an assignment it cannot expand. Enumerable turns those facts
// into the one yes/no a caller can act on, and it exists because some decisions
// treat the argv as the whole story: which paths a consent question must cover,
// whether a single path can stand for the call, and whether a command may run
// outside the OS sandbox, where nothing else bounds it.
//
// The answer is deliberately conservative, and it is a property of the READING,
// not of the intent:
//
//   - a glob stays enumerable: `src/*.go` is bounded by the directory it is
//     written against, and the caller's question is which place is touched;
//   - a `~` stays enumerable: the shell expands it to a value this process
//     knows (its own home), which is exactly the fact the boundary gate wants;
//   - a `$VAR`, a `$(...)`, an arithmetic expansion or a process substitution
//     does not: the value is a runtime fact, and nothing here may guess it;
//   - an environment assignment whose value names a path does not: `PATH=…`,
//     `LD_PRELOAD=/x.so` or `BASH_ENV=~/rc` make the command load something no
//     argument shows, and the assignment is not in the access list;
//   - a construct this layer walked past does not: a loop or a conditional is
//     reported rather than described, and "there is a for loop here" is
//     precisely the fact that says "do not reason about this from the text".
//
// False means "treat this call as one whose effect cannot be bounded from its
// text", never "there is nothing here".
func (p *Plan) Enumerable() (bool, string) {
	if p == nil {
		return false, "there is no parsed command"
	}
	if !p.Parsed {
		if p.ParseError != "" {
			return false, "the line did not parse: " + p.ParseError
		}
		return false, "the line did not parse"
	}
	if len(p.Constructs) > 0 {
		return false, "the line contains " + strings.Join(dedupeStrings(p.Constructs), ", ")
	}
	// AllCommands, not Commands: `echo "$(rm -rf /x)"` has one top-level
	// command and the substitution's commands hang off it. Enumerating the
	// display view would miss the very thing this predicate is about.
	for _, c := range p.AllCommands() {
		if _, literal := c.Program(); !literal {
			return false, "the program itself is computed at runtime"
		}
		for _, w := range c.Argv {
			if why, dynamic := expansionReason(w); dynamic {
				return false, why
			}
		}
		for _, w := range c.Assigns {
			if why, dynamic := expansionReason(w); dynamic {
				return false, why
			}
			if w.Dynamic {
				// A tilde or a glob in an assignment is computed at runtime,
				// and an array assignment is not a single value at all.
				return false, "the assignment " + strings.TrimSpace(w.Text) + " is computed at runtime"
			}
			if namesAPath(w.Text) {
				return false, "the environment assignment " + w.Text + " names a path"
			}
		}
		for _, r := range c.Redirs {
			if r.Dup {
				continue // `2>&1` names a descriptor, not a path
			}
			if why, dynamic := expansionReason(r.Target); dynamic {
				return false, why
			}
		}
	}
	return true, ""
}

// expansionReason reports the first part of w whose value is decided at run
// time, with a reason a sentence can carry.
func expansionReason(w Word) (string, bool) {
	for _, part := range w.Parts {
		var what string
		switch part.Kind {
		case VariablePart:
			what = "expands a variable"
		case CommandSubstPart:
			what = "runs a command substitution"
		case ArithmPart:
			what = "expands arithmetic"
		case ProcSubstPart:
			what = "creates a process substitution"
		default:
			continue
		}
		text := strings.TrimSpace(w.Text)
		if text == "" {
			return what, true
		}
		return "the word " + text + " " + what, true
	}
	return "", false
}

// namesAPath reports whether an assignment's value looks like a path. A value
// that names one makes the command load code or data that no argument shows,
// and the assignment is not part of the access list.
func namesAPath(value string) bool {
	v := strings.TrimSpace(value)
	return strings.Contains(v, "/") || strings.HasPrefix(v, "~")
}

// dedupeStrings returns the values once each, in order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
