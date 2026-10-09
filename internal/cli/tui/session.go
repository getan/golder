// This file binds the full-screen TUI to the real agent run seam and the local
// session store (US-009, FR-16/17). It is the TUI counterpart to the REPL's
// replDeps + streamRun + cli.PersistTurn plumbing (internal/cli/repl): it
// assembles an AgentContext + RunConfig from the model's Options, feeds them to
// the event bridge (bridge.go's startRun → runtime.StartRun/DrainStream), and
// persists the growing conversation to ~/.golder/sessions after each turn.
//
// It deliberately imports the SHARED lower-level packages the REPL also uses
// (session, runtime, provider, cli, cli/run, cli/headless, cli/ui) rather than
// the repl package itself, so the two entry paths share one store and one
// run-config shape without an import cycle (repl and tui are siblings; prompts
// imports tui, so tui must not reach back into repl/prompts).
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/btw"
	"github.com/getan/golder/internal/cli/dreamcmd"
	goalpkg "github.com/getan/golder/internal/cli/goal"
	"github.com/getan/golder/internal/cli/headless"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/dream"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/memory"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/plugin"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/session"
	"github.com/getan/golder/internal/trust"
)

// runSession holds the assembled per-session state for a TUI run: the persisted
// store + header, the growing conversation context, the live (mutable) run
// config, and the tool/credential collaborators. It mirrors the subset of
// repl.replDeps the TUI needs, and owns the same session-tree cursor bookkeeping
// (curLeaf / persisted) so each turn is persisted as a branch rather than a
// flattening rewrite.
type runSession struct {
	store     *session.Store
	header    session.SessionHeader
	agentCtx  *agentcore.AgentContext
	live      *cli.LiveConfig
	reg       *agenttool.ToolRegistry
	reminders *runtime.ReminderRegistry
	schedule  *agenttool.Schedule
	creds     *provider.CredentialStore
	// snap is the shared file-snapshot recorder backing /rewind (nil when the
	// file tools are disabled). Each completed turn commits a restore point so
	// /rewind can roll files and the conversation back together.
	snap *agenttool.FileSnapshotRecorder
	// goal is the session's autonomous-goal state (in-memory only, like the
	// REPL's). /goal drives it through the same package the REPL uses.
	goal *agenttool.GoalState
	// preTurnLeaf / preTurnLabel capture the branch cursor and prompt before the
	// current run advances them, so run end can commit this turn's rewind point.
	preTurnLeaf  string
	preTurnLabel string

	// cwd is the directory golder was launched in, captured once at session
	// assembly. It is the trust key and the /status environment display.
	cwd string
	// trust persists project-trust decisions (US-018, #134). It is nil when
	// trust is disabled (store could not be loaded / no cwd); when nil /status
	// reports "disabled" and the trust-gated hook layer is skipped.
	trust *trust.Manager
	// slash is the shared slash-command registry the TUI consults exactly as the
	// REPL does. It is assembled per-session against the live config (withSession
	// rebinds the model's registry to this one) so /model switches reach it.
	slash *runtime.SlashRegistry
	// telemetry holds the retained per-run telemetry events (US-001, #291) and
	// the cumulative accumulator that sums metrics across all runs in the
	// session. The run loop folds each run's TelemetryEvent into it; /status
	// reads it back through the Host contract.
	telemetry *cli.TelemetryHolder

	// memoryRoot is the persistent-memory Store root (empty when memory is
	// disabled). It routes auto-compaction checkpoints to
	// <memoryRoot>/sessions/<id>/, the canonical checkpoint location, and backs
	// the /dream lock check.
	memoryRoot string
	// memstore is the live persistent-memory Store (nil when memory is disabled).
	// It lets /memory inspect entry counts without re-opening the database.
	memstore *memory.Store

	// budget is the session-scoped context-window budget shared by every run of
	// the session: the context tools read it through the run context and the
	// low-budget reminder ladder persists across prompts. Nil falls back to a
	// run-local state.
	budget *contextbudget.State

	// dispatcher is the session's hook dispatcher, nil when no hooks are
	// configured (FR-18). hookDeps carries the session id / project dir stamped
	// onto every HookInput and hook process environment.
	dispatcher *hooks.Dispatcher
	hookDeps   run.HookDeps
	// hooksWired records that wireHooks ran. An undecided launch directory
	// defers hook wiring until the first-run trust picker is answered, so
	// project-layer hooks load exactly when trust is granted and SessionStart
	// fires exactly once.
	hooksWired bool
	// onEvent is the observer chain delivered to every run: the plugin notifier
	// (US-017) with the SessionEnd/PreCompact hook notifier chained after it.
	onEvent func(agentcore.AgentEvent)

	// curLeaf is the id of the on-disk entry the next turn descends from; persisted
	// is the number of agentCtx.Messages already written. persist() appends only
	// Messages[persisted:] as a branch from curLeaf (see cli.PersistTurn).
	curLeaf   string
	persisted int

	// pendingUserShell buffers `!` passthrough records that settled while a run
	// was in flight: the run goroutine owns agentCtx.Messages until the run
	// drains, so the tea goroutine parks them here and runEndMsg merges them in
	// just before persist. Only the tea goroutine touches this slice.
	pendingUserShell []agentcore.UserMessage

	// persistMu serializes session saves. Mid-run checkpoints run on the loop
	// goroutine (RunConfig.Checkpoint) while the tea goroutine may save at run
	// end, on /export, or on a session switch; the mutex keeps those writers
	// from interleaving on the branch cursor and header.
	persistMu sync.Mutex

	// compacted is set when the run loop compacted the context (CompactionEvent):
	// compaction rewrites Messages into a summary + recent tail, which both shrinks
	// the slice below persisted (so an incremental Messages[persisted:] would panic)
	// and invalidates the branch prefix. persist() honors this by re-saving the
	// flattened context linearly and resetting the branch cursor, then clears it.
	compacted bool

	// cancelRun cancels the in-flight run's context; startRun sets it and the
	// two-stage interrupt (Model.interruptFn → interrupt) calls it. It is nil
	// before the first run and after a run is cancelled.
	cancelRun context.CancelFunc

	// approvalCh routes tool-call approval dialogs to the UI while a run is in
	// flight. startRun sets it alongside the notes handler; nil (no run, or a
	// session driven by tests) makes confirmApproval answer "unavailable", so
	// the gate keeps its fail-closed behavior instead of allowing by silence.
	approvalCh chan tea.Msg
	// approvedForSession remembers "Approve for this session" answers, keyed by
	// tool + summary, so the same shape of call is not asked twice in one run
	// of the TUI. Cleared when the session is rebuilt.
	approvedForSession map[string]bool
	// deniedForSession remembers explicit denials, keyed the same way, so a
	// model that retries the exact call it was just refused is blocked with a
	// note instead of nagging the user with the same dialog again.
	deniedForSession map[string]bool

	// lastBtw is the /btw side thread's context from this process and
	// lastBtwBase the background-message index it diverged from.
	lastBtw     *agentcore.AgentContext
	lastBtwBase int

	// remote owns the running remote-control server+bridge (remotecontrol.go),
	// nil when /remote-control is off. buildConfig reads it to install the remote
	// confirm seam so risky tool calls route to the paired browser while connected.
	remote *remoteSession

	// perms is the live approval mode shared with the tool set; notes is the
	// announcement sink the gates publish review decisions to. startRun
	// registers a per-run handler that forwards notes into the run's event
	// channel (cleared when the run ends).
	perms *permissions.State
	notes *run.ReviewNotes
	// steerQ is the shared mid-run input queue: messages the user submits while
	// a run is in flight wait here and are delivered by the loop's steering
	// seam (after the next tool execution) or follow-up seam (when the turn
	// settles without another tool call). It is the same instance the TUI model
	// renders (bound by withSession); nil is a valid no-op queue.
	steerQ *steerQueue
	// trusted records the launch directory's trust decision at assembly, so
	// the reviewer grades calls with the same context the trust gate enforces.
	trusted bool
}

