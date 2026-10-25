package judge

// The tool table: what this package knows about one tool, in the two ways a
// gate needs it.
//
// Every tool goes through the gate. Native file readers (readOnly + paths) are
// settled by the routine grader's local rule before JEV/LLM, because a call
// that can only read files should not spend a model round trip proving it; the
// static floor and boundary still run around that verdict. Other tools are
// graded by JEV/local and then the session reviewer as before.
//
// What remains is two facts, plus the extractor that reads a call's paths:
//
//   - paths and op: what the workspace-boundary gate asks about, and which
//     capability an answer records. A read grant says nothing about writing,
//     which is why the two are asked and recorded apart.
//   - readOnly: whether read-only mode permits the tool. That mode exists to
//     stop changes, so an in-memory tool belongs and the rest do not.
//
// The two are independent, and the entries that look alike are the proof:
// write_stdin is permitted in read-only mode (a poll changes nothing) while
// bash is not; goal_complete is the reverse — it changes nothing on the host
// either, and it is permitted for the same reason.
//
// A tool absent from this table is treated by the strict default: its paths are
// not extracted (so the boundary gate has nothing to ask about), and read-only
// mode refuses it. That default is for a plugin or a tool added later: the mode
// stays an allow-list, so a new mutating tool cannot slip through it.

import (
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/controlcap"
	"github.com/getan/golder/internal/grants"
)

// toolInfo is one tool's leniencies. A zero toolInfo is the strict default, so
// a field left out is a field withheld.
type toolInfo struct {
	// paths reads the path spellings a call names, which the workspace boundary
	// asks about (boundary.go). nil for a tool that names none.
	paths func(args json.RawMessage) []string
	// op is the capability the boundary gate asks about for those paths: Read
	// for a tool that only reads, Write for the one that writes. It is what
	// decides whether an answer grants reading or writing, and whether a row
	// already in the table settles the question — a read grant says nothing
	// about writing, which is why the two are asked and recorded apart.
	//
	// The zero value is Read, which is what every reader wants and what a tool
	// added later gets by default: the strict direction for a tool nobody has
	// classified is to ask about reading, never to assume writing is settled.
	op grants.Capability
	// readOnly permits the tool under read-only mode.
	readOnly bool
	// shell reports that the call's paths come from a command line, so a dialog
	// about it should show the command rather than a path.
	shell bool
}

var tools = map[string]toolInfo{
	// Readers: they name paths, and read-only mode exists for them.
	"read":       {paths: pathArgs, readOnly: true},
	"view_image": {paths: pathArgs, readOnly: true},
	"grep":       {paths: pathArgs, readOnly: true},
	"find":       {paths: pathArgs, readOnly: true},
	"ls":         {paths: pathArgs, readOnly: true},

	// The shell: its paths are words in a command line rather than an argument,
	// which is why it carries a reader of its own. It is not permitted in
	// read-only mode: a command can write.
	"bash": {paths: bashPaths, shell: true},

	// The one writer, and the only entry that asks about writing. A patch
	// reaching outside the workspace is the same kind of question a read
	// outside it is — consent about a place — so it takes the same three
	// answers (once, this directory for the session, remembered) and the same
	// gate. Its paths come from the patch text (every file it names, move
	// destinations included), because a rename out of the workspace writes
	// outside it as surely as an edit there.
	//
	// bash is deliberately not here. A command's own writes are the sandbox's
	// business — the writable-root registry and the escalation dialog are the
	// mechanism, and the approval gate runs them — so asking here as well would
	// put the same question twice and invite two different answers to it.
	"apply_patch": {paths: patchArgs, op: grants.Write},

	// Network reads: permitted in read-only mode because a mode that forbade
	// reading the web would be read-only in name only. They are graded like
	// everything else — a URL is not a path the boundary gate can ask about, so
	// the reviewer is the only layer that sees where it points, which makes
	// reviewing it the point rather than a cost.
	"webfetch":  {readOnly: true},
	"websearch": {readOnly: true},

	// In-memory and control tools: nothing on the host changes, so read-only
	// mode permits them. They are reviewed like everything else, which costs one
	// call each and buys the property this design is for — one rule, no
	// exceptions to reason about.
	"todo":                  {readOnly: true},
	"memory_search":         {readOnly: true},
	"schedule_list":         {readOnly: true},
	"get_context_remaining": {readOnly: true},
	"new_context":           {readOnly: true},
	// schedule_create and schedule_delete are the same family: session-local
	// reminders that never touch the host. Leaving them out of the table made
	// read-only mode refuse them while permitting schedule_list — an accident of
	// which one was listed, not a decision about the tools.
	"schedule_create": {readOnly: true},
	"schedule_delete": {readOnly: true},

	// write_stdin polls or interrupts a running session. Read-only mode must not
	// let a session be typed into — a tty session's stdin is a command line —
	// so it is not permitted there, and it is reviewed like everything else:
	// what a written line does is exactly the kind of question a reviewer
	// answers, and the command that opened the session is not the command this
	// call sends it.
	"write_stdin": {},

	// The goal signals end a loop this process is running. They change nothing
	// on the host, so read-only mode permits them; they are reviewed all the
	// same, because ending a goal is a decision worth a second opinion.
	"goal_complete": {readOnly: true},
	"goal_blocked":  {readOnly: true},

	// The tuios facade decides read-only by ACTION, not by tool name; see
	// readOnlyAllowedCall. The zero entry keeps the name known to the table.
	"tuios":     {paths: func(raw json.RawMessage) []string { return facadePaths("tuios", raw) }},
	"workspace": {paths: func(raw json.RawMessage) []string { return facadePaths("workspace", raw) }},
}

