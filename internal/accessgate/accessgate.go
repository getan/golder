// Package accessgate answers one question for every tool: which paths does this
// call touch, may golder touch them, and if not, what single question should the
// user be asked?
//
// Why one gate. The boundary used to be enforced per tool: the file tools
// checked a `path` argument, bash had its own word-scanner, apply_patch had a
// third reading, and the trust store spoke a different vocabulary again. The
// result was three notions of "outside", three prompt shapes, and the specific
// confusion the user reported — a command naming /dev/null being asked about as
// if it wanted the filesystem root, because the bash path scanner and the file
// tools disagreed about what a path operand is.
//
// Here a tool call becomes a list of Resource values — path, operation, whether
// the path is fully known — and every tool's list is built the same way, from
// parsed facts where the tool has a command line and from declared arguments
// where it does not. One lookup decides each resource against the grants table,
// and one question covers the call.
//
// The rules that shape the decisions:
//
//   - A path inside the workspace is not a boundary question at all.
//   - A granted path is silent. The table is the memory of every answer the
//     user has already given, which is what makes asking once enough.
//   - A refusal is respected: the table can say no, and this gate does not
//     re-ask a question the user already answered in the negative.
//   - Everything else asks ONCE. A command naming three outside paths produces
//     one question listing three paths, not three questions — and the grant it
//     offers covers exactly the paths named, so answering cannot hand over a
//     directory nobody asked about.
package accessgate

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/patch"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/shellfacts"
)

// Resource is one path a call touches, with what it does there.
type Resource struct {
	// Path is the path as the call wrote it, before resolution. It is kept
	// un-resolved because that is what the user recognizes: they see the token
	// from their command, not a cleaned form of it.
	Path string
	// Abs is the cleaned absolute path the grant would cover. Empty when the
	// path could not be resolved, which makes the resource unaskable — a
	// question about a path nobody can name is not a question a person can
	// answer.
	Abs string
	// Op is what the call does to the path.
	Op grants.Capability
	// Dynamic reports that the path is not fully known (a variable, a glob, a
	// substitution). A grant for a dynamic path covers only the written text:
	// expanding it to decide what to ask would mean guessing at the value, and
	// guessing wrong in this direction hands over more than was named.
	Dynamic bool
	// Exact reports that only this path may be granted, not its subtree. It is
	// set for a path that names a file rather than a directory, so approving a
	// read of one file cannot hand over the directory holding it.
	Exact bool
	// Nested reports that the path sits inside a command substitution: it is
	// touched by a command that runs first. The dialog shows these separately
	// because "this runs before what you asked about" is a thing a person wants
	// to see.
	Nested bool
}

// Extractor turns one tool call into resources. Registering a tool here is how
// it joins the boundary; a tool that is not registered yields no resources and
// is therefore never asked about.
//
// Options travels with the call because extracting a shell command needs the
// injected read-only recognizer (see Options.ReadOnly): the resource's
// operation is narrowed by it, and a table of one-argument functions could not
// carry that in.
type Extractor func(args json.RawMessage, opts Options) []Resource

// extractors maps a tool name to its reader. It is a package variable rather
// than a switch so a test can assert the table covers every tool that touches
// paths, and so adding a tool is one line rather than a branch in three places.
var extractors = map[string]Extractor{
	"read":       pathArgExtractor(grants.Read, true),
	"view_image": pathArgExtractor(grants.Read, true),
	"grep":       pathArgExtractor(grants.Read, false),
	"find":       pathArgExtractor(grants.Read, false),
	"ls":         pathArgExtractor(grants.Read, false),
	"apply_patch": func(args json.RawMessage, _ Options) []Resource {
		var a struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil
		}
		// Every path the patch touches, including move destinations: a patch
		// that renames a file out of the workspace writes outside it just as
		// surely as one that edits there.
		var out []Resource
		for _, p := range patch.Paths(a.Patch) {
			out = append(out, Resource{Path: p, Op: grants.ReadWrite, Exact: true})
		}
		return out
	},
	"bash": func(args json.RawMessage, opts Options) []Resource {
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil
		}
		return FromCommand(a.Command, opts)
	},
}