// steerQueue is the mutex-guarded FIFO behind mid-run input. The TUI goroutine
// pushes typed messages and renders the pending list; the run loop's steering /
// follow-up seams drain it on the run goroutine, so both sides go through the
// lock. A nil *steerQueue is a valid empty queue, so a session assembled
// outside the TUI never has to initialize it.
type steerQueue struct {
	mu    sync.Mutex
	texts []string
}

// push appends a message to the queue (empty text is ignored, matching submit).
func (q *steerQueue) push(text string) {
	if q == nil || text == "" {
		return
	}
	q.mu.Lock()
	q.texts = append(q.texts, text)
	q.mu.Unlock()
}

// drain removes and returns every queued message in order. It is the loop-side
// operation: the run loop owns the messages from here on.
func (q *steerQueue) drain() []string {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	texts := q.texts
	q.texts = nil
	q.mu.Unlock()
	return texts
}

// pop removes and returns the oldest queued message. The model uses it when a
// run ends with messages still queued (typed after the loop's last drain, or
// left behind by an interrupt): exactly one is started as the next turn, so
// queued input is never silently dropped and runs do not nest.
func (q *steerQueue) pop() (string, bool) {
	if q == nil {
		return "", false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.texts) == 0 {
		return "", false
	}
	text := q.texts[0]
	q.texts = q.texts[1:]
	return text, true
}

// snapshot returns a copy of the pending messages for rendering.
func (q *steerQueue) snapshot() []string {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.texts...)
}

// newRunSession assembles the run session from the resolved Options, opening the
// shared ~/.golder/sessions store. When Options carries a ResumeID it loads that
// session's entries and rebuilds the context (the returned history seeds the
// replayed transcript); otherwise it starts a fresh session with a new header.
// It is the production entry; newRunSessionWithStore holds the store-agnostic
// core so tests can drive it against a temp-dir store.
func newRunSession(opts Options) (*runSession, []agentcore.Message, error) {
	store, err := headless.SessionStore()
	if err != nil {
		return nil, nil, err
	}
	return newRunSessionWithStore(store, opts)
}

