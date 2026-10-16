package judge

// Package judge grades the risk of a single tool call so the run gets a
// middle tier between "directory trusted, run anything" and "ask a human
// for everything" (trust tri-state in internal/trust, allow/deny lists in
// internal/cli/run). It is a leaf package: standard library plus
// internal/agentcore and internal/permissions, no imports from
// trust/runtime/hooks, so the cli layer can chain it onto the existing
// BeforeToolCall seam without creating an import cycle.
//
// Chain of responsibility, cheapest first:
//
//  1. StaticFloor — a tiny hard-deny list for unambiguously catastrophic
//     calls (sudo, mkfs, keys under .ssh). It never allows, it only denies.
//  2. LLMJudge — the active conversation model reviews the call against a
//     policy prompt and the user's recent messages, and returns one of
//     Allow/Confirm/Sandbox/Deny plus a rationale written in the user's
//     language. There is no separate classification service: the dependency
//     is the model the session is already using.
//  3. Fallback — any reviewer failure (timeout, transport error, malformed
//     answer) marks the verdict Failed: the gate then prompts (interactive)
//     or handles the call conservatively (sandbox or block), never allows.
//
// Enforcement lives in PermissionGate and is driven by the live
// permissions.Mode: read-only blocks mutating tools, ask prompts on anything
// above Allow, auto decides automatically and announces each non-trivial
// verdict with its rationale, full-access skips review and sandboxing while
// keeping the static floor. Sandbox execution lives in internal/seatbelt and is
// injected by internal/cli/run: an approved bash call runs under the platform
// sandbox by default, and the native file tools run through a one-call isolated
// worker. Network access and a sandbox escape are SEPARATE per-call decisions
// (Verdict.Network, Verdict.Direct): a risk level of Allow never implies either.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// Level is the risk tier for one tool call.
type Level int

const (
	// Allow runs without friction.
	Allow Level = iota
	// Confirm needs an explicit human yes.
	Confirm
	// Sandbox must run isolated; without a sandbox runner it prompts
	// (interactive) or blocks (headless) with a sandbox note.
	Sandbox
	// Deny never runs.
	Deny
)

func (l Level) String() string {
	switch l {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Sandbox:
		return "sandbox"
	default:
		return "deny"
	}
}

func levelFromString(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "sandbox":
		return Sandbox
	case "deny":
		return Deny
	default:
		return Confirm
	}
}

// isKnownTier reports whether s names one of the four tiers. Reviewers that
// answer outside the vocabulary fail closed.
func isKnownTier(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow", "confirm", "sandbox", "deny":
		return true
	default:
		return false
	}
}

// Verdict is one classification result.
type Verdict struct {
	Level      Level
	Reasons    []string
	Confidence float64
	Source     string
	// Network is an explicit one-call capability decision, independent of the
	// level. It is set only when this classifier actually reviewed a bash call
	// requesting network access. Allow/Confirm/Sandbox alone never imply it.
	Network bool
	// Direct is an explicit decision to waive the file sandbox for THIS call,
	// independent of the four risk tiers. A bare Allow is not a direct grant.
	Direct bool
	// Failed marks a reviewer that could not decide (timeout, transport
	// error, malformed answer). The gate must never treat it as an approval:
	// it prompts, sandboxes, or blocks instead.
	Failed bool
	// Risk ("low"/"medium"/"high"/"critical") and Authorization
	// ("unknown"/"low"/"medium"/"high") are the reviewer's own rating, used
	// in the verdict card. Empty for static verdicts.
	Risk          string
	Authorization string
	// Lang is the rationale's language ("zh"/"en"), so UI templates can
	// match it.
	Lang string
}

// Classifier grades one tool call. Implementations must be safe for
// concurrent use: parallel tool batches grade concurrently.
type Classifier interface {
	Classify(ctx context.Context, tool string, args json.RawMessage) Verdict
}

// Chain runs StaticFloor first (deny wins), then inner, and degrades any inner
// error to Confirm. A nil inner grades static-only.
//
// apply_patch has no exemption here. It used to: an ordinary in-workspace patch
// skipped the reviewer, measured at 467 calls and 18.5 minutes of review over
// ten sessions. The decision model made that affordable to remove — a graded
// call costs a fraction of the old reviewer's round trip — and removing it is
// what lets a patch be judged as a whole: the edit's content, where it lands,
// and whether its effect outlives the call (judge.OutlivingPaths, reported
// through the reviewer's state).
type Chain struct {
	Inner Classifier
}

// Classify implements Classifier: the static floor, then the reviewer.
//
// There is nothing between them. This chain used to carry two exemptions — a
// provably read-only shell command, and a set of tools with no side effects —
// and both are gone: every call is reviewed. The decision model is what made
// that affordable, and removing the exemptions is what made the rest of the
// design small, because a rule with no exceptions needs no machinery to
// justify its exceptions.
//
// The floor stays in front, and it is not an optimization: it is the one layer
// that denies credential material WITHOUT asking a model, so a verdict it
// reaches cannot be argued with, retried, or reached by a prompt. Everything
// else — including what a model would obviously allow, like `ls` — is the
// reviewer's to decide.
func (c Chain) Classify(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if v, ok := staticDeny(tool, args); ok {
		return v
	}
	if c.Inner == nil {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"no grader configured"}}
	}
	return c.Inner.Classify(ctx, tool, args)
}

