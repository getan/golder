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
// The directory offered for the second answer is as narrow as the read that
// asked for it — the path itself when the call named a directory, the
// directory holding it when it named a file — and never so broad that granting
// it would remove the boundary (permissions.BroadReadRoot refuses the
// filesystem root, the home directory and its parent); when even that
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
	"github.com/getan/golder/internal/accessgate"
	"github.com/getan/golder/internal/grants"
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

// readScopeShellTools are the command tools whose file operands are subject to
// the same boundary. They name no path argument — the paths are words in the
// command — so the gate reads them out of the command line (bashpaths.go). That
// reading is best-effort: what it cannot see (a variable's value, a script's
// own reads) stays the sandbox's business, but the common spellings are asked
// about like any other out-of-workspace read.
var readScopeShellTools = map[string]bool{"bash": true}

// readScopeTargets returns the raw path spellings this call may read: the path
// argument for the file tools, or the file operands a command line exposes.
// Nil means the call carries no path the boundary applies to.
func readScopeTargets(tool string, args json.RawMessage) []string {
	if paths := readScopePaths(tool, args); len(paths) > 0 {
		return paths
	}
	if !readScopeShellTools[strings.ToLower(strings.TrimSpace(tool))] {
		return nil
	}
	cmd, ok := bashCommand(args)
	if !ok {
		return nil
	}
	return shellTargets(cmd)
}

