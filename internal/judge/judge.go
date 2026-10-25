package judge

// Package judge grades the risk of a single tool call so the run gets a
// middle tier between "directory trusted, run anything" and "ask a human
// for everything" (trust tri-state in internal/trust, allow/deny lists in
// internal/cli/run). It stays below the layers that drive it — no imports from
// trust/runtime/hooks — so the cli layer can chain it onto the existing
// BeforeToolCall seam without creating an import cycle; its own imports are the
// leaf packages it shares with them (agentcore, permissions, grants,
// golderhome, patch, shellfacts).
//
// Chain of responsibility, cheapest first:
//
//  1. StaticFloor — a tiny hard-deny list for unambiguously catastrophic
//     calls (sudo, mkfs, keys under .ssh). It never allows, it only denies.
//  2. LLMJudge — the active conversation model reviews the call against a
//     policy prompt and the user's recent messages, and returns one of
//     Allow/Confirm/Contain/Deny plus a rationale written in the user's
//     language. There is no separate classification service: the dependency
//     is the model the session is already using.
//  3. Fallback — any reviewer failure (timeout, transport error, malformed
//     answer) marks the verdict Failed: the gate then prompts (interactive)
//     or handles the call conservatively (sandbox or block), never allows.
//
// Enforcement lives in ConsentGate and is driven by the live permissions.Mode:
// read-only blocks mutating tools, ask prompts on anything above Allow, auto
// decides automatically and announces each non-trivial
// verdict with its rationale, full-access skips review and sandboxing while
// keeping the static floor. Sandbox execution lives in internal/seatbelt and is
// injected by internal/cli/run: an approved bash call runs under the platform
// sandbox by default, and the native file tools run through a one-call isolated
// worker. Network is a separate per-call decision (Verdict.Network); a sandbox
// escape is authorized by the level itself, not a second answer: allow/confirm
// run the call, contain isolates it, deny blocks.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// Level is the risk tier for one tool call.
type Level int

const (
	// Allow runs without friction.
	Allow Level = iota
	// Confirm needs an explicit human yes.
	Confirm
	// Contain must run isolated: the reviewer will not vouch for this call
	// running anywhere else. Without a sandbox runner the gate prompts
	// (interactive) or blocks (headless) with a containment note. The name says
	// what the tier REQUIRES, not where the call ends up — Allow and Confirm
	// execute inside the sandbox too, unless the gate's envelope admits them
	// (see envelope.go).
	Contain
	// Deny never runs.
	Deny
)

func (l Level) String() string {
	switch l {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Contain:
		return "contain"
	default:
		return "deny"
	}
}

func levelFromString(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "contain":
		return Contain
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
	case "allow", "confirm", "contain", "deny":
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
	// level. It is set only when this classifier actually reviewed a call that
	// requests network access: a bash call asking network:on, or the in-process
	// network tools (webfetch, websearch), which cannot run without it. A level
	// of Allow/Confirm/Contain alone never implies it.
	Network bool
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
		// A chain with no inner grader cannot review anything. It used to
		// answer Allow, which made a mis-wired gate silently permissive; a
		// Failed verdict keeps the same "never guess" contract as every other
		// reviewer failure.
		return Verdict{Level: Confirm, Source: "static", Failed: true,
			Reasons: []string{"no reviewer configured"}}
	}
	return c.Inner.Classify(ctx, tool, args)
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
	// ApprovalConsent is the unified capability question. Its Needs name every
	// capability the call requires; the mode decides who answers, and one
	// answer settles all of them.
	ApprovalConsent ApprovalKind = "consent"
	// ApprovalTrust: a side-effect tool in a directory the user has not
	// trusted. The gate is the trust manager, not the reviewer; the dialog
	// asks the same question the REPL's first-run prompt does.
	ApprovalTrust ApprovalKind = "trust"
)

// ApprovalRequest is what the gate shows the user: the call itself plus the
// reviewer's verdict, so the decision is informed rather than a bare yes/no.
type ApprovalRequest struct {
	Kind ApprovalKind
	// Source names the layer that reached the verdict: jev, local, llm or
	// static. The dialog prints it in the decision line so the reader does not
	// have to infer the judge from the rationale's wording.
	Source string
	// Needs is the complete capability request for an ApprovalConsent. The
	// dialog shows them together; an approval grants exactly this set.
	Needs []Need
	// Mode is the approval mode in force for this call (auto, ask, read-only,
	// full-access). It is the answer to the reader's real question — "does a
	// human have to answer this?" — which is why every surface prints it: the
	// same verdict is the user's to settle under ask and already settled under
	// auto.
	Mode string
	// Level is the grade the gate acted on (allow, confirm, contain, deny),
	// printed as the canonical token rather than a paraphrase so the dialog,
	// the notes and /permissions all use one vocabulary.
	Level   Level
	Tool    string
	Summary string
	// Justification carries the model's reason when the call is an escalation
	// request (bash sandbox_permissions=require_escalated); empty otherwise.
	Justification string
	// TrustDir names the directory a trust question is about, or "" for every
	// other kind. The trust gate's session answers - allow local reads, allow
	// all tools - are grants for this directory, so the dialog names it rather
	// than leaving "this folder" to be guessed. Set only for ApprovalTrust.
	TrustDir  string
	Rationale string
	// Origin names the agent that asked, when it is not the session's own
	// conversation: a sub-agent's dispatch, filled from the call's context. The
	// dialog prints it so the person knows who is asking; a question that reads
	// as coming from the conversation when a delegated child asked it is a
	// question about a call they never saw.
	Origin string
}