// GateOpts tunes PermissionGate. Interactive drivers pass the shared stdin
// reader/writer so Confirm/Sandbox prompts behave exactly like the trust
// gate; headless drivers leave In nil and get the fail-closed path.
type GateOpts struct {
	In  *bufio.Reader
	Out io.Writer
	Mu  *sync.Mutex
	// Classifier grades one call. Production injects a Chain over the LLM
	// reviewer; nil keeps the static floor but fails closed on every gated
	// (non-read-only) tool instead of allowing it.
	Classifier Classifier
	// Sandboxed reports whether a tool call at the Sandbox tier will actually
	// run isolated by the execution layer (the bash tool's seatbelt runner).
	// When it returns true the gate lets the call through — isolation is the
	// enforcement, not a prompt or a block. Nil, or false for this tool,
	// prompts (interactive) or fails closed.
	Sandboxed func(toolName string) bool
	// RequireSandbox makes an authorized bash call default to OS isolation.
	// Only an explicit one-call Direct decision can override this requirement.
	RequireSandbox bool
	// Notify, when set, receives one Note per non-trivial verdict (approval,
	// sandbox routing, denial, read-only block, reviewer failure) so the
	// driver can show the decision and its rationale. Callbacks run on the
	// run goroutine; implementations must not block on the UI thread.
	Notify func(Note)
	// Confirm, when set, is the driver's approval dialog for a call the gate
	// cannot settle on its own: a Confirm tier outside auto mode, a Sandbox
	// tier the execution layer cannot isolate, or a failed review with no
	// containment. The interactive terminal drivers use In/Out instead; the
	// TUI has no stdin and supplies a callback that renders a dialog and
	// blocks on the user's answer. It runs on the run goroutine and must
	// itself be safe to block (the TUI implementation hands the request to
	// the UI goroutine and waits on a channel). nil keeps the fail-closed
	// behavior.
	Confirm func(ctx context.Context, req ApprovalRequest) ApprovalAnswer
	// Cwd is the directory the run is gated against. It is used to judge
	// whether an escalation's path is worth offering a grant for (a path the
	// command can already write needs none). Empty means no path grant is
	// offered.
	Cwd string
	// GrantWritable, when set, registers a path as writable for the session and
	// — when persist is true — makes that survive the process. It backs the
	// stdin prompt's `w`/`s` answers, the twin of the TUI dialog's rows, and is
	// a callback for the same reason the read grant's is: this package is a
	// leaf and cannot reach the sandbox registry or the managed store. why
	// carries the failure, or the note for a grant that was already in place.
	GrantWritable func(root string, persist bool) (made bool, why string)
	// OnPrompt, when set, is called with the question just before a prompt is
	// put to the person — whichever way that prompt travels (the driver's
	// Confirm dialog, or the stdin prompt) — and returns the function to call
	// once it has been answered. It exists so a driver can tell the terminal
	// around it that this pane is waiting for a human: see
	// internal/hostcompat. The returned function is called exactly once, even
	// when the prompt ran and the answer was "no", and even when no answer
	// arrived at all.
	//
	// A nil hook, or a hook returning nil, changes nothing. It is called on the
	// run goroutine, outside the prompt mutex, and must not block: it is the
	// driver's own transport, not part of the decision.
	OnPrompt func(question string) func()
}

// promptNote announces a prompt about to be shown and returns the function
// that says it is over. A nil hook yields a no-op, so callers can defer the
// result unconditionally.
func (o *GateOpts) promptNote(question string) func() {
	if o.OnPrompt == nil {
		return func() {}
	}
	done := o.OnPrompt(question)
	if done == nil {
		return func() {}
	}
	return done
}

// approvalQuestion is how a prompt for a tool call reads in one line, in the
// "approve <tool>: <what>" form the rail, the risk rules and the hosts all
// already read.
func approvalQuestion(tool, summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "approve " + tool
	}
	return "approve " + tool + ": " + summary
}

// ApprovalKind says why a call needs the user's answer.
type ApprovalKind string

const (
	// ApprovalConfirm: the reviewer graded the call Confirm and the mode is
	// not auto.
	ApprovalConfirm ApprovalKind = "confirm"
	// ApprovalNoSandbox: the reviewer asked for isolation but the execution
	// layer has no runner for this call, so approving runs it bare.
	ApprovalNoSandbox ApprovalKind = "no-sandbox"
	// ApprovalReviewFailed: the reviewer could not decide and the call cannot
	// be contained.
	ApprovalReviewFailed ApprovalKind = "review-failed"
	// ApprovalTrust: a side-effect tool in a directory the user has not
	// trusted. The gate is the trust manager, not the reviewer; the dialog
	// asks the same question the REPL's first-run prompt does.
	ApprovalTrust ApprovalKind = "trust"
	// ApprovalEscalation: the model asked to run a call outside the sandbox
	// (bash sandbox_permissions=require_escalated) while the reviewer graded it
	// Sandbox. The two sides disagree, and the mode decides who settles it:
	// auto re-contains the call (the reviewer's answer stands, announced either
	// way), ask puts the escape to the person with containment as the default,
	// headless re-contains or fails closed.
	ApprovalEscalation ApprovalKind = "escalation"
	// ApprovalReAdmit asks whether to re-admit, for READING and inside the
	// sandbox, the path a declined escape was blocked on. Auto asks it at the
	// moment it refuses the escape: dropping the sandbox for the whole call is
	// rarely what the call needs, and one path is usually all it does need. The
	// answer is recorded in the same table the gates and the sandbox read — the
	// read rows through the gate, the write row through the write dialog's own
	// callback — and the call then runs contained with that path reachable. A
	// "no" leaves auto's default untouched (contain the call when containment is
	// possible, refuse it otherwise) and records no denial, because nothing was
	// denied. Asked at most once per path per run. Only drivers with a dialog
	// ask it; without one the refusal message carries the same fix.
	ApprovalReAdmit ApprovalKind = "re-admit"
	// ApprovalNetwork asks for one call's network capability when the routine
	// grader could not authorize it. An approval keeps the filesystem sandbox;
	// this is NOT permission to run the call outside containment.
	ApprovalNetwork ApprovalKind = "network"
	// ApprovalBoundary: a read-only tool (read, view_image, grep, find, ls)
	// was pointed outside the workspace. The workspace is the directory the
	// user opened, so this asks for consent rather than a risk verdict; the
	// answer is "just this read" (ReadGrant) or "this directory for the
	// session" (ReadRoot). It is the one question no approval mode waives,
	// including full-access.
	ApprovalBoundary ApprovalKind = "boundary"
)

// ApprovalRequest is what the gate shows the user: the call itself plus the
// reviewer's verdict, so the decision is informed rather than a bare yes/no.
type ApprovalRequest struct {
	Kind ApprovalKind
	// Mode is the approval mode in force for this call (auto, ask, read-only,
	// full-access). It is the answer to the reader's real question — "does a
	// human have to answer this?" — which is why every surface prints it: the
	// same verdict is the user's to settle under ask and already settled under
	// auto.
	Mode string
	// Level is the grade the gate acted on (allow, confirm, sandbox, deny),
	// printed as the canonical token rather than a paraphrase so the dialog,
	// the notes and /permissions all use one vocabulary.
	Level   Level
	Tool    string
	Summary string
	// Justification carries the model's reason when the call is an escalation
	// request (bash sandbox_permissions=require_escalated); empty otherwise.
	Justification string
	// BoundaryDir is the directory a boundary request may be granted for the
	// session, or "" when no directory is offered (the answer is then "this
	// read" or "no"). Set only for ApprovalBoundary.
	BoundaryDir string
	// BoundaryWrite reports that the boundary request is about WRITING rather
	// than reading, so the dialog offers the write rows ("allow this write
	// once", "allow writing <dir>") and the answer records a write grant. Set
	// only for ApprovalBoundary.
	BoundaryWrite bool
	// ReadRoot is the path a re-admission question can bring back inside the
	// sandbox, for reading. Set only for ApprovalReAdmit; empty means there is
	// no path to offer and the caller keeps its refusal.
	ReadRoot string
	// TrustDir names the directory a trust question is about, or "" for every
	// other kind. The trust gate's session answers - allow local reads, allow
	// all tools - are grants for this directory, so the dialog names it rather
	// than leaving "this folder" to be guessed. Set only for ApprovalTrust.
	TrustDir      string
	Risk          string
	Authorization string
	Rationale     string
	// Origin names the agent that asked, when it is not the session's own
	// conversation: a sub-agent's dispatch, filled from the call's context. The
	// dialog prints it so the person knows who is asking; a question that reads
	// as coming from the conversation when a delegated child asked it is a
	// question about a call they never saw.
	Origin string
}

