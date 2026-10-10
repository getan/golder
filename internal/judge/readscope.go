package judge

// The read-scope gate: reading outside the workspace needs the user's answer.
//
// The workspace boundary is the directory the user opened. Crossing it is a
// question of consent rather than of risk, which is why this gate is a sibling
// of the permission gate instead of a tier inside it, and why no approval mode
// waives it: read-only, ask, auto and full-access all ask before a file outside
// the workspace is read. full-access turns off review and sandboxing, not the
// user's say over where data comes from, and a run with no prompt available
// (headless, a sub-agent thread with no UI) fails closed with guidance rather
// than guessing.
//
// Two answers are possible, and they are deliberately different sizes:
//
//   - "just this read" is a one-call grant. It travels in the executor context
//     (agentcore.WithReadGrant) and is consumed by the call it was asked for.
//   - "this directory" is a session grant in permissions.ReadRoots, which the
//     file tools consult and the OS sandbox's read whitelist includes, so the
//     next read there does not ask again.
//
// The directory offered for the second answer is never so broad that granting
// it would remove the boundary (permissions.BroadReadRoot refuses the
// filesystem root, the home directory and its parent); when even the file's
// directory is too broad, only the one-call answer is offered.
//
// Credential material is not on the table at all: a path the static floor
// refuses is passed through untouched, so the floor denies it and no dialog
// invites the user to hand over ~/.ssh or ~/.zshrc by pressing a key.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// readScopeTools are the read-only tools whose path argument is subject to the
// workspace boundary. The mutating tool (apply_patch) is not here: a read grant
// says nothing about writing, and widening writes is the sandbox's writable
// roots business.
var readScopeTools = map[string]bool{
	"read":       true,
	"view_image": true,
	"grep":       true,
	"find":       true,
	"ls":         true,
}

// ReadScopeOpts configures ReadScopeGate. Prompting follows the permission
// gate's two shapes: interactive drivers pass In/Out (the REPL's shared stdin),
// the TUI passes Confirm (it has no stdin), and a driver with neither gets the
// fail-closed path.
type ReadScopeOpts struct {
	// WorkspaceRoot is the directory the file tools treat as the boundary.
	// Empty means the process working directory, the same default the tools
	// use.
	WorkspaceRoot string
	// Roots are additional trusted read roots that are never asked about,
	// because golder itself advertised them (the skills directory is the one
	// case: the system prompt hands the model absolute SKILL.md paths).
	Roots []string
	In    *bufio.Reader
	Out   io.Writer
	Mu    *sync.Mutex
	// Confirm is the driver's approval dialog for a call that needs an answer.
	// It runs on the run goroutine and may block until the user answers.
	Confirm func(ctx context.Context, req ApprovalRequest) ApprovalAnswer
	// Notify receives the decision so the driver can show it, exactly as the
	// permission gate announces its verdicts.
	Notify func(Note)
}