// shellTargets extracts the paths a command line names.
//
// The parse comes first because it sees what a word-scanner cannot: a redirect
// target that never appears as an argument, a path inside a command
// substitution, and — the reported confusion — the true spelling of a path, so
// /dev/null is the device node the user typed rather than a path relative to
// the filesystem root.
//
// The result is the UNION of the parsed paths and the word-scanner's, and that
// is not belt-and-braces — each sees something the other cannot.
//
//   - The parser knows shell semantics: redirects, substitutions, quote
//     removal. It does not know PROGRAMS, which is why it cannot tell that
//     `sort --files0-from=/outside/list` names a file to read.
//   - The scanner knows the programs: bashpaths.go declares each whitelisted
//     program's file-reading flags (`sort --files0-from`, `grep -f`, `date -r`)
//     and its interpreter `-c` forms. It does not know shell semantics.
//
// Keeping only the parser silently dropped the scanner's half, and the boundary
// regression test caught it: `sort --files0-from=/outside/list` became neither
// asked about nor reviewed. For a consent gate the safe direction is to ask
// about more, not less, so the two are unioned rather than ranked.
func shellTargets(cmd string) []string {
	// The extraction itself lives in accessgate, which is also what the unified
	// access gate uses. Keeping one implementation matters more here than
	// anywhere else in this package: the two halves it unions (the shell parse
	// and the program-aware word scanner) are exactly what a duplicate would let
	// drift, and a boundary that asks about different paths depending on which
	// caller reached it is worse than either list alone.
	//
	// Both halves are injected rather than imported by accessgate for the same
	// reason this package injects them: the scanner and the read-only recognizer
	// live HERE, and accessgate sits below (it cannot import this package
	// without a cycle). So the dependency points down, and the knowledge flows
	// up through the callback fields.
	res := accessgate.FromCommand(cmd, accessgate.Options{
		ReadOnly:     ReadOnlyShellCommand,
		ScanOperands: bashPathCandidates,
	})
	out := make([]string, 0, len(res))
	for _, r := range res {
		if p := strings.TrimSpace(r.Path); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// insideSystemRoots reports whether p lives in the always-readable system
// whitelist. Asking about /usr/bin in every command that runs a program would
// train people to approve without reading, and the sandbox admits these paths
// anyway, so a path there is not a boundary question.
func insideSystemRoots(p string) bool {
	// The comparison lives in permissions so this gate and the sandbox cannot
	// disagree about "always readable": it checks both the literal spelling
	// (what the sandbox's own rules use) and the symlink-resolved one (where
	// "/tmp/x" and "/private/tmp/x" are one place).
	return permissions.InsideSystemReadRoots(p)
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
	// Persist, when set, makes a read grant survive the process: the gate
	// calls it after the user chooses the remembered option and reports the
	// outcome in the note. It is a callback rather than a direct write because
	// this package is a leaf (it cannot import the config layer that owns the
	// managed store); a driver without one simply cannot offer the remembered
	// answer, and the prompt says so.
	Persist func(dir string) (made bool, why string)
	// OnPrompt, when set, is told the question just before a prompt is put to
	// the person and returns the function to call once it is answered. See
	// GateOpts.OnPrompt: it is how a driver tells the terminal around it that
	// this pane waits for a human (internal/hostcompat).
	OnPrompt func(question string) func()
}

// promptNote is ReadScopeOpts' half of GateOpts.promptNote.
func (o *ReadScopeOpts) promptNote(question string) func() {
	if o.OnPrompt == nil {
		return func() {}
	}
	if done := o.OnPrompt(question); done != nil {
		return done
	}
	return func() {}
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
		// A call may name several paths (a command line lists file operands).
		// One question covers the call, so it offers the finest directory any
		// of those reads would need: a command that names a directory and a
		// subtree inside it (`ls ~/work ~/work/proj`) must ask about the
		// subtree, not hand over the whole ~/work just because that operand
		// came first — the coarse answer is a session grant, so it would
		// outlive the call. A path whose offered directory holds another's is
		// the coarser answer and loses to it; ties keep command order.
		type question struct{ raw, target, candidate string }
		var questions []question
		for _, candidateRaw := range readScopeTargets(call.Name, call.Arguments) {
			resolved, err := permissions.AbsAgainst(absRoot, candidateRaw)
			if err != nil {
				continue
			}
			if permissions.Inside(absRoot, resolved) || insideAnyRoot(opts.Roots, resolved) {
				continue
			}
			// Credential material is the static floor's to refuse. Passing it
			// through unchanged means the floor denies the call and no dialog
			// offers to grant it.
			if _, bad := credentialPathRead(candidateRaw); bad {
				continue
			}
			if permissions.ReadableAt(resolved) {
				continue
			}
			// The unified table is consulted alongside the legacy read-root
			// registry, and either one silences the question. Two readers is
			// deliberate during the changeover: the registry is what
			// config.toml and GOLDER_READABLE_ROOTS seed, while the table is
			// what every dialog answer now records, so a path granted through
			// either route is a path the user already decided about.
			if grants.Allows(resolved, grants.Read) {
				continue
			}
			// The system whitelist is not a boundary question: the sandbox
			// admits these paths, so asking would only train blind approval.
			if insideSystemRoots(resolved) {
				continue
			}
			questions = append(questions, question{
				raw: candidateRaw, target: resolved,
				candidate: permissions.ReadScopeCandidate(resolved),
			})
		}
		if len(questions) == 0 {
			return nil
		}
		chosen := questions[0]
		for _, q := range questions[1:] {
			switch {
			case q.candidate == "":
				// Too broad to offer a directory at all: never finer than a
				// path that can.
			case chosen.candidate == "":
				chosen = q
			case chosen.candidate != q.candidate && permissions.Inside(chosen.candidate, q.candidate):
				chosen = q
			}
		}
		raw, target := chosen.raw, chosen.target

		candidate := chosen.candidate
		lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
		// The dialog shows the path for a file tool and the command for a
		// shell tool: the path is what the user is deciding about, but for a
		// command they also need to see what will run.
		summary := raw
		if cmd, ok := bashCommand(call.Arguments); ok && readScopeShellTools[strings.ToLower(strings.TrimSpace(call.Name))] {
			summary = cmd
		}
		req := ApprovalRequest{
			Kind:          ApprovalReadScope,
			Tool:          call.Name,
			Summary:       summary,
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
				// Record the same answer in the unified table. Both readers stay
				// live during the changeover (see the check above), so writing
				// here is what makes the table the record of what the user
				// actually decided — which is what /trust lists and what the
				// next session loads.
				life := grants.Session
				if ans.Persisted {
					life = grants.Remembered
				}
				_, _ = grants.Add(grants.Grant{
					Path:     granted,
					Allow:    grants.Read,
					Scope:    grants.Subtree,
					Lifetime: life,
					Source:   "read dialog",
				})
				rationale := "reading " + granted + " is allowed for this session"
				if ans.Persisted {
					rationale = "reading " + granted + " is allowed and remembered across sessions"
				}
				notify(Note{
					Tool: call.Name, ToolCallID: call.ID, Kind: NoteApproved, Level: Allow, Lang: lang,
					Summary: target, Authorization: req.Authorization,
					Rationale: rationale,
				})
				return nil
			}
		}
		// The single-read answer is a decision too, and the narrowest one: it
		// covers exactly the path that was asked about, for this session only.
		// Recording it is what lets a repeated question about the same file stop
		// being asked — the alternative is that "allow once" leaves no trace and
		// the same command asks again next turn.
		if target != "" {
			_, _ = grants.Add(grants.Grant{
				Path:     target,
				Allow:    grants.Read,
				Scope:    grants.Exact,
				Lifetime: grants.Session,
				Source:   "read dialog (once)",
			})
		}
		notify(Note{
			Tool: call.Name, ToolCallID: call.ID, Kind: NoteApproved, Level: Allow, Lang: lang,
			Summary: target, Authorization: req.Authorization,
			Rationale: "approved for this read only",
		})
		// The one-call grant rides the call's context, where the file tools
		// honour it once. A command tool does not read that grant — it cannot,
		// the path is inside a command it will hand to a shell — so for a
		// command the approval means "run it this time", and the only standing
		// constraint remains the sandbox. That is why the dialog offers the
		// directory answer for commands too.
		return &agentcore.BeforeToolCallDecision{ReadGrant: target}
	}
}