// ApprovalAnswer is the user's decision.
type ApprovalAnswer struct {
	// Scope, Needs and Exec are the capability answer for ApprovalConsent.
	Scope Scope
	Needs []Need
	Exec  ExecMode
	// Approve runs the call. False blocks it (the model is told it was
	// denied and can propose something else).
	Approve bool
	// Always remembers the decision for this session, keyed by the request's
	// tool and summary prefix, so the same shape of call is not asked again.
	Always bool
	// Answered is false when the dialog could not run (ctx cancelled, no UI);
	// the gate then falls back to its fail-closed behavior.
	Answered bool
	// SessionReads, when set together with Approve, means the user allowed
	// local reads in the trust gate's directory for the session (the trust
	// dialog's "reads here" answer). The gate registers it with the trust
	// manager; being a trust decision rather than a workspace-boundary one,
	// reads outside the workspace still take the read-scope gate's consent.
	SessionReads bool
}

// NoteKind classifies one user-facing verdict note.
type NoteKind int

const (
	// NoteApproved: a call the gate allowed to run directly — auto's Confirm
	// grade, or a user's yes under ask.
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
	Summary string
	// Source names the layer that reached the verdict (jev/local/llm/static).
	// FormatNote prints it in the decision line.
	Source string
	// Needs names the capabilities the call required when it was not granted;
	// Granted names what the answer actually gave. They replace the old
	// high/medium/low risk and authorization words: a reader can act on
	// "write /tmp/out, network github.com" but not on "risk: medium".
	Needs         []Need
	Granted       []Need
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
	// Escalation records that the call asked to run outside the sandbox (bash's
	// sandbox_permissions=require_escalated). The gate decides such a call twice
	// — once on the command's own merits, once on the escape — and a note that
	// cannot say which of the two was refused renders as "allow → denied", which
	// reads as a contradiction instead of a decision.
	Escalation bool
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
// Contain verdict is settled — asking the user instead of silently recontaining
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

// askedToEscape reports whether a call asked to leave the sandbox, which only
// bash can do. It is what lets a note say what a denial refused.
func askedToEscape(call agentcore.AgentToolCall) bool {
	return strings.EqualFold(call.Name, "bash") && escalationRequested(call.Arguments)
}

// directForLevel reports whether a level authorizes a requested sandbox escape:
// allow and confirm run the call, contain isolates it and deny blocks. The level
// is the whole answer; there is no separate direct grant.
func directForLevel(level Level) bool {
	return level == Allow || level == Confirm
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
	if facadeNetworkRequested(tool, args) {
		return true
	}
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

// classifyForGate grades one call. A nil classifier keeps the static floor
// and fails closed for every gated tool, so wiring mistakes never widen
// permissions.
func classifyForGate(ctx context.Context, classifier Classifier, call agentcore.AgentToolCall) Verdict {
	if classifier != nil {
		// The pipeline — static floor, then the reviewer — lives in Chain, and it
		// has to run ONCE per call rather than once per layer. Every production
		// wiring builds a Chain (run.NewReviewer), so the common shape is passed
		// straight through; a bare grader is wrapped, which keeps the
		// deterministic floor in force for it too — a wiring mistake must not be
		// able to drop the credential denials.
		//
		// Running the floor HERE instead would run it a second time for every
		// gated call, the duplication this shape removes.
		if chain, ok := classifier.(Chain); ok {
			return chain.Classify(ctx, call.Name, call.Arguments)
		}
		return Chain{Inner: classifier}.Classify(ctx, call.Name, call.Arguments)
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
		Source:        v.Source,
		Summary:       riskSummary(call),
		Risk:          v.Risk,
		Authorization: v.Authorization,
		Escalation:    askedToEscape(call),
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
		// no opinion about: the containment request is what the permission gate
		// demanded, and ReadGrant is the workspace-boundary answer the
		// read-scope gate belongs to. Dropping either when the second gate
		// returns nil (the common case: a tool with no further opinion) would
		// silently discard the user's decision — the read would be refused after
		// they approved it.
		if firstDec != nil && (firstDec.Sandbox || firstDec.Network || firstDec.HostControl || firstDec.Direct || firstDec.Bare || firstDec.ReadGrant != "" || firstDec.WriteGrant != "") {
			if dec == nil {
				return firstDec
			}
			out := *dec
			out.Sandbox = out.Sandbox || firstDec.Sandbox
			out.Network = out.Network || firstDec.Network
			out.HostControl = out.HostControl || firstDec.HostControl
			out.Direct = (out.Direct || firstDec.Direct) && !out.Sandbox
			// Bare is an envelope decision too: dropping it just because the
			// second gate had no opinion would silently re-contain a call the
			// first gate had already settled as host-safe.
			out.Bare = out.Bare || (firstDec.Bare && !out.Sandbox)
			if out.Bare && out.BareReason == "" {
				out.BareReason = firstDec.BareReason
			}
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
