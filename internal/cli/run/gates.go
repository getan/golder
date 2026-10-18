// This file is the single assembly point for a session's tool gates: the trust
// gate, the workspace-boundary gate and the permission gate, composed once and
// installed on every run of the session — the main run, a /goal or /btw side
// run, and any sub-agent they dispatch.
//
// Before this, each driver built the chain itself (seven assembly sites:
// tui/repl/goal/btw/headless/subagent_rpc/task-factory), which is how the TUI
// came to gate reads differently from the REPL and how a sub-agent ended up
// with fewer gates than the run that spawned it. With the chain defined here,
// "which gates a run has" is one decision, and a derived run inherits the
// session's answer instead of re-deciding it.
//
// Two chains come out of one set of inputs. The interactive chain carries the
// driver's answer channels (stdin prompt or dialog callback); the derived chain
// has the same gates but NO channels, so a sub-agent thread cannot prompt — it
// fails closed with guidance, exactly as the read-scope gate has always done
// for children. That is deliberate: a child runs concurrently with its parent,
// and a dialog opened from under a running batch is not a question the user can
// answer without losing the thread of what they were reading.
package run

import (
	"bufio"
	"context"
	"io"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/trust"
)

// GatesOpts carries the session-scoped inputs of the gate chain. Every field is
// optional except Cwd; a zero Gates is the "no gates" run (an SDK session with
// nothing wired), not an error.
type GatesOpts struct {
	// Trust is the session trust store. Nil disables the trust gate, which is
	// the right shape for an explicit non-interactive invocation (headless,
	// whose authorization IS the invocation) and for the SDK.
	Trust *trust.Manager
	// Cwd is the directory the trust decision and the workspace boundary are
	// about: never a tool argument, always the run's workspace.
	Cwd string
	// Perms is the live approval mode shared with the tool set; /permissions
	// mutates it and the next call obeys.
	Perms *permissions.State
	// Notes receives one announcement per non-trivial decision.
	Notes *ReviewNotes
	// Sandboxed reports whether a Sandbox-tier call will actually be isolated
	// by the execution layer (see SandboxGate).
	Sandboxed func(toolName string) bool
	// Classifier builds the reviewer for one chain build. It is a factory
	// rather than a value because /model switches change the reviewer's model
	// between runs; nil keeps the static floor and fails closed on gated calls.
	Classifier func() judge.Classifier

	// In/Out/Mu are the stdin answer channel (interactive line drivers).
	In  *bufio.Reader
	Out io.Writer
	Mu  *sync.Mutex
	// Confirm is the driver's own answer channel (the TUI dialog, a browser
	// bridge). Used in preference to stdin when both are set.
	Confirm func(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer
	// TrustConfirm, when set, answers trust questions instead of Confirm. The
	// TUI uses it because a paired browser answers trust questions while local
	// dialogs answer the rest (the pre-existing routing); nil keeps Confirm for
	// every question.
	TrustConfirm func(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer
	// OnPrompt, when set, is told the question just before a prompt is put to
	// the person and returns the function to call once it is answered. It is
	// how a session reports "this pane is waiting for a human" to the terminal
	// multiplexer around it (see internal/hostcompat): the gates own every
	// moment a person is asked something, which is exactly the moment a host's
	// rail, Inbox and phone alerts care about.
	//
	// It is called on the run goroutine and must not block; the chain passes it
	// to all three gates (trust, read scope, permission), so one hook covers
	// every prompt a session can show.
	OnPrompt func(question string) func()
	// ExtraRoots are read roots the session advertised itself (the skills
	// directory), never asked about. See ReadableExtraRoots.
	ExtraRoots []string
}

// Gates is a session's assembled gate chain. It is immutable after
// construction except for the trust state its Manager carries, and safe for
// concurrent use: Install is called per run (possibly from concurrent
// sub-agent goroutines), and it only reads shared state.
type Gates struct {
	opts GatesOpts
}

// NewGates assembles the session's gates. It never returns nil: a Gates with
// no inputs still installs hooks and the authorization snapshot, and its chain
// is nil (an ungated run), which is what the callers previously expressed with
// a nil BeforeToolCall.
func NewGates(opts GatesOpts) *Gates { return &Gates{opts: opts} }

// Install wires the session's gates onto cfg: the interactive chain as the
// tool-execution seam, the hook seams when a dispatcher is resolved, and the
// chain a spawned sub-agent inherits.
//
// d is the session's hook dispatcher (nil when no hooks are configured, FR-18),
// passed per call rather than held on Gates because a driver may resolve hooks
// after assembling its gates — the TUI defers hook loading until the first-run
// trust answer.
func (g *Gates) Install(cfg *runtime.RunConfig, d *hooks.Dispatcher, deps HookDeps) {
	g.InstallWith(cfg, g.Chain(), d, deps)
}

// InstallWith is Install with a driver-supplied interactive seam: the REPL's
// remote-control bridge wraps the session's chain in its own transport (a
// paired browser answers instead of the terminal), so the driver passes the
// wrapped chain here rather than overwriting the seam after Install. The
// inherited chain, the hook seams and the Stop decorator are unchanged —
// whatever wraps the interactive chain, a sub-agent still inherits the
// session's gates rather than the wrapper.
func (g *Gates) InstallWith(cfg *runtime.RunConfig, seam agentcore.BeforeToolCallFunc, d *hooks.Dispatcher, deps HookDeps) {
	tec := &cfg.Batch.ToolExecutorConfig
	// The bash tool asks the same path question the gate asks, when the sandbox
	// has already refused: one answer channel and one registration, whichever
	// layer noticed the blocked path first (see BashTool.OnSandboxDenied). A
	// driver without a dialog gets a nil asker and keeps the old behavior — the
	// denial is labelled for the model and nobody is interrupted.
	if ask := g.SandboxDenialAsker(); ask != nil && tec.Registry != nil {
		for _, t := range tec.Registry.List() {
			if b, ok := t.(*agenttool.BashTool); ok {
				b.OnSandboxDenied = ask
			}
		}
	}
	if seam != nil {
		tec.BeforeToolCall = seam
	}
	// The hook rewrites the input before validation and the gates. The gates
	// then authorize exactly the input that will be executed, just once.
	InstallSeams(cfg, d, deps)
	child := &agentcore.ToolGateChain{
		PrepareArguments: tec.PrepareArguments,
		BeforeToolCall:   g.chain(),
		Snapshot:         g.Authorization,
	}
	if d != nil {
		// A derived run inherits the tool-use hooks too: a PreToolUse hook that
		// guards the session's files has no business skipping a sub-agent's
		// calls. The session's Stop hook is deliberately not inherited (a child's
		// end is its own), but the SubagentStop family is — that is the event
		// written for children, and it carries its own block budget here.
		child.AfterToolCall = postToolCallHook(d, deps)
		if sub := stopHook(d, deps, "SubagentStop", 0); sub != nil {
			child.OnChildStop = func(ctx context.Context, agentCtx *agentcore.AgentContext) (bool, string) {
				if dec := sub(ctx, agentCtx); dec != nil && dec.Block {
					return true, dec.Guidance
				}
				return false, ""
			}
		}
	}
	tec.GateChain = child
}

// Chain returns the interactive gate chain on its own, for a driver that wraps
// it in its own transport (the REPL's remote-control bridge intercepts confirm
// requests before they reach the terminal). Drivers that do not wrap should
// call Install instead, which also wires hooks and the inherited chain.
func (g *Gates) Chain() agentcore.BeforeToolCallFunc { return g.chainWithTrust(true) }

// ChainAfterTrust returns the same boundary → permission chain without the
// trust gate. A remote-control bridge uses it after the browser answered the
// trust question itself: the browser replaces the trust prompt, never the
// boundary or permission gates. Keeping this a named chain (rather than a
// context flag) makes the bypass impossible to reintroduce by accident.
func (g *Gates) ChainAfterTrust() agentcore.BeforeToolCallFunc {
	return g.chainWithTrust(false)
}

// InstallDriver wires the session's gates AND the resolved hook set onto cfg in
// one call: the interactive gate chain, the PreToolUse/PostToolUse seams, the
// Stop hook, the SessionStart dispatch, and the hook notifier chained onto
// onEvent. It is the single entry point a driver uses, so a driver cannot wire
// one half (gates) while forgetting the other (hooks), and the chain a spawned
// sub-agent inherits carries the tool-use hooks with it.
//
// set is the already-resolved, trust-gated hook set (see ResolveHookSet). When
// it is empty NewDispatcher returns nil and this is a no-op beyond the gates,
// the hot path paying nothing (FR-18).
func (g *Gates) InstallDriver(ctx context.Context, cfg *runtime.RunConfig, set hooks.HookSet, deps HookDeps, source string, onEvent func(agentcore.AgentEvent)) (*hooks.Dispatcher, func(agentcore.AgentEvent)) {
	d := BuildDispatcher(set, deps)
	g.Install(cfg, d, deps)
	if d == nil {
		return nil, onEvent
	}
	DispatchSessionStart(ctx, d, cfg, deps, source)
	n := hooks.NewHookNotifier(d, deps.SessionID, deps.ProjectDir)
	return d, chainEvent(onEvent, n.Handle)
}

// chain composes trust → workspace boundary → permission, wired with whatever
// answer channels the driver gave the session. One builder serves both the
// interactive chain and the chain a sub-agent inherits.
//
// It used to build two, and the difference was deliberate: the chain a child
// inherited was assembled WITHOUT the channels, so a child's question failed
// closed instead of opening a dialog under a running batch. That reasoning was
// wrong on both halves. The person IS there — they are watching the batch — so
// failing closed turned a question they would have answered into a silent
// refusal the child then had to work around; and the hazard it named, parking a
// goroutine on a dialog nobody can answer, is what the channels' own nil-ness
// already covers: a headless run, an embedder with no driver, and a
// process-isolated child all pass nothing here and still fail closed on their
// own.
//
// What a child's question must not do is arrive unlabeled, which is why the
// gates stamp Origin onto the request and the note from the call's context (see
// agentcore.WithAgentOrigin): the dialog says which agent is asking, so the
// person is not consenting to a call they believe their own conversation made.
func (g *Gates) chain() agentcore.BeforeToolCallFunc {
	return g.chainWithTrust(true)
}

func (g *Gates) chainWithTrust(includeTrust bool) agentcore.BeforeToolCallFunc {
	o := g.opts
	// One coordinator per chain: a path question that also covers network
	// records its answer here, and the permission gate consumes it without a
	// second dialog. It is per-chain rather than package-global so a child run
	// cannot read a parent call's answer.
	askCoord := judge.NewAskCoord()
	var gate agentcore.BeforeToolCallFunc
	if includeTrust {
		trustOpts := trust.GateOpts{Manager: o.Trust, Cwd: o.Cwd, Mu: o.Mu}
		trustOpts.In, trustOpts.Out = o.In, o.Out
		trustOpts.Confirm = o.Confirm
		if o.TrustConfirm != nil {
			trustOpts.Confirm = o.TrustConfirm
		}
		trustOpts.OnPrompt = o.OnPrompt
		gate = trust.Gate(trustOpts)
	}

	scopeOpts := judge.BoundaryOpts{
		WorkspaceRoot: o.Cwd,
		Roots:         o.ExtraRoots,
		Notify:        o.Notes.Emit,
		// The managed store lives in the config layer; the assembly point is
		// where judge and the store meet.
		Persist: PersistReadRoot,
		// The write side's two callbacks, both shared with the escalation dialog
		// so one answer means one thing: GrantWritable registers the sandbox root
		// AND the table row, and WritableAt reads the registry the sandbox
		// enforces with, so a directory a command may already write is not a
		// question a patch gets asked again.
		GrantWritable: GrantWritableRoot,
		WritableAt:    WritableAt,
	}
	scopeOpts.In, scopeOpts.Out, scopeOpts.Mu, scopeOpts.Confirm = o.In, o.Out, o.Mu, o.Confirm
	scopeOpts.OnPrompt = o.OnPrompt
	scopeOpts.AskCoord = askCoord
	if scopeGate := judge.BoundaryGate(scopeOpts); scopeGate != nil {
		gate = judge.ChainGates(gate, scopeGate)
	}

	permOpts := judge.GateOpts{
		Classifier: g.classifier(),
		Sandboxed:  o.Sandboxed,
		// Isolation is the default only where this platform has an
		// implementation. Hardcoding true would make every bash call on a
		// platform that never had a sandbox (Windows) fail closed.
		RequireSandbox: seatbelt.Supported() && seatbelt.ModeFromEnv() != seatbelt.ModeOff,
		Notify:         o.Notes.Emit,
		// The stdin prompt's `w`/`s` answers need the sandbox's writable registry
		// and the remembered row's spelling, both of which live here.
		Cwd:           o.Cwd,
		GrantWritable: GrantWritableRoot,
	}
	permOpts.In, permOpts.Out, permOpts.Mu, permOpts.Confirm = o.In, o.Out, o.Mu, o.Confirm
	permOpts.OnPrompt = o.OnPrompt
	permOpts.AskCoord = askCoord
	return judge.ChainGates(gate, judge.PermissionGate(o.Perms, permOpts))
}

// classifier builds the reviewer for one chain, tolerating a nil factory (the
// static floor then decides, and a gated call fails closed).
func (g *Gates) classifier() judge.Classifier {
	if g.opts.Classifier == nil {
		return nil
	}
	return g.opts.Classifier()
}

// Authorization snapshots the session's decisions for a run that cannot share
// this chain: a process-isolated sub-agent. It reports what the session
// answered, not what it was asked: Trusted is true when the run has no trust
// gate at all (an explicit non-interactive invocation) or the directory is
// trusted, so the child proceeds exactly where the parent would have.
func (g *Gates) Authorization() agentcore.ToolAuth {
	// Every read grant travels, because a child that saw none of them would ask
	// about a directory its parent had already been given — the grant the user
	// made would appear to evaporate across the process boundary. They come
	// from the one table, including grants made earlier in this same session.
	//
	// The write roots travel for the same reason, and used to be missing: a
	// session-only "allow writing /x" (a sandbox dialog, /boundary writable add)
	// lives in this process's table alone, so a child that was not handed it
	// would refuse a write its parent had been granted — and, having no prompt,
	// could not even ask. A remembered root needs no carrying: the child opens
	// grants.json itself.
	auth := agentcore.ToolAuth{
		ReadRoots:     dedupePaths(grants.ReadRoots()),
		WritableRoots: dedupePaths(grants.WritableRoots()),
	}
	if ns := grants.ProcessNetwork(); ns != nil {
		for _, g := range ns.Entries().Session {
			auth.NetworkRules = append(auth.NetworkRules, agentcore.NetworkRule{
				Kind: string(g.Kind), Key: g.Key, Tool: g.Tool, Allow: g.Allow,
			})
		}
	}
	if g.opts.Trust == nil {
		auth.SessionTrust = true
		return auth
	}
	auth.SessionTrust = g.opts.Trust.IsTrusted(g.opts.Cwd)
	auth.SessionReads = g.opts.Trust.ReadsTrusted(g.opts.Cwd)
	return auth
}

// dedupePaths keeps the first spelling of each path, so a grant present in both
// sources is not listed twice in the snapshot a child reads.
func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Seed applies a parent's authorization snapshot to this session's state,
// before any run is installed: the process-isolated sub-agent's side of
// Authorization. It never widens beyond what the parent held — the snapshot is
// the parent's decision — and it reports how many read roots were refused
// (too broad, or malformed), so the caller can note the difference.
func (g *Gates) Seed(auth agentcore.ToolAuth) (refusedRoots int) {
	if g.opts.Trust != nil {
		if auth.SessionTrust {
			g.opts.Trust.SetSessionTrust(g.opts.Cwd)
		}
		if auth.SessionReads {
			g.opts.Trust.SetSessionReads(g.opts.Cwd)
		}
	}
	for _, root := range auth.ReadRoots {
		if _, err := grants.AddReadRoot(root, grants.Session, "parent session"); err != nil {
			refusedRoots++
		}
	}
	// The write side is seeded as read+write rows, matching what the parent's
	// answer recorded: that is what AddWritableRoot means, and a child granted
	// "may write /x" is also allowed to read back what it wrote.
	for _, root := range auth.WritableRoots {
		if _, err := grants.AddWritableRoot(root, grants.Session, "parent session"); err != nil {
			refusedRoots++
		}
	}
	for _, rule := range auth.NetworkRules {
		if _, err := grants.AddNetwork(grants.NetworkGrant{
			Kind: grants.NetworkKind(rule.Kind), Key: rule.Key, Tool: rule.Tool,
			Allow: rule.Allow, Lifetime: grants.Session, Source: "parent session",
		}); err != nil {
			refusedRoots++
		}
	}
	return refusedRoots
}

// PersistReadRoot writes a read grant into the authorization table as a
// remembered row, so it survives the process. It lives here, in the gate
// assembly, because that is where the two layers that need each other can meet:
// judge owns the boundary gate and takes the answer, and this layer opens the
// managed file the row has to reach.
func PersistReadRoot(dir string) (made bool, why string) {
	root, err := grants.AddReadRoot(dir, grants.Remembered, "read dialog (remembered)")
	if err != nil {
		return false, "cannot remember this path: " + err.Error()
	}
	return true, root + " is remembered across sessions"
}

// WritableAt reports whether a path is already writable for this session,
// straight from the authorization table. The boundary gate asks it before
// putting a write question, so a patch into a directory a command may already
// write is not asked about again — the answer was given once, and the table is
// where it lives.
func WritableAt(path string) bool {
	p := permissions.Canonical(path)
	if p == "" {
		return false
	}
	return grants.Allows(p, grants.Write)
}

// SandboxDenialAsker builds the question a sandbox denial puts to the person,
// and applies the answer. It is the second trigger for the same question the
// refused-escape dialog asks (Kind ApprovalReAdmit, the same rows, the same
// registration), asked from the execution layer instead of the gate: the command
// already ran and the sandbox refused the path it needed, so running it again
// with that path admitted is the smaller fix — and the retry stays contained.
//
// nil means there is nothing to ask with (no driver dialog): the denial keeps
// its labeling for the model and nobody is interrupted.
func (g *Gates) SandboxDenialAsker() func(ctx context.Context, path, summary string) bool {
	if g == nil || (g.opts.Confirm == nil && g.opts.In == nil) {
		return nil
	}
	return func(ctx context.Context, path, summary string) bool {
		mode := ""
		if g.opts.Perms != nil {
			mode = g.opts.Perms.Mode().String()
		}
		req := judge.ApprovalRequest{
			Kind: judge.ApprovalReAdmit, Mode: mode, Level: judge.Sandbox,
			Tool: "bash", Summary: summary, ReadRoot: path,
			Rationale: "the sandbox denied " + path,
			// Which side was denied, so the dialog says it and leads with the
			// row that unblocks the retry (see judge.PathWriteNeed).
			NeedsWrite: judge.PathWriteNeed(path, summary),
		}
		req.Origin = agentcore.OriginFromContext(ctx)
		var ans judge.ApprovalAnswer
		if g.opts.Confirm != nil {
			ans = g.opts.Confirm(ctx, req)
		} else {
			lang := judge.ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
			ans = judge.AskReAdmit(ctx, g.opts.In, g.opts.Out, g.opts.Mu, path, summary, lang)
		}
		if !ans.Answered || !ans.Approve {
			return false
		}
		lang := judge.ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
		how, granted := judge.ApplyReAdmittedPath(ans, lang)
		if !granted {
			return false
		}
		if g.opts.Notes != nil {
			rationale := "the sandbox denied that path; " + how + " — retrying the command contained"
			if lang == "zh" {
				rationale = "沙箱拒绝了该路径；" + how + "，改为在沙箱内重跑"
			}
			// NoteSandboxed, the same kind the gate's own re-admission path
			// emits: the outcome is "the call runs contained", and the
			// rationale carries the granted path.
			g.opts.Notes.Emit(judge.Note{
				Tool: "bash", Kind: judge.NoteSandboxed, Level: judge.Allow, Lang: lang,
				Summary: summary, Rationale: rationale,
			})
		}
		return true
	}
}

// GrantWritableRoot makes a directory writable inside the sandbox, for this
// session and — when persist is true — for every later one. It is the callback
// behind the REPL prompt's `w`/`s` answers and the TUI dialog's rows.
//
// One row is the whole answer: the sandbox's profile builders read the table
// directly (seatbelt.WritableRoots delegates to it), so there is no second
// store to mirror into and no window where the two disagree.
func GrantWritableRoot(root string, persist bool) (made bool, why string) {
	p, err := permissions.NormalizePath(root)
	if err != nil {
		return false, "cannot grant write access to " + root + ": " + err.Error()
	}
	life, source := grants.Session, "write dialog"
	if persist {
		life, source = grants.Remembered, "write dialog (remembered)"
	}
	if _, err := grants.Add(grants.Grant{
		Path: p, Allow: grants.ReadWrite, Scope: grants.Subtree,
		Lifetime: life, Source: source,
	}); err != nil {
		return false, "cannot grant write access to " + root + ": " + err.Error()
	}
	if !persist {
		return true, ""
	}
	return true, p + " is writable and remembered"
}
