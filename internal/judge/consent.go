package judge

// Consent is the one authorization seam: every capability a call needs is
// collected first, the active permission mode decides who answers, and the
// person (if anyone) sees one request for the whole call. Risk review is an
// input to that decision, not a second permission system.
//
// The old gates asked boundary, network and sandbox questions independently.
// That made a single shell command capable of producing three dialogs, each
// with its own lifetime and wording. Here the call is reduced to a set of
// Needs; the mode policy settles them, and the driver only carries one request.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// Capability is one thing a call may need beyond the default sandbox policy.
type Capability uint8

const (
	CapReadOutside Capability = 1 << iota
	CapWriteOutside
	CapNetwork
	CapUnconfined
)

// Need is one capability request with the exact scope it would cover.
type Need struct {
	Cap Capability
	// Path is the resolved target for an outside-workspace need.
	Path string
	// Dir is the narrowest directory a session grant would cover.
	Dir string
	// Key names a network host/command.
	Key string
}

// Scope is the lifetime of an answered need.
type Scope uint8

const (
	ScopeOnce Scope = iota
	ScopeSession
	ScopeRemembered
)

// ExecMode is the execution envelope an answer chooses.
type ExecMode uint8

const (
	ExecSandboxed ExecMode = iota
	ExecUnconfined
)

// ConsentRequest is the complete question for one call. Needs is the exact set
// the call cannot run without; an Allow answer grants all of them.
type ConsentRequest struct {
	Tool          string
	Summary       string
	Justification string
	// Source names the layer that reached the verdict (jev/local/llm/static).
	// The driver prints it in the decision line.
	Source        string
	Needs         []Need
	Mode          string
	Level         Level
	Risk          string
	Authorization string
	Rationale     string
	Origin        string
	CanContain    bool
	RequestedExec ExecMode
}

// ConsentAnswer is one answer to a ConsentRequest. The driver never sets
// individual grant booleans: Needs and Scope are the decision.
type ConsentAnswer struct {
	Allow    bool
	Answered bool
	Scope    Scope
	Needs    []Need
	Exec     ExecMode
	// Reason is the reviewer's one-line explanation, when it gave one. The
	// dialog uses it for the note; a programmatic recovery uses it in the
	// transcript so a refusal is not silent.
	Reason string
}

// ConsentConfirm is the driver's one-question channel.
type ConsentConfirm func(ctx context.Context, req ConsentRequest) ConsentAnswer

// ConsentOpts configures the unified gate.
type ConsentOpts struct {
	Mode            *permissions.State
	Classifier      Classifier
	Cwd             string
	ExtraRoots      []string
	Sandboxed       func(tool string) bool
	RequireSandbox  bool
	Enforce         bool
	Notify          func(Note)
	Confirm         ConsentConfirm
	In              *bufio.Reader
	Out             io.Writer
	Mu              *sync.Mutex
	OnPrompt        func(question string) func()
	WritableAt      func(path string) bool
	GrantWritable   func(root string, persist bool) (bool, string)
	PersistReadRoot func(dir string) (bool, string)
}

