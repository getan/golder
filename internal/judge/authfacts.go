package judge

// What the session already knows about the paths a call touches, for the
// reviewer's state.
//
// The gates in front of the reviewer settle consent: a read outside the
// workspace was either approved, refused, or silenced by a grant the user made
// earlier. The reviewer then grades the call — and without these facts it
// grades it blind, because the same text means two different things depending
// on what already happened. `cat ~/proj/notes.md` is a question when nobody has
// approved ~/proj and a settled read when the user granted it a moment ago; a
// reviewer that cannot tell them apart is over-blocking in the first case or
// inventing consent in the second.
//
// Two properties are deliberate:
//
//   - The facts are computed by the gate's own predicates (boundaryQuestion,
//     permissions.InsideResolved), never by a second reading of the arguments.
//     A reviewer told something the gate does not believe is worse than a
//     reviewer told nothing: it would decide on a state that does not exist.
//   - Paths are reported resolved, in the same spelling the gate compares, so a
//     symlink inside the workspace that lands outside it is reported as outside
//     (the P0 escape, which was exactly this comparison done textually).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// AuthFact is what is known about one path a call touches.
type AuthFact struct {
	// Path is the resolved absolute path.
	Path string `json:"path"`
	// InWorkspace reports that the path lands inside the directory the user
	// opened, or inside a root golder itself advertised (the skills directory).
	InWorkspace bool `json:"inWorkspace"`
	// NeedsConsent reports that the boundary gate puts a question about this
	// path: outside the workspace, unread-approved, not credential material and
	// not on the system whitelist. It is the gate's own answer, so a model that
	// sees false knows consent is already settled — by the workspace, by a
	// grant, or by a refusal that will block the call.
	NeedsConsent bool `json:"needsConsent,omitempty"`
	// Read, Write and Execute are the authorization table's decision for this
	// path: "granted", "refused", or empty when nothing speaks to it. The three
	// are reported separately because the table keeps them separate — a
	// directory trusted for running commands does not authorize rewriting it.
	Read    string `json:"read,omitempty"`
	Write   string `json:"write,omitempty"`
	Execute string `json:"execute,omitempty"`
}

// AuthFacts describes the paths one tool call touches. It returns nil when the
// call names no path this package can read, which is the honest answer for a
// tool with no path arguments and for one the tool table has not been taught.
func AuthFacts(tool string, args json.RawMessage, workspaceRoot string, extraRoots []string, writableAt func(string) bool) []AuthFact {
	paths := TouchedPaths(tool, args)
	if len(paths) == 0 {
		return nil
	}
	op := boundaryOp(tool)
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		// The gate falls back to the process working directory when no root was
		// given (BoundaryGate does the same), so the facts must too — otherwise
		// the reviewer is told about a boundary the gate is not enforcing.
		if wd, err := os.Getwd(); err == nil {
			root = wd
		}
	}
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			root = permissions.Canonical(abs)
		}
	}
	var out []AuthFact
	seen := map[string]bool{}
	for _, raw := range paths {
		abs, err := permissions.AbsAgainst(root, raw)
		if err != nil {
			// A path that cannot be resolved cannot be described. Reporting it
			// unresolved would hand the model a spelling nobody can place, so it
			// is left out; the raw argument is still in the state.
			continue
		}
		resolved := permissions.Canonical(abs)
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, AuthFact{
			Path:         resolved,
			InWorkspace:  permissions.Inside(root, resolved) || insideAnyRoot(extraRoots, resolved),
			NeedsConsent: root != "" && boundaryQuestion(raw, resolved, root, extraRoots, op, writableAt),
			Read:         decision(resolved, grants.Read),
			Write:        decision(resolved, grants.Write),
			Execute:      decision(resolved, grants.Execute),
		})
	}
	return out
}

// decision renders the table's answer for one capability. The words are the
// ones the dialogs use, so a note, a log line and this state agree.
func decision(path string, cap grants.Capability) string {
	switch grants.Check(path, cap).Decision {
	case grants.Allowed:
		return "granted"
	case grants.Denied:
		return "refused"
	default:
		// Undecided is reported as silence rather than the word: a field that
		// says "undecided" invites a reader to treat it as a third answer, when
		// what it means is that nothing in the table speaks to this path.
		return ""
	}
}