func facadePaths(tool string, raw json.RawMessage) []string {
	r := controlcap.Parse(raw)
	var paths []string
	if r.Action == "new" && r.Cwd != "" {
		paths = append(paths, r.Cwd)
	}
	for _, cmd := range controlcap.Commands(tool, raw) {
		if r.Cwd != "" {
			cmd = "cd " + shellQuotePath(r.Cwd) + " && " + cmd
		}
		args, _ := json.Marshal(map[string]string{"command": cmd})
		paths = append(paths, bashPaths(args)...)
	}
	return paths
}

func shellQuotePath(p string) string { return "'" + strings.ReplaceAll(p, "'", "'\\''") + "'" }

// toolLookup normalizes a tool name and returns its entry.
func toolLookup(name string) (toolInfo, bool) {
	t, ok := tools[strings.ToLower(strings.TrimSpace(name))]
	return t, ok
}

// readOnlyAllowed reports whether read-only mode permits the tool. An unknown
// tool is refused, which is what keeps the mode an allow-list: plugin and future
// tools stay blocked until someone classifies them, so a new mutating tool
// cannot silently bypass the mode.
func readOnlyAllowed(name string) bool {
	t, ok := toolLookup(name)
	return ok && t.readOnly
}

// readOnlyAllowedCall is the action-aware form. The tuios facade carries both
// read and control actions, so /permissions read-only must inspect its action
// instead of treating the whole tool as writable.
func readOnlyAllowedCall(name string, args json.RawMessage) bool {
	if strings.EqualFold(strings.TrimSpace(name), "tuios") {
		var a struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(a.Action)) {
		case "list", "capture", "wait":
			return true
		}
		return false
	}
	return readOnlyAllowed(name)
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

// boundaryOp returns the capability the boundary gate asks about for this
// tool's paths: Read unless the table says otherwise.
func boundaryOp(name string) grants.Capability {
	t, ok := toolLookup(name)
	if !ok || t.op == 0 {
		return grants.Read
	}
	return t.op
}

// TouchedPaths returns the path spellings a call names, for a caller outside
// this package that needs the same extraction the boundary gate uses. It is how
// the reviewer's state reports what a call touches, so the model reasons about
// the same set the gate asks about rather than a second, drifting reading of
// the arguments — a mismatch between the two is the failure the boundary
// regression test exists to catch.
func TouchedPaths(tool string, args json.RawMessage) []string {
	return boundaryPaths(tool, args)
}