// newRunSessionWithStore is the store-agnostic core of newRunSession: given an
// already-opened store it resolves resume-vs-fresh, builds the live config and
// collaborators, and returns the session plus the resumed history (nil for a
// fresh session).
func newRunSessionWithStore(store *session.Store, opts Options) (*runSession, []agentcore.Message, error) {
	creds := provider.NewCredentialStore(nil)
	creds.SetOverride(opts.ProviderName, opts.APIKey)

	// cwd is the launch directory, captured once: it stamps fresh sessions, is
	// the trust key, and feeds /status's environment section and the hook layer.
	cwd, _ := os.Getwd()

	now := time.Now().UTC()
	var (
		agentCtx *agentcore.AgentContext
		header   session.SessionHeader
		history  []agentcore.Message
		curLeaf  string
	)
	if opts.ResumeID != "" {
		h, entries, err := store.LoadEntries(opts.ResumeID)
		if err != nil {
			return nil, nil, err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		if len(entries) > 0 {
			curLeaf = entries[len(entries)-1].ID
		}
		header = h
		sysPrompt := h.SystemPrompt
		if sysPrompt == "" {
			sysPrompt = opts.SysPrompt
		}
		agentCtx = &agentcore.AgentContext{SystemPrompt: sysPrompt, Messages: msgs, Tools: opts.Tools}
		history = msgs
	} else {
		agentCtx = &agentcore.AgentContext{SystemPrompt: opts.SysPrompt, Tools: opts.Tools}
		// Stamp the launch directory onto a fresh session (#526/#524) so the
		// session is attributed to a project and a later /dream pass can distill it
		// under the right scope, mirroring headless/REPL. An unresolvable cwd
		// yields "" (session stays unattributed) rather than aborting.
		header = session.SessionHeader{
			ID:           session.NewID(now),
			CreatedAt:    now,
			UpdatedAt:    now,
			Model:        opts.Model,
			Provider:     opts.ProviderName,
			SystemPrompt: opts.SysPrompt,
			Cwd:          cwd,
		}
	}

	live := &cli.LiveConfig{
		Model:         opts.Model,
		ProviderName:  opts.ProviderName,
		Provider:      opts.Provider,
		BaseURL:       opts.BaseURL,
		Protocol:      opts.Protocol,
		ThinkingLevel: opts.ThinkingLevel,
		ContextWindow: cli.ResolveContextWindow(opts.ProviderName, opts.Model),
	}

	// Project trust (US-018, #134): load the persisted trust store for the
	// launch directory, mirroring the REPL. A load failure (or an unresolvable
	// cwd) is non-fatal: trust is disabled (mgr stays nil) and the TUI still
	// runs — the store is surfaced rather than silently overwritten.
	mgr, mgrErr := trust.NewManager(trust.DefaultPath())
	if mgrErr != nil {
		fmt.Fprintf(os.Stderr, "golder: trust store unavailable, trust disabled: %v\n", mgrErr)
		mgr = nil
	}
	if cwd == "" && mgr != nil {
		fmt.Fprintf(os.Stderr, "golder: cannot resolve working directory, trust disabled\n")
		mgr = nil
	}

	s := &runSession{
		store:      store,
		header:     header,
		agentCtx:   agentCtx,
		live:       live,
		reg:        run.ToolRegistry(opts.Tools),
		reminders:  run.TodoReminders(opts.Tools),
		schedule:   agenttool.ScheduleFromTools(opts.Tools),
		snap:       run.SnapshotRecorderFromTools(opts.Tools),
		goal:       agenttool.NewGoalState(),
		creds:      creds,
		cwd:        cwd,
		trust:      mgr,
		slash:      newSlashRegistry(opts, live),
		telemetry:  cli.NewTelemetryHolder(),
		curLeaf:    curLeaf,
		persisted:  len(history),
		memoryRoot: run.MemoryRootFromTools(opts.Tools),
		memstore:   run.MemoryStoreFromTools(opts.Tools),
		perms:      opts.Permissions,
		notes:      opts.ReviewNotes,
		budget:     opts.Budget,
		trusted:    opts.Approve || (mgr != nil && mgr.IsTrusted(cwd)),
		steerQ:     &steerQueue{},
	}
	if s.perms == nil {
		s.perms = permissions.New(permissions.Auto)
	}
	if s.notes == nil {
		s.notes = run.NewReviewNotes()
	}
	if s.budget == nil {
		s.budget = contextbudget.New()
	}
	// /trust is a per-session command (its closure captures mgr + cwd), so it is
	// registered here rather than in newSlashRegistry. A nil mgr is a no-op.
	trust.RegisterCommand(s.slash, mgr, cwd)
	// /permissions is likewise per-session: its closure captures the shared
	// state object so a switch reaches the very gate the next run builds, and
	// its output follows the conversation language.
	prompts.RegisterPermissionCommand(s.slash, s.perms, func() string {
		return judge.ConversationLanguage(s.agentCtx.Messages)
	})

	s.hookDeps = run.HookDeps{SessionID: header.ID, ProjectDir: cwd, WarnLog: os.Stderr}
	// Wire hooks now when the trust decision is already known (--approve or a
	// saved decision). In an undecided directory the TUI asks first via the
	// first-run trust picker (Model.withSession); wireHooks then runs when the
	// answer lands, mirroring the REPL's ordering (dialog before hook
	// resolution) so project-layer hooks load exactly when trust is granted and
	// SessionStart fires exactly once. Until then only plugin events are live.
	if s.trust == nil || s.trusted || s.trust.NearestTrustDecision(cwd).Found {
		s.wireHooks(opts)
	} else if n := plugin.NewEventNotifier(opts.Plugins, os.Stderr); n != nil {
		s.onEvent = n.Handle
	}
	return s, history, nil
}

// wireHooks resolves the trust-gated hook set for the session's launch
// directory, builds the dispatcher, dispatches SessionStart once, and composes
// the SessionEnd/PreCompact observer with the plugin notifier. Trust is granted
// by --approve, a saved decision, or the first-run trust picker; project-layer
// hooks only apply when trusted (FR-14). A malformed hook layer disables hooks
// with a warning rather than failing the TUI launch. It is idempotent: the
// first call wins, so the picker answering after a resolved decision (or vice
// versa) can never double-dispatch SessionStart.
func (s *runSession) wireHooks(opts Options) {
	if s.hooksWired {
		return
	}
	s.hooksWired = true
	var baseOnEvent func(agentcore.AgentEvent)
	if n := plugin.NewEventNotifier(opts.Plugins, os.Stderr); n != nil {
		baseOnEvent = n.Handle
	}
	if set, err := run.ResolveHookSet(s.hookDeps.ProjectDir, s.trusted); err != nil {
		fmt.Fprintf(os.Stderr, "golder: hooks disabled: %v\n", err)
		s.onEvent = baseOnEvent
		return
	} else if d := run.BuildDispatcher(set, s.hookDeps); d != nil {
		s.dispatcher = d
		if s.reminders == nil {
			s.reminders = runtime.NewReminderRegistry()
		}
		ssCfg := runtime.RunConfig{Reminders: s.reminders}
		run.DispatchSessionStart(context.Background(), d, &ssCfg, s.hookDeps, sessionStartSource(opts))
		s.reminders = ssCfg.Reminders
		n := hooks.NewHookNotifier(d, s.hookDeps.SessionID, s.hookDeps.ProjectDir)
		s.onEvent = chainTUIEvent(baseOnEvent, n.Handle)
		return
	}
	s.onEvent = baseOnEvent
}

// decideTrust applies the first-run trust picker's answer (or Esc, choice "")
// and wires the deferred hooks for the first time. It returns a one-line
// outcome for the transcript.
func (s *runSession) decideTrust(opts Options, choice string) string {
	if s.trust == nil {
		s.wireHooks(opts)
		return "trust store unavailable; running without a trust decision"
	}
	switch choice {
	case "trust":
		if err := s.trust.SetDecision(s.cwd, trust.Trusted); err != nil {
			s.wireHooks(opts)
			return "could not save trust decision: " + err.Error()
		}
		s.trusted = true
		s.wireHooks(opts)
		return fmt.Sprintf("Trusted %s (saved; side-effect tools run without asking).", s.cwd)
	case "once":
		s.trust.SetSessionTrust(s.cwd)
		s.trusted = true
		s.wireHooks(opts)
		return fmt.Sprintf("Trusted %s for this session only.", s.cwd)
	case "reject":
		s.trust.ClearSessionTrust(s.cwd)
		if err := s.trust.SetDecision(s.cwd, trust.Untrusted); err != nil {
			s.wireHooks(opts)
			return "could not save trust decision: " + err.Error()
		}
		s.wireHooks(opts)
		return fmt.Sprintf("Marked %s untrusted (saved; side-effect tools stay under review).", s.cwd)
	default: // Esc: session-scoped no, nothing persisted
		s.trust.ClearSessionTrust(s.cwd)
		s.wireHooks(opts)
		return fmt.Sprintf("Left %s untrusted for this session.", s.cwd)
	}
}

// sessionStartSource maps the resolved run options to the SessionStart source
// tag: "resume" when continuing an existing session, "startup" otherwise.
func sessionStartSource(opts Options) string {
	if opts.ResumeID != "" {
		return "resume"
	}
	return "startup"
}

// chainTUIEvent composes the plugin notifier with the hook notifier into one
// observer; a nil operand is identity.
func chainTUIEvent(prev, next func(agentcore.AgentEvent)) func(agentcore.AgentEvent) {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	return func(ev agentcore.AgentEvent) {
		prev(ev)
		next(ev)
	}
}

// buildConfig assembles the RunConfig for one turn from the live config and
// collaborators. It replicates repl.streamRun's assembly (same LoopConfig fields,
// tool registry and reminders) minus the interactive trust confirmation hook: the
// TUI has no stdin prompt to confirm side-effect tool calls on, so tools run
// under the trust granted up front (--approve, a saved decision, or the
// first-run trust picker) rather than a per-call BeforeToolCall prompt. The
// stream fn is derived from the live provider and the API key resolved through
// the credential store, exactly as the REPL does.
//
// ch, when non-nil, is the run's event channel: the steering seams echo each
// delivered mid-run message into it so the transcript can show the message at
// the moment it enters the conversation (see installSteerSeams).
func (s *runSession) buildConfig(ch chan tea.Msg) runtime.RunConfig {
	cfg := runtime.RunConfig{
		LoopConfig: runtime.LoopConfig{
			Model:         s.live.Model,
			Provider:      s.live.ProviderName,
			ThinkingLevel: s.live.ThinkingLevel,
			Stream:        provider.StreamFnFromProvider(s.live.Provider),
			GetAPIKey:     s.creds.GetAPIKey,
			ContextWindow: s.live.ContextWindow,
			Compaction:    compaction.DefaultCompactionSettings,
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{
				Registry: s.reg,
			},
		},
		Reminders:  s.reminders,
		Budget:     s.budget,
		SessionID:  s.header.ID,
		MemoryRoot: s.memoryRoot,
	}
	// Per-turn wiring of the tool-execution + Stop seams; nil dispatcher is a
	// no-op so the hot path pays nothing when no hooks are configured (FR-18).
	if s.dispatcher != nil {
		run.InstallSeams(&cfg, s.dispatcher, s.hookDeps)
	}
	// Due session-local reminders ride the follow-up seam (issue #565); a nil
	// scheduler (no schedule_* tools) leaves the seam unwired.
	if s.schedule != nil {
		cfg.GetFollowUpMessages = s.schedule.FollowUpMessages
	}
	// Mid-run user messages ride the same two seams: steering delivers them
	// after the next tool execution; the follow-up wrapper delivers any that
	// are still queued when a tool-free turn settles, so a message typed while
	// the model streams its final answer is never left behind.
	s.installSteerSeams(&cfg, ch)
	// When remote control is active, route side-effect tool-call confirmations to
	// the paired browser (no-op when no client is connected or the cwd is trusted,
	// so the non-remote path is unchanged). The trust manager is read from the
	// shared store; a nil manager disables the seam.
	//
	// The permission gate runs underneath: its mode (read-only / ask / auto /
	// full-access) decides how a call is handled. The TUI has no stdin
	// prompt, so ask-mode confirmations fail closed with a note while auto
	// mode decides via the reviewer. The gate leads so its block
	// short-circuits before the browser is asked.
	reviewer := run.NewReviewer(s.live.Model, s.live.ProviderName, s.live.Provider, s.creds, s.header.ID, s.hookDeps.ProjectDir, s.trusted)
	judgeGate := judge.PermissionGate(s.perms, judge.GateOpts{
		Classifier: reviewer,
		Sandboxed:  run.SandboxGate(),
		Notify:     s.notes.Emit,
		Confirm:    s.confirmApproval,
	})
	// The trust gate leads: a side-effect tool in an untrusted directory is the
	// first question to answer (the REPL asks it on stdin; the TUI asks
	// through the approval dialog, or the paired browser while remote control
	// is connected). The permission gate then grades what is left. Order
	// matters: a blocked trust question must not be pre-empted by an approval
	// the reviewer would have granted.
	gate := judgeGate
	if trustGate := s.trustApprovalGate(s.remote); trustGate != nil {
		gate = judge.ChainGates(trustGate, judgeGate)
	}
	cfg.Batch.ToolExecutorConfig.BeforeToolCall = gate
	// Mid-run persistence: the loop checkpoints the conversation at turn
	// boundaries so a crash loses at most the round in flight instead of the
	// whole session. /btw runs clear this again (a side thread never touches
	// the main conversation or disk).
	cfg.Checkpoint = s.checkpointMidRun
	return cfg
}

// installSteerSeams wires the session's mid-run input queue into cfg. The
// steering seam delivers everything queued after the current turn's tool
// execution (pi's per-turn semantics); the follow-up seam is wrapped so
// anything still queued when the inner loop settles — a message typed while
// the model was streaming a tool-free final answer — becomes the next input
// instead of ending the run with the message stranded. User input wins over
// scheduled reminders and goal re-prompts because the wrapper drains steers
// before delegating to the follow-up source that was already installed.
func (s *runSession) installSteerSeams(cfg *runtime.RunConfig, ch chan tea.Msg) {
	pull := s.steerPull(ch)
	cfg.GetSteeringMessages = func(context.Context) []agentcore.AgentMessage { return pull() }
	cfg.GetFollowUpMessages = steeredFollowUp(cfg.GetFollowUpMessages, pull)
}

// steerPull returns the drain-and-convert closure shared by the steering and
// follow-up seams. Draining is atomic (one lock), so a message is delivered by
// exactly one seam; each drain is echoed to ch as steerInjectedMsg so the
// transcript can render the user block right as the loop commits the message
// to the conversation. The send is on the loop goroutine — the same goroutine
// the event pump runs on — so it cannot race the pump's own sends.
func (s *runSession) steerPull(ch chan tea.Msg) func() []agentcore.AgentMessage {
	return func() []agentcore.AgentMessage {
		texts := s.steerQ.drain()
		if len(texts) == 0 {
			return nil
		}
		if ch != nil {
			ch <- steerInjectedMsg{texts: texts}
		}
		msgs := make([]agentcore.AgentMessage, 0, len(texts))
		for _, text := range texts {
			content, err := ui.BuildUserContent(text)
			if err != nil {
				// A malformed image reference must not swallow the message;
				// fall back to plain text, exactly like startRun.
				content = agentcore.ContentList{agentcore.NewTextContent(text)}
			}
			msgs = append(msgs, agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: content})
		}
		return msgs
	}
}