// ApprovalAnswer is the user's decision.
type ApprovalAnswer struct {
	// Approve runs the call. False blocks it (the model is told it was
	// denied and can propose something else).
	Approve bool
	// Always remembers the decision for this session, keyed by the request's
	// tool and summary prefix, so the same shape of call is not asked again.
	Always bool
	// Contained, when set with Approve, answers "keep it in the sandbox" — the
	// escalation dialog's default row. The gate re-contains the call: a request
	// to leave the sandbox is not an entitlement to, and the reviewer had
	// already graded the call runnable under containment.
	Contained bool
	// Answered is false when the dialog could not run (ctx cancelled, no UI);
	// the gate then falls back to its fail-closed behavior.
	Answered bool
	// WritableRoot, when set together with Approve, means the user granted
	// this path read+write access INSIDE the sandbox for the session instead
	// of running the call unisolated. The driver has already registered the
	// grant with the sandbox (internal/seatbelt); the gate then re-contains
	// the call, so it runs under the widened sandbox rather than bare.
	WritableRoot string
	// ReadRoot, when set together with Approve, means the user granted this
	// directory for READING for the session (a read-scope answer). The grant
	// is registered by the gate itself as the read-root registry's single
	// owner, so every file tool and the sandbox see it on the next call.
	ReadRoot string
	// WriteRoot, when set together with Approve, means the user granted this
	// directory for WRITING: the write side's twin of ReadRoot, answered by the
	// boundary gate's question about a patch that reaches outside the workspace.
	// The gate registers it through BoundaryOpts.GrantWritable, the same callback
	// the escalation dialog uses, so the grant lands in the sandbox's writable
	// roots and in the authorization table at once.
	WriteRoot string
	// SessionReads, when set together with Approve, means the user allowed
	// local reads in the trust gate's directory for the session (the trust
	// dialog's "reads here" answer). The gate registers it with the trust
	// manager; being a trust decision rather than a workspace-boundary one,
	// reads outside the workspace still take the read-scope gate's consent.
	SessionReads bool
	// Persisted, when set together with Approve, means the driver has already
	// made this grant permanent (it wrote the managed store). The gate itself
	// never persists anything — it stays a decision layer — but its verdict
	// note must not say "for this session" when the grant outlives it.
	Persisted bool
}

// NoteKind classifies one user-facing verdict note.
type NoteKind int

const (
	// NoteApproved: auto mode approved a Confirm-tier call (runs directly).
	NoteApproved NoteKind = iota
	// NoteSandboxed: a Sandbox-tier call runs isolated.
	NoteSandboxed
	// NoteEscaped: a call ran OUTSIDE the sandbox — the reviewer approved an
	// escalation request, or the user did. It is the one outcome a sandboxed
	// session must never reach the user unannounced, so it has its own kind
	// rather than riding on NoteApproved.
	NoteEscaped
	// NoteDenied: the reviewer (or the static floor) denied the call.
	NoteDenied
	// NoteUnavailable: the reviewer failed and the call was handled
	// conservatively (sandboxed when possible, otherwise blocked).
	NoteUnavailable
	// NoteBlockedNoPrompt: the call needed confirmation but no interactive
	// prompt exists, so it was blocked.
	NoteBlockedNoPrompt
	// NoteReadOnly: read-only mode blocked a mutating tool.
	NoteReadOnly
	// NoteBoundaryGranted: the workspace-boundary gate was answered yes, so the
	// call may reach outside the workspace. It is NOT a reviewer verdict: the
	// rating suffix belongs to risk grading, and an answer to a consent question
	// has no risk grade behind it.
	NoteBoundaryGranted
	// NoteBoundaryDenied: the person said no to a boundary question. The path is
	// recorded as refused for the session, so the same question is not put again.
	NoteBoundaryDenied
	// NoteBoundaryNoPrompt: the boundary question could not be asked at all (no
	// dialog and no stdin), so the call failed closed. It is a failure to ASK,
	// not a verdict — nothing is recorded — and saying so is the point: an
	// earlier version reported it as an auto-review denial, which blamed the
	// reviewer for a question nobody was asked.
	NoteBoundaryNoPrompt
)

// Note is one verdict announcement for the user: the decision, the tool, and
// the reviewer's rationale (already written in the user's language).
type Note struct {
	Tool string
	// ToolCallID is the originating call id, so a transcript can file the
	// note directly above that call's card.
	ToolCallID string
	Kind       NoteKind
	Level      Level
	// Summary is a short preview of the call (command/path), when available.
	Summary       string
	Risk          string
	Authorization string
	Rationale     string
	Lang          string
	// Mode is the approval mode that was in force for this call (auto, ask,
	// read-only, full-access), stamped by the permission gate. It is what makes
	// a verdict line answer the reader's real question — the same grade means
	// "a human said yes" under ask and "the reviewer decided" under auto — so a
	// note built outside a gate (trust, boundary bookkeeping) may leave it empty
	// and render without it.
	Mode string
	// Origin names the agent that asked, when it is not the session's own
	// conversation (a sub-agent dispatch). Empty for a direct call. It is a
	// label for the reader, never an input to a decision.
	Origin string
}

