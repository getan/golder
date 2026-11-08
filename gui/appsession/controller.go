package appsession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/headless"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/contextbudget"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/hooks"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/memory"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
	"github.com/getan/golder/internal/session"
	"github.com/getan/golder/internal/trust"
)

// ErrBusy is returned by commands that cannot run while an agent turn is in
// flight.
var ErrBusy = errors.New("a turn is already running")

// ErrClosed is returned once Close has been called.
var ErrClosed = errors.New("controller is closed")

// ErrReadOnly is returned when a second frontend views a session whose writer
// lease is held by another live process.
var ErrReadOnly = errors.New("session is open read-only in this process")

// ProjectSetup is the live provider state a project environment factory needs.
// APIKey is launch-only secret material and must not be displayed or persisted.
type ProjectSetup struct {
	Model        string
	ProviderName string
	BaseURL      string
	Protocol     string
	APIKey       string
}

// ProjectEnvironmentFactory assembles the project-rooted tools, prompt,
// extensions and memory resources for one explicit desktop project folder.
type ProjectEnvironmentFactory func(ctx context.Context, cwd string, setup ProjectSetup) (run.Env, error)

// Options is the assembled run environment the GUI controller starts from. The
// command builds it with run.SetupEnv, exactly as the CLI/TUI do, so the GUI
// shares provider resolution, tool construction, skills and the system prompt.
type Options struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	APIKey       string
	Protocol     string
	// Credentials is the shared resolver assembled by run.SetupEnv. APIKey is a
	// fallback for embedders that construct Options directly.
	Credentials *provider.CredentialStore
	Version     string

	ThinkingLevel agentcore.ThinkingLevel
	Tools         []agentcore.AgentTool
	SysPrompt     string
	Prompt        runtime.PromptInputs

	// ResumeID loads an existing session instead of creating one.
	ResumeID string
	// Approve grants the launch directory session trust up front.
	Approve bool

	Skills        []*runtime.Skill
	Extensions    *extensions.Manager
	ConfigPrompts []string
	CliPrompts    []string
	// NoPromptTemplates disables prompt-template discovery.
	NoPromptTemplates bool

	Permissions *permissions.State
	ReviewNotes *run.ReviewNotes
	Budget      *contextbudget.State
	MaxContext  config.MaxContext
	Memory      *memory.Store

	// ProjectFactory is required to create or resume a session in a folder other
	// than the launch directory. CLI/TUI embedders may leave it nil.
	ProjectFactory ProjectEnvironmentFactory
	// GUI omits commands specific to the separate tuios frontend.
	GUI bool

	// Cwd is the project directory. Empty means the process working directory.
	Cwd string
	// Store overrides the shared session store (tests).
	Store *session.Store
	// Warn receives non-fatal setup warnings; nil discards them.
	Warn io.Writer
}

// Controller owns one conversation and the run in flight, if any.
type Controller struct {
	mu  sync.Mutex
	opt Options

	store    *session.Store
	header   session.SessionHeader
	agentCtx *agentcore.AgentContext
	live     *cli.LiveConfig
	reg      *agenttool.ToolRegistry
	remind   *runtime.ReminderRegistry
	schedule *agenttool.Schedule
	snap     *agenttool.FileSnapshotRecorder
	creds    *provider.CredentialStore
	slash    *runtime.SlashRegistry
	perms    *permissions.State
	notes    *run.ReviewNotes
	trust    *trust.Manager
	budget   *contextbudget.State
	memory   *memory.Store
	factory  ProjectEnvironmentFactory

	dispatcher *hooks.Dispatcher
	hookDeps   run.HookDeps

	cwd    string
	prompt runtime.PromptInputs
	// approvedCwd is the one launch folder --approve covers. Switching projects
	// must not silently transfer that trust decision.
	approvedCwd string

	curLeaf   string
	persisted int
	compacted bool

	items           []Item
	index           map[string]int
	lock            *sessionLock
	readOnly        bool
	writerInfo      session.WriterInfo
	seq             uint64
	events          chan Event
	done            chan struct{}
	closed          bool
	computerTool    *agenttool.ComputerTool
	computerRequest *agenttool.ComputerRequestTool
	guiTerminalTool agentcore.AgentTool

	pending      []*pendingApproval
	nextApproval int
	answered     map[string]bool

	// approvedForSession / deniedForSession remember "allow for this session"
	// answers, keyed by tool plus summary, so the same shape of call is not asked
	// twice.
	approvedForSession map[string]bool
	deniedForSession   map[string]bool

	// preTurnLeaf and preTurnLabel capture the branch cursor and prompt before a
	// turn advances them.
	preTurnLeaf  string
	preTurnLabel string

	running              bool
	privateAuthorization bool
	extensionMutation    bool
	runID                string
	cancel               context.CancelFunc
	runWG                sync.WaitGroup
	steerQueue           []queuedPrompt
	steerDelivered       []queuedPrompt
	steerSequence        uint64
	steerHandoff         bool

	status Status
	title  string
}

