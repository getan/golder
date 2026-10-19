package judge

// The boundary gate: stepping outside the workspace needs the user's answer,
// for reading and for writing alike.
//
// The workspace boundary is the directory the user opened. Crossing it is a
// question of consent rather than of risk, which is why this gate is a sibling
// of the permission gate instead of a tier inside it, and why no approval mode
// waives it: read-only, ask, auto and full-access all ask before a file outside
// the workspace is touched. full-access turns off review and sandboxing, not the
// user's say over where data comes from or goes, and a run with no prompt
// available (headless, a sub-agent thread with no UI) fails closed with guidance
// rather than guessing.
//
// The answers are deliberately different sizes (the read side below, the write
// side in the same shape — see BoundaryWrite):
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
	// outcome in the note. It is a callback because the managed file is opened
	// by the cli assembly, which owns its path and its on-disk shape; a driver
	// without one simply cannot offer the remembered answer, and the prompt says
	// so (the session grant still lands in the table the gate writes itself).
	Persist func(dir string) (made bool, why string)
	// GrantWritable, when set, registers a directory as writable — for the
	// session, and across sessions when persist is true — and reports what
	// happened. It is the same callback the escalation dialog's writable answers
	// use, wired once at the assembly point, and sharing it is the point: it
	// registers the sandbox's writable root AND the authorization table's row,
	// so an approval the next patch honours is also one the next command sees —
	// one answer, one meaning. Nil means the remembered and directory rungs are
	// not offered, and the one-write answer is all the prompt can show.
	GrantWritable func(root string, persist bool) (made bool, why string)
	// WritableAt reports whether a path is already writable for this session
	// (the sandbox's writable-root registry, which config.toml and
	// permissions.toml seed). It is injected for the same reason Persist is:
	// that registry lives above this package. Nil means only the authorization
	// table is consulted, which is the stricter reading — a seeded root then
	// asks once and is recorded in the table by the answer.
	WritableAt func(path string) bool
	// OnPrompt, when set, is told the question just before a prompt is put to
	// the person and returns the function to call once it is answered. See
	// GateOpts.OnPrompt: it is how a driver tells the terminal around it that
	// this pane waits for a human (internal/hostcompat).
	OnPrompt func(question string) func()
	// AskCoord carries this prompt's network answer to the permission gate when
	// the same call needs both a path decision and a network grant. A nil
	// coordinator keeps the two gates asking separately, which is the old
	// behavior and the safe fallback for drivers that do not wire it.
	AskCoord *AskCoord
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
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		// The sink is shared by every run in the process, so a note produced by a
		// sub-agent's call would otherwise read as one the conversation produced.
		// The origin rides the context precisely so this can name it.
		notify := func(n Note) {
			if n.Origin == "" {
				n.Origin = agentcore.OriginFromContext(ctx)
			}
			if opts.Notify != nil {
				opts.Notify(n)
			}
		}
		// What this call does to the paths it names: read for the file and search
		// tools, write for the one that writes. It decides which grant an answer
		// records, which rows the dialog offers, and which silencers apply — a
		// read grant says nothing about writing, and a writable root says nothing
		// about reading.
		op := boundaryOp(call.Name)
		writing := op.Has(grants.Write)
		shellCmd := ""
		if shellTool(call.Name) {
			shellCmd, _ = bashCommand(call.Arguments)
		}
		lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
		// A call may name several paths (a command line lists file operands).
		// One question covers the call, so it offers the finest directory any
		// of those reads would need: a command that names a directory and a
		// subtree inside it (`ls ~/work ~/work/proj`) must ask about the
		// subtree, not hand over the whole ~/work just because that operand
		// came first — the coarse answer is a session grant, so it would
		// outlive the call. A path whose offered directory holds another's is
		// the coarser answer and loses to it; ties keep command order.
		type question struct {
			raw, target, candidate string
			writing                bool
		}
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
			// A shell command's operand can be a write even though bash's table
			// entry asks about reading: `echo hi > /outside/f` and `rm /outside/f`
			// change that path, and a read grant says nothing about writing. The
			// command's own text decides the side.
			candOp := op
			candWriting := writing
			cloneDir := false
			if !writing && shellCmd != "" {
				// A git clone's destination is the write the command's own text
				// does not describe anywhere else: PathWritten reads redirects,
				// write verbs and path-taking flags, and the directory a clone
				// creates is an operand of the clone SUBCOMMAND. Without this the
				// question about `git clone <url> ../repo` is asked — and
				// answered — as a read, so the grant it records keeps the very
				// write the clone needs out of the sandbox.
				cloneDir = cloneCreates(shellCmd, candidateRaw)
				if cloneDir || PathWritten(resolved, shellCmd) {
					candOp |= grants.Write
					candWriting = true
				}
			}
			switch boundaryQuestion(candidateRaw, resolved, absRoot, opts.Roots, candOp, opts.WritableAt) {
			case boundarySilent:
				continue
			case boundaryRefused:
				// The user answered this question already, in the negative.
				// Asking again is the thing that trains people to approve
				// without reading, so the call is refused instead — with the
				// way to change the answer named, because a block nobody can
				// lift is a trap.
				notify(Note{
					Tool: call.Name, ToolCallID: call.ID, Kind: NoteBoundaryDenied, Level: Deny, Lang: lang,
					Summary: resolved, Authorization: localized(lang, "本会话已拒绝", "refused earlier in this session"),
					Rationale: localized(lang, "用户此前拒绝了该路径", "the user refused this path earlier"),
				})
				return blockRefused(call, resolved, writing)
			}
			candidate := permissions.BoundaryCandidate(resolved)
			if cloneDir {
				// A clone destination is a directory the command is about to
				// CREATE, so the narrow grant is that directory itself.
				// BoundaryCandidate widens a path that does not exist to its
				// parent — its rule for "there is nothing there yet for a grant
				// to name" — and for a clone into a sibling of the workspace that
				// parent holds every sibling there is: the whole tree, handed
				// over to create one repository.
				candidate = filepath.Clean(resolved)
			}
			questions = append(questions, question{
				raw: candidateRaw, target: resolved,
				candidate: candidate,
				writing:   candWriting,
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
		writing = chosen.writing

		candidate := chosen.candidate
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
			BoundaryWrite: writing,
			Authorization: "outside the workspace",
			Rationale:     target + " is outside the workspace",
		}
		if NetworkRequested(call.Name, call.Arguments) {
			req.NetworkRequest = true
			if _, key, ok := NetworkKey(call.Name, call.Arguments); ok {
				req.NetworkKey = key
				req.NetworkRememberable = true
			}
		}
		var ans ApprovalAnswer
		switch {
		case interactive:
			ans = promptBoundary(ctx, opts, target, candidate, writing, req.NetworkRequest, req.NetworkKey, req.NetworkRememberable)
		case opts.Confirm != nil:
			ans = opts.Confirm(ctx, req)
		}
		if req.NetworkRequest && opts.AskCoord != nil && ans.Answered && ans.Approve {
			// The permission gate would otherwise ask the network side again.
			// Carry the answer only after the path side approved: a denied path
			// already ends the call here.
			opts.AskCoord.Put(call.ID, ans)
		}
		if !ans.Answered || !ans.Approve {
			// The two failures are told apart in the KIND, not just the wording:
			// "the user said no" is a decision (and is recorded below), while
			// "nobody could be asked" is a failure to ask, which records nothing
			// and must not read as a verdict. Reporting both as a reviewer denial
			// — which is what this gate used to do — blamed auto-review for a
			// consent question it never saw.
			rationale, kind := localized(lang, "用户拒绝了本次请求", "denied by the user"), NoteBoundaryDenied
			if !ans.Answered {
				rationale, kind = localized(lang, "本次运行无人可问", "no prompt available"), NoteBoundaryNoPrompt
			} else {
				// A refusal is a decision, and it is recorded so the same
				// question is not put again this session. Only a real answer
				// records: "no prompt available" is a failure to ask, not a
				// verdict, and recording it would turn a headless run's silence
				// into a refusal the user never gave.
				//
				// Exact scope, mirroring the one-read grant this answer sits
				// beside: the question named a path, so the answer covers that
				// path. A subtree refusal would be bigger than the question —
				// "no to this file" is not "no to everything under its
				// directory" — and it would silence a question the user has not
				// been asked. The lifetime is the session: Esc is "not now",
				// not "never".
				//
				// Exact also keeps the refusal ahead of a broader grant: the
				// table answers most-specific-first, so granting the directory
				// afterwards does not silently reopen the file that was refused
				// by name.
				for _, q := range questions {
					if q.target == "" {
						continue
					}
					_, _ = grants.Add(grants.Grant{
						Path:     q.target,
						Deny:     op,
						Scope:    grants.Exact,
						Lifetime: grants.Session,
						Source:   "boundary dialog (denied)",
					})
				}
			}
			notify(Note{
				Tool: call.Name, ToolCallID: call.ID, Kind: kind, Level: Deny, Lang: lang,
				Summary: target, Authorization: req.Authorization, Rationale: rationale,
			})
			return blockBoundary(call, target, candidate, writing, ans.Answered)
		}
		// "This directory" registers a session grant; "just this one" rides the
		// call's context. A directory the registry refuses (too broad, or an
		// invalid spelling) falls back to the one-call grant rather than failing
		// the access the user just approved.
		//
		// The write side goes through opts.GrantWritable, which is the same
		// callback the escalation dialog uses: it registers the sandbox's
		// writable root AND the table's row, so an approval that lets the next
		// patch write is also one the next command sees. One answer, one meaning.
		if granted := strings.TrimSpace(ans.WriteRoot); granted != "" {
			if opts.GrantWritable != nil {
				if made, why := opts.GrantWritable(granted, ans.Persisted); made {
					rationale := localized(lang,
						"本会话允许写入 "+granted,
						"writing "+granted+" is allowed for this session")
					if ans.Persisted {
						rationale = localized(lang,
							"已允许写入 "+granted+"，并记住（跨会话）",
							"writing "+granted+" is allowed and remembered across sessions")
					}
					notify(Note{
						Tool: call.Name, ToolCallID: call.ID, Kind: NoteBoundaryGranted, Level: Allow, Lang: lang,
						Summary: target, Authorization: req.Authorization, Rationale: rationale,
					})
					return nil
				} else if why != "" {
					// The registration failed, so the directory rung is not
					// available; fall through to the one-write answer rather than
					// failing access the user just approved.
					notify(Note{
						Tool: call.Name, ToolCallID: call.ID, Kind: NoteBoundaryGranted, Level: Allow, Lang: lang,
						Summary: target, Authorization: req.Authorization,
						Rationale: localized(lang, "仅本次写入该文件；"+why, "writing this file only; "+why),
					})
				}
			}
		}
		if granted := strings.TrimSpace(ans.ReadRoot); granted != "" {
			life := grants.Session
			if ans.Persisted {
				life = grants.Remembered
			}
			// The row IS the grant: the boundary gate, the file tools and the
			// sandbox all read this one table, so recording it here is what
			// makes the answer take effect everywhere on the next call.
			if _, err := grants.AddReadRoot(granted, life, "read dialog"); err == nil {
				rationale := localized(lang,
					"本会话允许读取 "+granted,
					"reading "+granted+" is allowed for this session")
				if ans.Persisted {
					rationale = localized(lang,
						"已允许读取 "+granted+"，并记住（跨会话）",
						"reading "+granted+" is allowed and remembered across sessions")
				}
				notify(Note{
					Tool: call.Name, ToolCallID: call.ID, Kind: NoteBoundaryGranted, Level: Allow, Lang: lang,
					Summary: target, Authorization: req.Authorization,
					Rationale: rationale,
				})
				return nil
			}
		}
		// The single-path answer is a decision too, and the narrowest one: it
		// covers exactly the path that was asked about, for this session only.
		// Recording it is what lets a repeated question about the same file stop
		// being asked — the alternative is that "allow once" leaves no trace and
		// the same command asks again next turn.
		onceCap, onceSource, onceReason := grants.Read, "read dialog (once)", "approved for this read only"
		if writing {
			onceCap, onceSource, onceReason = grants.Write, "write dialog (once)", "approved for this write only"
		}
		onceReason = localized(lang,
			map[bool]string{true: "仅批准本次写入", false: "仅批准本次读取"}[writing],
			onceReason)
		if target != "" {
			_, _ = grants.Add(grants.Grant{
				Path:     target,
				Allow:    onceCap,
				Scope:    grants.Exact,
				Lifetime: grants.Session,
				Source:   onceSource,
			})
		}
		notify(Note{
			Tool: call.Name, ToolCallID: call.ID, Kind: NoteBoundaryGranted, Level: Allow, Lang: lang,
			Summary: target, Authorization: req.Authorization,
			Rationale: onceReason,
		})
		// The one-call grant rides the call's context, where the tool honours it
		// once — read for the readers, write for the writer. A command tool does
		// not read either grant (it cannot: the path is inside a command it will
		// hand to a shell), so for a command the approval means "run it this
		// time", and the only standing constraint remains the sandbox. That is
		// why the dialog offers the directory answer for commands too.
		if writing {
			return &agentcore.BeforeToolCallDecision{WriteGrant: target}
		}
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
//   - already granted, through either reader — the legacy registry (the
//     read-root list for a read, the sandbox's writable roots for a write) or
//     the authorization table (what every dialog answer records now);
//   - the system whitelist, which the sandbox admits anyway, so asking would
//     only train people to approve without reading. It applies to READS only:
//     those paths are read-only by design, so a write there is a question, and
//     a prompt that offers to grant /usr is a prompt that should not exist.
//
// The two sides do not share their silencers, and the asymmetry is the point:
// a read at /etc/hosts needs no answer, a write to it is a question the user
// should see; a directory granted for reading says nothing about writing it,
// and a sandbox writable root (which implies reading) says nothing about
// whether a read has been approved.
//
// A REFUSAL outranks all of it. The table's answer for this path can be "the
// user said no", and that is not a reason to stay silent — it is a reason to
// block without asking, which is why the result is three-valued rather than a
// boolean.
func boundaryQuestion(raw, resolved, absRoot string, extraRoots []string, op grants.Capability, writableAt func(string) bool) boundaryAction {
	if permissions.Inside(absRoot, resolved) || insideAnyRoot(extraRoots, resolved) {
		return boundarySilent
	}
	if op.Has(grants.Write) {
		// Writing into live secret material is the static floor's to refuse, so
		// the path passes through untouched and no dialog offers it a key.
		if _, bad := secretPath(raw); bad {
			return boundarySilent
		}
		if grants.Check(resolved, grants.Write).Decision == grants.Denied {
			return boundaryRefused
		}
		if WriteAllowed(resolved, writableAt) {
			return boundarySilent
		}
		if permissions.InsideSystemWriteRoots(resolved) {
			return boundarySilent
		}
		return boundaryAsk
	}
	if _, bad := credentialPathRead(raw); bad {
		return boundarySilent
	}
	if grants.Check(resolved, grants.Read).Decision == grants.Denied {
		return boundaryRefused
	}
	if grants.Allows(resolved, grants.Read) {
		return boundarySilent
	}
	if insideSystemRoots(resolved) {
		return boundarySilent
	}
	return boundaryAsk
}

// boundaryAction is what the gate owes one path.
type boundaryAction uint8

const (
	// boundarySilent: nothing at all. The path is inside the boundary, already
	// granted, credential material the static floor owns, or on the system
	// whitelist the sandbox admits anyway.
	boundarySilent boundaryAction = iota
	// boundaryAsk: put the question to the user.
	boundaryAsk
	// boundaryRefused: the user already answered no about this path. The call is
	// blocked without asking, because a question already answered is not worth
	// putting twice.
	boundaryRefused
)

// WriteAllowed reports whether a write to path is already authorized for this
// session: the session's writable registry covers it, or the authorization
// table carries a write grant for it.
//
// It is shared, deliberately, by the two callers that must not disagree — the
// gate, which decides whether to ASK, and the write tool, which decides whether
// to WRITE. The gate stays silent exactly when this answers true, so a mismatch
// is a failure the user sees: a patch refused after they approved it, or a
// question about a directory they just granted. Both sources are consulted
// because both are answers the user gave: the registry is where config.toml,
// permissions.toml and the sandbox dialog put theirs, the table is where every
// dialog answer now lands — including the one-call "allow this write" row,
// which is recorded as an exact-path grant and would otherwise silence the
// question while the tool kept refusing.
//
// writableAt may be nil for a caller that has no registry to consult; the table
// alone then decides, which is the stricter reading.
func WriteAllowed(path string, writableAt func(string) bool) bool {
	if writableAt != nil && writableAt(path) {
		return true
	}
	return grants.Allows(path, grants.Write)
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
func promptBoundary(ctx context.Context, opts BoundaryOpts, target, candidate string, writing bool, networkRequest bool, networkKey string, networkRememberable bool) ApprovalAnswer {
	// The pane is about to wait for the person: say so before the prompt starts,
	// and say it is over when the answer lands, however this prompt travels.
	verb, gerund := "read", "reading"
	if writing {
		verb, gerund = "write", "writing"
	}
	done := opts.promptNote("allow " + gerund + " " + target)
	defer done()
	if opts.Mu != nil {
		opts.Mu.Lock()
		defer opts.Mu.Unlock()
	}
	out := opts.Out
	if out == nil {
		return ApprovalAnswer{}
	}
	from := ""
	if origin := agentcore.OriginFromContext(ctx); origin != "" {
		from = " (from " + origin + ")"
	}
	fmt.Fprintf(out, "\ngolder: %s is outside the workspace and this call %ss it%s.\n", target, verb, from)
	if candidate != "" {
		// The directory and remembered rungs need a way to register and, for the
		// latter, a way to write it down. A write grant goes through
		// GrantWritable; a read grant through Persist, which is the read side's
		// caller-owned store. A driver with neither still gets the one-shot
		// answer, which needs no registry.
		canGrantDir := opts.GrantWritable != nil
		if !writing {
			canGrantDir = true
		}
		canPersist := opts.Persist != nil
		if writing {
			canPersist = opts.GrantWritable != nil
		}
		if canGrantDir && canPersist {
			// The remembered rung is offered only where a driver can actually
			// write it down: an option that silently forgets would be worse
			// than one that is absent.
			fmt.Fprintf(out, "  [y] allow this %s once   [a] allow %s %s and below (this session)"+
				"   [s] allow %s %s and below (remembered)   [N] deny\n", verb, gerund, candidate, gerund, candidate)
		} else if canGrantDir {
			fmt.Fprintf(out, "  [y] allow this %s once   [a] allow %s %s and below (this session)   [N] deny\n",
				verb, gerund, candidate)
		} else {
			fmt.Fprintf(out, "  [y] allow this %s once   [N] deny\n", verb)
		}
	} else {
		fmt.Fprintf(out, "  [y] allow this %s once   [N] deny\n", verb)
	}
	fmt.Fprint(out, "Choice: ")
	// The prompt is interruptible: Ctrl+C during it cancels the run and
	// yields a denial immediately instead of trapping the user.
	line, ok := readLineOrCancel(ctx, opts.In)
	if !ok {
		return ApprovalAnswer{}
	}
	ans := ApprovalAnswer{Answered: true}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		ans.Approve = true
	case "a", "all":
		if candidate != "" {
			if writing {
				ans.Approve, ans.WriteRoot = true, candidate
			} else {
				ans.Approve, ans.ReadRoot = true, candidate
			}
		}
	case "s", "save":
		if candidate != "" && writing && opts.GrantWritable != nil {
			// The gate registers the grant — it owns the callback, and one owner
			// means one registration: the sandbox's writable root and the table's
			// row are written together or not at all. This answer only says which
			// directory and which lifetime; the gate reports what happened.
			ans.Approve, ans.WriteRoot, ans.Persisted = true, candidate, true
		}
		if candidate != "" && !writing && opts.Persist != nil {
			made, why := opts.Persist(candidate)
			ans.Approve, ans.ReadRoot = true, candidate
			if made {
				ans.Persisted = true
			} else if why != "" {
				fmt.Fprintf(out, "  %s\n", why)
			}
		}
	}
	if !ans.Approve || !networkRequest {
		return ans
	}
	// The path is settled; the same popup can now ask the network side. Asking
	// here rather than letting the permission gate open a second prompt is the
	// whole point of the combined request.
	if networkKey != "" && networkRememberable {
		fmt.Fprintf(out, "Network access: [y] this call only   [a] this session for %s   [s] remember %s   [N] deny\n",
			networkKey, networkKey)
	} else {
		fmt.Fprint(out, "Network access: [y] this call only   [N] deny\n")
	}
	fmt.Fprint(out, "Network choice: ")
	nline, nok := readLineOrCancel(ctx, opts.In)
	if !nok {
		return ApprovalAnswer{Answered: true}
	}
	switch strings.ToLower(strings.TrimSpace(nline)) {
	case "y", "yes":
		ans.Network = true
	case "a", "all":
		if networkKey != "" && networkRememberable {
			ans.Network, ans.NetworkSession = true, true
		}
	case "s", "save":
		if networkKey != "" && networkRememberable {
			ans.Network, ans.NetworkRemember = true, true
		}
	}
	return ans
}

// blockBoundary refuses a read outside the workspace with guidance the model
// can act on: which path, which directory would cover it, and how a person
// makes that permanent.
func blockBoundary(call agentcore.AgentToolCall, target, candidate string, writing, answered bool) *agentcore.BeforeToolCallDecision {
	// The guidance names the two commands that actually make a grant permanent.
	// It used to point at config.toml's [permissions] table, which no longer
	// exists — advice that sends a person to edit a file that does nothing is
	// worse than no advice.
	verb, cmd := "read", "/boundary readable add"
	if writing {
		verb, cmd = "write", "/boundary writable add"
	}
	msg := fmt.Sprintf("tool %q blocked: %s is outside the workspace and the %s was not approved", call.Name, target, verb)
	if candidate != "" {
		msg += fmt.Sprintf("; approving it, or running %s %s, would allow it", cmd, candidate)
	} else {
		msg += fmt.Sprintf("; running %s on a directory containing it would allow it", cmd)
	}
	if !answered {
		msg += " — no prompt is available in this run, so ask the user to run it interactively or to configure the root"
	}
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}

// blockRefused refuses a path the user already answered no about, without
// asking again. The message says so plainly and names how to change the answer:
// a refusal that reads like a fresh denial would leave the model (and the
// person) thinking the question had just been put, and a block nobody can lift
// is a trap rather than a decision.
func blockRefused(call agentcore.AgentToolCall, target string, writing bool) *agentcore.BeforeToolCallDecision {
	verb, cmd := "read", "/boundary readable add"
	if writing {
		verb, cmd = "write", "/boundary writable add"
	}
	msg := fmt.Sprintf("tool %q blocked: the %s of %s was refused earlier in this session, so it is not asked again", call.Name, verb, target)
	msg += fmt.Sprintf("; to change that, run %s %s, or start a new session", cmd, target)
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}