// steeredFollowUp wraps a follow-up source with the steer drain: queued user
// input is consulted first, and the wrapped source (scheduled reminders, goal
// re-prompts, or nil) only runs when the queue is empty.
func steeredFollowUp(orig func(context.Context, *agentcore.AgentContext) []agentcore.AgentMessage, pull func() []agentcore.AgentMessage) func(context.Context, *agentcore.AgentContext) []agentcore.AgentMessage {
	return func(ctx context.Context, agentCtx *agentcore.AgentContext) []agentcore.AgentMessage {
		if msgs := pull(); len(msgs) > 0 {
			return msgs
		}
		if orig != nil {
			return orig(ctx, agentCtx)
		}
		return nil
	}
}

// rebuildDoneMsg reports the outcome of a manual context operation (/compact)
// to the model: label names the operation, summary is the status line to show
// in the transcript, err is set when it failed (the context is then left
// unchanged).
type rebuildDoneMsg struct {
	label   string
	summary string
	err     error
}

// dreamDoneMsg reports the outcome of a manual /dream: report is the parsed
// consolidation report on success, summary a plain notice (e.g. a dream is
// already running), err a spawn or parse failure.
type dreamDoneMsg struct {
	report  dream.Report
	summary string
	err     error
}

// compactCmd runs a manual context compaction off the tea loop and yields a
// rebuildDoneMsg the model folds into the transcript. It mirrors the REPL's
// runManualCompact. ctx is the session-op context: Ctrl+C/Esc while the
// summarization call is in flight cancels it, so an unresponsive provider can
// never lock the UI in the running state (the op reports the cancellation
// through its done message).
func (s *runSession) compactCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		summary, err := s.compact(ctx)
		if err != nil && ctx.Err() == context.Canceled {
			return rebuildDoneMsg{label: "compact", summary: "compact cancelled (context left unchanged)"}
		}
		return rebuildDoneMsg{label: "compact", summary: summary, err: err}
	}
}