// ConsentGate builds the single gate production drivers install after trust.
// It replaces the separate boundary and permission gates.
func ConsentGate(opts ConsentOpts) agentcore.BeforeToolCallFunc {
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		mode := permissions.FullAccess
		if opts.Mode != nil {
			mode = opts.Mode.Mode()
		}
		lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
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

		if v, bad := staticDeny(call.Name, call.Arguments); bad {
			notify(noteFor(call, v, NoteDenied, lang))
			return blockCall(call, v, DenyGuidance)
		}
		if mode == permissions.FullAccess {
			// Full access is a real mode: no review, no consent, no sandbox.
			// Only the static floor above survives.
			needs := collectNeeds(ctx, opts, call, Verdict{}, mode, lang)
			if !hasPathNeed(needs) {
				// No path grant is needed: the tool's own full-access branch
				// already runs it bare and its network branch already admits
				// egress. Returning an empty decision would only add noise to
				// callers that treat nil as "nothing to say".
				return nil
			}
			dec := applyNeeds(call, opts, needs, ScopeOnce, lang)
			if dec == nil {
				return nil
			}
			return dec
		}
		if dec := boundaryBlocked(call, opts); dec != nil {
			return dec
		}

		if mode == permissions.ReadOnly && !readOnlyAllowedCall(call.Name, call.Arguments) {
			notify(Note{
				Tool: call.Name, ToolCallID: call.ID, Kind: NoteReadOnly, Level: Deny, Lang: lang,
				Summary:   riskSummary(call),
				Rationale: localized(lang, "只读档位只允许工作区内读取", "read-only permits in-workspace reads only"),
			})
			return blockCall(call, Verdict{Level: Deny, Source: "static", Reasons: []string{"read-only permissions mode"}},
				"switch with /permissions ask|auto|full-access to allow mutating tools")
		}
		v := classifyForGate(ctx, opts.Classifier, call)
		if mode == permissions.ReadOnly {
			needs := collectNeeds(ctx, opts, call, v, mode, lang)
			if len(needs) > 0 {
				notify(Note{
					Tool: call.Name, ToolCallID: call.ID, Kind: NoteReadOnly, Level: Deny, Lang: lang,
					Summary:   riskSummary(call),
					Rationale: localized(lang, "只读档位只允许工作区内读取", "read-only permits in-workspace reads only"),
				})
				return blockCall(call, Verdict{Level: Deny, Source: "static", Reasons: []string{"read-only permissions mode"}},
					"switch with /permissions ask|auto|full-access to allow mutating tools")
			}
			return nil
		}

		needs := collectNeeds(ctx, opts, call, v, mode, lang)
		if v.Level == Deny {
			n := noteFor(call, v, NoteDenied, lang)
			n.Needs = needs
			notify(n)
			return blockCall(call, v, DenyGuidance)
		}
		if mode == permissions.Auto {
			return autoDecision(ctx, opts, call, v, needs, lang, notify)
		}
		return askDecision(ctx, opts, call, v, needs, mode, lang, notify)
	}
}

// ReviewRecovery settles a post-execution capability request with the SAME
// mode policy as ConsentGate. It exists because a sandbox denial is discovered
// after the call already ran: there is no tool call left to gate, only one
// concrete Need ("this path"), and asking the original question again would be
// both late and confusing.
//
// The distinction that keeps this honest:
//
//   - auto: the reviewer decides alone. An allow grants the Need for this retry;
//     a deny/contain/failure refuses it. Confirm and the stdin dialog are never
//     consulted, so auto remains fully automatic.
//   - ask: the reviewer filters an outright denial; otherwise this is the one
//     human dialog, carrying the same Need list the gate would have shown.
//   - read-only: refuse.
//   - full-access: allow; there is no sandbox to recover from.
//
// The caller applies the answer (path registration, retry). Policy lives here
// so the after-the-fact path cannot drift from the before-the-call one.
func ReviewRecovery(ctx context.Context, opts ConsentOpts, req ConsentRequest) ConsentAnswer {
	if len(req.Needs) == 0 {
		return ConsentAnswer{Answered: true}
	}
	mode := permissions.FullAccess
	if opts.Mode != nil {
		mode = opts.Mode.Mode()
	}
	lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
	switch mode {
	case permissions.ReadOnly:
		return ConsentAnswer{Answered: true, Reason: localized(lang, "只读档位不授予额外能力", "read-only does not grant extra capabilities")}
	case permissions.FullAccess:
		return ConsentAnswer{Answered: true, Allow: true, Scope: ScopeOnce, Needs: req.Needs}
	}

	// The reviewer sees the command that was denied and the path the denial
	// named. It returns one verdict; the mode decides what that verdict means.
	payload := map[string]string{"command": req.Summary}
	if req.Justification != "" {
		payload["justification"] = req.Justification
	}
	if req.Rationale != "" {
		payload["justification"] = req.Rationale
	}
	args, _ := json.Marshal(payload)
	call := agentcore.AgentToolCall{ID: "sandbox-denial-recovery", Name: "bash", Arguments: args}
	v := classifyForGate(ctx, opts.Classifier, call)

	req.Mode = mode.String()
	req.Source = v.Source
	req.Level = v.Level
	req.Risk = riskWord(v.Risk, lang)
	req.Authorization = authWord(v.Authorization, lang)
	if req.Rationale == "" {
		req.Rationale = firstReason(v)
	}
	if mode == permissions.Auto {
		if v.Failed || v.Level == Deny || v.Level == Contain {
			return ConsentAnswer{Answered: true, Reason: firstReason(v)}
		}
		return ConsentAnswer{Answered: true, Allow: true, Scope: ScopeOnce, Needs: req.Needs, Reason: firstReason(v)}
	}

	// Ask. A reviewer denial is final without a dialog; a review failure is not:
	// the person can still make the capability decision the reviewer could not.
	if !v.Failed && v.Level == Deny {
		return ConsentAnswer{Answered: true, Reason: firstReason(v)}
	}
	if opts.Confirm != nil {
		return opts.Confirm(ctx, req)
	}
	return promptConsent(ctx, opts.In, opts.Out, opts.Mu, req, lang)
}

