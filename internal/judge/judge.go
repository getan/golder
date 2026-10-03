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
// keeping the static floor. Sandbox execution routing lives in
// internal/seatbelt and is injected into BashTool by internal/cli/run:
// sandbox-tier verdicts run under sandbox-exec on macOS
// (PIGO_SANDBOX=auto), every command runs sandboxed under
// PIGO_SANDBOX=enforce, and without a runner the gate's approval runs the
// command directly.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/permissions"
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

// ungradedTools never reaches the grader: local reads and in-memory tools are
// side-effect free, and write_stdin only controls a session whose original
// command was already graded when bash spawned it (it can poll output or
// interrupt — it cannot start anything new). Grading it again would add
// latency and could block a benign poll on a verdict with no sandbox tier.
var ungradedTools = map[string]bool{
	"read": true, "view_image": true, "grep": true, "find": true, "ls": true,
	"webfetch": true, "websearch": true, "todo": true,
	"memory_search": true, "schedule_list": true,
	"write_stdin": true,
	// Context-budget tools: get_context_remaining only reads the live budget,
	// and new_context only requests a summarized window rollover at the turn
	// boundary — neither touches the host, so an LLM review would add latency
	// and could block a benign control call on a verdict with no sandbox tier.
	"get_context_remaining": true, "new_context": true,
}

// Chain runs StaticFloor first (deny wins), then inner, and degrades any
// inner error to Confirm. A nil inner grades static-only.
type Chain struct {
	Inner Classifier
}

// Classify implements Classifier.
func (c Chain) Classify(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if v, ok := staticDeny(tool, args); ok {
		return v
	}
	if ungradedTools[strings.ToLower(tool)] {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"read-only tool"}}
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
	// Notify, when set, receives one Note per non-trivial verdict (approval,
	// sandbox routing, denial, read-only block, reviewer failure) so the
	// driver can show the decision and its rationale. Callbacks run on the
	// run goroutine; implementations must not block on the UI thread.
	Notify func(Note)
}

// NoteKind classifies one user-facing verdict note.
type NoteKind int

const (
	// NoteApproved: auto mode approved a Confirm-tier call (runs directly).
	NoteApproved NoteKind = iota
	// NoteSandboxed: a Sandbox-tier call runs isolated.
	NoteSandboxed
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
	notify := func(n Note) {
		if opts.Notify != nil {
			opts.Notify(n)
		}
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		mode := permissions.FullAccess
		if state != nil {
			mode = state.Mode()
		}
		lang := ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
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
		switch {
		case v.Failed:
			notify(noteFor(call, v, NoteUnavailable, lang))
			if sandboxable(opts, call.Name) {
				// Contain it: the execution layer runs the call isolated (the
				// decision rides the executor context down to the tool).
				return &agentcore.BeforeToolCallDecision{Sandbox: true}
			}
			if interactive && !optsIsAuto(state) && promptRisk(ctx, opts, call, v, true) {
				return nil
			}
			return blockCall(call, v, "reviewer unavailable and no sandbox for this tool; failing closed")
		case v.Level == Allow:
			return nil
		case v.Level == Deny:
			notify(noteFor(call, v, NoteDenied, lang))
			return blockCall(call, v, DenyGuidance)
		case v.Level == Sandbox:
			if sandboxable(opts, call.Name) {
				notify(noteFor(call, v, NoteSandboxed, lang))
				return &agentcore.BeforeToolCallDecision{Sandbox: true}
			}
			if interactive && !optsIsAuto(state) && promptRisk(ctx, opts, call, v, true) {
				return nil
			}
			notify(noteFor(call, v, NoteBlockedNoPrompt, lang))
			return blockCall(call, v, "no sandbox runner for this call (PIGO_SANDBOX=off or sandbox-exec unavailable); failing closed")
		default: // Confirm
			if optsIsAuto(state) {
				notify(noteFor(call, v, NoteApproved, lang))
				return nil
			}
			if interactive && promptRisk(ctx, opts, call, v, false) {
				return nil
			}
			notify(noteFor(call, v, NoteBlockedNoPrompt, lang))
			return blockCall(call, v, "needs confirmation; failing closed without a prompt")
		}
	}
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

// readOnlyAllowed lists the tools read-only mode still permits. It is an
// allow-list on purpose: plugin and future tools are blocked by default until
// someone classifies them, so a new mutating tool cannot silently bypass the
// mode.
var readOnlyAllowedTools = map[string]bool{
	"read": true, "view_image": true, "grep": true, "find": true, "ls": true,
	"webfetch": true, "websearch": true, "todo": true,
	"memory_search": true, "schedule_list": true,
	"goal_complete": true, "goal_blocked": true,
	"get_context_remaining": true, "new_context": true,
}

// readOnlyAllowed reports whether tool may run under read-only mode.
func readOnlyAllowed(tool string) bool {
	return readOnlyAllowedTools[strings.ToLower(strings.TrimSpace(tool))]
}

// classifyForGate grades one call. A nil classifier keeps the static floor
// and fails closed for every gated tool, so wiring mistakes never widen
// permissions.
func classifyForGate(ctx context.Context, opts GateOpts, call agentcore.AgentToolCall) Verdict {
	if opts.Classifier != nil {
		return opts.Classifier.Classify(ctx, call.Name, call.Arguments)
	}
	if v, ok := staticDeny(call.Name, call.Arguments); ok {
		return v
	}
	if ungradedTools[strings.ToLower(call.Name)] {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"read-only tool"}}
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
		if firstDec != nil && firstDec.Sandbox {
			if dec == nil {
				return firstDec
			}
			out := *dec
			out.Sandbox = true
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

func promptRisk(ctx context.Context, opts GateOpts, call agentcore.AgentToolCall, v Verdict, sandbox bool) bool {
	if opts.Mu != nil {
		opts.Mu.Lock()
		defer opts.Mu.Unlock()
	}
	out := opts.Out
	if out == nil {
		return false
	}
	if sandbox {
		// Reached only when the execution layer cannot isolate this call
		// (no runner for the tool, PIGO_SANDBOX=off, or no sandbox-exec):
		// approving runs it unisolated, so say so plainly.
		fmt.Fprintf(out, "\npigo judges %q as risk %s [sandbox unavailable: approves run unisolated].\n", call.Name, v.Level)
	} else {
		fmt.Fprintf(out, "\npigo judges %q as risk %s.\n", call.Name, v.Level)
	}
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
	fmt.Fprint(out, "Allow? [y/N]: ")
	// The read is interruptible: a Ctrl+C during the prompt cancels the run
	// context and yields a denial immediately instead of trapping the user
	// until they answer (same contract as the trust gate).
	line, ok := readLineOrCancel(ctx, opts.In)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
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

// cacheKey hashes (tool, args) so repeated identical calls grade once per
// process. Failures are never cached: only successful reviewer verdicts.
func cacheKey(tool string, args json.RawMessage) string {
	h := sha256.Sum256(append([]byte(strings.ToLower(tool)+"\x00"), bytes.TrimSpace(args)...))
	return hex.EncodeToString(h[:])
}