// compact replaces the context with a fresh summarization checkpoint plus the
// recent tail (the same force-a-new-summary flow as Codex's /compact). It
// replaces agentCtx.Messages in place on success and flags compacted so persist()
// re-saves the flattened context linearly. A failure leaves the context unchanged.
func (s *runSession) compact(ctx context.Context) (string, error) {
	msgs := s.agentCtx.Messages
	settings := compaction.DefaultCompactionSettings
	before := compaction.EstimateContextTokens(msgs).Tokens

	stream := provider.StreamFnFromProvider(s.live.Provider)
	model := provider.Model{Provider: s.live.ProviderName, ID: s.live.Model, ContextWindow: s.live.ContextWindow}
	// Resolve the API key like a normal turn so summarization authenticates
	// against auth-requiring providers.
	scfg := provider.StreamConfig{}
	if s.creds != nil {
		scfg.APIKey = s.creds.GetAPIKey(context.Background(), s.live.ProviderName)
	}
	res, err := compaction.Compact(ctx, stream, model, msgs, settings, -1, nil, "", scfg)
	if err != nil {
		return "", err
	}
	if res == nil {
		return fmt.Sprintf("nothing to compact (%d tokens, %d messages)", before, len(msgs)), nil
	}
	rebuilt := res.RebuildContext(msgs, time.Now().UnixMilli())
	s.agentCtx.Messages = rebuilt
	s.markCompacted()
	after := compaction.EstimateContextTokens(rebuilt).Tokens
	summarized := len(msgs) - (len(rebuilt) - 1)
	return fmt.Sprintf("compacted: %d → %d tokens, summarized %d messages, kept %d",
		before, after, summarized, len(rebuilt)-1), nil
}

// dreamCmd runs the manual /dream consolidation subprocess off the tea loop
// (the child is LLM-backed and bounded by dreamcmd.RunTimeout) and yields the
// parsed report. A live lock from a background run is reported instead of
// spawning a second child. ctx is the session-op context: Ctrl+C/Esc while the
// child runs kills it through the same cancellation.
func (s *runSession) dreamCmd(ctx context.Context, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		if dreamcmd.LockHeld(s.memoryRoot) {
			return dreamDoneMsg{summary: "a dream consolidation is already running"}
		}
		ctx, cancel := context.WithTimeout(ctx, dreamcmd.RunTimeout)
		defer cancel()
		res, err := dreamcmd.Spawn(ctx, s.hookDeps.ProjectDir, dryRun)
		if err != nil {
			if ctx.Err() == context.Canceled {
				return dreamDoneMsg{summary: "dream cancelled"}
			}
			return dreamDoneMsg{err: err}
		}
		return dreamDoneMsg{report: res.Report}
	}
}

// newRunChannel creates the event channel for one run and wires the per-run
// callbacks that must target it: review notes (gate decisions render as
// transcript cards) and approval dialogs (confirmApproval hands requests to
// the UI and blocks on the reply). Every run type — a prompt, /btw, /goal —
// must build its channel through here: a run that skipped the approval wiring
// would send its first confirmation to a previous run's dead channel, where
// the request is never displayed and the tool call stalls until the user
// interrupts.
func (s *runSession) newRunChannel() chan tea.Msg {
	ch := newEventChan()
	if s.notes != nil {
		s.notes.Set(func(n judge.Note) { ch <- judgeNoteMsg{note: n} })
	}
	// Approval dialogs ride the same per-run channel: the gate's Confirm
	// callback hands the request to the UI and blocks on its reply, so the run
	// goroutine pauses exactly while the user decides.
	s.approvalCh = ch
	return ch
}

