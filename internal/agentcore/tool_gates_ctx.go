package agentcore

import "context"

// ToolGateChain is the tool-gating chain a run executes under: the before/after
// tool-call seams plus the authorization snapshot a derived run needs when it
// cannot share them (a sub-agent in another process).
//
// It exists so a derived run inherits the session's gates instead of each
// dispatch site assembling its own. The tool executor publishes the chain it
// was configured with onto every tool call's context (see WithToolGates), so
// the sub-agent tool can hand the very same chain to the child run — the child
// is then gated by the session's trust, workspace-boundary and permission gates
// with no per-driver wiring, and a nested child inherits again the same way.
//
// The session's own loop-level seams (OnStop, checkpoints) are deliberately NOT
// here: a child run is a bounded unit whose end is its own, and inheriting the
// session's Stop hook would let a hook written for the conversation trap a
// child in a loop. OnChildStop is the counterpart that does belong: the hook
// event written for children, with the child's own context and block budget.
type ToolGateChain struct {
	// BeforeToolCall is the composed gate (trust → read-scope → permission, then
	// the PreToolUse hook seam). Nil means the run is ungated.
	BeforeToolCall BeforeToolCallFunc
	// AfterToolCall is the PostToolUse seam. Nil is a no-op.
	AfterToolCall AfterToolCallFunc
	// Snapshot, when set, reports the session authorizations a derived run must
	// be seeded with when it runs where these seams cannot reach — the process
	// boundary. Nil means nothing to carry.
	Snapshot func() ToolAuth
	// OnChildStop, when set, is consulted when the derived run is about to end
	// naturally: the SubagentStop hook family, which fires with the child's
	// context and its own consecutive-block budget. It reports whether to keep
	// the child running and any guidance to append. Nil leaves the child's end
	// to the child.
	OnChildStop func(ctx context.Context, agentCtx *AgentContext) (block bool, guidance string)
}

// ToolAuth is the authorization state a derived run is seeded with when it
// crosses a process boundary. It carries the session's decisions, not the
// session's prompts: a process-isolated sub-agent speaks JSON-RPC on its stdin,
// so it has nowhere to ask, and every request it cannot settle from this
// snapshot fails closed on its own side.
type ToolAuth struct {
	// SessionTrust is the session trust decision for the working directory: the
	// interactive "trust this folder" answer, the "all tools here" answer to a
	// trust question, or (for a non-interactive run) the invocation itself.
	SessionTrust bool `json:"sessionTrust,omitempty"`
	// SessionReads is the trust gate's middle rung: local reads allowed in the
	// working directory for this session.
	SessionReads bool `json:"sessionReads,omitempty"`
	// ReadRoots are the workspace-boundary grants (directories the user allowed
	// the read tools to reach outside the workspace, this session).
	ReadRoots []string `json:"readRoots,omitempty"`
}

// toolGatesKey is the unexported context key carrying a run's gate chain from
// the tool executor to a derived run (a sub-agent).
type toolGatesKey struct{}

// WithToolGates returns a child context carrying chain. The tool executor
// publishes it for the executing tool, so a sub-agent the tool spawns can run
// under the same gates as the run that dispatched it.
func WithToolGates(ctx context.Context, chain *ToolGateChain) context.Context {
	if chain == nil {
		return ctx
	}
	return context.WithValue(ctx, toolGatesKey{}, chain)
}

// ToolGatesFromContext returns the chain published for this tool call, or nil
// when the call runs outside a gated executor (a unit test, an SDK session with
// no gates).
func ToolGatesFromContext(ctx context.Context) *ToolGateChain {
	chain, _ := ctx.Value(toolGatesKey{}).(*ToolGateChain)
	return chain
}