// ReadScopeGate builds the beforeToolCall gate that asks before a read-only
// tool reaches outside the workspace. It returns nil when there is no gate to
// build (no resolvable workspace root); callers chain it in front of the
// permission gate so consent is settled before the call is graded.
func ReadScopeGate(opts ReadScopeOpts) agentcore.BeforeToolCallFunc {
	root := strings.TrimSpace(opts.WorkspaceRoot)
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		root = wd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	interactive := opts.In != nil && opts.Out != nil
	notify := func(n Note) {
		if opts.Notify != nil {
			opts.Notify(n)
		}
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		raw, ok := readScopePath(call.Name, call.Arguments)
		if !ok {
			return nil
		}
		target, err := permissions.AbsAgainst(absRoot, raw)
		if err != nil {
			return nil
		}
		if permissions.Inside(absRoot, target) || insideAnyRoot(opts.Roots, target) {
			return nil
		}
		// Credential material is the static floor's to refuse. Passing it
		// through unchanged means the floor denies the call and no dialog
		// offers to grant it.
		if _, bad := credentialPathRead(raw); bad {
			return nil
		}
		if permissions.ReadableAt(target) {
			return nil
		}

		candidate := permissions.ReadScopeCandidate(target)
		lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
		req := ApprovalRequest{
			Kind:          ApprovalReadScope,
			Tool:          call.Name,
			Summary:       raw,
			ReadScope:     candidate,
			Authorization: "outside the workspace",
			Rationale:     target + " is outside the workspace",
		}
		var ans ApprovalAnswer
		switch {
		case interactive:
			ans = promptReadScope(ctx, opts, target, candidate)
		case opts.Confirm != nil:
			ans = opts.Confirm(ctx, req)
		}
		if !ans.Answered || !ans.Approve {
			rationale := "denied by the user"
			if !ans.Answered {
				rationale = "no prompt available"
			}
			notify(Note{
				Tool: call.Name, ToolCallID: call.ID, Kind: NoteDenied, Level: Deny, Lang: lang,
				Summary: target, Authorization: req.Authorization, Rationale: rationale,
			})
			return blockReadScope(call, target, candidate, ans.Answered)
		}
		// "This directory" registers a session grant; "just this read" rides
		// the call's context. A directory the registry refuses (too broad, or
		// an invalid spelling) falls back to the one-call grant rather than
		// failing the read the user just approved.
		if granted := strings.TrimSpace(ans.ReadRoot); granted != "" {
			if _, err := permissions.AddReadRoot(granted); err == nil {
				notify(Note{
					Tool: call.Name, ToolCallID: call.ID, Kind: NoteApproved, Level: Allow, Lang: lang,
					Summary: target, Authorization: req.Authorization,
					Rationale: "reading " + granted + " is allowed for this session",
				})
				return nil
			}
		}
		notify(Note{
			Tool: call.Name, ToolCallID: call.ID, Kind: NoteApproved, Level: Allow, Lang: lang,
			Summary: target, Authorization: req.Authorization,
			Rationale: "approved for this read only",
		})
		return &agentcore.BeforeToolCallDecision{ReadGrant: target}
	}
}

// readScopePath returns a read tool call's path argument. Tools that read
// nothing (or read the workspace root itself, where the argument is empty) are
// not boundary questions.
func readScopePath(tool string, args json.RawMessage) (string, bool) {
	if !readScopeTools[strings.ToLower(strings.TrimSpace(tool))] {
		return "", false
	}
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", false
	}
	p := strings.TrimSpace(a.Path)
	return p, p != ""
}

// insideAnyRoot reports whether target is inside one of the extra trusted roots.
func insideAnyRoot(roots []string, target string) bool {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if permissions.Inside(root, target) {
			return true
		}
	}
	return false
}

// promptReadScope asks on stdin (interactive drivers only). The lock is held
// across the prompt so a parallel batch cannot interleave two questions, the
// same contract promptRisk follows.
func promptReadScope(ctx context.Context, opts ReadScopeOpts, target, candidate string) ApprovalAnswer {
	if opts.Mu != nil {
		opts.Mu.Lock()
		defer opts.Mu.Unlock()
	}
	out := opts.Out
	if out == nil {
		return ApprovalAnswer{}
	}
	fmt.Fprintf(out, "\ngolder: %s is outside the workspace.\n", target)
	if candidate != "" {
		fmt.Fprintf(out, "  [y] allow this read once   [a] allow reading %s (this session)   [N] deny\n", candidate)
	} else {
		fmt.Fprint(out, "  [y] allow this read once   [N] deny\n")
	}
	fmt.Fprint(out, "Choice: ")
	// The read is interruptible: Ctrl+C during the prompt cancels the run and
	// yields a denial immediately instead of trapping the user.
	line, ok := readLineOrCancel(ctx, opts.In)
	if !ok {
		return ApprovalAnswer{}
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return ApprovalAnswer{Approve: true, Answered: true}
	case "a", "all":
		if candidate != "" {
			return ApprovalAnswer{Approve: true, ReadRoot: candidate, Answered: true}
		}
	}
	return ApprovalAnswer{Answered: true}
}

// blockReadScope refuses a read outside the workspace with guidance the model
// can act on: which path, which directory would cover it, and how a person
// makes that permanent.
func blockReadScope(call agentcore.AgentToolCall, target, candidate string, answered bool) *agentcore.BeforeToolCallDecision {
	msg := fmt.Sprintf("tool %q blocked: %s is outside the workspace and the read was not approved", call.Name, target)
	if candidate != "" {
		msg += "; approving it, or adding the directory to [permissions] readable_roots in config.toml (or GOLDER_READABLE_ROOTS), would allow it"
	} else {
		msg += "; adding a directory above it to [permissions] readable_roots in config.toml (or GOLDER_READABLE_ROOTS) would allow it"
	}
	if !answered {
		msg += " — no prompt is available in this run, so ask the user to run it interactively or to configure the root"
	}
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}