// New creates a controller. Open must be called before any other command.
func New(opts Options) *Controller {
	return &Controller{
		opt:                opts,
		index:              map[string]int{},
		answered:           map[string]bool{},
		approvedForSession: map[string]bool{},
		deniedForSession:   map[string]bool{},
		events:             make(chan Event, 1024),
		done:               make(chan struct{}),
	}
}

// Events returns the ordered event stream. The channel is never closed; Close
// ends delivery and unblocks the producer.
func (c *Controller) Events() <-chan Event { return c.events }

// Done closes when this controller is closed, including a background session.
func (c *Controller) Done() <-chan struct{} { return c.done }

// TerminalEligible reports whether a live GUI controller can receive a
// terminal capability. It does not itself grant access to any pane.
func (c *Controller) TerminalEligible() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opt.GUI && !c.closed && !c.readOnly && c.agentCtx != nil && len(c.opt.Tools) != 0
}

// Open resolves the session (resume or fresh), assembles the live configuration
// and wires the slash commands and hooks. It is idempotent only in the sense
// that a second call reports an error instead of replacing a live session.
func (c *Controller) Open(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.store != nil {
		c.mu.Unlock()
		return errors.New("controller is already open")
	}
	c.mu.Unlock()

	store := c.opt.Store
	if store == nil {
		var err error
		store, err = headless.SessionStore()
		if err != nil {
			return err
		}
	}
	cwd := strings.TrimSpace(c.opt.Cwd)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if err := ensureInitialProject(cwd); err != nil {
		c.warnf("project list unavailable: %v", err)
	}

	creds := c.opt.Credentials
	if creds == nil {
		creds = provider.NewCredentialStore(nil)
		creds.SetOverride(c.opt.ProviderName, c.opt.APIKey)
	}

	now := time.Now().UTC()
	var (
		agentCtx *agentcore.AgentContext
		header   session.SessionHeader
		history  []agentcore.Message
		curLeaf  string
	)
	if c.opt.ResumeID != "" {
		h, entries, err := store.LoadEntries(c.opt.ResumeID)
		if err != nil {
			return err
		}
		msgs := make(agentcore.MessageList, len(entries))
		for i, e := range entries {
			msgs[i] = e.Message
		}
		if len(entries) > 0 {
			curLeaf = entries[len(entries)-1].ID
		}
		header = h
		agentCtx = &agentcore.AgentContext{
			SystemPrompt: cli.ResumeSystemPrompt(h, c.opt.Prompt),
			Messages:     msgs,
			Tools:        c.opt.Tools,
		}
		history = msgs
	} else {
		agentCtx = &agentcore.AgentContext{SystemPrompt: c.opt.SysPrompt, Tools: c.opt.Tools}
		header = session.SessionHeader{
			ID:                 session.NewID(now),
			CreatedAt:          now,
			UpdatedAt:          now,
			Model:              c.opt.Model,
			Provider:           c.opt.ProviderName,
			SystemPrompt:       c.opt.SysPrompt,
			BaseInstruction:    c.opt.Prompt.Base,
			AppendInstructions: c.opt.Prompt.Appends,
			Cwd:                cwd,
		}
	}

	live := &cli.LiveConfig{
		Model:         c.opt.Model,
		ProviderName:  c.opt.ProviderName,
		Provider:      c.opt.Provider,
		BaseURL:       c.opt.BaseURL,
		Protocol:      c.opt.Protocol,
		ThinkingLevel: c.opt.ThinkingLevel,
		ContextWindow: cli.ResolveContextWindow(c.opt.ProviderName, c.opt.Model),
		MaxContext:    c.opt.MaxContext,
	}

	mgr, mgrErr := trust.NewManager(trust.DefaultPath())
	if mgrErr != nil {
		c.warnf("trust store unavailable, trust disabled: %v", mgrErr)
		mgr = nil
	}
	if cwd == "" {
		mgr = nil
	}

	slash, err := prompts.BuildSlashRegistry(live, creds, c.opt.Skills, prompts.PromptTemplateSources{
		Extensions:     c.opt.Extensions,
		Settings:       c.opt.ConfigPrompts,
		CLI:            c.opt.CliPrompts,
		Disable:        c.opt.NoPromptTemplates,
		ProjectDir:     projectPromptDir(cwd),
		ProjectTrusted: mgr != nil && mgr.IsTrusted(cwd),
	})
	if err != nil {
		c.warnf("slash-commands: %v", err)
		slash = runtime.NewSlashRegistry()
	}
	trust.RegisterCommand(slash, mgr, cwd)

	perms := c.opt.Permissions
	if perms == nil {
		perms = permissions.New(permissions.Auto)
	}
	notes := c.opt.ReviewNotes
	if notes == nil {
		notes = run.NewReviewNotes()
	}
	budget := c.opt.Budget
	if budget == nil {
		budget = contextbudget.New()
	}
	prompts.RegisterPermissionCommand(slash, perms, func() string {
		return judge.ConversationLanguage(agentCtx.Messages)
	})
	prompts.RegisterBoundaryCommand(slash, func() string {
		return judge.ConversationLanguage(agentCtx.Messages)
	})
	if !c.opt.GUI {
		prompts.RegisterPaneCommand(slash, c.opt.Tools)
	}

	ctrl := &Controller{
		opt:      c.opt,
		store:    store,
		header:   header,
		agentCtx: agentCtx,
		live:     live,
		reg:      run.ToolRegistry(c.opt.Tools),
		remind:   run.TodoReminders(c.opt.Tools),
		schedule: agenttool.ScheduleFromTools(c.opt.Tools),
		snap:     run.SnapshotRecorderFromTools(c.opt.Tools),
		creds:    creds,
		slash:    slash,
		perms:    perms,
		notes:    notes,
		trust:    mgr,
		budget:   budget,
		memory:   c.opt.Memory,
		factory:  c.opt.ProjectFactory,
		cwd:      cwd,
		prompt:   c.opt.Prompt,
		curLeaf:  curLeaf,
	}
	if c.opt.Approve {
		ctrl.approvedCwd = cwd
	}

	// Move the assembled state into the receiver under the lock, keeping the
	// event channel and done signal that callers may already hold.
	lk, readOnly, holder, err := acquireSessionAccess(store.Dir(), header.ID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.store, c.header, c.agentCtx, c.live = ctrl.store, ctrl.header, ctrl.agentCtx, ctrl.live
	c.reg, c.remind, c.schedule, c.snap = ctrl.reg, ctrl.remind, ctrl.schedule, ctrl.snap
	c.creds, c.slash, c.perms, c.notes = ctrl.creds, ctrl.slash, ctrl.perms, ctrl.notes
	c.trust, c.budget, c.memory, c.factory = ctrl.trust, ctrl.budget, ctrl.memory, ctrl.factory
	c.cwd, c.prompt = ctrl.cwd, ctrl.prompt
	c.approvedCwd = ctrl.approvedCwd
	c.curLeaf = ctrl.curLeaf
	c.lock = lk
	c.readOnly, c.writerInfo = readOnly, holder
	c.persisted = len(history)
	c.hookDeps = run.HookDeps{SessionID: header.ID, ProjectDir: cwd, WarnLog: c.warnWriter()}
	c.mu.Unlock()

	// seed takes the lock itself (to publish the derived title and context
	// estimate), so it runs after the assignment block releases it.
	c.seed(history)
	c.mu.Lock()
	if strings.TrimSpace(header.DisplayName) != "" {
		c.title = header.DisplayName
	}
	c.status = Status{
		Model:        live.Model,
		Provider:     live.ProviderName,
		Thinking:     live.ThinkingLevel,
		Permission:   perms.Mode().String(),
		SessionID:    header.ID,
		Title:        c.title,
		ContextUsed:  estimateTokens(history),
		ContextLimit: live.ContextWindow,
		ReadOnly:     readOnly,
		Writer:       writerView(holder),
	}
	c.mu.Unlock()

	c.installComputerRequest()
	if !readOnly {
		if err := agenttool.ConfigurePaneAgent(ctx, c.opt.Tools, header.ID); err != nil {
			c.warnf("pane control registration unavailable: %v", err)
		}
		c.wireHooks(ctx, c.opt.ResumeID != "")
	}
	c.emitStatus()
	return nil
}

func projectPromptDir(cwd string) string {
	if cwd == "" {
		return ""
	}
	return cwd + string(os.PathSeparator) + ".golder" + string(os.PathSeparator) + "prompts"
}

// wireHooks resolves the trust-gated hook set and dispatches SessionStart once.
func (c *Controller) wireHooks(ctx context.Context, resumed bool) {
	c.mu.Lock()
	trusted := c.trust == nil || c.trust.IsTrusted(c.cwd) || c.approvedCwd == c.cwd
	deps := c.hookDeps
	c.mu.Unlock()
	if !trusted && c.trust != nil && !c.trust.NearestTrustDecision(c.cwd).Found {
		// An undecided directory waits for the trust answer; hooks load then.
		return
	}
	set, err := run.ResolveHookSet(deps.ProjectDir, trusted)
	if err != nil {
		c.warnf("hooks disabled: %v", err)
		return
	}
	d := run.BuildDispatcher(set, deps)
	if d == nil {
		return
	}
	source := "startup"
	if resumed {
		source = "resume"
	}
	c.mu.Lock()
	c.dispatcher = d
	cfg := runtime.RunConfig{Reminders: c.remind}
	c.mu.Unlock()
	run.DispatchSessionStart(ctx, d, &cfg, deps, source)
	c.mu.Lock()
	c.remind = cfg.Reminders
	c.mu.Unlock()
}

// Close ends the controller: a run in flight is cancelled and every pending
// approval is answered as unanswered, which the gates treat as fail-closed.
func (c *Controller) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.cancel
	pending := c.pending
	lock := c.lock
	computer := c.computerTool
	c.lock = nil
	c.pending = nil
	c.mu.Unlock()

	close(c.done)
	if computer != nil {
		computer.Session.Stop()
	}
	if cancel != nil {
		cancel()
	}
	for _, p := range pending {
		select {
		case p.reply <- judge.ApprovalAnswer{}:
		default:
		}
	}
	c.runWG.Wait()
	_ = c.persist()
	if computer != nil {
		_ = computer.Session.Close()
	}
	lock.release()
	agenttool.ClosePaneAgent(c.opt.Tools)
	run.KillShellSessions(c.opt.Tools)
	if c.opt.Extensions != nil {
		_ = c.opt.Extensions.Close()
	}
	if c.memory != nil {
		_ = c.memory.Close()
	}
	return nil
}