// PermissionGate builds the mode-aware BeforeToolCall gate. The mode is read
// per call, so a /permissions switch applies to the next tool call even
// mid-run:
//
//   - read-only: mutating tools are blocked with a NoteReadOnly note.
//   - ask:       verdicts above Allow prompt the human when In is set, and
//     are blocked (with a note) otherwise.
//   - auto:      verdicts above Allow are decided by the reviewer — Confirm
//     runs, Sandbox runs isolated, Deny blocks — each announced with a note.
//   - full-access: no review; only the static floor can deny.
//
// A nil state is treated as full-access (no gate).
func PermissionGate(state *permissions.State, opts GateOpts) agentcore.BeforeToolCallFunc {
	interactive := opts.In != nil && opts.Out != nil
	// readmitAsked remembers the paths this gate already put to the person, so a
	// retried escape does not re-open a question they have answered. Per gate:
	// that is the lifetime of the answer it guards.
	var readmitAsked OnceSet
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		mode := permissions.FullAccess
		if state != nil {
			mode = state.Mode()
		}
		lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
		// The sink is shared by every run in the process, so a note a sub-agent's
		// call produced would otherwise be indistinguishable from one the main
		// conversation produced. The origin rides the context precisely so this
		// can name it, and the mode is stamped the same way: every line a call
		// produces says which mode decided it.
		notify := func(n Note) {
			if n.Origin == "" {
				n.Origin = agentcore.OriginFromContext(ctx)
			}
			if n.Mode == "" {
				n.Mode = mode.String()
			}
			if opts.Notify != nil {
				opts.Notify(n)
			}
		}
		// ask carries the mode and the language into every approval request, so
		// the dialog prints the decision line without each call site repeating it.
		ask := func(ctx context.Context, call agentcore.AgentToolCall, v Verdict, kind ApprovalKind) (*agentcore.BeforeToolCallDecision, bool) {
			return askApproval(ctx, opts, mode.String(), call, v, kind, lang)
		}
		switch mode {
		case permissions.FullAccess:
			if v, ok := staticDeny(call.Name, call.Arguments); ok {
				notify(noteFor(call, v, NoteDenied, lang))
				return blockCall(call, v, DenyGuidance)
			}
			return nil
		case permissions.ReadOnly:
			if readOnlyAllowed(call.Name) {
				return nil
			}
			notify(Note{
				Tool: call.Name, Kind: NoteReadOnly, Level: Deny, Lang: lang,
				Summary: riskSummary(call),
			})
			v := Verdict{Level: Deny, Source: "static", Lang: lang, Reasons: []string{"read-only permissions mode"}}
			return blockCall(call, v, "switch with /permissions ask|auto|full-access to allow mutating tools")
		}

		v := classifyForGate(ctx, opts, call)
		network := NetworkRequested(call.Name, call.Arguments)
		// Do not offer a "network inside the sandbox" answer if the process
		// cannot create that sandbox. The user can instead explicitly authorize
		// a direct one-call run; an unanswered dialog still fails closed.
		askNetwork := func() (*agentcore.BeforeToolCallDecision, bool) {
			kind := ApprovalNetwork
			// A shell that cannot be isolated is a bigger question than a network
			// grant — approving runs the command bare — so it keeps the no-sandbox
			// kind. A tool that never had a sandbox to lose (webfetch, websearch)
			// is asked about the network and nothing else.
			if opts.RequireSandbox && strings.EqualFold(call.Name, "bash") && !sandboxable(opts, call.Name) {
				kind = ApprovalNoSandbox
			}
			return ask(ctx, call, v, kind)
		}
		grant := func(sandbox bool) *agentcore.BeforeToolCallDecision {
			return &agentcore.BeforeToolCallDecision{Sandbox: sandbox, Network: network && v.Network}
		}
		// readmitPath asks whether to bring a path a refused escape needs back
		// inside the sandbox. It is deliberately not routed through
		// askApproval: a "no" here is not a refusal of the CALL — auto may
		// still contain it — so it must block nothing and leave no "denied by
		// the user" note behind. Returns the contained decision on a yes, and
		// nil when the person declined or no grant could be registered.
		readmitPath := func(root string) *agentcore.BeforeToolCallDecision {
			req := ApprovalRequest{
				Kind: ApprovalReAdmit, Mode: mode.String(), Level: v.Level,
				Tool: call.Name, Summary: riskSummary(call),
				Justification: escalationJustification(call),
				Risk:          riskWord(v.Risk, lang), Authorization: authWord(v.Authorization, lang),
				Rationale: firstReason(v), ReadRoot: root,
			}
			req.Origin = agentcore.OriginFromContext(ctx)
			done := opts.promptNote(approvalQuestion(req.Tool, req.Summary))
			defer done()
			ans := opts.Confirm(ctx, req)
			if !ans.Answered || !ans.Approve {
				return nil
			}
			how, granted := ApplyReAdmittedPath(ans, lang)
			if !granted {
				return nil
			}
			notify(Note{
				Tool: call.Name, ToolCallID: call.ID, Kind: NoteSandboxed, Level: Sandbox, Lang: lang,
				Summary: riskSummary(call), Risk: riskWord(v.Risk, lang),
				Authorization: authWord(v.Authorization, lang),
				Rationale: localized(lang,
					"越界请求被拒；已把该路径放回沙箱，改为在沙箱内运行："+how,
					"the escape was declined; the path is back inside the sandbox, so it runs contained: "+how),
			})
			return &agentcore.BeforeToolCallDecision{Sandbox: true}
		}
		// An escalation is a request, not an escape, and the mode decides who
		// answers it. Auto answers by review alone: an explicit Direct grant from
		// the reviewer runs the call bare (and says so), anything less is
		// re-contained — pulling a human in here would break the one promise
		// auto makes. Ask answers with a human, whose dialog now defaults to
		// containment, because the reviewer already graded the call runnable
		// inside the sandbox.
		if escalationRequested(call.Arguments) && strings.EqualFold(call.Name, "bash") {
			// Containment is available whenever the sandbox can do the
			// enforcing: the reviewer graded the call sandbox-tier, or could not
			// be reached at all. A network request the reviewer did not
			// authorize is not contained by running the command locally.
			containable := sandboxable(opts, call.Name) &&
				(v.Failed || v.Level == Sandbox) && (!network || v.Network)
			// The path the request named, for the guidance a refusal carries: a
			// smaller fix than dropping the sandbox for the whole call usually
			// exists, and naming it beats inviting the same request again.
			deniedPath := GrantablePathFromText(riskSummary(call), escalationJustification(call), opts.Cwd)
			switch {
			case v.Level == Deny && !v.Failed:
				notify(noteFor(call, v, NoteDenied, lang))
				return blockCall(call, v, DenyGuidance)
			case optsIsAuto(state):
				if !v.Failed && v.Level != Sandbox && v.Direct {
					// The reviewer itself approved the escape. Announcing it is
					// what keeps "ran outside the sandbox" from being invisible.
					notify(Note{
						Tool: call.Name, ToolCallID: call.ID, Kind: NoteEscaped, Level: Allow, Lang: lang,
						Summary: riskSummary(call), Risk: riskWord(v.Risk, lang),
						Authorization: authWord(v.Authorization, lang),
						Rationale: localized(lang,
							"评审批准了本次越界，命令将在沙箱外运行",
							"review approved the escape; the call runs outside the sandbox"),
					})
					return &agentcore.BeforeToolCallDecision{Direct: true}
				}
				// The escape is refused, and what the call needs is usually one
				// path: offer to bring it back INSIDE the sandbox before falling
				// through to auto's defaults. That is the smaller answer than
				// running the call bare, and it covers both shapes of refusal —
				// a call that can be contained (which would otherwise run and
				// fail on the very path) and one that cannot (which would
				// otherwise just stop). Asked once per path per run: a refusal
				// must not become a nag, and it changes nothing below.
				if deniedPath != "" && opts.Confirm != nil && readmitAsked.First(deniedPath) {
					if dec := readmitPath(deniedPath); dec != nil {
						return dec
					}
				}
				if containable {
					notify(Note{
						Tool: call.Name, ToolCallID: call.ID, Kind: NoteSandboxed, Level: Sandbox, Lang: lang,
						Summary: riskSummary(call), Risk: riskWord(v.Risk, lang),
						Authorization: authWord(v.Authorization, lang),
						Rationale: localized(lang,
							"模型请求在沙箱外运行，auto 由评审决定：判定可收容，改为在沙箱内运行",
							"the model asked to run outside; auto leaves that to review, which graded it containable — it runs in the sandbox"),
					})
					return grant(true)
				}
				notify(noteFor(call, v, NoteDenied, lang))
				return blockCall(call, v, "sandbox escape declined: auto mode answers escapes by review alone — make the command work inside the sandbox, or ask the user to switch /permissions to ask"+escapeFixGuidance(deniedPath))
			}
			if interactive && opts.Confirm == nil {
				ans := promptRisk(ctx, opts, mode.String(), call, v, ApprovalEscalation)
				if ans.allow {
					if ans.writableRoot != "" {
						rationale := "contained in the sandbox; " + ans.writableRoot + " is writable for this session"
						if ans.persisted {
							rationale = "contained in the sandbox; " + ans.writableRoot + " is writable and remembered"
						}
						notify(Note{
							Tool: call.Name, ToolCallID: call.ID, Kind: NoteApproved, Level: Allow, Lang: lang,
							Summary: riskSummary(call), Risk: riskWord(v.Risk, lang),
							Authorization: authWord(v.Authorization, lang), Rationale: rationale,
						})
						return grant(true)
					}
					return &agentcore.BeforeToolCallDecision{Direct: true}
				}
				if containable {
					notify(noteFor(call, v, NoteSandboxed, lang))
					return grant(true)
				}
				return blockCall(call, v, "sandbox escape was not approved; failing closed")
			}
			if dec, ok := ask(ctx, call, v, ApprovalEscalation); ok {
				return dec
			}
			if containable {
				notify(noteFor(call, v, NoteSandboxed, lang))
				return grant(true)
			}
			return blockCall(call, v, "running outside the sandbox was not authorized; failing closed"+escapeFixGuidance(deniedPath))
		}
		switch {
		case v.Failed:
			if network {
				// An unavailable reviewer is not an authorization to turn the
				// network on. The same approval dialog can decide it explicitly.
				// The note waits for that answer: nothing about this call is
				// announced while a question about it is still open.
				if dec, ok := askNetwork(); ok {
					return dec
				}
				notify(noteFor(call, v, NoteUnavailable, lang))
				return blockCall(call, v, "cannot review this network request; failing closed")
			}
			notify(noteFor(call, v, NoteUnavailable, lang))
			if sandboxable(opts, call.Name) {
				// Contain it: the execution layer runs the call isolated (the
				// decision rides the executor context down to the tool).
				return grant(true)
			}
			if interactive && !optsIsAuto(state) && promptRisk(ctx, opts, mode.String(), call, v, ApprovalNoSandbox).allow {
				return &agentcore.BeforeToolCallDecision{Direct: true}
			}
			if dec, ok := ask(ctx, call, v, ApprovalReviewFailed); ok {
				return dec
			}
			return blockCall(call, v, "reviewer unavailable and no sandbox for this tool; failing closed")
		case v.Level == Allow:
			if network && !v.Network {
				if dec, ok := askNetwork(); ok {
					return dec
				}
				return blockCall(call, v, "network access was not authorized; failing closed")
			}
			if network {
				// A shell that reaches the network needs BOTH the filesystem
				// sandbox and the grant: where the platform demands isolation, a
				// shell we cannot isolate must not run networked at all. The rule
				// names bash because it is about exec tools — a web tool reaches
				// the network from this process and has no filesystem boundary to
				// lose, so its consent is the network question above, not a
				// missing runner.
				if opts.RequireSandbox && strings.EqualFold(call.Name, "bash") && !sandboxable(opts, call.Name) {
					return blockCall(call, v, "no OS sandbox available for networked bash; failing closed")
				}
				return grant(false)
			}
			if strings.EqualFold(call.Name, "bash") && opts.RequireSandbox && !sandboxable(opts, call.Name) {
				if dec, ok := ask(ctx, call, v, ApprovalNoSandbox); ok {
					return dec
				}
				return blockCall(call, v, "no OS sandbox available for bash; failing closed")
			}
			return nil
		case v.Level == Deny:
			notify(noteFor(call, v, NoteDenied, lang))
			return blockCall(call, v, DenyGuidance)
		case v.Level == Sandbox:
			if network && !v.Network {
				if dec, ok := askNetwork(); ok {
					return dec
				}
				return blockCall(call, v, "network access was not authorized; failing closed")
			}
			if sandboxable(opts, call.Name) {
				notify(noteFor(call, v, NoteSandboxed, lang))
				return grant(true)
			}
			if interactive && !optsIsAuto(state) && promptRisk(ctx, opts, mode.String(), call, v, ApprovalNoSandbox).allow {
				return &agentcore.BeforeToolCallDecision{Direct: true}
			}
			if dec, ok := ask(ctx, call, v, ApprovalNoSandbox); ok {
				return dec
			}
			notify(noteFor(call, v, NoteBlockedNoPrompt, lang))
			return blockCall(call, v, "no sandbox runner for this call (GOLDER_SANDBOX=off or no platform sandbox available); failing closed")
		default: // Confirm
			if network && !v.Network {
				if dec, ok := askNetwork(); ok {
					return dec
				}
				return blockCall(call, v, "network access was not authorized; failing closed")
			}
			if strings.EqualFold(call.Name, "bash") && opts.RequireSandbox && !sandboxable(opts, call.Name) {
				if dec, ok := ask(ctx, call, v, ApprovalNoSandbox); ok {
					return dec
				}
				return blockCall(call, v, "no OS sandbox available for bash; failing closed")
			}
			if optsIsAuto(state) {
				notify(noteFor(call, v, NoteApproved, lang))
				if network {
					return grant(false)
				}
				return nil
			}
			if interactive && promptRisk(ctx, opts, mode.String(), call, v, ApprovalConfirm).allow {
				if network {
					return grant(false)
				}
				return nil
			}
			if dec, ok := ask(ctx, call, v, ApprovalConfirm); ok {
				if network && dec == nil {
					return grant(false)
				}
				if network && dec != nil && !dec.Block && !dec.Direct {
					dec.Network = true
				}
				return dec
			}
			notify(noteFor(call, v, NoteBlockedNoPrompt, lang))
			return blockCall(call, v, "needs confirmation; failing closed without a prompt")
		}
	}
}