// collectNeeds asks each subsystem only what it would require, never whether
// the person agrees. Boundary, network and sandbox are dimensions of one list.
func collectNeeds(ctx context.Context, opts ConsentOpts, call agentcore.AgentToolCall, v Verdict, mode permissions.Mode, lang string) []Need {
	var needs []Need
	needs = append(needs, boundaryNeeds(call, opts, mode, lang)...)
	if NetworkRequested(call.Name, call.Arguments) && !v.Network {
		need := Need{Cap: CapNetwork}
		if key, ok := NetworkKey(call.Name, call.Arguments); ok {
			need.Key = key
		}
		needs = append(needs, need)
	}
	if escalationRequested(call.Arguments) && strings.EqualFold(call.Name, "bash") && !opts.Enforce {
		// In auto the reviewer's Direct is the grant, and Contain means it chose
		// containment; neither needs a human-shaped capability. An allow/confirm
		// without Direct leaves the escape ungranted, and autoDecision blocks
		// that call instead of running it bare. In ask the person still decides
		// the escape. Enforce mode never offers one: it contains every bash call,
		// so a CapUnconfined need would only block or ask for an impossible answer.
		if mode == permissions.Ask || (mode == permissions.Auto && !v.Direct && v.Level != Contain) {
			needs = append(needs, Need{Cap: CapUnconfined})
		}
	}
	if strings.EqualFold(call.Name, "bash") && opts.RequireSandbox &&
		(opts.Sandboxed == nil || !opts.Sandboxed(call.Name)) &&
		(!v.Direct || opts.Enforce) {
		// RequireSandbox is a default, not an absolute: the tool's sandboxRoute
		// honours an explicit one-call Direct before it checks for a runner (the
		// comment on RequireSandbox says so). Enforce is the absolute setting and
		// still fails closed without a runner.
		needs = append(needs, Need{Cap: CapUnconfined})
	}
	return dedupeNeeds(needs)
}