// Snapshot returns the complete current state.
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := Snapshot{
		Status:  c.statusWithQueueLocked(),
		Project: c.cwd,
		Seq:     c.seq,
		Items:   make([]Item, len(c.items)),
		Pending: make([]ApprovalView, 0, len(c.pending)),
	}
	for i, it := range c.items {
		snap.Items[i] = cloneItem(it)
	}
	for _, p := range c.pending {
		snap.Pending = append(snap.Pending, p.view)
	}
	return snap
}

// Project returns the project directory.
func (c *Controller) Project() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwd
}

// Version returns the desktop/core build version supplied by the command.
func (c *Controller) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.opt.Version) == "" {
		return "dev"
	}
	return c.opt.Version
}

// emit assigns the next sequence number and delivers one event. It is safe to
// call with or without the lock held; callers should release the lock first so
// a slow reader cannot block state changes.
func (c *Controller) emit(kind EventKind, build func(*Event)) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.seq++
	ev := Event{Seq: c.seq, Session: c.header.ID, Kind: kind}
	if build != nil {
		build(&ev)
	}
	c.mu.Unlock()
	select {
	case c.events <- ev:
	case <-c.done:
	}
}

func (c *Controller) emitStatus() {
	c.emit(EventStatus, func(ev *Event) {
		st := c.statusWithQueueLocked()
		st.Permission = c.perms.Mode().String()
		st.Model = c.live.Model
		st.Provider = c.live.ProviderName
		st.Thinking = c.live.ThinkingLevel
		ev.Status = &st
	})
}

func (c *Controller) setError(err error) {
	c.mu.Lock()
	if err != nil {
		c.status.Error = err.Error()
	} else {
		c.status.Error = ""
	}
	c.mu.Unlock()
}

func (c *Controller) warnf(format string, args ...any) {
	if c.opt.Warn == nil {
		return
	}
	fmt.Fprintf(c.opt.Warn, "golder-gui: "+format+"\n", args...)
}

func (c *Controller) warnWriter() io.Writer {
	if c.opt.Warn != nil {
		return c.opt.Warn
	}
	return io.Discard
}