// ApplyReAdmittedPath registers what a re-admission answer granted and returns
// the phrase a note should carry. Read answers are registered here — the gate
// owns the read-root registry — while the write rows register themselves (the
// write dialog owns that callback), and a write grant carries read, so either
// shape leaves the blocked path reachable inside the sandbox. ok=false means the
// answer granted nothing: a denial, or a registration the registry refused.
//
// It is the one place that turns one of these answers into a table row, so the
// two triggers that ask the question — a refused escape, and a command the
// sandbox just denied — cannot drift apart in what a "yes" means.
func ApplyReAdmittedPath(ans ApprovalAnswer, lang string) (how string, ok bool) {
	readRoot, writeRoot := strings.TrimSpace(ans.ReadRoot), strings.TrimSpace(ans.WritableRoot)
	switch {
	case readRoot != "":
		life, source := grants.Session, "path re-admission"
		if ans.Persisted {
			life, source = grants.Remembered, "path re-admission (remembered)"
		}
		if _, err := grants.AddReadRoot(readRoot, life, source); err != nil {
			return "", false
		}
		how = localized(lang, "本会话可读 "+readRoot, readRoot+" is readable for this session")
		if ans.Persisted {
			how = localized(lang, readRoot+" 可读并已记住", readRoot+" is readable and remembered")
		}
		return how, true
	case writeRoot != "":
		// The write rows register the root themselves, and a write grant carries
		// read, so the blocked path is covered either way.
		return localized(lang, "本会话可读写 "+writeRoot+"（仍在沙箱内）",
			writeRoot+" is readable and writable for this session (still contained)"), true
	}
	return "", false
}

