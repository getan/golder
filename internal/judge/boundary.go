package judge

// The boundary gate: reading outside the workspace needs the user's answer.
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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

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

// BoundaryOpts configures BoundaryGate. Prompting follows the permission
// gate's two shapes: interactive drivers pass In/Out (the REPL's shared stdin),
// the TUI passes Confirm (it has no stdin), and a driver with neither gets the
// fail-closed path.
type BoundaryOpts struct {
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

// promptNote is BoundaryOpts' half of GateOpts.promptNote.
func (o *BoundaryOpts) promptNote(question string) func() {
	if o.OnPrompt == nil {
		return func() {}
	}
	if done := o.OnPrompt(question); done != nil {
		return done
	}
	return func() {}
}

// BoundaryGate builds the beforeToolCall gate that asks before a read-only
// tool reaches outside the workspace. It returns nil when there is no gate to
// build (no resolvable workspace root); callers chain it in front of the
// permission gate so consent is settled before the call is graded.
func BoundaryGate(opts BoundaryOpts) agentcore.BeforeToolCallFunc {
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
	// Resolve the root the way the kernel will, so the containment test below
	// compares locations rather than spellings.
	absRoot = permissions.Canonical(absRoot)
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
		for _, candidateRaw := range boundaryPaths(call.Name, call.Arguments) {
			resolved, err := permissions.AbsAgainst(absRoot, candidateRaw)
			if err != nil {
				continue
			}
			// Resolve before judging: a symlink inside the workspace can point
			// outside it, and a textual comparison would call that in-workspace
			// and stay silent — the escape the tools' own guard missed too.
			resolved = permissions.Canonical(resolved)
			if !boundaryQuestion(candidateRaw, resolved, absRoot, opts.Roots) {
				continue
			}
			questions = append(questions, question{
				raw: candidateRaw, target: resolved,
				candidate: permissions.BoundaryCandidate(resolved),
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
		if cmd, ok := bashCommand(call.Arguments); ok && shellTool(call.Name) {
			summary = cmd
		}
		req := ApprovalRequest{
			Kind:          ApprovalBoundary,
			Tool:          call.Name,
			Summary:       summary,
			BoundaryDir:   candidate,
			Authorization: "outside the workspace",
			Rationale:     target + " is outside the workspace",
		}
		var ans ApprovalAnswer
		switch {
		case interactive:
			ans = promptBoundary(ctx, opts, target, candidate)
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
			return blockBoundary(call, target, candidate, ans.Answered)
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

// boundaryQuestion reports whether one path needs the user's answer, given the
// path as written and as resolved. It is the whole test, in one place, because
// two callers need the same answer and must not drift:
//
//   - the gate below, which asks the question;
//   - the reviewer's state (AuthFacts), which tells the model whether a consent
//     question is in front of this call — a model told "outside the workspace"
//     without "and already granted" either re-asks what the user just approved
//     or approves what nobody has seen.
//
// Each clause is a reason the question is not worth asking:
//
//   - inside the workspace, or inside a root the session treats as its own (the
//     skills directory golder advertises itself);
//   - credential material, which is the static floor's to refuse: passing it
//     through untouched means the floor denies the call and no dialog offers to
//     grant what the user should never hand over by pressing a key;
//   - already granted, through either reader — the legacy read-root registry
//     (what config.toml and GOLDER_READABLE_ROOTS seed) or the authorization
//     table (what every dialog answer records now);
//   - the system whitelist, which the sandbox admits anyway, so asking would
//     only train people to approve without reading.
func boundaryQuestion(raw, resolved, absRoot string, extraRoots []string) bool {
	if permissions.Inside(absRoot, resolved) || insideAnyRoot(extraRoots, resolved) {
		return false
	}
	if _, bad := credentialPathRead(raw); bad {
		return false
	}
	if permissions.ReadableAt(resolved) {
		return false
	}
	if grants.Allows(resolved, grants.Read) {
		return false
	}
	if insideSystemRoots(resolved) {
		return false
	}
	return true
}

// insideAnyRoot reports whether target is inside one of the extra trusted roots.
func insideAnyRoot(roots []string, target string) bool {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if permissions.InsideResolved(root, target) {
			return true
		}
	}
	return false
}

// promptBoundary asks on stdin (interactive drivers only). The lock is held
// across the prompt so a parallel batch cannot interleave two questions, the
// same contract promptRisk follows.
func promptBoundary(ctx context.Context, opts BoundaryOpts, target, candidate string) ApprovalAnswer {
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

// blockBoundary refuses a read outside the workspace with guidance the model
// can act on: which path, which directory would cover it, and how a person
// makes that permanent.
func blockBoundary(call agentcore.AgentToolCall, target, candidate string, answered bool) *agentcore.BeforeToolCallDecision {
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