// pathArgExtractor reads a tool whose paths are a `path` argument, either one
// string or a list. exact marks a tool that names files rather than roots.
func pathArgExtractor(op grants.Capability, exact bool) Extractor {
	return func(args json.RawMessage, _ Options) []Resource {
		var out []Resource
		for _, p := range pathArgs(args) {
			// Deliberately not resolved here: resolution belongs to the checker,
			// which has the workspace root and the home directory.
			out = append(out, Resource{Path: p, Op: op, Exact: exact})
		}
		return out
	}
}

// pathArgs reads the `path` argument in either declared shape.
func pathArgs(args json.RawMessage) []string {
	var raw struct {
		Path json.RawMessage `json:"path"`
	}
	if err := json.Unmarshal(args, &raw); err != nil || len(raw.Path) == 0 {
		return nil
	}
	if raw.Path[0] == '"' {
		var one string
		if err := json.Unmarshal(raw.Path, &one); err != nil {
			return nil
		}
		if p := strings.TrimSpace(one); p != "" {
			return []string{p}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw.Path, &many); err != nil {
		return nil
	}
	out := make([]string, 0, len(many))
	for _, p := range many {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// FromCommand turns a shell command line into resources, using the parsed facts
// rather than the text. This is the function that fixes the reported class of
// bug: the paths come from a parse, so /dev/null is a device node in /dev and
// not a path relative to the filesystem root.
//
// The operation for a path named as an argument is the program's business, so
// it is narrowed by asking the recognizer that already exists for exactly this
// question: a command proven read-only (judge.ReadOnlyShellCommand, the same
// predicate the permission gate uses to skip the reviewer) reads, and anything
// else is treated as read+write. Reusing that predicate rather than keeping a
// second list of read-only programs here is what keeps the two layers from
// disagreeing — and without the narrowing, a plain `cat /data/x` would ask
// again for write access the user never needed to grant, which is how a prompt
// becomes something people dismiss without reading.
func FromCommand(command string, opts Options) []Resource {
	plan := shellfacts.Parse(command)
	if !plan.Parsed {
		// An unreadable command line is not silently path-free. The parser's
		// half is lost, but the program-aware scanner's half still applies, so
		// the boundary is never narrower than it was before the parser existed.
		return resourcesFrom(opts.ScanOperands, command, opts, false)
	}
	readOnly := false
	if opts.ReadOnly != nil {
		readOnly = opts.ReadOnly(command)
	}
	var out []Resource
	seen := map[string]bool{}
	for _, a := range plan.PathAccesses() {
		p := strings.TrimSpace(a.Path)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		op := grants.ReadWrite
		switch {
		case a.Op&shellfacts.Write != 0:
			op = grants.ReadWrite
		case a.Op&shellfacts.Read != 0:
			op = grants.Read
		case a.Op&shellfacts.Execute != 0:
			// Running a program is the reviewer's question (what does it do?),
			// not a path grant: the gate records that the path is touched and
			// leaves the judgement above.
			op = grants.Execute
		case readOnly:
			op = grants.Read
		}
		out = append(out, Resource{
			Path:    a.Path,
			Op:      op,
			Dynamic: a.Dynamic,
			// Exact stays false: the offer is the path the command named. A
			// grant recorded for a path that happens to be a file covers only
			// that file anyway (the table matches the exact key before any
			// ancestor), so marking it exact would only suppress the offer
			// without narrowing anything.
			Exact:  false,
			Nested: a.Nested,
		})
	}

	// Union in the scanner's operands. The two halves see different things —
	// the parse knows shell semantics, the scanner knows programs — so a path
	// only one of them names must still reach the boundary. An operand the
	// parse already reported keeps its parsed operation.
	scanned := resourcesFrom(opts.ScanOperands, command, opts, true)
	for _, r := range scanned {
		if seen[r.Path] {
			continue
		}
		seen[r.Path] = true
		out = append(out, r)
	}
	return out
}

// resourcesFrom turns a scanner's operand list into resources. parsed reports
// whether a parse also ran: when it did, the scanner's paths can only add to
// what the parser saw, so their operation is the narrowed read; when it did not,
// nothing about the command is known and read+write is the honest answer.
func resourcesFrom(scan func(string) []string, command string, opts Options, parsed bool) []Resource {
	if scan == nil {
		return nil
	}
	op := grants.ReadWrite
	if parsed {
		op = grants.Read
		if opts.ReadOnly != nil && !opts.ReadOnly(command) {
			// A command that is not provably read-only may write, and a scanner
			// operand is a path the program was told to use.
			op = grants.ReadWrite
		}
	}
	var out []Resource
	for _, p := range scan(command) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, Resource{Path: p, Op: op})
	}
	return out
}

// Result is what the gate decided about one call.
type Result struct {
	// Ask is true when the call needs a question.
	Ask bool
	// Denied lists resources the grants table refuses. A refusal is a previous
	// answer, so the gate reports it instead of asking again.
	Denied []Resource
	// Pending lists the resources a question must cover: the ones that are
	// neither inside the workspace nor already granted nor refused.
	Pending []Resource
	// Unknown reports that the call's paths could not be determined at all
	// (an unparsable command line). A caller must treat this as "ask", never as
	// "nothing to ask about".
	Unknown bool
}

// Options configure Check.
type Options struct {
	// Store is the grants table. A nil store makes every path outside the
	// workspace pending, which is the behavior before any grant was ever made.
	Store *grants.Store
	// WorkspaceRoot is the directory the user opened.
	WorkspaceRoot string
	// Home resolves a leading tilde. Empty uses the process home.
	Home string
	// ReadOnly reports whether a shell command is provably read-only. It is
	// injected rather than imported: the recognizer lives in package judge,
	// which sits ABOVE this gate (the assembly point wires both), so a direct
	// dependency would be a cycle. A nil function narrows nothing — every
	// argument is then treated as possibly writing, which asks for more than
	// the command needs and never less. That is the safe default for a caller
	// that did not wire one.
	ReadOnly func(command string) bool
	// ScanOperands returns the path-shaped words a command line may read,
	// including the operands a PROGRAM's flags name (`sort --files0-from=/x`,
	// `grep -f /x`) — knowledge the shell parse does not have, because it
	// describes programs rather than syntax. It is injected for the same reason
	// ReadOnly is: the table of programs lives above this package.
	//
	// A nil function contributes nothing, which means a caller that wires only
	// ReadOnly still gets every path the parse can see; it simply loses the
	// flag-operand half. That direction under-asks, so the assembly point wires
	// both.
	ScanOperands func(command string) []string
}

// Check decides one tool call. tool is the tool name; args its arguments.
func Check(tool string, args json.RawMessage, opts Options) Result {
	extract, known := extractors[strings.ToLower(strings.TrimSpace(tool))]
	if !known {
		// A tool with no declared paths cannot cross the boundary through this
		// gate. Network tools are the deliberate case: their reach is the
		// network, which is the reviewer's question, not a path question.
		return Result{}
	}
	resources := extract(args, opts)

	// A shell call that did not yield a parse is unknown, not empty. This is
	// the one place the two are distinguishable, and conflating them is how a
	// command nobody could read would sail through unasked.
	if strings.EqualFold(tool, "bash") && len(resources) == 0 {
		if cmd := commandOf(args); cmd != "" && !shellfacts.Parse(cmd).Parsed {
			return Result{Ask: true, Unknown: true}
		}
	}

	root := opts.WorkspaceRoot
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Result{Ask: true, Unknown: true}
	}

	var out Result
	seen := map[string]bool{}
	for _, r := range resources {
		abs, err := permissions.AbsAgainst(absRoot, r.Path)
		if err != nil {
			// A path that cannot be resolved cannot be granted, so it is
			// pending: the question names the written form.
			r.Abs = ""
			if !seen[r.Path] {
				seen[r.Path] = true
				out.Pending = append(out.Pending, r)
			}
			continue
		}
		r.Abs = abs
		key := abs + "|" + r.Op.String()
		if seen[key] {
			continue
		}
		seen[key] = true

		if permissions.Inside(absRoot, abs) {
			continue
		}
		if insideSystem(abs) {
			// The sandbox admits these, so asking would only teach the user to
			// approve without reading. It is the same list the read-scope gate
			// uses, which is why /dev/null is quiet here.
			continue
		}
		if opts.Store != nil {
			switch v := opts.Store.Check(abs, r.Op); v.Decision {
			case grants.Allowed:
				continue
			case grants.Denied:
				out.Denied = append(out.Denied, r)
				continue
			}
		}
		out.Pending = append(out.Pending, r)
	}

	sortResources(out.Pending)
	if len(out.Denied) > 0 {
		return out
	}
	out.Ask = len(out.Pending) > 0
	return out
}