// prompt to the growing context as a user message, then hands the context and a
// freshly-built config to the event bridge (bridge.startRun → runtime.StartRun +
// DrainStream on a goroutine), returning the bridge channel and the first
// waitForEvent Cmd so Update can pump the run's events. The context grows in
// place (agentCtx is a pointer), so the next turn continues the conversation.
func (s *runSession) startRun(prompt string) (chan tea.Msg, tea.Cmd) {
	content, err := ui.BuildUserContent(prompt)
	if err != nil {
		// A malformed image reference must not swallow the turn: fall back to the
		// raw prompt as plain text so the run still starts.
		content = agentcore.ContentList{agentcore.NewTextContent(prompt)}
	}
	// UserPromptSubmit runs before the prompt is committed to the context: a block
	// aborts the turn (emitting a runEndMsg carrying the reason) without leaving a
	// dangling user message; additionalContext is injected into this turn only.
	if s.dispatcher != nil {
		pc := runtime.RunConfig{Reminders: s.reminders}
		if block, reason := run.DispatchUserPromptSubmit(context.Background(), s.dispatcher, &pc, s.hookDeps, prompt); block {
			ch := newEventChan()
			go func() { ch <- runEndMsg{err: fmt.Errorf("prompt blocked by hook: %s", reason)} }()
			return ch, waitForEvent(ch)
		}
		s.reminders = pc.Reminders
	}
	s.agentCtx.Messages = append(s.agentCtx.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   content,
	})
	// Persist the prompt before the run starts: the first mid-run checkpoint
	// lands only after the model's first reply, and a crash during that reply
	// must not lose what the user typed. Best-effort — the run-end save retries
	// and reports failures; the write here is serialized against the loop's
	// checkpoints by persistMu, though no run is in flight yet.
	_ = s.persist()
	// Use a cancellable context so the two-stage interrupt (FR-14) can stop this
	// run: cancelling propagates through StartRun/DrainStream, which then emits a
	// runEndMsg and the model returns to idle.
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelRun = cancel
	// Remember the branch cursor and prompt before the turn advances them, so
	// run end can commit a rewind restore point for exactly this turn.
	s.preTurnLeaf = s.curLeaf
	s.preTurnLabel = prompt
	ch := s.newRunChannel()
	return ch, startRunOn(ch, ctx, s.agentCtx, s.buildConfig(ch), s.onEvent)
}

// confirmApproval is the permission gate's approval dialog for the TUI. It
// hands the request to the UI goroutine and blocks until the user answers or
// the run is cancelled. An interrupted run (ctx done) or a session with no UI
// channel answers "not answered", which the gate treats as fail-closed — a
// pending dialog is never silently an approval.
func (s *runSession) confirmApproval(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer {
	if key := approvalKey(req); key != "" && s.approvedForSession[key] {
		return judge.ApprovalAnswer{Approve: true, Always: true, Answered: true}
	}
	if key := approvalKey(req); key != "" && s.deniedForSession[key] {
		return judge.ApprovalAnswer{Answered: true}
	}
	// A path the user already granted to the sandbox (a prior dialog, a
	// manual /permissions writable, or config) settles an escalation without
	// a dialog: the call is contained with the grant, which is exactly what
	// the grant pre-decided. Without this, every retry would re-ask.
	if root := escalationPathCandidate(req, s.cwd); root != "" {
		for _, granted := range seatbelt.WritableRoots() {
			if coveredBy(root, granted) {
				return judge.ApprovalAnswer{Approve: true, WritableRoot: root, Answered: true}
			}
		}
	}
	ch := s.approvalCh
	if ch == nil {
		return judge.ApprovalAnswer{}
	}
	reply := make(chan judge.ApprovalAnswer, 1)
	select {
	case ch <- approvalRequestMsg{req: req, reply: reply}:
	case <-ctx.Done():
		return judge.ApprovalAnswer{}
	}
	select {
	case ans := <-reply:
		if key := approvalKey(req); key != "" {
			switch {
			case ans.Approve && ans.Always:
				if s.approvedForSession == nil {
					s.approvedForSession = map[string]bool{}
				}
				s.approvedForSession[key] = true
				// An earlier denial must not keep shadowing a later explicit
				// approval of the same call.
				delete(s.deniedForSession, key)
			case ans.Answered && !ans.Approve:
				if s.deniedForSession == nil {
					s.deniedForSession = map[string]bool{}
				}
				s.deniedForSession[key] = true
			}
		}
		return ans
	case <-ctx.Done():
		// The run was cancelled with the dialog open; the UI closes it on
		// runEndMsg and its answer (if any) lands in the buffered reply.
		return judge.ApprovalAnswer{}
	}
}

// approvalKey keys the "approve for this session" memory: the tool plus its
// summary (the command text), so approving a build does not also approve an
// unrelated command that happens to share the tool.
func approvalKey(req judge.ApprovalRequest) string {
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		return ""
	}
	return req.Tool + "\x00" + summary
}

// interrupt cancels the in-flight run, if any. It is bound to Model.interruptFn
// by withSession so pressing Esc / Ctrl+C while running stops the current run
// instead of quitting the program (FR-14). Safe to call when no run is active.
func (s *runSession) interrupt() {
	if s.cancelRun != nil {
		s.cancelRun()
	}
}

// startBtwRun starts a one-shot /btw side run: the question is appended to a
// fresh copy of the main context and streamed back, and nothing is written to
// the main conversation or to disk. The side context is remembered so a bare
// /btw can replay it.
func (s *runSession) startBtwRun(question string) (chan tea.Msg, tea.Cmd) {
	side := btw.NewSideContext(s.agentCtx)
	side.Messages = append(side.Messages, agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(question)},
	})
	s.lastBtw = side
	s.lastBtwBase = len(side.Messages) - 1
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelRun = cancel
	ch := s.newRunChannel()
	return ch, startRunOn(ch, ctx, side, s.sideRunConfig(ch), s.onEvent)
}

// sideRunConfig is buildConfig for a /btw side thread: the run reads the main
// conversation but never writes it, so mid-run checkpoints are disabled (the
// run-end persist is skipped for side runs too).
func (s *runSession) sideRunConfig(ch chan tea.Msg) runtime.RunConfig {
	cfg := s.buildConfig(ch)
	cfg.Checkpoint = nil
	return cfg
}

