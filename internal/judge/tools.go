package judge

// The tool table: every tool that earns a leniency, in the three ways this
// package grades one.
//
// It replaces four tables that each described the same tools from one angle —
// which tools skip the reviewer, which are permitted in read-only mode, which
// carry read paths the workspace boundary applies to, and how to read a path
// out of the arguments. The overlap was not a coincidence: read, grep, find, ls
// and view_image appeared in all of them. That is one fact per tool written
// four times, so a tool added to one table and forgotten in another was a bug
// waiting for a caller — and the fourth table, living in another package,
// could not even see the first three.
//
// The three facts are independent, and the two entries that look alike are the
// proof: write_stdin is ungraded (the command it feeds was already graded when
// bash spawned it, so re-grading a poll proves nothing) but is NOT permitted in
// read-only mode (a mode that lets a running session be typed into is not
// read-only); goal_complete is the reverse.
//
// A tool absent from this table is graded by the strict default: reviewed, and
// refused by read-only mode. Fail-safe in both directions — a new mutating tool
// cannot slip through the mode, and a new read-only tool pays a review rather
// than escaping one. Only tools that get a leniency need an entry.

import (
	"encoding/json"
	"strings"
)

// toolInfo is one tool's leniencies. A zero toolInfo is the strict default, so
// a field left out is a field withheld.
type toolInfo struct {
	// paths reads the path spellings a call names, which the workspace boundary
	// asks about (boundary.go). nil for a tool that names none.
	paths func(args json.RawMessage) []string
	// ungraded skips the risk reviewer: the tool cannot change anything on the
	// host, so there is no risk for a reviewer to weigh and grading it only adds
	// latency.
	ungraded bool
	// readOnly permits the tool under read-only mode.
	readOnly bool
	// shell reports that the call's paths come from a command line, so a dialog
	// about it should show the command rather than a path.
	shell bool
}

var tools = map[string]toolInfo{
	// Readers: they name paths, they change nothing, and read-only mode exists
	// for them.
	"read":       {paths: pathArgs, ungraded: true, readOnly: true},
	"view_image": {paths: pathArgs, ungraded: true, readOnly: true},
	"grep":       {paths: pathArgs, ungraded: true, readOnly: true},
	"find":       {paths: pathArgs, ungraded: true, readOnly: true},
	"ls":         {paths: pathArgs, ungraded: true, readOnly: true},

	// The shell: its paths are words in a command line rather than an argument,
	// which is why it carries a reader of its own. It is neither ungraded (what
	// a command does is the reviewer's whole question) nor permitted in
	// read-only mode.
	"bash": {paths: bashPaths, shell: true},

	// The one writer (apply_patch) is absent on purpose, and the reason is the
	// shape of the answer rather than its size. Every question this table feeds
	// is settled with a READ grant ("allow reading <dir>"), which is why the
	// boundary gate is not the gate for a write: a write question needs the
	// sandbox's writable-root registry and the escalation dialog. An
	// out-of-workspace write has no consent path at all — the tool's own
	// resolveWithin refuses it outright, with no prompt and no grant — so such a
	// patch is refused rather than asked about, and only the reviewer sees it.

	// Network reads. ungraded because the reviewer would be grading a URL, which
	// is neither a path to consent to nor a mutation to weigh; read-only because
	// a mode that forbade reading the web would be read-only in name only.
	"webfetch":  {ungraded: true, readOnly: true},
	"websearch": {ungraded: true, readOnly: true},

	// In-memory and control tools: nothing on the host changes, so an LLM review
	// could only block a benign control call on a verdict with no sandbox tier
	// to fall back on.
	"todo":                  {ungraded: true, readOnly: true},
	"memory_search":         {ungraded: true, readOnly: true},
	"schedule_list":         {ungraded: true, readOnly: true},
	"get_context_remaining": {ungraded: true, readOnly: true},
	"new_context":           {ungraded: true, readOnly: true},

	// write_stdin polls or interrupts a session whose command was graded when
	// bash spawned it — it cannot start anything new, so it needs no review —
	// but read-only mode must not let a session be typed into.
	"write_stdin": {ungraded: true},

	// The goal signals end a loop this process is running. They change nothing
	// on the host, so read-only mode permits them; they are reviewed all the
	// same, because ending a goal is a decision worth a second opinion.
	"goal_complete": {readOnly: true},
	"goal_blocked":  {readOnly: true},
}

// toolLookup normalizes a tool name and returns its entry.
func toolLookup(name string) (toolInfo, bool) {
	t, ok := tools[strings.ToLower(strings.TrimSpace(name))]
	return t, ok
}

// ungraded reports whether the tool skips the reviewer. An unknown tool does
// not: a tool this table has not been taught is graded like a mutating one.
func ungraded(name string) bool {
	t, ok := toolLookup(name)
	return ok && t.ungraded
}

// readOnlyAllowed reports whether read-only mode permits the tool. An unknown
// tool is refused, which is what keeps the mode an allow-list: plugin and future
// tools stay blocked until someone classifies them, so a new mutating tool
// cannot silently bypass the mode.
func readOnlyAllowed(name string) bool {
	t, ok := toolLookup(name)
	return ok && t.readOnly
}

// shellTool reports whether the tool's paths come from a command line.
func shellTool(name string) bool {
	t, ok := toolLookup(name)
	return ok && t.shell
}

// boundaryPaths returns the paths a call names for the workspace boundary to
// ask about. Nil means the call carries no path the boundary applies to — a
// tool that names none, or one this table has not been taught.
func boundaryPaths(name string, args json.RawMessage) []string {
	t, ok := toolLookup(name)
	if !ok || t.paths == nil {
		return nil
	}
	return t.paths(args)
}