// insideSystem reports whether a path is in the always-readable system
// whitelist. It delegates to permissions so this gate and the sandbox keep one
// definition of "always readable" rather than two that drift.
func insideSystem(abs string) bool {
	// Canonicalize the query the same way the whitelist entries were built
	// (permissions.SystemReadRoots canonicalizes them): on macOS "/etc" and
	// "/private/etc" are one place, and a boundary that admits one spelling
	// while asking about the other is noise. Without this half, `cat /etc/hosts`
	// is asked about even though /etc is whitelisted.
	abs = permissions.Canonical(abs)
	for _, root := range permissions.SystemReadRoots() {
		if permissions.Inside(root, abs) {
			return true
		}
	}
	return false
}

// sortResources orders a question's rows: files before directories, then by
// path. A stable order means the same call always asks the same question.
func sortResources(rs []Resource) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Exact != rs[j].Exact {
			return rs[i].Exact
		}
		return rs[i].Path < rs[j].Path
	})
}

// commandOf reads a bash call's command text.
func commandOf(args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return ""
	}
	return strings.TrimSpace(a.Command)
}

// Scope is the grant a question may offer for one resource: the path a single
// "remember this" would cover, and whether it may be granted as a subtree.
type Scope struct {
	// Path is the directory a session grant would cover, or the file itself
	// when no wider grant is safe.
	Path string
	// Subtree reports whether Path may be granted for everything under it.
	Subtree bool
}

// Offer returns the grant each pending resource may offer.
//
// The rule: offer the directory the call named, never a parent of it. A command
// that reads /etc/hosts and /etc/hostname may offer /etc once; a command that
// reads one file offers that file. Widening to a common parent would hand over
// a tree nobody named, which is the over-grant this whole design exists to
// avoid — and it is why the offer is per resource rather than one merged path.
func Offer(resources []Resource, opts Options) []Scope {
	var out []Scope
	seen := map[string]bool{}
	for _, r := range resources {
		if r.Abs == "" {
			// Nothing resolvable to offer; the dialog keeps the one-read answer.
			continue
		}
		target := r.Abs
		subtree := !r.Exact
		if !subtree {
			target = filepath.Dir(r.Abs)
		}
		if _, broad := permissions.BroadReadRoot(target); broad {
			// Refuse to offer a root the store itself would reject, so the
			// dialog cannot promise something the grant call would then refuse.
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, Scope{Path: target, Subtree: subtree})
	}
	return out
}

// Tools returns the tool names this gate covers, sorted, so a test can assert
// the set rather than trusting the table was read.
func Tools() []string {
	out := make([]string, 0, len(extractors))
	for name := range extractors {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