// startGoalRun starts an autonomous goal run on the shared conversation: the
// run gets the goal-control tools, the goal reminder, and the follow-up hook
// that keeps re-prompting until the model completes/blocks the goal or a guard
// trips (all from the goal package, so the REPL and TUI share one loop). label
// tags the rewind restore point the run commits on completion.
func (s *runSession) startGoalRun(label string) (chan tea.Msg, tea.Cmd) {
	s.preTurnLeaf = s.curLeaf
	s.preTurnLabel = label
	ch := s.newRunChannel()
	cfg := s.buildConfig(ch)
	cfg.Batch.ToolExecutorConfig.Registry = goalpkg.ToolRegistry(s.reg, s.goal)
	cfg.Reminders = goalpkg.Reminders(s.reg, s.goal)
	// Wrap the goal re-prompt with the steer drain: a user message typed
	// mid-run is delivered before the goal is nudged again.
	cfg.GetFollowUpMessages = steeredFollowUp(goalpkg.FollowUp(s), s.steerPull(ch))
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelRun = cancel
	return ch, startRunOn(ch, ctx, s.agentCtx, cfg, s.onEvent)
}

// persist writes the messages produced since the last persist as a new branch
// descending from the active leaf, advancing the leaf and the persisted cursor.
// It mirrors cli.PersistTurn: growing the on-disk tree with AppendBranch (rather
// than a linear rewrite) keeps history intact. A no-op when nothing new was
// produced, so an idle turn-end never regenerates entry ids.
//
// It is called both from the tea goroutine (run end, /export, session
// switches) and from the loop goroutine (mid-run checkpoints via
// checkpointMidRun); persistMu serializes the two.
func (s *runSession) persist() error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	return s.persistLocked()
}

// mergePendingUserShell appends the passthrough records buffered while a run
// was in flight to the shared context. The caller is the tea goroutine, and
// only after the run has fully drained (runEndMsg), so the agentCtx write does
// not race the loop: runEndMsg clears running and then calls this before
// persist, which also writes the merged records as part of this turn's branch.
func (s *runSession) mergePendingUserShell() {
	if len(s.pendingUserShell) == 0 {
		return
	}
	for _, m := range s.pendingUserShell {
		s.agentCtx.Messages = append(s.agentCtx.Messages, m)
	}
	s.pendingUserShell = nil
}

// persistLocked is persist's body. The caller must hold persistMu.
func (s *runSession) persistLocked() error {
	// A compaction during the run rewrote Messages into a summary + recent tail,
	// so the append-a-tail branch model no longer holds: the prefix changed and
	// the slice may be shorter than persisted. Re-save the flattened context
	// linearly and reset the branch cursor to the new leaf, mirroring the REPL's
	// /compact handling.
	if s.compacted || s.persisted > len(s.agentCtx.Messages) {
		s.header.UpdatedAt = time.Now().UTC()
		s.header.Model = s.live.Model
		s.header.Provider = s.live.ProviderName
		if err := s.store.Save(s.header, s.agentCtx.Messages); err != nil {
			return err
		}
		s.persisted = len(s.agentCtx.Messages)
		s.curLeaf = ""
		if _, entries, err := s.store.LoadEntries(s.header.ID); err == nil && len(entries) > 0 {
			s.curLeaf = entries[len(entries)-1].ID
		}
		s.compacted = false
		return nil
	}
	tail := s.agentCtx.Messages[s.persisted:]
	if len(tail) == 0 {
		return nil
	}
	s.header.UpdatedAt = time.Now().UTC()
	s.header.Model = s.live.Model
	s.header.Provider = s.live.ProviderName
	leaf, err := s.store.AppendBranch(s.header, s.curLeaf, tail)
	if err != nil {
		return err
	}
	s.curLeaf = leaf
	s.persisted = len(s.agentCtx.Messages)
	return nil
}

// markCompacted records that the context was rewritten in place (auto-
// compaction or a manual /compact): the next persist re-saves the flattened
// conversation linearly instead of appending an incremental branch. Safe to
// call from any goroutine.
func (s *runSession) markCompacted() {
	s.persistMu.Lock()
	s.compacted = true
	s.persistMu.Unlock()
}

// checkpointMidRun is the RunConfig.Checkpoint hook: the run loop hands it the
// conversation at turn boundaries so a crash loses at most the round in flight.
// It runs on the loop goroutine, which owns agentCtx.Messages; the write itself
// is serialized against the tea goroutine's saves by persistMu. Failures are
// deliberately silent — the run-end persist retries and reports them — because
// a mid-run save must never interrupt the run.
func (s *runSession) checkpointMidRun(_ context.Context, _ *agentcore.AgentContext, compacted bool) {
	if compacted {
		s.markCompacted()
	}
	_ = s.persist()
}

// seedTranscript replays a resumed session's prior messages into the transcript
// so the user sees the conversation so far before re-prompting (the TUI analogue
// of repl.replayTranscript). User and assistant text become their respective
// blocks, and every tool call replays as the same card the live run showed —
// paired with its recorded result, so the response body, the colored diff, and
// the warn state survive a restart. Cards replay folded exactly as they were
// (apply_patch expanded, reads folded), so a resumed transcript reads like the
// session it continues.
func seedTranscript(t *transcript, history []agentcore.Message) {
	// A long session replays hundreds of blocks (and thousands of rendered
	// lines): suspend reflow while appending so the replay pays for one layout
	// pass instead of re-laying the growing history per message, which is
	// quadratic and froze resumed sessions for minutes.
	t.beginBatch()
	defer t.endBatch()
	results := toolResultsByCallID(history)
	for _, m := range history {
		switch msg := m.(type) {
		case agentcore.UserMessage:
			if text := agentcore.ContentToText(msg.Content); text != "" {
				// A `!` passthrough record replays as the shell card it rendered
				// live, not as raw <user_shell_command> XML dumped into the
				// transcript.
				if d, ok := cli.ParseUserShellRecord(text); ok {
					t.addToolCard(replayUserShellCard(d))
					continue
				}
				t.addUser(text)
			}
		case agentcore.AssistantMessage:
			if text := agentcore.ContentToText(msg.Content); text != "" {
				t.finalizeTurn(msg)
			}
			for _, c := range msg.ToolCalls() {
				var res *agentcore.ToolResultMessage
				if r, ok := results[c.ID]; ok {
					res = &r
				}
				t.addToolCard(replayToolCard(c, res))
			}
		}
	}
}

// toolResultsByCallID indexes a session's tool results by the call they answer,
// so a replayed call can find its own result.
func toolResultsByCallID(history []agentcore.Message) map[string]agentcore.ToolResultMessage {
	results := make(map[string]agentcore.ToolResultMessage)
	for _, m := range history {
		if tr, ok := m.(agentcore.ToolResultMessage); ok && tr.ToolCallID != "" {
			results[tr.ToolCallID] = tr
		}
	}
	return results
}