// askApproval routes a call that needs the user's answer through the driver's
// Confirm callback. mode and lang ride along so the question can say which mode
// is asking and which grade it is about — the two facts that tell a person
// whether this dialog is even theirs to answer. ok=false means no callback is
// wired (or it could not answer): the caller keeps its fail-closed behavior, so
// a driver that never wired approval can not be tricked into allowing by silence.
func askApproval(ctx context.Context, opts GateOpts, mode string, call agentcore.AgentToolCall, v Verdict, kind ApprovalKind, lang string) (*agentcore.BeforeToolCallDecision, bool) {
	if opts.Confirm == nil {
		return nil, false
	}
	req := ApprovalRequest{
		Kind:          kind,
		Mode:          mode,
		Level:         v.Level,
		Tool:          call.Name,
		Summary:       riskSummary(call),
		Justification: escalationJustification(call),
		Risk:          riskWord(v.Risk, lang),
		Authorization: authWord(v.Authorization, lang),
		Rationale:     firstReason(v),
	}
	req.Origin = agentcore.OriginFromContext(ctx)
	done := opts.promptNote(approvalQuestion(req.Tool, req.Summary))
	defer done()
	ans := opts.Confirm(ctx, req)
	if !ans.Answered {
		return nil, false
	}
	if !ans.Approve {
		if opts.Notify != nil {
			denied := "denied by the user"
			if lang == "zh" {
				denied = "用户拒绝了本次请求"
			}
			opts.Notify(Note{
				Tool: call.Name, ToolCallID: call.ID, Kind: NoteDenied, Level: Deny, Lang: lang,
				Summary: req.Summary, Risk: req.Risk, Authorization: req.Authorization,
				Rationale: denied, Origin: req.Origin, Mode: mode,
			})
		}
		return blockCall(call, v, "denied by the user; propose a different approach or ask what they would prefer"), true
	}
	if opts.Notify != nil {
		approved := "approved by the user"
		if lang == "zh" {
			approved = "用户批准"
		}
		rationale := approved
		if root := strings.TrimSpace(ans.WritableRoot); root != "" {
			rationale = "contained in the sandbox; " + root + " is writable for this session"
			if lang == "zh" {
				rationale = "已收容在沙箱内；本会话可写 " + root
			}
			if ans.Persisted {
				rationale = "contained in the sandbox; " + root + " is writable and remembered"
				if lang == "zh" {
					rationale = "已收容在沙箱内；" + root + " 可写并已记住"
				}
			}
		}
		opts.Notify(Note{
			Tool: call.Name, ToolCallID: call.ID, Kind: NoteApproved, Level: Allow, Lang: lang,
			Summary: req.Summary, Risk: req.Risk, Authorization: req.Authorization,
			Rationale: rationale, Origin: req.Origin, Mode: mode,
		})
	}
	if ans.Contained {
		// "Keep it in the sandbox" — the escalation dialog's default answer, and
		// an approval of the call rather than of the escape: the tool runs with
		// the sandbox doing the enforcing.
		return &agentcore.BeforeToolCallDecision{Sandbox: true}, true
	}
	if strings.TrimSpace(ans.WritableRoot) != "" {
		// The call ran into the sandbox (or could not be reviewed); with the
		// path now writable, keeping it isolated is strictly safer than the
		// unisolated approval it replaces.
		return &agentcore.BeforeToolCallDecision{Sandbox: true}, true
	}
	switch kind {
	case ApprovalNetwork:
		return &agentcore.BeforeToolCallDecision{Sandbox: true, Network: true}, true
	case ApprovalEscalation, ApprovalNoSandbox, ApprovalReviewFailed:
		return &agentcore.BeforeToolCallDecision{Direct: true}, true
	default:
		return nil, true
	}
}

// escapeFixGuidance names the smaller fix for a declined escape. The request
// asks to drop the sandbox for a whole call, when what the call needs is
// usually one path: a read re-admitted inside the sandbox (/boundary readable
// add, or GOLDER_SANDBOX_READABLE for the next run), a write granted by the ask
// dialog's w/s rows, which keep the call contained. root is named when the
// request itself carried a plausible one, so the advice points at a real
// operand instead of a placeholder.
func escapeFixGuidance(root string) string {
	at := ""
	if root != "" {
		at = " (" + root + ")"
	}
	return " A smaller fix usually exists: a blocked read" + at +
		" can be re-admitted inside the sandbox with `/boundary readable add <path>` (or GOLDER_SANDBOX_READABLE)," +
		" and a blocked write with the ask dialog's w/s rows, which grant the path and keep the call contained."
}

