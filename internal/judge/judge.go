package judge

// Package judge grades the risk of a single tool call so the run gets a
// middle tier between "directory trusted, run anything" and "ask a human
// for everything" (trust tri-state in internal/trust, allow/deny lists in
// internal/cli/run). It is a leaf package: standard library plus
// internal/agentcore only, no imports from trust/runtime/hooks, so the cli
// layer can chain it onto the existing BeforeToolCall seam without creating
// an import cycle.
//
// Chain of responsibility, cheapest first:
//
//  1. StaticFloor — a tiny hard-deny list for unambiguously catastrophic
//     calls (sudo, mkfs, keys under .ssh). It never allows, it only denies.
//  2. JevJudge — a fast local classification model over (tool, arguments)
//     returning one of Allow/Confirm/Sandbox/Deny with calibrated
//     confidence. Low confidence escalates one tier toward Deny, except a
//     direct deny claim needs strong evidence: a shaky deny is held at
//     Sandbox where isolation contains it.
//  3. Fallback — any classifier error (no key, timeout, bad response)
//     degrades to Confirm, never to Allow. With no key configured the Jev
//     step is skipped and only the static floor applies, preserving today's
//     behavior for zero-config runs.
//
// Enforcement (prompt vs block) lives in Gate and is chosen per driver:
// interactive drivers (REPL) prompt on Confirm/Sandbox, headless drivers
// (TUI/headless, no stdin to read) fail closed at the sandbox floor.
// Sandbox execution routing lives in internal/seatbelt and is injected into
// BashTool by internal/cli/run: sandbox-tier verdicts run under sandbox-exec
// on macOS (PIGO_SANDBOX=auto), every command runs sandboxed under
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
	"os"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
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

// Verdict is one classification result.
type Verdict struct {
	Level      Level
	Reasons    []string
	Confidence float64
	Source     string
}

// Classifier grades one tool call. Implementations must be safe for
// concurrent use: parallel tool batches grade concurrently.
type Classifier interface {
	Classify(ctx context.Context, tool string, args json.RawMessage) Verdict
}

// readOnlyTools never reaches the model: local reads and in-memory tools
// are side-effect free, so grading them would only add latency and cost.
var readOnlyTools = map[string]bool{
	"read": true, "grep": true, "find": true, "ls": true,
	"webfetch": true, "websearch": true, "todo": true,
	"memory_search": true, "schedule_list": true,
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
	if readOnlyTools[strings.ToLower(tool)] {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"read-only tool"}}
	}
	if c.Inner == nil {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"no grader configured"}}
	}
	return c.Inner.Classify(ctx, tool, args)
}

// DefaultClassifier wires the static floor to the Jev grader. With no key
// configured it degrades to static-only (today's behavior plus hard denies).
func DefaultClassifier() Classifier {
	return Chain{Inner: NewJevJudge()}
}

// GateEnabled reports whether grading is on. PIGO_JUDGE=off disables the
// gate entirely (callers should leave BeforeToolCall unset); anything else
// enables it.
func GateEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("PIGO_JUDGE")), "off")
}

// GateOpts tunes the BeforeToolCall adapter. Interactive drivers pass the
// shared stdin reader/writer so Confirm/Sandbox prompt exactly like the
// trust gate; headless drivers pass Interactive=false and fail closed.
type GateOpts struct {
	In          *bufio.Reader
	Out         io.Writer
	Mu          *sync.Mutex
	Interactive bool
	// Floor is the lowest verdict tier that reaches the gate; verdicts below
	// it pass untouched. The zero value (Allow) grades everything. Drivers
	// without a prompt that already run under up-front trust (TUI/headless)
	// set Floor=Sandbox so only sandbox/deny verdicts block.
	Floor Level
	// Sandboxed reports whether a tool call at the Sandbox tier will actually
	// run isolated by the execution layer (the bash tool's seatbelt runner).
	// When it returns true the gate lets the call through — isolation is the
	// enforcement, not a prompt or a block. Nil, or false for this tool,
	// keeps the previous behavior: prompt when interactive, fail closed when
	// not. The predicate comes from the wiring layer (cli/run), so this leaf
	// package stays unaware of platforms and runners.
	Sandboxed func(toolName string) bool
}

// InteractiveGate prompts on Confirm/Sandbox and blocks on Deny.
func InteractiveGate(in *bufio.Reader, out io.Writer, mu *sync.Mutex) agentcore.BeforeToolCallFunc {
	return GateFunc(GateOpts{In: in, Out: out, Mu: mu, Interactive: true}, DefaultClassifier())
}

// InteractiveGateOpts is InteractiveGate with the full option set, so a driver
// can also pass Floor and Sandboxed.
func InteractiveGateOpts(opts GateOpts) agentcore.BeforeToolCallFunc {
	return GateFunc(opts, DefaultClassifier())
}