// replayToolCard rebuilds the finished card a historical tool call produced.
// res is the call's recorded result, or nil when the call never ran (the run
// was interrupted before it); that case mirrors the live closeout and lands on
// cardWarn so the transcript does not promise output that never arrived.
func replayToolCard(c agentcore.ToolCallContent, res *agentcore.ToolResultMessage) *toolCard {
	var input map[string]any
	if len(c.Arguments) > 0 {
		_ = json.Unmarshal(c.Arguments, &input)
	}
	card := &toolCard{
		id:       c.ID,
		name:     c.Name,
		input:    input,
		state:    cardSuccess,
		expanded: defaultCardExpanded(c.Name),
	}
	if res == nil {
		card.state = cardWarn
		return card
	}
	card.complete(!res.IsError, agentcore.ContentToText(res.Content), res.Details)
	return card
}

// switchTo replaces the active session with the stored session id, persisting
// the current session first so unsaved turns are not lost. It returns the
// loaded messages for transcript seeding. The live model/provider follow the
// stored header: a different provider name re-resolves the driver from the
// registry (defaults; API keys resolve from the environment via the session's
// credential store), while the thinking level and context window stay with the
// current live config. Hook identity follows the new session; no SessionStart
// is re-dispatched (project hooks fired once at launch).
func (s *runSession) switchTo(id string) ([]agentcore.Message, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("usage: /resume <session-id>")
	}
	if id == s.header.ID {
		return nil, fmt.Errorf("already on session %s", id)
	}
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	h, entries, err := s.store.LoadEntries(id)
	if err != nil {
		return nil, err
	}
	return s.adopt(h, entries, true)
}

// adopt replaces the active session with header h and its entry path: it
// rebuilds the flat message list and resets the branch cursor. With followHeader
// the stored model/provider win (re-resolving the driver when the provider
// differs), as /resume needs; without it the live config stays untouched (branch
// switches and imports keep serving with the provider already in use).
func (s *runSession) adopt(h session.SessionHeader, entries []session.Entry, followHeader bool) ([]agentcore.Message, error) {
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	sysPrompt := h.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = s.agentCtx.SystemPrompt
	}
	if followHeader && h.Provider != "" && h.Provider != s.live.ProviderName {
		prov, name, err := provider.ResolveProvider(h.Model, "", "", h.Provider, os.Getenv)
		if err != nil {
			return nil, fmt.Errorf("resolve session provider: %w", err)
		}
		s.live.Provider = prov
		s.live.ProviderName = name
		s.live.BaseURL = ""
		s.live.Protocol = ""
	}
	if followHeader && h.Model != "" {
		s.live.Model = h.Model
	}
	// The resumed session may run a different model than the launch one;
	// re-resolve the context budget so the gauge and auto-compaction follow.
	s.live.RefreshContextWindow()
	s.header = h
	s.agentCtx = &agentcore.AgentContext{SystemPrompt: sysPrompt, Messages: msgs, Tools: s.agentCtx.Tools}
	s.persisted = len(msgs)
	s.compacted = false
	s.curLeaf = ""
	if len(entries) > 0 {
		s.curLeaf = entries[len(entries)-1].ID
	}
	// A side thread branched from another conversation is meaningless now.
	s.lastBtw = nil
	s.lastBtwBase = 0
	s.hookDeps.SessionID = h.ID
	return msgs, nil
}

// importSession loads a JSONL export as a fresh session and adopts it, so the
// next prompt continues the imported conversation.
func (s *runSession) importSession(path string) ([]agentcore.Message, error) {
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	h, entries, err := s.store.Import(path, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return s.adopt(h, entries, false)
}

// cloneSession duplicates the current conversation at its active leaf into a
// fresh independent session and adopts it.
func (s *runSession) cloneSession() ([]agentcore.Message, error) {
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	_, entries, err := s.store.LoadEntries(s.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("nothing to clone yet — send a message first")
	}
	leafID := s.curLeaf
	if leafID == "" {
		leafID = entries[len(entries)-1].ID
	}
	h, path, err := s.store.Fork(s.header.ID, leafID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return s.adopt(h, path, false)
}

// forkCandidates lists the historical user messages (entry order), one line
// each, for the /fork picker. Index n in the listing maps to the n-th user
// message in forkAt.
func (s *runSession) forkCandidates() ([]string, error) {
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	_, entries, err := s.store.LoadEntries(s.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if u, ok := e.Message.(agentcore.UserMessage); ok {
			out = append(out, ui.OneLine(agentcore.ContentToText(u.Content)))
		}
	}
	return out, nil
}

// forkAt branches from BEFORE the n-th historical user message (1-based, as
// listed by forkCandidates) and adopts the new branch: it holds everything up to
// but excluding that message, so the user re-prompts from there on.
func (s *runSession) forkAt(n int) ([]agentcore.Message, error) {
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	_, entries, err := s.store.LoadEntries(s.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var targets []session.Entry
	for _, e := range entries {
		if _, ok := e.Message.(agentcore.UserMessage); ok {
			targets = append(targets, e)
		}
	}
	if n < 1 || n > len(targets) {
		return nil, fmt.Errorf("invalid selection %d (want 1..%d)", n, len(targets))
	}
	h, path, err := s.store.Fork(s.header.ID, targets[n-1].ParentID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return s.adopt(h, path, false)
}

// treeLines returns the session's branch tree (render order) after persisting
// the live turn, so the tree reflects unsaved messages.
func (s *runSession) treeLines() ([]session.TreeLine, error) {
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	_, entries, err := s.store.LoadEntries(s.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return session.RenderTreeLines(entries, s.curLeaf), nil
}

// switchBranchIndex switches the active branch to the n-th tree node (1-based,
// as listed by treeLines) and rebuilds the context from its root→leaf path.
func (s *runSession) switchBranchIndex(n int) ([]agentcore.Message, error) {
	if err := s.persist(); err != nil {
		return nil, fmt.Errorf("save current session: %w", err)
	}
	_, entries, err := s.store.LoadEntries(s.header.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lines := session.RenderTreeLines(entries, s.curLeaf)
	if n < 1 || n > len(lines) {
		return nil, fmt.Errorf("invalid selection %d (want 1..%d)", n, len(lines))
	}
	target := lines[n-1].Entry
	path := session.PathToLeaf(entries, target.ID)
	return s.adopt(s.header, path, false)
}