// readScopePaths returns a read tool call's path argument(s). grep and find
// accept a directory or a list of them, so both JSON shapes are read: a list
// the gate could not see would let a search reach outside the workspace with
// no question asked. Tools that read nothing, or read the workspace root
// itself (where the argument is empty), are not boundary questions.
func readScopePaths(tool string, args json.RawMessage) []string {
	if !readScopeTools[strings.ToLower(strings.TrimSpace(tool))] {
		return nil
	}
	var raw struct {
		Path json.RawMessage `json:"path"`
	}
	if err := json.Unmarshal(args, &raw); err != nil || len(raw.Path) == 0 {
		return nil
	}
	// The string form: what ls, read and view_image declare.
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
	// The list form (grep/find); a JSON null decodes to no paths.
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
	// The pane is about to wait for the person: say so before the read starts,
	// and say it is over when the answer lands, however this prompt travels.
	done := opts.promptNote("allow reading " + target)
	defer done()
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
		if opts.Persist != nil {
			// The remembered rung is offered only where a driver can actually
			// write it down: an option that silently forgets would be worse
			// than one that is absent.
			fmt.Fprintf(out, "  [y] allow this read once   [a] allow reading %s (this session)"+
				"   [s] allow reading %s (remembered)   [N] deny\n", candidate, candidate)
		} else {
			fmt.Fprintf(out, "  [y] allow this read once   [a] allow reading %s (this session)   [N] deny\n", candidate)
		}
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
	case "s", "save":
		if candidate != "" && opts.Persist != nil {
			made, why := opts.Persist(candidate)
			ans := ApprovalAnswer{Approve: true, ReadRoot: candidate, Answered: true}
			if made {
				ans.Persisted = true
			} else if why != "" {
				fmt.Fprintf(out, "  %s\n", why)
			}
			return ans
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