// escalationJustification extracts the model's reason from a bash escalation
// request, so the dialog can show it. Other tools have none.
func escalationJustification(call agentcore.AgentToolCall) string {
	var a struct {
		Justification string `json:"justification"`
	}
	if err := json.Unmarshal(call.Arguments, &a); err != nil {
		return ""
	}
	return strings.TrimSpace(a.Justification)
}

// escalationRequested reports whether the model asked to run this call outside
// the sandbox (bash's sandbox_permissions=require_escalated). It is a request,
// not a grant: the gate still owns the decision, this only changes how a
// Sandbox verdict is settled — asking the user instead of silently recontaining
// the call. The reviewer sees it in the call's arguments, which is how it gets
// weighed. The literal mirrors agenttool's
// sandboxPermissionEscalated; the judge package cannot import it (agenttool
// imports judge).
func escalationRequested(args json.RawMessage) bool {
	var a struct {
		SandboxPermissions string `json:"sandbox_permissions"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.SandboxPermissions), "require_escalated")
}

// EscalationRequested is a request fact for the fast decision-model reviewer.
// It does not authorize the escape; only the final Verdict.Direct does.
func EscalationRequested(tool string, args json.RawMessage) bool {
	return strings.EqualFold(tool, "bash") && escalationRequested(args)
}

// NetworkRequested reports whether a call reaches the network. Two shapes count:
// bash asking for it (`network: "on"`), and the tools whose whole effect IS an
// outbound request — webfetch and websearch. The second shape matters because
// those tools run in this process, where no OS sandbox can contain them: the
// gate is the only boundary they have, so a call that skipped the network
// question would reach the internet on the strength of a tool description
// rather than on a decision.
//
// A request is never an authorization: the gate grants it only after the
// selected reviewer or the person answers, and the executor never reads the
// argument as a capability.
func NetworkRequested(tool string, args json.RawMessage) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash":
		var a struct {
			Network string `json:"network"`
		}
		return json.Unmarshal(args, &a) == nil && a.Network == "on"
	case "webfetch", "websearch":
		return true
	}
	return false
}

// firstReason returns the verdict's leading reason, for the dialog body.
func firstReason(v Verdict) string {
	if len(v.Reasons) == 0 {
		return ""
	}
	return v.Reasons[0]
}

// optsIsAuto reports whether the state's mode is auto (the gate then decides
// without prompting).
func optsIsAuto(state *permissions.State) bool {
	return state != nil && state.Mode() == permissions.Auto
}

// sandboxable reports whether the execution layer can isolate this call.
func sandboxable(opts GateOpts, tool string) bool {
	return opts.Sandboxed != nil && opts.Sandboxed(tool)
}

// classifyForGate grades one call. A nil classifier keeps the static floor
// and fails closed for every gated tool, so wiring mistakes never widen
// permissions.
func classifyForGate(ctx context.Context, opts GateOpts, call agentcore.AgentToolCall) Verdict {
	if opts.Classifier != nil {
		// The pipeline — static floor, then the reviewer — lives in Chain, and it
		// has to run ONCE per call rather than once per layer. Every production
		// wiring builds a Chain (run.NewReviewer), so the common shape is passed
		// straight through; a bare grader is wrapped, which keeps the
		// deterministic floor in force for it too — a wiring mistake must not be
		// able to drop the credential denials.
		//
		// Running the floor HERE instead would run it a second time for every
		// gated call, the duplication this shape removes.
		if chain, ok := opts.Classifier.(Chain); ok {
			return chain.Classify(ctx, call.Name, call.Arguments)
		}
		return Chain{Inner: opts.Classifier}.Classify(ctx, call.Name, call.Arguments)
	}
	if v, ok := staticDeny(call.Name, call.Arguments); ok {
		return v
	}
	return Verdict{
		Level:   Confirm,
		Failed:  true,
		Source:  "static",
		Reasons: []string{"no reviewer configured, failing closed"},
	}
}

// noteFor builds the user-facing note for one verdict.
func noteFor(call agentcore.AgentToolCall, v Verdict, kind NoteKind, lang string) Note {
	n := Note{
		Tool:          call.Name,
		ToolCallID:    call.ID,
		Kind:          kind,
		Level:         v.Level,
		Summary:       riskSummary(call),
		Risk:          v.Risk,
		Authorization: v.Authorization,
		Lang:          v.Lang,
	}
	if len(v.Reasons) > 0 {
		n.Rationale = v.Reasons[0]
	}
	if n.Lang == "" {
		n.Lang = lang
	}
	return n
}

// ChainGates composes two BeforeToolCall gates so the run gets trust first,
// then the risk grade, then the user hooks: a block from first
// short-circuits (second never runs), so an earlier gate stays authoritative
// over a later one. A nil operand is identity. A sandbox request from either
// gate survives composition: the second gate's decision wins field-by-field as
// before, but the sandbox bit is OR-ed in so a sandbox-tier grade cannot be
// dropped by a later no-op gate.
func ChainGates(first, second agentcore.BeforeToolCallFunc) agentcore.BeforeToolCallFunc {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		firstDec := first(ctx, call)
		if firstDec != nil && firstDec.Block {
			return firstDec
		}
		dec := second(ctx, call)
		if dec != nil && dec.Block {
			return dec
		}
		// Merge the first gate's requests into the second's decision. Both
		// fields describe what the FIRST gate asked for and the second gate has
		// no opinion about: Sandbox is the containment the permission gate
		// demanded, and ReadGrant is the workspace-boundary answer the
		// read-scope gate belongs to. Dropping either when the second gate
		// returns nil (the common case: a tool with no further opinion) would
		// silently discard the user's decision — the read would be refused after
		// they approved it.
		if firstDec != nil && (firstDec.Sandbox || firstDec.Network || firstDec.Direct || firstDec.ReadGrant != "" || firstDec.WriteGrant != "") {
			if dec == nil {
				return firstDec
			}
			out := *dec
			out.Sandbox = out.Sandbox || firstDec.Sandbox
			out.Network = out.Network || firstDec.Network
			out.Direct = (out.Direct || firstDec.Direct) && !out.Sandbox
			if out.ReadGrant == "" {
				out.ReadGrant = firstDec.ReadGrant
			}
			if out.WriteGrant == "" {
				out.WriteGrant = firstDec.WriteGrant
			}
			return &out
		}
		return dec
	}
}

func blockCall(call agentcore.AgentToolCall, v Verdict, tail string) *agentcore.BeforeToolCallDecision {
	msg := fmt.Sprintf("tool %q blocked by risk judge (%s", call.Name, v.Level)
	if len(v.Reasons) > 0 {
		msg += ": " + strings.Join(v.Reasons, "; ")
	}
	msg += ")"
	if tail != "" {
		msg += "; " + tail
	}
	content := agentcore.ContentList{agentcore.NewTextContent(msg)}
	return &agentcore.BeforeToolCallDecision{Block: true, Content: &content}
}

// DenyGuidance is the actionable tail on hard blocks: what to change to get
// the call executable again. It ships in tail (not in the suffix) so every
// Deny path carries the same way back.
const DenyGuidance = "refine the call to avoid privilege escalation, destructive scope, or credential material; split it into smaller reviewable steps"

// riskAnswer is the stdin prompt's decision. It is richer than a bool because
// an escalation can be answered with a path grant rather than an unisolated
// approval: allow runs the call (unisolated when the prompt was an escalation),
// writableRoot asks the gate to keep it contained with that root writable
// instead, and persisted says the grant was written down.
type riskAnswer struct {
	allow        bool
	writableRoot string
	persisted    bool
}

// promptRisk asks the person on stdin (interactive drivers only) whether a call
// may run. mode and kind are what the decision line needs to open with: the
// same grade is a different question under auto and under ask, and the kind
// says what is being weighed — an escape, a missing sandbox runner, or an
// ordinary confirmation.
//
// An escalation additionally offers the twin of the TUI dialog's rows: `w`
// re-admits just the path the command was denied, keeping the call contained,
// and `s` does the same while remembering the grant across sessions. Both need
// opts.GrantWritable and a plausible path; without them the prompt is the plain
// y/N, which is what a driver that cannot grant should see.
func promptRisk(ctx context.Context, opts GateOpts, mode string, call agentcore.AgentToolCall, v Verdict, kind ApprovalKind) riskAnswer {
	// The pane is about to wait for a person, whichever way this prompt
	// travels; the driver's transport hears about it before the read starts and
	// hears it is over when the answer lands.
	done := opts.promptNote(approvalQuestion(call.Name, riskSummary(call)))
	defer done()
	if opts.Mu != nil {
		opts.Mu.Lock()
		defer opts.Mu.Unlock()
	}
	out := opts.Out
	if out == nil {
		return riskAnswer{}
	}
	// A sub-agent's question says who is asking, for the same reason the TUI
	// dialog does: the person is consenting to a call their own conversation did
	// not make, and the stdin prompt is the only label they will get.
	from := ""
	if origin := agentcore.OriginFromContext(ctx); origin != "" {
		from = " (from " + origin + ")"
	}
	// The decision line leads, as it does in the dialog and the transcript
	// note: tool · mode · grade → who settles it. Stdin has no color and no
	// layout to carry that context, so this line has to say the whole thing by
	// itself — and "which mode am I in" is the first question a reader has.
	rating := ""
	if v.Risk != "" || v.Authorization != "" {
		rating = fmt.Sprintf(" (risk: %s, authorization: %s)", riskWord(v.Risk, v.Lang), authWord(v.Authorization, v.Lang))
	}
	fmt.Fprintf(out, "\n%s%s%s\n", DecisionLine(call.Name, mode, v.Level, AskDisposition(kind, v.Lang)), rating, from)
	n := len(v.Reasons)
	if n > 3 {
		n = 3
	}
	for _, r := range v.Reasons[:n] {
		fmt.Fprintf(out, "  - %s\n", truncateRunes(r, 160))
	}
	if summary := riskSummary(call); summary != "" {
		fmt.Fprintf(out, "  %s\n", summary)
	}
	if j := escalationJustification(call); j != "" {
		fmt.Fprintf(out, "  model's reason: %s\n", truncateRunes(j, 160))
	}
	grantRoot := ""
	if kind == ApprovalEscalation && opts.GrantWritable != nil {
		grantRoot = GrantablePathFromText(riskSummary(call), escalationJustification(call), opts.Cwd)
	}
	if grantRoot != "" {
		fmt.Fprintf(out, "Allow? [y]es (runs outside the sandbox) / [n]o / [w]rites to %s (this session)"+
			" / [s]ave writes to %s (remembered) [y/N/w/s]: ", grantRoot, grantRoot)
	} else if kind == ApprovalEscalation || kind == ApprovalNoSandbox {
		fmt.Fprint(out, "Allow? [y]es (runs outside the sandbox) / [n]o [y/N]: ")
	} else {
		fmt.Fprint(out, "Allow? [y/N]: ")
	}
	// The read is interruptible: a Ctrl+C during the prompt cancels the run
	// context and yields a denial immediately instead of trapping the user
	// until they answer (same contract as the trust gate).
	line, ok := readLineOrCancel(ctx, opts.In)
	if !ok {
		return riskAnswer{}
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return riskAnswer{allow: true}
	case "w", "write", "writes":
		if grantRoot != "" {
			if made, why := opts.GrantWritable(grantRoot, false); made {
				return riskAnswer{allow: true, writableRoot: grantRoot}
			} else if why != "" {
				fmt.Fprintf(out, "  %s\n", why)
			}
		}
		return riskAnswer{}
	case "s", "save":
		if grantRoot != "" {
			if made, why := opts.GrantWritable(grantRoot, true); made {
				return riskAnswer{allow: true, writableRoot: grantRoot, persisted: true}
			} else if why != "" {
				fmt.Fprintf(out, "  %s\n", why)
			}
		}
		return riskAnswer{}
	default:
		return riskAnswer{}
	}
}

// readLineOrCancel reads one line, returning ok=false when ctx fires first.
// The orphaned read may deliver late input to the next prompt on the shared
// reader; that matches the existing typed-ahead contract (input is never
// split between prompts). Duplicated from internal/trust on purpose: the
// leaf packages stay independent and the function is twelve lines.
func readLineOrCancel(ctx context.Context, in *bufio.Reader) (line string, ok bool) {
	type result struct {
		line string
	}
	ch := make(chan result, 1)
	go func() {
		line, _ := in.ReadString('\n')
		ch <- result{line}
	}()
	select {
	case r := <-ch:
		return r.line, true
	case <-ctx.Done():
		return "", false
	}
}

func riskSummary(call agentcore.AgentToolCall) string {
	raw := strings.TrimSpace(string(call.Arguments))
	if raw == "" || raw == "{}" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return truncateRunes(raw, 200)
	}
	if cmd, ok := args["command"].(string); ok && cmd != "" {
		return "command: " + truncateRunes(cmd, 200)
	}
	if p, ok := args["path"].(string); ok && p != "" {
		return "path: " + truncateRunes(p, 200)
	}
	if p, ok := args["prompt"].(string); ok && p != "" {
		return "task: " + truncateRunes(p, 200)
	}
	return truncateRunes(raw, 200)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …"
}