// EnforcingGate never prompts: anything above Allow blocks. For drivers
// without a stdin prompt (TUI/headless).
func EnforcingGate() agentcore.BeforeToolCallFunc {
	return GateFunc(GateOpts{}, DefaultClassifier())
}

// EnforcingGateFrom never prompts and blocks only verdicts at or above min.
// Non-interactive drivers running under up-front trust pass Sandbox so
// Confirm-level calls keep flowing while sandbox/deny verdicts fail closed.
func EnforcingGateFrom(min Level) agentcore.BeforeToolCallFunc {
	return GateFunc(GateOpts{Floor: min}, DefaultClassifier())
}

// EnforcingGateOpts is EnforcingGateFrom with the full option set, so a driver
// can also pass Sandboxed.
func EnforcingGateOpts(opts GateOpts) agentcore.BeforeToolCallFunc {
	return GateFunc(opts, DefaultClassifier())
}

// ChainGates composes two BeforeToolCall gates so the run gets trust first,
// then the risk grade, then the user hooks: a block from first
// short-circuits (second never runs), so an earlier gate stays authoritative
// over a later one. A nil operand is identity.
func ChainGates(first, second agentcore.BeforeToolCallFunc) agentcore.BeforeToolCallFunc {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		if dec := first(ctx, call); dec != nil && dec.Block {
			return dec
		}
		return second(ctx, call)
	}
}

// GateFunc adapts a Classifier to the BeforeToolCall seam. A nil classifier
// is identity. When grading is disabled via PIGO_JUDGE=off it returns nil so
// callers can wire it unconditionally.
func GateFunc(opts GateOpts, c Classifier) agentcore.BeforeToolCallFunc {
	if !GateEnabled() || c == nil {
		return nil
	}
	return func(ctx context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		v := c.Classify(ctx, call.Name, call.Arguments)
		if v.Level < opts.Floor {
			return nil
		}
		// Silent Allows stay silent only for read-only tools. Side-effect
		// tools announce their grade on one line so the automated verdict
		// is always visible; Confirm/Sandbox/Deny announce themselves via
		// the prompt/block below.
		if v.Level == Allow && opts.Out != nil && opts.Interactive && !readOnlyTools[strings.ToLower(call.Name)] {
			printVerdict(opts, call, v)
		}
		switch v.Level {
		case Allow:
			return nil
		case Deny:
			// No escape hatch on hard blocks: point at the way back.
			return blockCall(call, v, DenyGuidance)
		case Sandbox:
			// The execution layer can isolate this call (bash under
			// sandbox-exec): isolation is stronger than a prompt, so let it
			// through and let the runner add the sandbox.
			if opts.Sandboxed != nil && opts.Sandboxed(call.Name) {
				return nil
			}
			if !opts.Interactive || opts.In == nil {
				return blockCall(call, v, "no sandbox runner for this call (PIGO_SANDBOX=off or sandbox-exec unavailable); failing closed")
			}
			if promptRisk(ctx, opts, call, v, true) {
				return nil
			}
			return blockCall(call, v, "denied at prompt")
		default:
			if !opts.Interactive || opts.In == nil {
				return blockCall(call, v, "needs confirmation; failing closed without a prompt")
			}
			if promptRisk(ctx, opts, call, v, false) {
				return nil
			}
			return blockCall(call, v, "denied at prompt")
		}
	}
}

// printVerdict announces one automated grade on a single line, codex-style:
// a small badge plus the tool and a truncated preview, e.g.
// "  [judge: allow] bash: go test ./...". It holds the prompt mutex so
// parallel batches cannot interleave verdict lines.
func printVerdict(opts GateOpts, call agentcore.AgentToolCall, v Verdict) {
	if opts.Mu != nil {
		opts.Mu.Lock()
		defer opts.Mu.Unlock()
	}
	fmt.Fprintf(opts.Out, "  [judge: %s] %s", v.Level, call.Name)
	if summary := riskSummary(call); summary != "" {
		fmt.Fprintf(opts.Out, ": %s", summary)
	}
	fmt.Fprintln(opts.Out)
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
	// The off switch is advertised only on recoverable tiers
	// (Confirm/Sandbox): a Deny must not point at the escape hatch, it
	// carries remediation guidance in tail instead.
	if v.Level != Deny {
		msg += " (PIGO_JUDGE=off disables the gate)"
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
// process. Failures are never cached: only successful Jev verdicts.
func cacheKey(tool string, args json.RawMessage) string {
	h := sha256.Sum256(append([]byte(strings.ToLower(tool)+"\x00"), bytes.TrimSpace(args)...))
	return hex.EncodeToString(h[:])
}