func dedupeNeeds(in []Need) []Need {
	seen := map[string]bool{}
	out := make([]Need, 0, len(in))
	for _, n := range in {
		key := fmt.Sprintf("%d|%s|%s|%s", n.Cap, n.Path, n.Dir, n.Key)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	return out
}

// boundaryNeeds is the gate's whole boundary policy in one list. It is the
// successful, silent half of the old BoundaryGate: refused paths still block,
// but an allowed path is just a Need the mode policy settles.
func boundaryNeeds(call agentcore.AgentToolCall, opts ConsentOpts, mode permissions.Mode, lang string) []Need {
	root := strings.TrimSpace(opts.Cwd)
	if root == "" {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	absRoot = permissions.Canonical(absRoot)
	op := boundaryOp(call.Name)
	shellCmd := ""
	if shellTool(call.Name) {
		shellCmd, _ = bashCommand(call.Arguments)
	}
	var needs []Need
	for _, raw := range boundaryPaths(call.Name, call.Arguments) {
		resolved, err := permissions.AbsAgainst(absRoot, raw)
		if err != nil {
			continue
		}
		resolved = permissions.Canonical(resolved)
		candOp := op
		writing := candOp.Has(grants.Write)
		cloneDir := false
		if !writing && shellCmd != "" {
			cloneDir = cloneCreates(shellCmd, raw)
			if cloneDir || PathWritten(resolved, shellCmd) {
				candOp |= grants.Write
				writing = true
			}
		}
		switch boundaryQuestion(raw, resolved, absRoot, opts.ExtraRoots, candOp, opts.WritableAt) {
		case boundarySilent:
			continue
		case boundaryRefused:
			return nil // the caller sees a refusal separately; see boundaryRefusal
		}
		dir := permissions.BoundaryCandidate(resolved)
		if cloneDir {
			dir = filepath.Clean(resolved)
		}
		cap := CapReadOutside
		if writing {
			cap = CapWriteOutside
		}
		needs = append(needs, Need{Cap: cap, Path: resolved, Dir: dir})
	}
	return needs
}

// boundaryBlocked reports a refusal the person already gave. It is checked in
// the same pass as the needs so a refused path never produces a dialog.
func boundaryBlocked(call agentcore.AgentToolCall, opts ConsentOpts) *agentcore.BeforeToolCallDecision {
	root := strings.TrimSpace(opts.Cwd)
	if root == "" {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	absRoot = permissions.Canonical(absRoot)
	op := boundaryOp(call.Name)
	for _, raw := range boundaryPaths(call.Name, call.Arguments) {
		resolved, err := permissions.AbsAgainst(absRoot, raw)
		if err != nil {
			continue
		}
		resolved = permissions.Canonical(resolved)
		if boundaryQuestion(raw, resolved, absRoot, opts.ExtraRoots, op, opts.WritableAt) == boundaryRefused {
			return blockRefused(call, resolved, op.Has(grants.Write))
		}
	}
	return nil
}

func autoDecision(ctx context.Context, opts ConsentOpts, call agentcore.AgentToolCall, v Verdict, needs []Need, lang string, notify func(Note)) *agentcore.BeforeToolCallDecision {
	if v.Failed {
		if len(needs) > 0 {
			notify(Note{Tool: call.Name, ToolCallID: call.ID, Kind: NoteUnavailable, Level: v.Level, Lang: lang,
				Source: v.Source, Needs: needs,
				Summary: riskSummary(call), Rationale: localized(lang, "评审不可用；能力扩张未获授权", "review was unavailable; capability expansion was not authorized")})
			return blockCall(call, v, "the reviewer could not decide the capability request; nothing was granted")
		}
		if opts.Sandboxed != nil && opts.Sandboxed(call.Name) {
			return &agentcore.BeforeToolCallDecision{Sandbox: true}
		}
		notify(Note{Tool: call.Name, ToolCallID: call.ID, Kind: NoteUnavailable, Level: v.Level, Lang: lang,
			Source:  v.Source,
			Summary: riskSummary(call), Rationale: localized(lang, "评审不可用；无可收容路径", "review unavailable; no containment path")})
		return blockCall(call, v, "the reviewer could not decide and the call cannot be contained")
	}
	if v.Level == Contain {
		if len(needs) > 0 {
			notify(Note{Tool: call.Name, ToolCallID: call.ID, Kind: NoteDenied, Level: v.Level, Lang: lang,
				Source: v.Source, Needs: needs,
				Summary: riskSummary(call), Rationale: localized(lang, "评审要求收容，但调用需要额外能力", "review required containment, but the call needs extra capabilities")})
			return blockCall(call, v, "the call needs a capability the reviewer did not grant")
		}
		if opts.Sandboxed != nil && opts.Sandboxed(call.Name) {
			notify(noteFor(call, v, NoteSandboxed, lang))
			return containedDecision(call, v)
		}
		return blockCall(call, v, "review required containment but no sandbox is available")
	}
	// The sandbox escape is the one capability that does not ride the level: the
	// reviewer answers it separately (Verdict.Direct). A refused escape is a
	// need the gate cannot satisfy, and running the call bare because the level
	// happened to be confirm is how a direct:deny became an escape.
	if hasNeed(needs, CapUnconfined) {
		n := noteFor(call, v, NoteDenied, lang)
		n.Needs = needs
		guidance := ""
		if escalationRequested(call.Arguments) {
			n.Rationale = localized(lang,
				"评审拒绝了沙箱外请求；命令未运行",
				"the reviewer declined the sandbox escape; the call did not run")
			guidance = "sandbox escape declined: auto mode answers escapes by review alone — make the command work inside the sandbox, or ask the user to switch /permissions to ask"
		} else {
			// The escape is not the reason: RequireSandbox found no runner, and
			// the call cannot be contained at all.
			n.Rationale = localized(lang,
				"没有可用的 OS 沙箱，命令未运行",
				"no OS sandbox runner is available; the call did not run")
			guidance = "no OS sandbox is available and the call requires isolation; install the platform sandbox or use full-access"
		}
		notify(n)
		return blockCall(call, v, guidance)
	}
	// Allow and Confirm are the reviewer's authorization in auto. The model (or
	// the local/JEV layer standing in for it) has settled risk, so the call's
	// ordinary capabilities ride that one answer. This is the no-interruption
	// promise.
	dec := applyNeeds(call, opts, needs, ScopeOnce, lang)
	if dec == nil {
		dec = &agentcore.BeforeToolCallDecision{}
	}
	if NetworkRequested(call.Name, call.Arguments) {
		dec.Network = true
	}
	if askedToEscape(call) && v.Direct {
		// Only bash has an escape request. A native tool carrying the argument by
		// accident (or injection) must not inherit the bash-only Direct bit.
		dec.Direct = true
	}
	if len(needs) == 0 {
		if bare, why := envelopeFor(call, lang); bare {
			dec.Bare, dec.BareReason = true, why
		}
	}
	if opts.Enforce {
		// Enforce is the strictest setting: the tool forces every bash call into
		// the sandbox, so a Direct bit would be a promise it cannot keep. Clear
		// the envelope bits and report what will actually happen.
		dec.Direct = false
		dec.Sandbox = true
		dec.Bare, dec.BareReason = false, ""
	}
	kind := NoteApproved
	if opts.Enforce {
		kind = NoteSandboxed
	} else if dec.Direct {
		// Running outside the sandbox must never be invisible; it is the one
		// outcome a sandboxed session has to announce separately.
		kind = NoteEscaped
	}
	n := Note{Tool: call.Name, ToolCallID: call.ID, Kind: kind, Level: v.Level, Lang: lang,
		Source: v.Source, Granted: needs,
		Summary: riskSummary(call), Risk: v.Risk, Authorization: v.Authorization,
		Rationale: consentRationale(v, needs, lang)}
	if opts.Enforce && askedToEscape(call) {
		n.Escalation = true
	}
	notify(n)
	return dec
}

func askDecision(ctx context.Context, opts ConsentOpts, call agentcore.AgentToolCall, v Verdict, needs []Need, mode permissions.Mode, lang string, notify func(Note)) *agentcore.BeforeToolCallDecision {
	// A clearly safe call with no expansion is the JEV/local fast path: it never
	// reaches a person. This is what keeps `ask` usable for routine work.
	if v.Level == Allow && len(needs) == 0 && !v.Failed {
		dec := &agentcore.BeforeToolCallDecision{}
		if NetworkRequested(call.Name, call.Arguments) {
			dec.Network = true
		}
		if bare, why := envelopeFor(call, lang); bare {
			dec.Bare, dec.BareReason = true, why
		}
		return dec
	}
	req := ConsentRequest{
		Tool: call.Name, Summary: riskSummary(call), Justification: escalationJustification(call),
		Source: v.Source,
		Needs:  needs, Mode: mode.String(), Level: v.Level,
		Risk: riskWord(v.Risk, lang), Authorization: authWord(v.Authorization, lang),
		Rationale: firstReason(v), Origin: agentcore.OriginFromContext(ctx),
		CanContain: opts.Sandboxed != nil && opts.Sandboxed(call.Name),
	}
	if req.Rationale == "" {
		req.Rationale = consentRationale(v, needs, lang)
	}
	done := func() {}
	if opts.OnPrompt != nil {
		if d := opts.OnPrompt(approvalQuestion(call.Name, req.Summary)); d != nil {
			done = d
		}
	}
	defer done()
	var ans ConsentAnswer
	if opts.Confirm != nil {
		ans = opts.Confirm(ctx, req)
	} else {
		ans = promptConsent(ctx, opts.In, opts.Out, opts.Mu, req, lang)
	}
	if !ans.Answered || !ans.Allow {
		notify(Note{Tool: call.Name, ToolCallID: call.ID, Kind: NoteDenied, Level: Deny, Lang: lang,
			Source: v.Source, Needs: needs,
			Summary: req.Summary, Rationale: localized(lang, "用户拒绝了本次请求", "denied by the user")})
		return blockCall(call, v, "denied by the user; propose a different approach or ask what they would prefer")
	}
	dec := applyNeeds(call, opts, ans.Needs, ans.Scope, lang)
	if dec == nil {
		dec = &agentcore.BeforeToolCallDecision{}
	}
	// The person's answer decides the envelope. Asking for an unconfined run
	// and then forcing Sandbox would silently discard it: the executor ignores
	// Direct whenever Sandbox is set, so the command would run contained and
	// fail on the very path the answer was meant to open. Containment is the
	// default only when the answer did not choose otherwise.
	unconfined := ans.Exec == ExecUnconfined && hasNeed(ans.Needs, CapUnconfined)
	if opts.Enforce {
		// The tool's ForceSandbox ignores the envelope; never report an escape
		// the execution layer will not perform.
		unconfined = false
	}
	if unconfined {
		dec.Direct = true
	} else if v.Level == Contain {
		dec.Sandbox = true
	}
	if opts.Enforce {
		dec.Direct = false
		dec.Sandbox = true
	}
	// applyNeeds already carries a network need the person granted. The
	// reviewer's own grant has no Need (it would have blocked the call
	// otherwise), so carry that one here; never infer a grant from Approve
	// alone when the answer omitted the capability.
	if v.Network && NetworkRequested(call.Name, call.Arguments) {
		dec.Network = true
	}
	granted := ans.Needs
	if len(granted) == 0 {
		granted = needs
	}
	kind := NoteApproved
	if opts.Enforce {
		kind = NoteSandboxed
	} else if unconfined {
		// The person chose to leave the sandbox; say so rather than folding it
		// into an ordinary approval.
		kind = NoteEscaped
	}
	n := Note{Tool: call.Name, ToolCallID: call.ID, Kind: kind, Level: Allow, Lang: lang,
		Source: v.Source, Granted: granted,
		Summary: req.Summary, Risk: v.Risk, Authorization: v.Authorization,
		Rationale: consentRationale(v, ans.Needs, lang)}
	if opts.Enforce && askedToEscape(call) {
		n.Escalation = true
	}
	notify(n)
	return dec
}

// containedDecision is the execution envelope for a call the reviewer asked to
// contain. Containment is a filesystem boundary, not a veto on capabilities the
// same review granted separately: a contained command may still have network
// access (the grant applies inside the sandbox, not on the host). Dropping the
// bit here made the executor refuse a call the reviewer had already authorized.
func containedDecision(call agentcore.AgentToolCall, v Verdict) *agentcore.BeforeToolCallDecision {
	dec := &agentcore.BeforeToolCallDecision{Sandbox: true}
	if v.Network && NetworkRequested(call.Name, call.Arguments) {
		dec.Network = true
	}
	return dec
}

// applyNeeds turns one answered set into execution flags and standing grants.
// One answer, one registration path: no caller writes a grant directly.
func applyNeeds(call agentcore.AgentToolCall, opts ConsentOpts, needs []Need, scope Scope, lang string) *agentcore.BeforeToolCallDecision {
	dec := &agentcore.BeforeToolCallDecision{}
	for _, n := range needs {
		switch n.Cap {
		case CapReadOutside:
			if scope == ScopeOnce {
				if dec.ReadGrant == "" {
					dec.ReadGrant = n.Path
				}
				_, _ = grants.Add(grants.Grant{Path: n.Path, Allow: grants.Read, Scope: grants.Exact,
					Lifetime: grants.Session, Source: "consent (once)"})
				continue
			}
			life := grants.Session
			if scope == ScopeRemembered {
				life = grants.Remembered
				if opts.PersistReadRoot != nil {
					_, _ = opts.PersistReadRoot(n.Dir)
				}
			}
			if n.Dir != "" {
				_, _ = grants.AddReadRoot(n.Dir, life, "consent")
			}
		case CapWriteOutside:
			if scope == ScopeOnce {
				if dec.WriteGrant == "" {
					dec.WriteGrant = n.Path
				}
				_, _ = grants.Add(grants.Grant{Path: n.Path, Allow: grants.Write, Scope: grants.Exact,
					Lifetime: grants.Session, Source: "consent (once)"})
				continue
			}
			if n.Dir != "" && opts.GrantWritable != nil {
				_, _ = opts.GrantWritable(n.Dir, scope == ScopeRemembered)
			}
		case CapNetwork:
			dec.Network = true
		case CapUnconfined:
			// The caller decides the execution envelope; an escape is never
			// remembered and never establishes a standing grant.
		}
	}
	return dec
}

func hasNeed(needs []Need, cap Capability) bool {
	for _, n := range needs {
		if n.Cap == cap {
			return true
		}
	}
	return false
}

func hasPathNeed(needs []Need) bool {
	for _, n := range needs {
		if n.Cap == CapReadOutside || n.Cap == CapWriteOutside {
			return true
		}
	}
	return false
}

func consentRationale(v Verdict, needs []Need, lang string) string {
	if len(needs) == 0 {
		if len(v.Reasons) > 0 {
			return v.Reasons[0]
		}
		return localized(lang, "无需额外能力", "no extra capabilities were needed")
	}
	parts := make([]string, 0, len(needs))
	for _, n := range needs {
		parts = append(parts, needLabel(n, lang))
	}
	return localized(lang, "已授权："+strings.Join(parts, "、"), "granted: "+strings.Join(parts, ", "))
}

func needLabel(n Need, lang string) string {
	switch n.Cap {
	case CapReadOutside:
		return localized(lang, "读取 "+n.Path, "read "+n.Path)
	case CapWriteOutside:
		return localized(lang, "写入 "+n.Path, "write "+n.Path)
	case CapNetwork:
		if n.Key != "" {
			return localized(lang, "网络 "+n.Key, "network "+n.Key)
		}
		return localized(lang, "联网", "network")
	case CapUnconfined:
		return localized(lang, "沙箱外运行", "run outside the sandbox")
	default:
		return "capability"
	}
}

// promptConsent is the REPL's single-question shape. It never asks two
// questions: every need is shown together.
func promptConsent(ctx context.Context, in *bufio.Reader, out io.Writer, mu *sync.Mutex, req ConsentRequest, lang string) ConsentAnswer {
	if in == nil || out == nil {
		return ConsentAnswer{}
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	fmt.Fprintf(out, "\n%s needs authorization:\n", req.Tool)
	if s := strings.TrimSpace(req.Summary); s != "" {
		fmt.Fprintf(out, "  %s\n", s)
	}
	for _, n := range req.Needs {
		fmt.Fprintf(out, "  • %s\n", needLabel(n, lang))
	}
	onlyOnce := false
	for _, n := range req.Needs {
		if n.Cap == CapUnconfined {
			onlyOnce = true
		}
	}
	if onlyOnce {
		fmt.Fprint(out, "  [y] allow this call once   [N] deny\nChoice: ")
	} else {
		fmt.Fprint(out, "  [y] allow once   [a] allow these scopes for this session   [s] remember   [N] deny\nChoice: ")
	}
	line, ok := readLineOrCancel(ctx, in)
	if !ok {
		return ConsentAnswer{}
	}
	ans := ConsentAnswer{Answered: true, Needs: req.Needs, Exec: ExecSandboxed}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		ans.Allow, ans.Scope = true, ScopeOnce
	case "a", "all", "session":
		if onlyOnce {
			return ConsentAnswer{Answered: true}
		}
		ans.Allow, ans.Scope = true, ScopeSession
	case "s", "save", "remember":
		if onlyOnce {
			return ConsentAnswer{Answered: true}
		}
		ans.Allow, ans.Scope = true, ScopeRemembered
	default:
		return ans
	}
	for _, n := range req.Needs {
		if n.Cap == CapUnconfined {
			ans.Exec = ExecUnconfined
		}
	}
	return ans
}

// PromptConsent is the exported stdin shape for drivers that own no Confirm
// callback (notably the sandbox-denial recovery question).
func PromptConsent(ctx context.Context, in *bufio.Reader, out io.Writer, mu *sync.Mutex, req ConsentRequest, lang string) ConsentAnswer {
	return promptConsent(ctx, in, out, mu, req, lang)
}

// ConsentFromApproval adapts the driver callback to the consent gate: the
// driver speaks ApprovalRequest/ApprovalAnswer, the gate speaks
// ConsentRequest/ConsentAnswer.
func ConsentFromApproval(f func(context.Context, ApprovalRequest) ApprovalAnswer) ConsentConfirm {
	if f == nil {
		return nil
	}
	return func(ctx context.Context, req ConsentRequest) ConsentAnswer {
		legacy := ApprovalRequest{
			Kind: ApprovalConsent, Tool: req.Tool, Summary: req.Summary,
			Source:        req.Source,
			Justification: req.Justification, Mode: req.Mode, Level: req.Level,
			Rationale: req.Rationale, Origin: req.Origin, Needs: req.Needs,
		}
		a := f(ctx, legacy)
		needs := a.Needs
		if len(needs) == 0 && a.Approve {
			// Older dialog implementations answer only Approve; the request is
			// the exact set they were shown and approved.
			needs = req.Needs
		}
		return ConsentAnswer{Allow: a.Approve, Answered: a.Answered, Scope: a.Scope,
			Needs: needs, Exec: a.Exec}
	}
}
