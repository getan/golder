package tui

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/dreamcmd"
	goalpkg "github.com/getan/golder/internal/cli/goal"
	"github.com/getan/golder/internal/cli/memstatus"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/cli/status"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/history"
	"github.com/getan/golder/internal/memory"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// Model is the root Bubble Tea model for the full-screen TUI. It composes a
// scrolling transcript (US-005) with the persistent status bar (#386) and a
// minimal input line, and owns the run lifecycle: on prompt submit it starts an
// agent run through the event bridge (bridge.go) and pumps the resulting tea.Msg
// stream into the transcript one message at a time. Downstream nodes grow the
// input into a full textarea (#390), render tool cards (#389), and wire the real
// session/run assembly (#392); the Init/Update/View contract and the alt-screen
// + quit-key handling stay stable.
type Model struct {
	opts  Options
	theme Theme

	// width and height track the terminal size reported by tea.WindowSizeMsg.
	// They are zero until the first size message arrives; View degrades to a
	// minimal render in that window.
	width  int
	height int

	// transcript is the scrolling message log (user / assistant / system turns).
	transcript transcript

	// input is the multi-line prompt editor (#390). It wraps a bubbles textarea
	// so CJK / emoji are edited by rune (no dropped-byte bug), Enter submits and
	// Shift+Enter inserts a newline. It is blurred while a run is in flight.
	input input

	// history holds previously submitted inputs (prompts and slash commands, in
	// order), and histIdx is the browse cursor into it: len(history) means "not
	// browsing — on the live draft", any smaller index points at a recalled entry.
	// histDraft stashes the in-progress buffer when browsing begins so ↓ past the
	// newest entry restores it. ↑/↓ walk history when the caret is on the first /
	// last line of the composer, so multi-line editing is unaffected.
	history   []string
	histIdx   int
	histDraft string
	// histLoaded marks the one-time lazy merge of the global prompt history
	// (opts.HistoryPath) into history: the first browse/search reads the file,
	// so a session that never browses never touches it. Entries recorded before
	// the load are kept — the disk window is merged in front of them.
	histLoaded bool

	// running is true while an agent run is draining through runCh. Input submit
	// is gated on it so a new run cannot start mid-run.
	running bool
	// runCh is the bridge channel for the in-flight run, or nil when idle. Update
	// re-issues waitForEvent(runCh) after every bridged msg except runEndMsg.
	runCh chan tea.Msg

	// startRunFn launches an agent run for the submitted prompt, returning the
	// bridge channel and the first waitForEvent Cmd (see bridge.startRun). It is
	// bound to runSession.startRun by withSession (#392): the real binding
	// constructs an AgentContext + RunConfig from opts and the live session. It is
	// nil for a session-less model (the pure constructor / tests), in which case a
	// submit records the prompt but starts no run.
	startRunFn func(prompt string) (chan tea.Msg, tea.Cmd)

	// session is the assembled run/persistence state (store, header, growing
	// context, live config). It is nil for a session-less model; when set, the
	// model persists the conversation to ~/.golder/sessions after each turn ends.
	session *runSession

	// interruptFn cancels the in-flight run (the first stage of the two-stage
	// interrupt, FR-14): pressing Esc / Ctrl+C while running signals the run to
	// stop rather than quitting the program. It is a seam wired alongside
	// startRunFn by session assembly (#392) — typically the run ctx's cancel
	// func. Until then it may be nil, in which case an interrupt while running is
	// a safe no-op (the pump keeps draining until it ends on its own).
	interruptFn func()

	// quitting is set when a quit key (Ctrl+C / Ctrl+D) is seen, so View can be a
	// no-op on the final frame while the program tears down and restores the
	// terminal.
	quitting bool

	// quitArmedAt marks when the first idle Ctrl+C/Esc armed a quit: a second
	// press within quitArmWindow quits, otherwise the arm expires and the next
	// press re-arms. Zero = disarmed. A single idle press must never quit —
	// too easy to fat-finger away a session.
	quitArmedAt time.Time

	// sideRun marks the in-flight run as a /btw side question: on run end the
	// transcript says the side thread closed and nothing is persisted.
	sideRun bool
	// goalRun marks the in-flight run as an autonomous /goal run: on run end the
	// goal outcome is reported and the turn is persisted like a normal turn.
	goalRun bool
	// pendingTrust is set by withSession when the launch directory has no saved
	// trust decision and --approve did not grant session trust. Init turns it
	// into the first-run trust picker (codex-style "do you trust this folder").
	pendingTrust bool
	// approval is the tool-call approval dialog (approval.go). While active it
	// owns every key press, so a pending decision cannot be dismissed by
	// typing.
	approval approvalDialog

	// statusBar renders the persistent bottom line (#386, US-003). It is fed the
	// terminal width, telemetry-derived context usage, and the async git probe
	// result; View renders it just above the input line.
	statusBar statusBar

	// cwd is the launch directory, captured once at construction and reused for
	// the git probe and the status bar's path display.
	cwd string

	// slash is the shared slash-command registry (#383) the TUI consults exactly
	// as the REPL does: /model, /help, user templates, plugin commands and skills.
	// It is bound to live so a /model switch mutates the same config the run loop
	// reads. Built in NewModel (built-ins + disk templates) and rebuilt in
	// withSession against the session's live config.
	slash *runtime.SlashRegistry
	// live is the mutable run configuration the /model command switches. In a
	// session-bound model it is the SAME pointer the run loop reads (set by
	// withSession), so a switch takes effect on the next turn.
	live *cli.LiveConfig
	// menu is the autocomplete popup shown while a "/name" is being typed (#391).
	// It filters slash by the typed prefix; the model intercepts arrow/Tab/Enter
	// keys to drive it before delegating to the textarea.
	menu slashMenu

	// toolCards indexes the rich tool-call cards (#389, US-006) by tool-call id so
	// a toolEndMsg can locate the card started earlier and flip its state / attach
	// the parsed response. Each card is also appended to the transcript as an
	// ordered block (by pointer), so mutating one here re-renders it inline on the
	// next reflow.
	toolCards map[string]*toolCard
	// lastToolCard points at the most recently started card; Ctrl+T toggles its
	// expanded state and re-flows the transcript.
	lastToolCard *toolCard
	// pendingCardClick remembers a left press that landed on a tool card. If
	// the button is released on the same cell without dragging, the click
	// toggles that card's expanded state — the mouse affordance that lets any
	// card (not just the newest, which Ctrl+T handles) be expanded. Dragging or
	// releasing elsewhere cancels it so text selection keeps working.
	pendingCardClick     *toolCard
	pendingCardClickCell point

	// draggingScrollbar is set while the left mouse button is held after pressing
	// on the transcript scrollbar column, so subsequent motion events drag the
	// thumb (and scroll the viewport) until the button is released.
	draggingScrollbar bool

	// sel is the current mouse text selection over the rendered shell (screen
	// cells). A left-press off the scrollbar starts it, drag extends it, and it
	// persists after release so Ctrl+C can copy the highlighted text.
	sel selection

	// selDragging is true between a left press (plain or shift) and its release.
	// Only motion while dragging extends the selection: after release the cursor
	// stays put so hover movement never collapses a finished selection out from
	// under a follow-up shift+click.
	selDragging bool

	// selScrollDir is the edge-autoscroll direction while a text-selection drag
	// is pinned at the transcript's top (-1) or bottom (+1) edge, 0 when off.
	// Terminals clamp drag coordinates to the window, so without this the
	// selection could never extend past the visible page; each selScrollTickMsg
	// scrolls a few lines under the edge-pinned cursor until release or the end.
	selScrollDir int
	// selScrollGen invalidates stale autoscroll ticks: every fresh press or
	// release bumps it, and ticks carrying an older gen are dropped.
	selScrollGen int

	// spinner is the animated "working" indicator (verb + elapsed/token/effort
	// stats) shown on the row above the input while a run is in flight.
	spinner spinner

	// logoRunning is true while ticks keep advancing the startup wordmark's
	// entrance.
	// Only a fresh session (no resumed history) arms it: a resumed banner sits
	// scrolled above the fold, so animating it would reflow the whole history
	// for frames nobody sees. The flag is released once the entrance has
	// played, after which the banner is a static history cell.
	logoRunning bool

	// subagents is the ordered set of live sub-agents dispatched by the `task`
	// tool (SPEC 4.4, US-006). A toolStartMsg with name=="task" adds a row (and
	// records its start time), subagentProgressMsg refreshes activity/tokens, and
	// the task's toolEndMsg removes it. View renders it as a multi-line panel just
	// above the spinner; it contributes zero rows when empty.
	subagents subagentPanel

	// pastes stores the full text of collapsed multi-line pastes, keyed by the id
	// shown in the "[Pasted text #N +M lines]" placeholder left in the composer.
	// submit expands the placeholders back to their content before sending, so a
	// large paste never floods the editor (mirroring Claude Code).
	pastes map[int]string
	// pasteSeq is the monotonic counter behind the paste placeholder ids. It keeps
	// climbing across submits so ids stay unique for the session.
	pasteSeq int

	// images maps the id shown in an "[Image #N]" placeholder to the temp PNG a
	// Ctrl+V / Cmd+V image paste was saved to. submit expands the placeholder to an
	// "@image:<path>" reference so BuildUserContent attaches the image as
	// multimodal content (mirroring Claude Code's image paste).
	images map[int]string
	// imageSeq is the monotonic counter behind the image placeholder ids.
	imageSeq int
}

// NewModel builds the root model from the assembled Options. It reads the
// current working directory (for the status bar's path display and git probe)
// and assembles the shared slash-command registry (#391), which reads the user
// prompt-template dirs (~/.golder/{commands,prompts}) and the pre-loaded skills;
// missing dirs are not an error. The registry is bound here to a live config
// derived from Options; withSession rebinds it to the session's live config so a
// /model switch reaches the run loop.
func NewModel(opts Options) Model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	theme := DefaultTheme()
	live := &cli.LiveConfig{
		Model:         opts.Model,
		ProviderName:  opts.ProviderName,
		Provider:      opts.Provider,
		BaseURL:       opts.BaseURL,
		Protocol:      opts.Protocol,
		ThinkingLevel: opts.ThinkingLevel,
		ContextWindow: cli.DefaultContextWindow,
	}
	return Model{
		opts:       opts,
		theme:      theme,
		transcript: newTranscript(theme),
		input:      newInput(),
		cwd:        cwd,
		statusBar:  newStatusBar(theme, opts, cwd),
		toolCards:  make(map[string]*toolCard),
		slash:      newSlashRegistry(opts, live),
		live:       live,
		menu:       newSlashMenu(theme),
		spinner:    newSpinner(theme),
		pastes:     make(map[int]string),
		images:     make(map[int]string),
	}
}

// withSession binds the assembled run session to the model: it wires the real
// run seam (startRunFn) and, for a resumed session, replays the prior history
// into the transcript so the user sees the conversation so far before entering
// interactive mode. Run calls it right after NewModel; the session-less
// constructor path (tests, pure construction) leaves startRunFn nil.
func (m Model) withSession(s *runSession, history []agentcore.Message) Model {
	m.session = s
	m.startRunFn = s.startRun
	m.interruptFn = s.interrupt
	// Rebind the registry to the session's own one (assembled against s.live, the
	// very config the run loop reads via buildConfig) so /model mutates the live
	// config, /trust reaches the session's trust manager (registered in
	// newRunSessionWithStore), and /status can list skill/plugin/user commands.
	m.live = s.live
	m.slash = s.slash
	// A fresh session starts with the wordmark blank so it can type itself in;
	// a resumed one shows the settled word (its banner is history nobody is
	// watching) by starting at the resting frame.
	startFrame := 0
	if len(history) > 0 {
		startFrame = logoFrames
	}
	m.transcript.addBanner(renderBannerFrame(m.theme, m.opts, m.cwd, startFrame))
	m.logoRunning = len(history) == 0
	seedTranscript(&m.transcript, history)
	// First-run trust dialog (parity with the REPL and codex): an undecided
	// launch directory gets asked before the first prompt. The picker opens in
	// Init (tests that drive Update directly stay prompt-free).
	m.pendingTrust = s.trust != nil && !m.opts.Approve &&
		!s.trust.NearestTrustDecision(m.cwd).Found
	return m
}

// resumeTitle renders a session's list title: its content preview, or the
// model when the session has no text yet.
func resumeTitle(it cli.ResumeItem) string {
	if it.Preview != "" {
		return it.Preview
	}
	if it.Header.Model != "" {
		return it.Header.Model
	}
	return "(empty session)"
}

// resumeMeta renders the second list line: model and update time.
func resumeMeta(it cli.ResumeItem) string {
	model := it.Header.Model
	if model == "" {
		model = "?"
	}
	return fmt.Sprintf("%s · %s", model, it.Header.UpdatedAt.Format("01-02 15:04"))
}

// modelsFetchedMsg carries the async live-catalog fetch for the /model
// picker: opening the picker must not block the tea loop on the network.
type modelsFetchedMsg struct {
	ids []string
	err error
}

// openModelPicker shows the interactive model picker for the live provider.
// A cached catalog opens synchronously; otherwise the fetch runs off-loop and
// the picker opens on modelsFetchedMsg.
func (m Model) openModelPicker() (tea.Model, tea.Cmd) {
	if m.session == nil || m.live == nil {
		m.transcript.addSystem("No active session.")
		return m, nil
	}
	if ids := prompts.CachedModelCatalog(m.live); len(ids) > 0 {
		m.menu.openPickerDetailed(modelPickItems(m.live.ProviderName, ids), m.live.Model, "model")
		m.transcript.addSystem("Select a model (↑↓ + Enter, Esc cancels):")
		m.relayout()
		return m, nil
	}
	m.transcript.addSystem("Fetching models…")
	// The reasoning catalog needs no fetch here: startup already refreshes it
	// off the hot path, and the lookups below read the disk cache lazily, so
	// the level stage gets real levels whenever a cache exists (from this or a
	// previous session) and the family fallback otherwise.
	return m, func() tea.Msg {
		ids, err := prompts.EnsureModelCatalog(m.live, m.session.creds)
		return modelsFetchedMsg{ids: ids, err: err}
	}
}

// applyModelsFetched opens the picker once the async catalog fetch lands.
func (m Model) applyModelsFetched(msg modelsFetchedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.transcript.addSystem(fmt.Sprintf("model list unavailable: %v\nswitch directly with /model <id>", msg.err))
		return m, nil
	}
	if len(msg.ids) == 0 {
		m.transcript.addSystem("model list unavailable: endpoint returned no models")
		return m, nil
	}
	current := ""
	if m.live != nil {
		current = m.live.Model
	}
	m.menu.openPickerDetailed(modelPickItems(m.live.ProviderName, msg.ids), current, "model")
	m.transcript.addSystem("Select a model (↑↓ + Enter, Esc cancels):")
	m.relayout()
	return m, nil
}

// modelPickItems builds the stage-1 model rows, annotating each id with the
// reasoning levels models.dev advertises for it (only when known, so the
// fallback ladder never masquerades as catalog data).
func modelPickItems(providerName string, ids []string) []pickItem {
	items := make([]pickItem, 0, len(ids))
	for _, id := range ids {
		it := pickItem{Title: id, Value: id}
		if lv := provider.KnownReasoningLevels(providerName, id); len(lv) > 0 {
			it.Detail = "reasoning: " + joinLevels(lv)
		}
		items = append(items, it)
	}
	return items
}

// joinLevels renders reasoning levels as "low|high|max".
func joinLevels(levels []agentcore.ThinkingLevel) string {
	parts := make([]string, len(levels))
	for i, l := range levels {
		parts[i] = string(l)
	}
	return strings.Join(parts, "|")
}

// openProviderPicker shows the interactive provider picker: each row names the
// provider, the environment variable(s) it reads, and whether a credential is
// found (names only, never values). Confirming re-runs /provider <name>, so the
// switch logic (default model, endpoint override clearing, catalog reset) stays
// in the shared registry action.
func (m Model) openProviderPicker() (tea.Model, tea.Cmd) {
	if m.session == nil {
		m.transcript.addSystem("No active session.")
		return m, nil
	}
	// Providers with a credential found come first so the ready-to-use ones are
	// quick to pick (same ordering as the /provider text listing).
	specs := prompts.ProvidersAvailableFirst(m.session.creds)
	picks := make([]pickItem, 0, len(specs))
	for _, spec := range specs {
		envs := "no key needed"
		if len(spec.EnvVars) > 0 {
			envs = strings.Join(spec.EnvVars, " / ")
		}
		summary := prompts.ProviderEndpointSummary(m.live, spec)
		picks = append(picks, pickItem{
			Title:  spec.Name,
			Detail: envs + " · " + prompts.CredentialSummary(m.session.creds, spec.Name) + " · " + strings.Join(provider.BaseURLEnvVars(spec), " / "),
			Value:  spec.Name,
			Info:   strings.Split(summary, " · "),
		})
	}
	m.menu.openPickerDetailed(picks, m.live.ProviderName, "provider")
	m.transcript.addSystem("Select a provider (↑↓ + Enter, Esc cancels):")
	m.relayout()
	return m, nil
}

// openProxyPicker persists each toggle immediately. Staying in the picker lets
// users select several providers without retyping the command.
func (m Model) openProxyPicker(focus string) (tea.Model, tea.Cmd) {
	return m.reopenProxyPicker(focus, true)
}

// reopenProxyPicker rebuilds the proxy picker after a toggle. The usage hint
// is transcript history, so it is announced only on the initial open;
// re-announcing on every toggle would stack a duplicate hint per Enter.
func (m Model) reopenProxyPicker(focus string, announce bool) (tea.Model, tea.Cmd) {
	cfg, err := provider.ProxySettings()
	if err != nil {
		m.transcript.addSystem("proxy: " + err.Error())
		return m, nil
	}
	address := provider.DisplayURL(provider.ProxyURL())
	if address == "" {
		address = "not configured"
	}
	picks := []pickItem{{Title: "Proxy address", Detail: address + " · Enter to edit", Value: "url"}}
	selected := 0
	for _, spec := range prompts.ProxyProvidersSelectedFirst(cfg.Providers) {
		mark, action := "[ ] ", " on"
		if slices.Contains(cfg.Providers, spec.Name) {
			mark, action = "[x] ", " off"
		}
		if spec.Name == focus {
			selected = len(picks)
		}
		picks = append(picks, pickItem{
			Title: mark + spec.Name, Value: spec.Name + action,
			Detail: provider.ProxyStatus(spec.Name, spec.DefaultBaseURL),
		})
	}
	m.menu.openPickerDetailed(picks, "", "proxy")
	m.menu.selected = selected
	if announce {
		m.transcript.addSystem("Proxy: ↑↓ + Enter toggles and saves; Esc closes. Unchecked providers connect directly.")
	}
	m.relayout()
	return m, nil
}

// openTrustPicker asks the first-run trust question through the approval
// dialog, so the launch prompt and a mid-run approval are one UI (the REPL's
// equivalent is the stdin prompt in internal/trust).
func (m Model) openTrustPicker() (tea.Model, tea.Cmd) {
	if m.session == nil || m.session.trust == nil {
		return m, nil
	}
	m.openTrustApproval()
	m.relayout()
	return m, nil
}

// openPermissionsPicker shows the four approval modes with their descriptions
// (the codex permissions-preset parity), marking the active one. The picker is
// intentionally English-only — the canonical mode names and short preset
// descriptions read the same for every user — while the verdict notes and the
// slash-command status keep following the conversation language.
func (m Model) openPermissionsPicker() (tea.Model, tea.Cmd) {
	if m.session == nil {
		m.transcript.addSystem("No active session.")
		return m, nil
	}
	current := permissions.Auto.String()
	if m.session.perms != nil {
		current = m.session.perms.Mode().String()
	}
	modes := []permissions.Mode{permissions.ReadOnly, permissions.Ask, permissions.Auto, permissions.FullAccess}
	picks := make([]pickItem, 0, len(modes))
	for _, mode := range modes {
		picks = append(picks, pickItem{Title: mode.Label("en"), Detail: mode.Description("en"), Value: mode.String()})
	}
	m.menu.openPickerDetailed(picks, current, "permissions")
	m.transcript.addSystem("Select a permission mode (↑↓ + Enter, Esc cancels):")
	m.relayout()
	return m, nil
}

// resumeSession implements /resume: it swaps the active session without
// leaving the TUI, following runSession.switchTo (persist current, load target,
// live model/provider follow the stored header). The transcript is reset and
// reseeded from the loaded history. Refused while a run is in flight.
func (m Model) resumeSession(id string) (tea.Model, tea.Cmd) {
	if m.running {
		m.transcript.addSystem("Interrupt the current run first, then /resume.")
		return m, nil
	}
	if m.session == nil {
		m.transcript.addSystem("No active session to switch from.")
		return m, nil
	}
	if id == "" {
		items, err := cli.RecentSessionsWithPreview(m.session.store, 10)
		if err != nil {
			m.transcript.addSystem(fmt.Sprintf("resume: list sessions: %v", err))
			return m, nil
		}
		if len(items) == 0 {
			m.transcript.addSystem("No saved sessions yet.")
			return m, nil
		}
		picks := make([]pickItem, 0, len(items))
		for _, it := range items {
			picks = append(picks, pickItem{Title: resumeTitle(it), Detail: resumeMeta(it), Value: it.Header.ID})
		}
		m.menu.openPickerDetailed(picks, m.session.header.ID, "resume")
		m.transcript.addSystem("Select a session (↑↓ + Enter, Esc cancels):")
		m.relayout()
		return m, nil
	}
	if resolved, err := cli.ResolveResumeID(m.session.store, id); err != nil {
		m.transcript.addSystem(fmt.Sprintf("resume: %v", err))
		return m, nil
	} else {
		id = resolved
	}
	msgs, err := m.session.switchTo(id)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("resume: %v", err))
		return m, nil
	}
	m.transcript.reset()
	m.transcript.addBanner(renderBannerFrame(m.theme, m.opts, m.cwd, logoFrames))
	seedTranscript(&m.transcript, msgs)
	m.transcript.addSystem(fmt.Sprintf("Resumed session %s (%s).", id, m.live.Model))
	m.logoRunning = false
	return m, nil
}

// Init implements tea.Model. It kicks off the async git probe so the status bar
// can show the branch/dirty state as soon as it resolves; the alt-screen is
// requested declaratively via the AltScreen field on the View returned by View.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchGitCmd(m.cwd), m.input.Focus(), func() tea.Msg {
		return tea.RequestBackgroundColor()
	}}
	// The wordmark's entrance is armed by withSession before Init runs, so the
	// first tick is scheduled here; the chain stops itself on the final frame.
	if m.logoRunning {
		cmds = append(cmds, m.tickLogo(1))
	}
	// An undecided launch directory asks the first-run trust question before
	// anything else: the message opens the picker on the Update goroutine.
	if m.pendingTrust {
		cmds = append(cmds, func() tea.Msg { return trustPromptMsg{} })
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model. It tracks the terminal size, drives the minimal
// input line, starts runs on submit, and pumps bridged run events into the
// transcript and status bar. It quits on the standard exit keys (Ctrl+C /
// Ctrl+D).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Re-wrapping re-indexes every content line, so a live selection would
		// point at the wrong text: drop it (and any autoscroll loop).
		m.sel = selection{}
		m.selDragging = false
		m.selScrollDir = 0
		m.selScrollGen++
		m.relayout()
		return m, nil

	case gitInfoMsg:
		m.statusBar.SetGit(msg)
		return m, nil

	case trustPromptMsg:
		return m.openTrustPicker()

	case approvalRequestMsg:
		// A tool call is waiting on the user's decision: arm the dialog and
		// keep pumping run events (the run goroutine is blocked on the reply
		// channel, so no further run events arrive until it is answered).
		m.openApproval(msg.req, msg.reply)
		m.relayout()
		return m, nil

	case modelsFetchedMsg:
		return m.applyModelsFetched(msg)

	case tea.BackgroundColorMsg:
		// Feed the terminal's real background to the Markdown renderer so glamour
		// picks a matching light/dark palette WITHOUT issuing its own terminal
		// query (which would leak its reply into the input — see SetMarkdownDark).
		// Re-flow so any already-finalized assistant block re-renders in the right
		// palette.
		SetMarkdownDark(msg.IsDark())
		m.transcript.reflow()
		return m, nil

	case tea.MouseWheelMsg:
		// Mouse-wheel scrolling reaches the transcript viewport whether idle or
		// running, so history stays scrollable with the wheel — not just PgUp/PgDn.
		// The viewport (MouseWheelEnabled by default) turns the wheel event into a
		// scroll; enabling MouseModeCellMotion in View is what makes the terminal
		// deliver these events under the alt-screen at all.
		//
		// A content-anchored selection survives the scroll untouched (endpoints
		// are content lines, so the highlight tracks the text instead of being
		// cleared); only an explicit drag or click moves it. A below-transcript
		// selection keeps the legacy behavior (cleared: its screen rows no longer
		// mean anything). Cross-page extension is the edge-drag's job.
		m.pendingCardClick = nil
		if m.sel.active && !m.sel.below {
			cmd := m.transcript.update(msg)
			return m, cmd
		}
		cmd := m.transcript.update(msg)
		m.sel = selection{}
		return m, cmd

	case tea.MouseClickMsg:
		// A left press on the scrollbar column grabs the thumb (jump + drag). A left
		// press anywhere else begins a text selection at that cell, replacing any
		// prior one; a bare click (no drag) leaves it empty so it clears the old
		// highlight without starting a copyable range. Shift+left-click instead
		// extends the live selection: the anchor stays where the previous press
		// put it and the cursor jumps here, selecting everything in between —
		// across pages, either direction — with no edge aiming.
		if msg.Button == tea.MouseLeft {
			m.pendingCardClick = nil
			if m.onScrollbar(msg.X, msg.Y) {
				m.draggingScrollbar = true
				m.transcript.scrollToRow(msg.Y)
				return m, nil
			}
			// A press on a tool card arms a click-toggle (see pendingCardClick);
			// the release handler fires it only if the cell did not move, so a
			// drag across the card still selects its text.
			if msg.Mod&tea.ModShift == 0 {
				if p, below := m.screenToSel(msg.X, msg.Y); !below {
					if card := m.transcript.toolCardAt(p.y); card != nil {
						m.pendingCardClick = card
						m.pendingCardClickCell = point{msg.X, msg.Y}
					}
				}
			}
			if msg.Mod&tea.ModShift != 0 && m.sel.active {
				click, below := m.dragToSel(msg.X, msg.Y)
				m.sel.cursor = click
				m.sel.below = below
			} else {
				click, below := m.screenToSel(msg.X, msg.Y)
				m.sel = selection{active: true, anchor: click, cursor: click, below: below}
			}
			// Any press orphans autoscroll ticks from an earlier drag, and
			// pauses stick-to-bottom so streamed lines do not yank the content
			// out from under the selection.
			m.selDragging = true
			m.selScrollDir = 0
			m.selScrollGen++
			m.transcript.follow = false
			return m, nil
		}
		return m, nil

	case tea.MouseMotionMsg:
		// While the thumb is grabbed, vertical motion drags it regardless of the
		// cursor's column. Otherwise, motion after a left press extends the text
		// selection to the current cell.
		if m.draggingScrollbar {
			m.transcript.scrollToRow(msg.Y)
			return m, nil
		}
		if m.sel.active && m.selDragging {
			// Motion after the press means a drag, not a click: cancel the
			// card toggle so the gesture becomes a text selection.
			m.pendingCardClick = nil
			m.sel.cursor, m.sel.below = m.dragToSel(msg.X, msg.Y)
			return m, m.updateSelAutoscroll()
		}
		return m, nil

	case tea.MouseReleaseMsg:
		m.draggingScrollbar = false
		m.selDragging = false
		// Release ends edge-autoscroll: the selection persists for Ctrl+C, but
		// no further ticks should move the viewport under it.
		m.selScrollDir = 0
		m.selScrollGen++
		if m.sel.active {
			m.sel.cursor, m.sel.below = m.dragToSel(msg.X, msg.Y)
		}
		// A press that landed on a card and released on the same cell — no
		// drag in between — is a click: toggle the card's expand/collapse.
		if card := m.pendingCardClick; card != nil {
			cell := m.pendingCardClickCell
			m.pendingCardClick = nil
			if msg.X == cell.x && msg.Y == cell.y {
				card.expanded = !card.expanded
				m.transcript.reflow()
				return m, nil
			}
		}
		return m, nil

	case selScrollTickMsg:
		// Stale tick (older drag), released button, or scrolling switched off:
		// drop it so no orphaned loop survives.
		if msg.gen != m.selScrollGen || !m.sel.active || m.selScrollDir == 0 {
			return m, nil
		}
		if m.transcript.viewportHeight() <= 0 {
			m.selScrollDir = 0
			return m, nil
		}
		// Scroll under the content-anchored cursor: endpoints are content lines,
		// so moving the viewport while they stay put extends the highlight
		// across pages; Ctrl+C later copies the content lines in range.
		if !m.transcript.scrollLines(m.selScrollDir * selScrollStep) {
			m.selScrollDir = 0
			return m, nil
		}
		return m, selScrollTick(m.selScrollGen)

	case tea.PasteMsg:
		// Bracketed paste (e.g. Cmd+V / right-click paste): the terminal delivers
		// the whole clipboard payload as one message. A multi-line paste is
		// collapsed to a compact placeholder (expanded at submit); a single-line
		// paste is inserted verbatim. See handlePaste.
		if !m.running {
			return m.handlePaste(msg.Content)
		}
		return m, nil

	case tea.ClipboardMsg:
		// OSC52 clipboard read reply (from tea.ReadClipboard on Ctrl+V / Cmd+V).
		// Route through the same collapse-or-insert path as bracketed paste.
		if !m.running {
			return m.handlePaste(msg.Content)
		}
		return m, nil

	case clipboardImageMsg:
		// Reply to a Ctrl+V / Cmd+V image-read attempt. With an image, drop an
		// "[Image #N]" placeholder (expanded to an @image reference at submit); with
		// none, fall back to a normal OSC52 text read so plain-text paste still works.
		if !m.running {
			if msg.ok {
				return m.handleImagePaste(msg.path)
			}
			return m, tea.ReadClipboard
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case spinnerTickMsg:
		// Advance the working animation and schedule the next frame, but only while
		// a run is in flight; once idle the tick is not re-issued so the spinner
		// stops without a lingering goroutine.
		if !m.running {
			return m, nil
		}
		m.spinner.advance()
		return m, m.tickSpinner()

	case logoFrameMsg:
		// Advance the wordmark's entrance one frame. The final frame is the
		// settled word, the animation disarms itself, and the banner stays
		// behind as a static history cell.
		if !m.logoRunning {
			return m, nil
		}
		if msg.frame >= logoFrames {
			m.logoRunning = false
			m.transcript.setBannerText(renderBannerFrame(m.theme, m.opts, m.cwd, logoFrames))
			return m, nil
		}
		m.transcript.setBannerText(renderBannerFrame(m.theme, m.opts, m.cwd, msg.frame))
		return m, m.tickLogo(msg.frame + 1)

	case textDeltaMsg:
		m.spinner.addTokens(msg.delta)
		m.transcript.appendDelta(msg.delta)
		m.remoteEcho(msg.delta)
		return m, m.pumpNext()

	case turnEndMsg:
		m.transcript.finalizeTurn(msg.msg)
		// Surface a failed or empty turn so a provider/API error is never silent.
		// The loop delivers request failures (e.g. a 4xx from the endpoint) as a
		// terminal assistant message with stopReason error/aborted via TurnEndEvent
		// — not as the run's result error (runEndMsg.err) — so without this check
		// the TUI would finalize an empty turn and return to the prompt with no
		// output at all. Mirrors the headless driver and the line-based REPL.
		switch msg.msg.StopReason {
		case agentcore.StopReasonError:
			reason := strings.TrimSpace(msg.msg.ErrorMessage)
			if reason == "" {
				reason = "the provider returned an error with no message"
			}
			m.transcript.addSystem("error: " + reason)
		case agentcore.StopReasonAborted:
			m.transcript.addSystem("error: aborted")
		default:
			// A turn that ends cleanly (end_turn) but produced no text, no thinking,
			// and no tool calls means the endpoint accepted the request but sent back
			// nothing usable (e.g. a 200 whose body was not in the wire format this
			// protocol expects). Note it instead of showing nothing.
			if len(msg.msg.Content) == 0 && len(msg.results) == 0 {
				m.transcript.addSystem("note: empty response from the model (no content). " +
					"Check that --model, --base-url and --protocol match the same provider.")
			}
		}
		return m, m.pumpNext()

	case judgeNoteMsg:
		// A permission-gate decision (approval, sandbox routing, denial,
		// read-only block) with its rationale; it renders right above the
		// tool card it decided.
		m.transcript.addReviewNote(msg.note)
		m.relayout()
		return m, m.pumpNext()

	case toolAnnounceMsg:
		// A call seen live in a streaming partial opens its card immediately
		// (codex Running order: call row above the text it produces). The
		// transcript seals the in-progress text so later deltas render after
		// the card. The executor's later start for the same id only completes
		// setup; a repeated announce is a no-op.
		if card, ok := m.toolCards[msg.id]; ok {
			if card.input == nil && msg.input != nil {
				card.input = msg.input
				m.transcript.reflow()
			}
			return m, nil
		}
		card := &toolCard{
			id:       msg.id,
			name:     msg.name,
			input:    msg.input,
			state:    cardRunning,
			expanded: defaultCardExpanded(msg.name),
		}
		m.toolCards[msg.id] = card
		m.lastToolCard = card
		m.transcript.announceToolCard(card)
		return m, m.pumpNext()

	case toolStartMsg:
		// The executor phase completes what the live announce skipped (full
		// args, echo, sub-agent row) without adding a second transcript block;
		// a call never announced takes the full legacy create path (#389).
		if card, ok := m.toolCards[msg.id]; ok {
			if msg.input != nil {
				card.input = msg.input
			}
			m.lastToolCard = card
			m.transcript.reflow()
			m.remoteEcho("\n· " + msg.name + "\n")
			if msg.name == "task" {
				m.subagents.add(msg.id, taskDescription(msg.input), time.Now())
				m.relayout()
			}
			return m, m.pumpNext()
		}
		// Create a rich tool-call card, index it by id for the later end event, and
		// append it as an ordered transcript block so it renders inline (#389).
		card := &toolCard{
			id:       msg.id,
			name:     msg.name,
			input:    msg.input,
			state:    cardRunning,
			expanded: defaultCardExpanded(msg.name),
		}
		m.toolCards[msg.id] = card
		m.lastToolCard = card
		m.transcript.addToolCard(card)
		m.remoteEcho("\n· " + msg.name + "\n")
		// A `task` tool call dispatches a sub-agent: open a status-panel row keyed by
		// the tool-call id (matching the later progress/end events) and record its
		// start so elapsed can be shown live (SPEC 4.4).
		if msg.name == "task" {
			m.subagents.add(msg.id, taskDescription(msg.input), time.Now())
			m.relayout() // the new panel row shrinks the transcript to fit
		}
		return m, m.pumpNext()

	case toolUpdateMsg:
		// A `task` sub-agent forwards its text as incremental tool-update deltas;
		// accumulate them onto the matching panel row so the expanded view can show
		// the running output. appendOutput is a no-op for non-task ids (nothing to
		// attach to), so ordinary tool updates are unaffected. Relayout only when the
		// delta lands on the currently expanded row, whose growing output changes the
		// panel height; other rows' output is buffered without touching the layout.
		m.subagents.appendOutput(msg.id, msg.partial)
		if m.subagents.expandedID() == msg.id {
			m.relayout()
		}
		return m, m.pumpNext()

	case subagentProgressMsg:
		// A running sub-agent reported structured progress: refresh its panel row's
		// activity/tokens. update adds the row if it is missing so a late/out-of-order
		// progress (arriving before the task's start) is still shown (SPEC 5.4).
		m.subagents.update(msg.id, msg.desc, msg.activity, msg.tokens, time.Now())
		m.relayout() // a first-seen id adds a row; keep the transcript sized to it
		return m, m.pumpNext()

	case toolEndMsg:
		// Flip the card's state and attach the parsed response tree. The card is
		// held by pointer in the transcript, so a reflow re-renders it in place.
		if card, ok := m.toolCards[msg.id]; ok {
			card.complete(msg.ok, msg.result, msg.details)
			m.transcript.reflow()
		}
		// Retire the sub-agent's status-panel row (a no-op for non-task tools whose id
		// was never added), reclaiming its reserved height.
		if _, wasSub := m.subagents.byID[msg.id]; wasSub {
			m.subagents.remove(msg.id)
			m.relayout()
		}
		return m, m.pumpNext()

	case telemetryMsg:
		// Feed the status bar's context-usage readout, and retain the event on the
		// session's telemetry holder so /status can render the cumulative + last-run
		// telemetry report (US-002, #292). Then keep the pump running.
		m.statusBar.SetTelemetry(telemetryEventView{
			util:   msg.ev.ContextUtilization,
			window: msg.ev.ContextWindow,
			tokens: msg.ev.ContextTokens,
		})
		if m.session != nil && m.session.telemetry != nil {
			m.session.telemetry.Fold(msg.ev)
		}
		return m, m.pumpNext()

	case compactionStartMsg:
		m.spinner.pin("Compacting conversation")
		return m, m.pumpNext()

	case compactionMsg:
		m.spinner.unpin()
		if m.session != nil {
			m.session.markCompacted()
		}
		m.transcript.addSystem("(context compacted)")
		return m, m.pumpNext()

	case rebuildDoneMsg:
		// A manual /compact finished: clear the pinned spinner (no run is
		// pumping, so stop it and drop out of the running state) and report the
		// outcome. The op already applied its context change and set
		// session.compacted on success.
		m.spinner.unpin()
		m.spinner.stop()
		m.running = false
		if msg.err != nil {
			m.transcript.addSystem(msg.label + " failed: " + msg.err.Error() + " (context left unchanged)")
		} else {
			m.transcript.addSystem(msg.summary)
		}
		m.relayout()
		return m, nil

	case dreamDoneMsg:
		// A manual /dream finished: clear the pinned spinner and render either
		// the notice (already-running), the failure, or the full report table.
		m.spinner.unpin()
		m.spinner.stop()
		m.running = false
		switch {
		case msg.err != nil:
			m.transcript.addSystem("dream failed: " + msg.err.Error())
		case msg.summary != "":
			m.transcript.addSystem(msg.summary)
		default:
			var buf bytes.Buffer
			dreamcmd.RenderReportTable(&buf, msg.report)
			m.transcript.addSystem(strings.TrimRight(buf.String(), "\n"))
		}
		m.relayout()
		return m, nil

	case runEndMsg:
		m.running = false
		m.runCh = nil
		m.spinner.stop()
		// A run that ended with an approval dialog still open (interrupt, or a
		// cancelled gate) leaves the decision moot: close it so the dialog does
		// not outlive the call it was asking about. The run goroutine is gone,
		// so no reply is delivered.
		m.approval = approvalDialog{}
		// The run is over: any still-open sub-agent rows are stale (their tasks ended
		// with the run), so clear the panel to reclaim its height.
		m.subagents = subagentPanel{}
		// Any card still marked running never received its end event — the run
		// was interrupted, or it ended while a tool was pending. Leaving it as
		// "Running" would promise output that will never arrive, so close it as
		// a warn (the transcript keeps the call and its partial output).
		for _, card := range m.toolCards {
			if card.state == cardRunning {
				card.state = cardWarn
			}
		}
		m.relayout()
		if msg.err != nil {
			m.transcript.addSystem("Run ended: " + msg.err.Error())
		}
		// Persist the turn's new messages as a branch so the conversation survives
		// exit and can be resumed (FR-16). This is race-free: the pump goroutine
		// owns agentCtx.Messages during the run and only sends runEndMsg after
		// DrainStream returns (loop done), so no goroutine is still writing the
		// context when persist reads it here on the tea goroutine. A save failure
		// is surfaced but non-fatal.
		if m.session != nil {
			// The run is fully drained (runEndMsg is sent last), so no gate
			// can publish another note into this run's channel: drop the
			// handler before the next run installs its own.
			m.session.notes.Set(nil)
			if m.sideRun {
				// A /btw side run never touches the main conversation or disk.
				m.transcript.addSystem("(end of side thread — the main conversation is unchanged)")
			} else {
				if err := m.session.persist(); err != nil {
					m.transcript.addSystem("Session save failed: " + err.Error())
				}
				// Group this turn's file mutations into a rewind restore point.
				m.session.commitRewindPoint()
				if m.goalRun {
					// An interrupted goal stays resumable: mark it paused before
					// reporting, mirroring the REPL's interrupt handling.
					if msg.err != nil {
						if m.session.Goal().Snapshot().Status == agenttool.GoalActive {
							m.session.Goal().SetStatus(agenttool.GoalPaused)
						}
					}
					var buf bytes.Buffer
					goalpkg.RenderOutcome(&buf, m.session.Goal().Snapshot())
					if s := strings.TrimRight(buf.String(), "\n"); s != "" {
						m.transcript.addSystem(s)
					}
				}
			}
		}
		m.sideRun = false
		m.goalRun = false
		// The editor was blurred at submit; re-enable it so the next prompt can be
		// typed, and re-probe git since a run may have changed the working tree.
		focus := m.input.Focus()
		return m, tea.Batch(focus, fetchGitCmd(m.cwd))

	case remoteInputMsg:
		// A prompt arrived from the paired browser (remote-control). Always re-issue
		// the listener so successive remote prompts keep arriving. While a run is in
		// flight the prompt is refused with a note (mirroring the local single-run
		// gate); when idle it is echoed as a user block and run — as a slash command
		// if it starts with "/", else a normal prompt.
		text := strings.TrimSpace(msg.text)
		if m.running || text == "" {
			if m.running && text != "" {
				m.transcript.addSystem("(remote input ignored: a run is in progress)")
				m.relayout()
			}
			return m, m.waitRemoteInput()
		}
		var cmd tea.Cmd
		var next tea.Model = m
		if strings.HasPrefix(text, "/") {
			next, cmd = m.runSlash(text)
		} else {
			m.transcript.addUser(text)
			m.remoteEcho("\n> " + text + "\n")
			m.relayout()
			next, cmd = m.startPrompt(text)
		}
		m = next.(Model)
		return m, tea.Batch(cmd, m.waitRemoteInput())
	}
	return m, nil
}

// handleKey processes a key press. It resolves the keys the shell owns —
// two-stage interrupt/quit, prompt submit, transcript scrolling — and delegates
// everything else (character entry, in-buffer cursor movement, Shift+Enter
// newline) to the input editor while idle. Keys are matched via KeyPressMsg
// .String() so the mapping is terminal-independent.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// A pending tool-call approval owns every key: the decision must not be
	// dismissed or answered accidentally, and typing behind it would be lost
	// input anyway once the dialog closes.
	if m.approval.active {
		if m.approvalKey(msg.String()) {
			m.relayout()
			return m, nil
		}
	}
	// While idle with the autocomplete popup open, the arrow / Tab / Esc keys
	// drive the menu instead of the transcript or textarea (FR-15). Enter is left
	// to the main switch below, which routes through submit → runSlash so the
	// selected/typed command runs. These are matched via KeyPressMsg.String() so
	// the mapping is terminal-independent.
	if !m.running && m.menu.active {
		switch msg.String() {
		case "up":
			m.menu.moveUp()
			return m, nil
		case "down":
			m.menu.moveDown()
			return m, nil
		case "tab":
			m = m.completeSlash()
			m.relayout()
			return m, nil
		case "esc":
			m.menu.close()
			m.relayout()
			return m, nil
		case "enter":
			return m.submitSlashSelected()
		}
	}

	// While a sub-agent run is streaming, the composer is disabled (no typing until
	// the run ends), so ↑/↓ drive a selection cursor over the live sub-agent status
	// rows and Enter expands the selected row to show its accumulated output inline.
	// Esc is the one-key escape back to the composer: with a row selected it drops
	// the selection AND re-focuses the input box in a single press, so arrowing into
	// the panel is never a trap. With no selection Esc falls through to its
	// two-stage interrupt role below. The Value()=="" guard is a safety net for the
	// rare case where text reached the buffer (e.g. a paste): then arrows edit the
	// buffer rather than the panel.
	if m.running && m.subagents.active() > 0 && m.input.Value() == "" {
		switch msg.String() {
		case "up":
			m.subagents.selectUp()
			m.relayout()
			return m, nil
		case "down":
			m.subagents.selectDown()
			m.relayout()
			return m, nil
		case "enter":
			m.subagents.toggleExpand()
			m.relayout()
			return m, nil
		case "esc":
			if m.subagents.hasSelection() {
				m.subagents.clearSelection()
				focus := m.input.Focus()
				m.relayout()
				return m, focus
			}
		}
	}

	switch msg.String() {
	case "ctrl+c":
		// Ctrl+C copies the current mouse selection when there is one (over OSC52),
		// clearing it afterward. With no selection, a non-empty composer is
		// discarded first — like a shell — so a half-typed prompt is never lost to
		// the quit arm and the interrupt/quit path only runs on an empty box.
		// Copying works even mid-run, so grabbing streamed output never interrupts
		// the run.
		if !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			if text != "" {
				return m, tea.SetClipboard(text)
			}
			return m, nil
		}
		if !m.running && m.input.Value() != "" {
			return m.clearDraft(), nil
		}
		return m.interruptOrQuit()
	case "super+c":
		// Cmd+C on macOS is the platform-standard copy: copy the mouse selection
		// when there is one (clearing it), else the whole input buffer. Unlike
		// Ctrl+C it never interrupts/quits — Cmd+C means "copy" on macOS. Most
		// terminals intercept Cmd+C for their own native copy and never deliver it
		// here; this branch serves terminals that forward the Super modifier.
		if !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			if text != "" {
				return m, tea.SetClipboard(text)
			}
			return m, nil
		}
		if !m.running {
			if v := m.input.Value(); v != "" {
				return m, tea.SetClipboard(v)
			}
		}
		return m, nil
	case "esc":
		return m.interruptOrQuit()
	case "ctrl+t":
		// Toggle the most-recent tool card between its one-line summary and the
		// full detail, then re-flow so the change shows inline (#389, codex
		// parity: ctrl+t expands collapsed output).
		if m.lastToolCard != nil {
			m.lastToolCard.expanded = !m.lastToolCard.expanded
			m.transcript.reflow()
		}
		return m, nil
	case "ctrl+d":
		// Ctrl+D quits only when idle; mid-run it is ignored so a run is never
		// dropped by a stray EOF key.
		if !m.running {
			m.shutdownRemote()
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case "enter":
		// Enter submits the composed buffer (FR-13). Shift+Enter inserts a newline
		// (rebound in newInput) so the editor is a true multi-line composer; when
		// the slash menu is open, Enter runs the highlighted command (handled
		// above), so this branch is only reached with the menu closed.
		if !m.running {
			return m.submit()
		}
		return m, nil
	case "pgup", "pgdown":
		// Page scrolling reaches the transcript viewport whether idle or running,
		// so history stays readable while a run streams. A content-anchored
		// selection survives it untouched (same rule as the wheel handler);
		// a below-transcript one is dropped. Line-oriented keys (up / down /
		// home / end) belong to the multi-line editor and are delegated below.
		if m.sel.active && !m.sel.below {
			cmd := m.transcript.update(msg)
			return m, cmd
		}
		m.sel = selection{}
		cmd := m.transcript.update(msg)
		return m, cmd
	case "ctrl+v":
		// Explicit paste key: first try to pull an image off the clipboard (Claude
		// Code-style image paste); the reply arrives as clipboardImageMsg and, when
		// no image is present, falls back to an OSC52 text read (tea.ClipboardMsg).
		// This is intercepted before textarea so its own Ctrl+V binding — which reads
		// via an external process and returns an unexported message the model can't
		// route — is bypassed. The common Cmd+V path does not reach here; it arrives
		// as a bracketed tea.PasteMsg handled in Update.
		if !m.running {
			return m, readClipboardImage
		}
		return m, nil
	case "super+v":
		// Cmd+V on macOS is the platform-standard paste. Most terminals turn it
		// into a bracketed paste (tea.PasteMsg, handled in Update); this branch
		// covers terminals that instead forward the Super modifier as a key. Try an
		// image read first, falling back to an OSC52 text read when none is present.
		if !m.running {
			return m, readClipboardImage
		}
		return m, nil
	case "ctrl+y":
		// Copy: the editor has no text selection, so this copies the whole buffer
		// to the system clipboard over OSC52. A no-op on an empty buffer.
		if !m.running {
			if v := m.input.Value(); v != "" {
				return m, tea.SetClipboard(v)
			}
		}
		return m, nil
	}

	// Everything else is editing input; gated on idle so keystrokes never corrupt
	// an in-flight prompt. textarea handles CJK / emoji by rune and Shift+Enter as
	// a newline. After the buffer changes, refresh the autocomplete popup so it
	// opens/filters/closes as the user types a "/name" prefix.
	if !m.running {
		// ↑/↓ walk the submitted-prompt history when the caret is at the top / bottom
		// edge of the composer; otherwise they move the caret within a multi-line
		// draft (handled by the textarea below).
		switch msg.String() {
		case "up":
			return m.historyPrev(msg)
		case "down":
			return m.historyNext(msg)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.menu.refresh(m.input.Value(), m.slash)
		m.relayout()
		return m, cmd
	}
	return m, nil
}

// submit starts a run for the current buffer: it appends the user block, clears
// and blurs the editor, flips to running, and — when a run starter is wired —
// returns the first pump Cmd. With no starter (pre-#392) it records the prompt
// and a system note without launching anything, and leaves the editor ready for
// the next line.
func (m Model) submit() (tea.Model, tea.Cmd) {
	prompt := strings.TrimSpace(m.expandImages(m.expandPastes(m.input.Value())))
	if prompt == "" {
		return m, nil
	}
	// Record the input into the browse history, then exit browse mode. The
	// EXPANDED prompt is stored (not the raw text with "[Pasted text #N]"
	// placeholders): the paste bodies and image paths are dropped right after
	// submit, so a recalled placeholder could never be re-expanded and would be
	// sent literally.
	m.recordHistory(prompt)
	// The placeholders have been expanded into the prompt, so the stored paste
	// bodies and image paths are consumed; drop them (the id counters keep climbing).
	m.pastes = make(map[int]string)
	m.images = make(map[int]string)
	// A "/name ..." line is a slash-command invocation, not a prompt: resolve it
	// against the shared registry (same as the REPL) rather than sending it to the
	// agent verbatim.
	if strings.HasPrefix(prompt, "/") {
		return m.runSlash(prompt)
	}
	m.transcript.addUser(prompt)
	m.remoteEcho("\n> " + prompt + "\n")
	m.input.Clear()
	m.menu.close()
	m.relayout()
	return m.startPrompt(prompt)
}

// completeSlash fills the buffer with the highlighted candidate's "/name " so the
// user can go on to type arguments; the trailing space ends name-completion, so
// the refresh closes the popup. It is the Tab action while the menu is open.
func (m Model) completeSlash() Model {
	if m.menu.picking() {
		return m
	}
	if c, ok := m.menu.current(); ok {
		m.input.SetValue("/" + c.Name + " ")
		m.menu.refresh(m.input.Value(), m.slash)
	}
	return m
}

// submitSlashSelected runs the command the popup highlights (Enter while the
// menu is open). Navigating with the arrows then pressing Enter runs the
// selected command even if the typed prefix is shorter; with no selection it
// falls back to the raw buffer so a fully-typed "/name" still runs.
func (m Model) submitSlashSelected() (tea.Model, tea.Cmd) {
	if m.menu.picking() {
		kind := m.menu.pickKind
		if kind == "" {
			kind = "model"
		}
		item, ok := m.menu.pickCurrent()
		if kind == "proxy" && ok {
			m.menu.close()
			m.input.Clear()
			if item == "url" {
				m.input.SetValue("/proxy url ")
				m.relayout()
				return m, nil
			}
			out, err := m.slash.ResolveOutcome("/proxy " + item)
			if err != nil {
				m.transcript.addSystem("proxy: " + err.Error())
			} else {
				m.transcript.addSystem(out.Message)
			}
			return m.reopenProxyPicker(strings.Fields(item)[0], false)
		}
		// Stage 1 of the /model flow: after the model, pick its reasoning level
		// when the catalog (or the family fallback) says it has one — the
		// codex-style two-step. The switch itself runs at stage 2 so Esc on the
		// level picker leaves the live model untouched.
		if kind == "model" && ok && m.live != nil {
			if levels := provider.ReasoningLevels(m.live.ProviderName, item); len(levels) > 0 {
				m.menu.openLevelPicker(item, levels, m.live.ThinkingLevel)
				m.transcript.addSystem(fmt.Sprintf("Select a reasoning level for %s (↑↓ + Enter, Esc cancels):", item))
				m.relayout()
				return m, nil
			}
		}
		// Stage 2: run the switch and the level as one /model <id> <level>.
		if kind == "model-level" && ok {
			model := m.menu.pickModel
			m.menu.close()
			m.input.Clear()
			m.relayout()
			if model == "" {
				return m, nil
			}
			line := "/model " + model + " " + item
			m.recordHistory(line)
			return m.runSlash(line)
		}
		if item, ok := m.menu.pickCurrent(); ok {
			m.menu.close()
			m.input.Clear()
			m.relayout()
			m.recordHistory("/" + kind + " " + item)
			return m.runSlash("/" + kind + " " + item)
		}
		m.menu.close()
		m.relayout()
		return m, nil
	}
	line := strings.TrimSpace(m.input.Value())
	if c, ok := m.menu.current(); ok {
		line = "/" + c.Name
	}
	m.recordHistory(line)
	return m.runSlash(line)
}

// runSlash resolves a slash-command line against the shared registry and folds
// its outcome into the transcript, mirroring the REPL's dispatch: the invocation
// is echoed as a user block; an action command's status (e.g. /help, /model)
// renders as a system block; a prompt/skill command's expanded text starts a
// run; a hybrid (plugin) command shows its notifications then runs its prompt.
// An unknown command surfaces the resolver error as a system block.
func (m Model) runSlash(line string) (tea.Model, tea.Cmd) {
	// /exit terminates the TUI, mirroring the REPL loop which intercepts it
	// before slash resolution. It registers only as a no-op /help builtin, so
	// without this the registry would resolve it to an empty action.
	if line == "/exit" {
		m.shutdownRemote()
		m.quitting = true
		return m, tea.Quit
	}
	// /resume switches the active session in place (see runSession.switchTo).
	// A bare /resume lists recent sessions; switching mid-run is refused so an
	// in-flight run is never torn down.
	if line == "/resume" || strings.HasPrefix(line, "/resume ") {
		return m.resumeSession(strings.TrimSpace(strings.TrimPrefix(line, "/resume")))
	}
	// Bare /model opens the interactive model picker (arrow keys + Enter)
	// instead of printing a text list; /model <n|id|think…> still resolves
	// through the registry below.
	if line == "/model" {
		return m.openModelPicker()
	}
	// Bare /provider opens the interactive provider picker (each row shows the
	// env vars the provider reads and whether one is set);
	// /provider <name> still resolves through the registry below.
	if line == "/provider" {
		return m.openProviderPicker()
	}
	if line == "/proxy" {
		return m.openProxyPicker("")
	}
	// Bare /permissions opens the interactive mode picker (arrow keys +
	// Enter) with the codex-style preset list and descriptions;
	// /permissions <mode> still resolves through the registry below.
	if line == "/permissions" {
		return m.openPermissionsPicker()
	}
	// /memory is intercepted before registry resolution (like /compact): it
	// prints the persistent-memory + infinite-context report, reading the live
	// memory store, memory root, session id, and messages that a slash Action
	// closure (string→string) cannot reach.
	if line == "/memory" || strings.HasPrefix(line, "/memory ") {
		m.transcript.addUser(line)
		m.input.Clear()
		m.menu.close()
		var buf bytes.Buffer
		var store *memory.Store
		var memoryRoot, sessionID string
		var msgs agentcore.MessageList
		window := m.live.ContextWindow
		if m.session != nil {
			store = m.session.memstore
			memoryRoot = m.session.memoryRoot
			sessionID = m.session.header.ID
			msgs = m.session.agentCtx.Messages
		}
		memstatus.RunMemory(&buf, store, memoryRoot, sessionID, msgs, window)
		m.transcript.addSystem(strings.TrimRight(buf.String(), "\n"))
		m.relayout()
		return m, nil
	}
	// /status is intercepted before registry resolution (like /memory): it prints
	// the shared runtime/context/project/credentials/telemetry report, which reads
	// the session's live collaborators (live config, trust manager, telemetry
	// holder, slash registry) that a slash Action closure (string→string) cannot
	// reach. The rendering lives in the shared status package so the TUI and the
	// REPL produce byte-identical output.
	if line == "/status" || strings.HasPrefix(line, "/status ") {
		m.transcript.addUser(line)
		m.input.Clear()
		m.menu.close()
		if m.session == nil {
			m.transcript.addSystem("(status unavailable: no active session)")
			m.relayout()
			return m, nil
		}
		var buf bytes.Buffer
		status.RunStatus(&buf, m.session)
		m.transcript.addSystem(strings.TrimRight(buf.String(), "\n"))
		m.relayout()
		return m, nil
	}
	// /compact and /dream do their work off the tea loop (a summarization call or
	// the dream subprocess can take a while), so they arm the spinner and report
	// through a done message. Each replaces context or consolidates memory in
	// place — work a string→string Action closure cannot do.
	if line == "/compact" {
		return m.startSessionOp(line, "Compacting conversation", func() tea.Cmd { return m.session.compactCmd() })
	}
	if line == "/dream" || strings.HasPrefix(line, "/dream ") {
		dryRun := dreamcmd.HasDryRun(line)
		return m.startSessionOp(line, "Dreaming (consolidating memory)", func() tea.Cmd { return m.session.dreamCmd(dryRun) })
	}
	// /export writes the live session to a file; it must persist the current turn
	// first so unsaved messages are included.
	if line == "/export" || strings.HasPrefix(line, "/export ") {
		return m.runExport(line)
	}
	// /import materializes a JSONL export as a fresh session and switches to it,
	// swapping the header and context in place — work a slash Action cannot do.
	if line == "/import" || strings.HasPrefix(line, "/import ") {
		return m.runImport(line)
	}
	// /clone duplicates the current conversation into a new independent session
	// and switches to it (the same branch swap /resume performs).
	if line == "/clone" {
		return m.runClone(line)
	}
	// /fork branches from before a chosen historical message: bare /fork opens a
	// picker, /fork <n> performs the branch and switches to it.
	if line == "/fork" || strings.HasPrefix(line, "/fork ") {
		return m.runFork(line)
	}
	// /tree shows the session branch tree as a picker; /tree <n> switches the
	// active branch to the n-th node and rebuilds the context from its path.
	if line == "/tree" || strings.HasPrefix(line, "/tree ") {
		return m.runTree(line)
	}
	// /rewind rolls files and the conversation back to before an earlier turn:
	// bare /rewind opens the restore-point picker, /rewind <n> performs it.
	if line == "/rewind" || strings.HasPrefix(line, "/rewind ") {
		return m.runRewind(line)
	}
	// /btw asks a side question against a copy of the conversation: the answer
	// streams into the transcript but nothing is saved or sent to the model as
	// part of the main conversation.
	if line == "/btw" || strings.HasPrefix(line, "/btw ") {
		return m.runBtw(line)
	}
	// /goal drives the autonomous goal loop: bare /goal shows status, pause and
	// clear act on the state, and an objective (or resume) starts the run
	// through the same goal package the REPL uses.
	if line == "/goal" || strings.HasPrefix(line, "/goal ") {
		return m.runGoal(line)
	}
	// /remote-control is intercepted before registry resolution (like /compact):
	// it starts/stops the LAN mirror server, which owns state (server, bridge,
	// listener Cmd) a string→string slash Action cannot hold.
	if line == "/remote-control" || strings.HasPrefix(line, "/remote-control ") {
		return m.runRemoteControl(line)
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	m.relayout()
	if m.slash == nil {
		m.transcript.addSystem("Slash commands unavailable")
		return m, nil
	}
	outcome, err := m.slash.ResolveOutcome(line)
	if err != nil {
		m.transcript.addSystem(err.Error())
		return m, nil
	}
	if outcome.Message != "" {
		m.transcript.addSystem(outcome.Message)
	}
	// A live-state command (/model, /think) may have mutated m.live; sync the
	// status bar so the model/thinking segments reflect the switch immediately.
	if m.live != nil {
		m.statusBar.SetModel(m.live.Model)
		m.statusBar.SetThinking(string(m.live.ThinkingLevel))
	}
	// An action command is complete once its status is shown; a hybrid with no
	// prompt (notifications only) likewise starts no run.
	if outcome.Kind == runtime.SlashAction || outcome.Prompt == "" {
		return m, nil
	}
	return m.startPrompt(outcome.Prompt)
}

// startSessionOp echoes a session operation that does its work off the tea loop
// (rebuild, compact, dream): the invocation is recorded, the spinner is armed
// and pinned to label, and the op's done message reports the outcome into the
// transcript when it lands.
func (m Model) startSessionOp(line, label string, cmd func() tea.Cmd) (tea.Model, tea.Cmd) {
	if m.running {
		m.transcript.addUser(line)
		m.input.Clear()
		m.menu.close()
		m.transcript.addSystem("Interrupt the current run first, then " + commandToken(line) + ".")
		m.relayout()
		return m, nil
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	if m.session == nil {
		m.transcript.addSystem("(" + strings.TrimPrefix(commandToken(line), "/") + " unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	m.spinner.begin(time.Now(), m.thinkingLabel())
	m.spinner.pin(label)
	m.running = true
	m.relayout()
	return m, tea.Batch(cmd(), m.tickSpinner())
}

// commandToken returns the leading "/name" token of a slash line.
func commandToken(line string) string {
	name := strings.TrimPrefix(strings.TrimSpace(line), "/")
	if i := strings.IndexAny(name, " \t"); i >= 0 {
		name = name[:i]
	}
	return "/" + name
}

// runExport handles /export [path]: persist the live turn so unsaved messages
// are included, then write the session to path (default <session-id>.jsonl in
// the working directory; a .html extension writes a self-contained transcript).
func (m Model) runExport(line string) (tea.Model, tea.Cmd) {
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	if m.running {
		m.transcript.addSystem("Interrupt the current run first, then /export.")
		m.relayout()
		return m, nil
	}
	if m.session == nil {
		m.transcript.addSystem("(export unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	path := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "/export")), `"`)
	if path == "" {
		path = m.session.header.ID + ".jsonl"
	}
	if err := m.session.persist(); err != nil {
		m.transcript.addSystem("export: save current session: " + err.Error())
		m.relayout()
		return m, nil
	}
	n, err := m.session.store.Export(m.session.header.ID, path)
	if err != nil {
		m.transcript.addSystem("export failed: " + err.Error())
	} else {
		m.transcript.addSystem(fmt.Sprintf("exported %d entries to %s", n, path))
	}
	m.relayout()
	return m, nil
}

// runImport handles /import <path>: materialize a JSONL export as a fresh
// independent session and switch the transcript to it.
func (m Model) runImport(line string) (tea.Model, tea.Cmd) {
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	if m.session == nil {
		m.transcript.addSystem("(import unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addSystem("Interrupt the current run first, then /import.")
		m.relayout()
		return m, nil
	}
	path := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "/import")), `"`)
	if path == "" {
		m.transcript.addSystem("usage: /import <path.jsonl>")
		m.relayout()
		return m, nil
	}
	msgs, err := m.session.importSession(path)
	if err != nil {
		m.transcript.addSystem("import failed: " + err.Error())
		m.relayout()
		return m, nil
	}
	m.reseedTranscript(fmt.Sprintf("Imported session %s from %s (%d messages).", m.session.header.ID, path, len(msgs)))
	return m, nil
}

// runClone handles /clone: duplicate the current conversation at its active leaf
// into a new independent session and switch to it.
func (m Model) runClone(line string) (tea.Model, tea.Cmd) {
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	if m.session == nil {
		m.transcript.addSystem("(clone unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addSystem("Interrupt the current run first, then /clone.")
		m.relayout()
		return m, nil
	}
	prev := m.session.header.ID
	msgs, err := m.session.cloneSession()
	if err != nil {
		m.transcript.addSystem("clone: " + err.Error())
		m.relayout()
		return m, nil
	}
	m.reseedTranscript(fmt.Sprintf("Cloned session %s → %s (%d messages).", prev, m.session.header.ID, len(msgs)))
	return m, nil
}

// runFork handles /fork [n]: bare /fork opens the picker of historical user
// messages; /fork <n> branches from before the n-th message and switches to the
// new branch.
func (m Model) runFork(line string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/fork"))
	if m.session == nil {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("(fork unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("Interrupt the current run first, then /fork.")
		m.relayout()
		return m, nil
	}
	if arg == "" {
		m.input.Clear()
		m.menu.close()
		users, err := m.session.forkCandidates()
		if err != nil {
			m.transcript.addSystem("fork: " + err.Error())
			m.relayout()
			return m, nil
		}
		if len(users) == 0 {
			m.transcript.addSystem("no user messages to fork from")
			m.relayout()
			return m, nil
		}
		picks := make([]pickItem, 0, len(users))
		for n, text := range users {
			picks = append(picks, pickItem{
				Title: fmt.Sprintf("%d. %s", n+1, text),
				Value: strconv.Itoa(n + 1),
			})
		}
		m.menu.openPickerDetailed(picks, "", "fork")
		m.transcript.addSystem("Fork from which message? (↑↓ + Enter, Esc cancels)")
		m.relayout()
		return m, nil
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	n, err := strconv.Atoi(arg)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("fork: invalid selection %q — run /fork to list messages", arg))
		m.relayout()
		return m, nil
	}
	prev := m.session.header.ID
	msgs, err := m.session.forkAt(n)
	if err != nil {
		m.transcript.addSystem("fork: " + err.Error())
		m.relayout()
		return m, nil
	}
	m.reseedTranscript(fmt.Sprintf("Forked session %s → %s (%d messages).", prev, m.session.header.ID, len(msgs)))
	return m, nil
}

// runTree handles /tree [n]: bare /tree opens the branch picker; /tree <n>
// switches the active branch to the n-th node and rebuilds the context from it.
func (m Model) runTree(line string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/tree"))
	if m.session == nil {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("(tree unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("Interrupt the current run first, then /tree.")
		m.relayout()
		return m, nil
	}
	if arg == "" {
		m.input.Clear()
		m.menu.close()
		lines, err := m.session.treeLines()
		if err != nil {
			m.transcript.addSystem("tree: " + err.Error())
			m.relayout()
			return m, nil
		}
		if len(lines) == 0 {
			m.transcript.addSystem("session tree is empty — send a message first")
			m.relayout()
			return m, nil
		}
		picks := make([]pickItem, 0, len(lines))
		mark := ""
		for i, l := range lines {
			value := strconv.Itoa(i + 1)
			if l.Entry.ID == m.session.curLeaf {
				mark = value
			}
			picks = append(picks, pickItem{Title: l.Text, Value: value})
		}
		m.menu.openPickerDetailed(picks, mark, "tree")
		m.transcript.addSystem("Session tree — pick a branch to switch to (↑↓ + Enter, Esc cancels)")
		m.relayout()
		return m, nil
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	n, err := strconv.Atoi(arg)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("tree: invalid selection %q — run /tree to list nodes", arg))
		m.relayout()
		return m, nil
	}
	msgs, err := m.session.switchBranchIndex(n)
	if err != nil {
		m.transcript.addSystem("tree: " + err.Error())
		m.relayout()
		return m, nil
	}
	m.reseedTranscript(fmt.Sprintf("Switched to branch at node %d (%d messages).", n, len(msgs)))
	return m, nil
}

// runRewind handles /rewind [n]: bare /rewind opens the restore-point picker;
// /rewind <n> restores the files and conversation to before the n-th point.
func (m Model) runRewind(line string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/rewind"))
	if m.session == nil {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("(rewind unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("Interrupt the current run first, then /rewind.")
		m.relayout()
		return m, nil
	}
	if arg == "" {
		m.input.Clear()
		m.menu.close()
		points := m.session.rewindPoints()
		if len(points) == 0 {
			m.transcript.addUser(line)
			m.transcript.addSystem("no restore points yet — file edits create them")
			m.relayout()
			return m, nil
		}
		picks := make([]pickItem, 0, len(points))
		for i, p := range points {
			files := fmt.Sprintf("%d files", len(p.Snapshots))
			if len(p.Snapshots) == 1 {
				files = "1 file"
			}
			label := p.Label
			if label == "" {
				label = "(no prompt)"
			}
			picks = append(picks, pickItem{
				Title: fmt.Sprintf("%d. %s  %s  %s", i+1, p.Time.Local().Format(time.Kitchen), files, label),
				Value: strconv.Itoa(i + 1),
			})
		}
		m.menu.openPickerDetailed(picks, "", "rewind")
		m.transcript.addSystem("Restore points — pick one to roll files + conversation back to before it (↑↓ + Enter, Esc cancels)")
		m.relayout()
		return m, nil
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	n, err := strconv.Atoi(arg)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("rewind: invalid selection %q — run /rewind to list points", arg))
		m.relayout()
		return m, nil
	}
	if n < 1 || n > len(m.session.rewindPoints()) {
		m.transcript.addSystem(fmt.Sprintf("rewind: selection %d out of range (1..%d)", n, len(m.session.rewindPoints())))
		m.relayout()
		return m, nil
	}
	restored, warnings, msgs, err := m.session.rewindTo(n)
	if err != nil {
		m.transcript.addSystem("rewind failed: " + err.Error())
		m.relayout()
		return m, nil
	}
	notice := fmt.Sprintf("Rewound to before point %d — next prompt continues from here.", n)
	if len(restored) > 0 {
		notice += fmt.Sprintf("\nRestored %d file(s):", len(restored))
		for _, p := range restored {
			notice += "\n  " + p
		}
	} else {
		notice += "\nNo files to restore for this point."
	}
	for _, w := range warnings {
		notice += "\n  warning: " + w
	}
	m.transcript.reset()
	m.transcript.addBanner(renderBannerFrame(m.theme, m.opts, m.cwd, logoFrames))
	seedTranscript(&m.transcript, msgs)
	m.transcript.addSystem(notice)
	m.logoRunning = false
	m.relayout()
	return m, nil
}

// runBtw handles /btw [question]: a question starts a side run against a copy of
// the conversation (streamed into the transcript, never persisted); a bare /btw
// replays the most recent side thread.
func (m Model) runBtw(line string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/btw"))
	if m.session == nil {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("(btw unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("Interrupt the current run first, then /btw.")
		m.relayout()
		return m, nil
	}
	if arg == "" {
		m.transcript.addUser(line)
		m.input.Clear()
		m.menu.close()
		if m.session.lastBtw == nil {
			m.transcript.addSystem("usage: /btw <question> — ask a quick side question without touching the main conversation")
			m.relayout()
			return m, nil
		}
		m.transcript.addSystem("Side thread (not part of the conversation):")
		seedTranscript(&m.transcript, m.session.lastBtw.Messages[m.session.lastBtwBase:])
		m.relayout()
		return m, nil
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	m.transcript.addSystem("Side thread — this exchange is not saved to the conversation.")
	m.input.Blur()
	ch, cmd := m.session.startBtwRun(arg)
	m.runCh = ch
	m.running = true
	m.sideRun = true
	m.spinner.begin(time.Now(), m.thinkingLabel())
	m.relayout()
	return m, tea.Batch(cmd, m.tickSpinner())
}

// runGoal handles /goal [--tokens N] <objective> | pause | resume | clear: the
// state actions render immediately, while an objective (or resume) starts the
// autonomous run through the shared goal package.
func (m Model) runGoal(line string) (tea.Model, tea.Cmd) {
	args := strings.TrimSpace(strings.TrimPrefix(line, "/goal"))
	if m.session == nil {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("(goal unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	if m.running {
		m.transcript.addUser(line)
		m.input.Clear()
		m.transcript.addSystem("Interrupt the current run first, then /goal.")
		m.relayout()
		return m, nil
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	var buf bytes.Buffer
	startRun := false
	runLabel := ""
	switch {
	case args == "":
		goalpkg.RenderStatus(&buf, m.session.Goal())
	case args == "clear":
		m.session.Goal().Clear()
		buf.WriteString("goal cleared")
	case args == "pause":
		snap := m.session.Goal().Snapshot()
		if snap.Status != agenttool.GoalActive && snap.Status != agenttool.GoalPaused {
			buf.WriteString("no active goal to pause")
		} else {
			m.session.Goal().SetStatus(agenttool.GoalPaused)
			buf.WriteString("goal paused — run /goal resume to continue")
		}
	case args == "resume":
		snap := m.session.Goal().Snapshot()
		if snap.Status != agenttool.GoalPaused && snap.Status != agenttool.GoalBudgetLimited {
			buf.WriteString("no paused goal to resume")
		} else {
			m.session.Goal().Resume()
			fmt.Fprintf(&buf, "resuming goal: %s", ui.OneLine(snap.Objective))
			startRun = true
			runLabel = "goal resume"
		}
	default:
		objective, budget, err := goalpkg.ParseObjective(args)
		if err != nil {
			buf.WriteString("golder: " + err.Error())
		} else if strings.TrimSpace(objective) == "" {
			buf.WriteString("usage: /goal [--tokens N] <objective>")
		} else {
			goalpkg.Start(m.session.Goal(), objective, budget)
			if budget > 0 {
				fmt.Fprintf(&buf, "goal set (token budget %d): %s", budget, ui.OneLine(objective))
			} else {
				fmt.Fprintf(&buf, "goal set: %s", ui.OneLine(objective))
			}
			startRun = true
			runLabel = "goal: " + ui.OneLine(objective)
		}
	}
	if s := strings.TrimRight(buf.String(), "\n"); s != "" {
		m.transcript.addSystem(s)
	}
	if !startRun {
		m.relayout()
		return m, nil
	}
	m.input.Blur()
	ch, cmd := m.session.startGoalRun(runLabel)
	m.runCh = ch
	m.running = true
	m.goalRun = true
	m.spinner.begin(time.Now(), m.thinkingLabel())
	m.relayout()
	return m, tea.Batch(cmd, m.tickSpinner())
}

// reseedTranscript rebuilds the transcript from the freshly adopted session
// context: the launch banner, the historical turns, and a one-line notice.
func (m *Model) reseedTranscript(notice string) {
	m.transcript.reset()
	m.transcript.addBanner(renderBannerFrame(m.theme, m.opts, m.cwd, logoFrames))
	seedTranscript(&m.transcript, m.session.agentCtx.Messages)
	m.transcript.addSystem(notice)
	m.logoRunning = false
	m.relayout()
}

// recordHistory appends an submitted input to the browse history (skipping a
// consecutive duplicate, like a shell) and resets the browse cursor to the live
// draft, so the next ↑ starts from the most recent entry and any stashed draft is
// dropped. A blank entry is never stored. The entry is also appended to the
// global history file (best-effort) so the next session can browse it.
func (m *Model) recordHistory(entry string) {
	entry = strings.TrimSpace(entry)
	if entry != "" && (len(m.history) == 0 || m.history[len(m.history)-1] != entry) {
		m.history = append(m.history, entry)
	}
	m.histIdx = len(m.history)
	m.histDraft = ""
	m.appendHistoryFile(entry)
}

// ensureHistoryLoaded lazily merges the global prompt history into the browse
// list, once: the first ↑ / Ctrl+R reads the file so a session that never
// browses never touches it. The disk window (newest first N) lands in front of
// anything recorded in this session, and consecutive duplicates are collapsed
// so resubmitting a just-recalled entry does not show twice.
func (m *Model) ensureHistoryLoaded() {
	if m.histLoaded {
		return
	}
	m.histLoaded = true
	if m.opts.HistoryPath == "" {
		return
	}
	entries, err := history.Load(m.opts.HistoryPath, history.MaxEntries)
	if err != nil {
		return
	}
	texts := make([]string, 0, len(entries))
	for _, e := range entries {
		if t := strings.TrimSpace(e.Text); t != "" {
			texts = append(texts, t)
		}
	}
	m.history = dedupConsecutive(append(texts, m.history...))
	m.histIdx = len(m.history)
}

// appendHistoryFile persists one submitted input to the global history file.
// Failures are deliberately silent: history is a convenience, and a prompt
// must never fail to submit because its history write did.
func (m *Model) appendHistoryFile(entry string) {
	if entry == "" || m.opts.HistoryPath == "" {
		return
	}
	sessionID := ""
	if m.session != nil {
		sessionID = m.session.header.ID
	}
	_ = history.Append(m.opts.HistoryPath, history.Entry{
		TS:        time.Now().Unix(),
		SessionID: sessionID,
		Cwd:       m.cwd,
		Text:      entry,
	})
}

// dedupConsecutive collapses runs of identical entries (like a shell's
// ignore-dups), keeping the first occurrence of each run.
func dedupConsecutive(entries []string) []string {
	out := entries[:0]
	for _, e := range entries {
		if len(out) == 0 || out[len(out)-1] != e {
			out = append(out, e)
		}
	}
	return out
}

// historyPrev recalls the previous submitted input into the composer, but only
// when the caret is on the first line — otherwise ↑ moves the caret within a
// multi-line draft. The first recall stashes the live draft so historyNext can
// restore it, and the cursor lands past the newest entry (len(history)) initially.
func (m Model) historyPrev(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.input.Line() != 0 {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.menu.refresh(m.input.Value(), m.slash)
		m.relayout()
		return m, cmd
	}
	m.ensureHistoryLoaded()
	if len(m.history) == 0 {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.menu.refresh(m.input.Value(), m.slash)
		m.relayout()
		return m, cmd
	}
	if m.histIdx == len(m.history) {
		m.histDraft = m.input.Value()
	}
	if m.histIdx > 0 {
		m.histIdx--
	}
	m.input.SetValue(m.history[m.histIdx])
	m.menu.refresh(m.input.Value(), m.slash)
	m.relayout()
	return m, nil
}

// historyNext walks forward toward more recent inputs — restoring the stashed
// draft once it steps past the newest entry — but only while browsing and with
// the caret on the last line; otherwise ↓ moves the caret within a multi-line
// draft.
func (m Model) historyNext(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.histIdx >= len(m.history) || m.input.Line() != m.input.LineCount()-1 {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.menu.refresh(m.input.Value(), m.slash)
		m.relayout()
		return m, cmd
	}
	m.histIdx++
	if m.histIdx == len(m.history) {
		m.input.SetValue(m.histDraft)
	} else {
		m.input.SetValue(m.history[m.histIdx])
	}
	m.menu.refresh(m.input.Value(), m.slash)
	m.relayout()
	return m, nil
}

// startPrompt launches an agent run for prompt, blurring the editor and flipping
// to running when a run starter is wired. With no starter (pre-session model /
// tests) it records the pre-#392 system note and stays idle. It is shared by a
// plain submit and by a slash prompt/skill command.
func (m Model) startPrompt(prompt string) (tea.Model, tea.Cmd) {
	if m.startRunFn == nil {
		m.transcript.addSystem("(run not wired up: see session assembly in #392)")
		return m, nil
	}
	m.input.Blur()
	ch, cmd := m.startRunFn(prompt)
	m.runCh = ch
	m.running = true
	m.spinner.begin(time.Now(), m.thinkingLabel())
	m.relayout()
	return m, tea.Batch(cmd, m.tickSpinner())
}

// thinkingLabel returns the current thinking-effort label for the spinner stats
// (e.g. "medium"), or "" when no thinking level is configured so the stat is
// omitted. It reads the live config the /model command mutates, falling back to
// the launch Options.
func (m Model) thinkingLabel() string {
	if m.live != nil && m.live.ThinkingLevel != "" {
		return string(m.live.ThinkingLevel)
	}
	return string(m.opts.ThinkingLevel)
}

// taskDescription pulls the human-readable "description" out of a `task` tool
// call's decoded arguments for the sub-agent panel's row label. It returns ""
// when absent or non-string (the description field is optional in the schema),
// in which case the panel row leads with the activity instead.
func taskDescription(input map[string]any) string {
	if s, ok := input["description"].(string); ok {
		return s
	}
	return ""
}

// tickSpinner schedules the next spinner animation frame. The model re-issues it
// on each spinnerTickMsg while running, so the animation self-sustains until the
// run ends (the tick is simply not re-issued once idle).
func (m Model) tickSpinner() tea.Cmd {
	return tea.Tick(spinnerInterval, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// quitArmWindow is how long an armed quit stays live: a second idle Ctrl+C /
// Esc within this window quits; after it the arm expires.
const quitArmWindow = 3 * time.Second

// clearDraft discards the composer, its paste/image placeholder bodies included,
// and closes the slash menu, reflowing for the shorter editor. It is the
// shell-like first stage of Ctrl+C: a lone press with text in the box has no
// other side effect, so a quit arm is never set and no run is touched.
func (m Model) clearDraft() Model {
	m.input.Clear()
	m.menu.close()
	m.pastes = make(map[int]string)
	m.images = make(map[int]string)
	m.relayout()
	return m
}

// interruptOrQuit is the shared Esc / bare-Ctrl+C action: a two-stage interrupt
// (FR-14) that stops an in-flight run on the first press and stays in the
// program. When idle the first press only arms a quit (with a visible hint);
// a second press within quitArmWindow quits.
func (m Model) interruptOrQuit() (tea.Model, tea.Cmd) {
	if m.running {
		if m.interruptFn != nil {
			m.interruptFn()
		}
		m.transcript.addSystem("(interrupting the current run…)")
		return m, nil
	}
	if !m.quitArmedAt.IsZero() && time.Since(m.quitArmedAt) < quitArmWindow {
		m.shutdownRemote()
		m.quitting = true
		return m, tea.Quit
	}
	m.quitArmedAt = time.Now()
	m.transcript.addSystem("Press Ctrl+C again to quit.")
	return m, nil
}

// shutdownRemote stops the remote-control server on quit so the listener and
// WebSocket are released cleanly. A no-op when remote control is off or no
// session is bound.
func (m Model) shutdownRemote() {
	if m.session != nil {
		m.session.stopRemote()
	}
}

// feedInput forwards a message (a paste payload) to the editor, then refreshes
// the slash menu and re-lays out because inserted text can add lines (growing
// the editor) or begin a "/name". It is the shared tail of the paste handlers.
func (m Model) feedInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.menu.refresh(m.input.Value(), m.slash)
	m.relayout()
	return m, cmd
}

// pastePlaceholderRe matches the "[Pasted text #N +M lines]" tokens handlePaste
// leaves in the composer, capturing the id so expandPastes can swap the stored
// body back in at submit.
var pastePlaceholderRe = regexp.MustCompile(`\[Pasted text #(\d+) \+\d+ lines\]`)

// handlePaste inserts a pasted payload into the editor. A multi-line paste is
// collapsed to a compact "[Pasted text #N +M lines]" placeholder (the full body
// stashed in m.pastes for expansion at submit), so a large paste does not flood
// the composer — mirroring Claude Code. A single-line paste is inserted verbatim.
func (m Model) handlePaste(content string) (tea.Model, tea.Cmd) {
	if content == "" {
		return m, nil
	}
	if strings.Contains(content, "\n") {
		if m.pastes == nil {
			m.pastes = make(map[int]string)
		}
		m.pasteSeq++
		id := m.pasteSeq
		m.pastes[id] = content
		lines := strings.Count(content, "\n") + 1
		placeholder := fmt.Sprintf("[Pasted text #%d +%d lines]", id, lines)
		return m.feedInput(tea.PasteMsg{Content: placeholder})
	}
	return m.feedInput(tea.PasteMsg{Content: content})
}

// expandPastes replaces every paste placeholder in s with its stored body, so
// the submitted prompt carries the real pasted text rather than the compact
// token the user saw in the composer. An unknown id (e.g. the user edited the
// token) is left as-is. It returns s unchanged when no pastes are stashed.
func (m Model) expandPastes(s string) string {
	if len(m.pastes) == 0 {
		return s
	}
	return pastePlaceholderRe.ReplaceAllStringFunc(s, func(tok string) string {
		sm := pastePlaceholderRe.FindStringSubmatch(tok)
		id, err := strconv.Atoi(sm[1])
		if err != nil {
			return tok
		}
		if body, ok := m.pastes[id]; ok {
			return body
		}
		return tok
	})
}

// handleImagePaste stashes a pasted image (already saved to a temp PNG at path)
// and drops a compact "[Image #N]" placeholder into the composer, mirroring the
// text-paste placeholder. submit expands it into an "@image:<path>" reference so
// BuildUserContent attaches the image as multimodal content. An empty path falls
// back to a plain text read.
func (m Model) handleImagePaste(path string) (tea.Model, tea.Cmd) {
	if path == "" {
		return m, tea.ReadClipboard
	}
	if m.images == nil {
		m.images = make(map[int]string)
	}
	m.imageSeq++
	id := m.imageSeq
	m.images[id] = path
	placeholder := fmt.Sprintf("[Image #%d]", id)
	return m.feedInput(tea.PasteMsg{Content: placeholder})
}

// imagePlaceholderRe matches the "[Image #N]" tokens handleImagePaste leaves in
// the composer, capturing the id so expandImages can swap the stored temp path
// back in as an "@image:<path>" reference at submit.
var imagePlaceholderRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// expandImages replaces every image placeholder in s with an "@image:<path>"
// reference so BuildUserContent reads and attaches the pasted image. An unknown id
// (e.g. the user edited the token) is left as-is. It returns s unchanged when no
// images are stashed.
func (m Model) expandImages(s string) string {
	if len(m.images) == 0 {
		return s
	}
	return imagePlaceholderRe.ReplaceAllStringFunc(s, func(tok string) string {
		sm := imagePlaceholderRe.FindStringSubmatch(tok)
		id, err := strconv.Atoi(sm[1])
		if err != nil {
			return tok
		}
		if p, ok := m.images[id]; ok {
			return "@image:" + p
		}
		return tok
	})
}

// pumpNext re-issues waitForEvent for the in-flight run so the next bridged msg
// is pulled. It returns nil once the run has ended (runCh cleared), stopping the
// pump.
func (m Model) pumpNext() tea.Cmd {
	if m.running && m.runCh != nil {
		return waitForEvent(m.runCh)
	}
	return nil
}

// View implements tea.Model. It renders the shell on the alt-screen: the
// scrolling transcript filling the top rows, then the autocomplete popup (when
// open) and the multi-line input editor, and finally the persistent status bar
// (#386) on the very bottom row — below the input, per the layout fix. Setting
// AltScreen on the returned View is how Bubble Tea v2 enters/leaves the alternate
// screen buffer, so the user's scrollback is restored on quit.
func (m Model) View() tea.View {
	if m.quitting {
		return tea.View{AltScreen: true}
	}

	content, inputRow := m.renderContent()
	content = m.applySelection(content)

	// MouseModeCellMotion enables click/release/wheel events. Without it the
	// alt-screen swallows the wheel (no native scrollback), so history could only
	// be reached via PgUp/PgDn; enabling it lets the wheel scroll the transcript
	// and drives both scrollbar drag and mouse text selection.
	v := tea.View{Content: content, AltScreen: true, MouseMode: tea.MouseModeCellMotion}
	// Park the REAL terminal cursor on the input caret: this is the anchor the
	// IME candidate window follows (the old virtual cursor hid the real one, so
	// the candidate popup landed in the wrong place). inputRow counts the frame
	// rows above the editor; Cursor() already includes the wrapper's top rule.
	if cur := m.input.Cursor(); cur != nil {
		cur.Position.Y += inputRow
		if cur.Position.Y >= 0 && (m.height <= 0 || cur.Position.Y < m.height) {
			v.Cursor = cur
		}
	}
	return v
}

// renderContent builds the full-screen shell string (transcript, autocomplete
// popup, input editor, status bar) without any selection overlay. View wraps it
// with applySelection for display, and selectedText reuses it to extract the
// copied text from the exact rows the user sees. It also returns inputRow, the
// zero-based frame row where the input editor starts, so View can offset the
// real cursor onto the caret (the IME anchor).
func (m Model) renderContent() (string, int) {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	status := m.statusBar.Render(width)

	// The input editor renders its own prompt column and cursor across as many
	// rows as the buffer currently spans (up to maxInputRows).
	input := m.input.View()

	// Fallback transcript rows before the first size message; once sized the
	// viewport is pre-sized by relayout and pads its own content.
	rows := transcriptHeight(height)
	sized := m.width > 0 && m.height > 0

	var b strings.Builder
	inputRow := 0
	countRows := func(s string) int {
		if s == "" {
			return 0
		}
		return strings.Count(s, "\n") + 1
	}
	if sized {
		// The viewport pads its content to exactly the rows relayout reserved.
		tv := m.transcript.view()
		b.WriteString(tv)
		b.WriteByte('\n')
		inputRow += countRows(tv)
	} else {
		for i := 0; i < rows; i++ {
			b.WriteByte('\n')
		}
		inputRow += rows
	}
	// The working spinner sits on its own row just above the input while a run is
	// in flight (relayout reserves the row so the transcript shrinks to fit). The
	// sub-agent status panel, when any `task` sub-agents are live, renders on the
	// rows just ABOVE the spinner: one line each, elapsed refreshed every tick.
	if m.running {
		if panel := m.subagents.view(m.theme, width, time.Now()); panel != "" {
			b.WriteString(panel)
			b.WriteByte('\n')
			inputRow += countRows(panel)
		}
		if line := m.spinner.view(width); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
			inputRow += countRows(line)
		}
	}
	// The approval dialog takes precedence in the overlay slot: while a
	// decision is pending the composer is unusable, so the slash menu (if it
	// was open) is suppressed rather than stacked above it.
	if dialog := m.approvalView(width); dialog != "" {
		b.WriteString(dialog)
		b.WriteByte('\n')
		inputRow += countRows(dialog)
	} else if menu := m.menu.view(width); menu != "" {
		// The autocomplete popup renders just above the input line as an
		// overlay (it contributes no rows while idle, so the empty-shell
		// layout is unchanged).
		b.WriteString(menu)
		b.WriteByte('\n')
		inputRow += countRows(menu)
	}
	b.WriteString(input)
	b.WriteByte('\n')
	// The status bar is the final line, pinned to the very bottom of the shell
	// below the input editor.
	b.WriteString(status)
	return b.String(), inputRow
}

// applySelection overlays the mouse selection highlight onto the rendered
// content, inverting the selected cells like a terminal's own selection. Only
// rows the selection intersects are rewritten (as plain text with the span
// inverted); untouched rows keep their original coloring. It is a no-op when the
// selection is empty.
func (m Model) applySelection(content string) string {
	if m.sel.empty() {
		return content
	}
	start, end := m.sel.ordered()
	hi := lipgloss.NewStyle().Reverse(true)
	rows := strings.Split(content, "\n")
	if m.sel.below {
		// Legacy screen-row highlight for below-transcript selections.
		for y := start.y; y <= end.y && y < len(rows); y++ {
			if y < 0 {
				continue
			}
			c0, c1, ok := rowRange(start, end, y)
			if !ok {
				continue
			}
			rows[y], _ = selectRow(rows[y], c0, c1, hi)
		}
		return strings.Join(rows, "\n")
	}
	// Content-anchored highlight: map each in-range content line to its current
	// screen row (line minus viewport offset), skipping lines scrolled out of
	// view. Only the transcript's top vh rows are addressable this way.
	vh := m.transcript.viewportHeight()
	off := m.transcript.vp.YOffset()
	for line := start.y; line <= end.y; line++ {
		row := line - off
		if row < 0 || row >= vh || row >= len(rows) {
			continue
		}
		c0, c1 := 0, maxCol
		if line == start.y {
			c0 = start.x
		}
		if line == end.y {
			c1 = end.x
		}
		if c1 < c0 {
			c1 = c0
		}
		rows[row], _ = selectRow(rows[row], c0, c1, hi)
	}
	return strings.Join(rows, "\n")
}

// selectedText extracts the plain text under the current selection from the rows
// the user sees, joining rows with newlines and trimming each row's trailing
// padding so copied text has no ragged whitespace tail. It returns "" when the
// selection is empty.
func (m Model) selectedText() string {
	if m.sel.empty() {
		return ""
	}
	start, end := m.sel.ordered()
	// Content-anchored copy reads the transcript's full content lines (not just
	// the visible frame), so a selection spanning pages copies everything in
	// range. Indices clamp into the live content.
	if !m.sel.below {
		lines := m.transcript.contentLines()
		if len(lines) == 0 {
			return ""
		}
		a, b := start.y, end.y
		if a < 0 {
			a = 0
		}
		if b >= len(lines) {
			b = len(lines) - 1
		}
		var sb strings.Builder
		for line := a; line <= b; line++ {
			c0, c1 := 0, maxCol
			if line == a {
				c0 = start.x
			}
			if line == b {
				c1 = end.x
			}
			if c1 < c0 {
				c1 = c0
			}
			_, text := selectRow(lines[line], c0, c1, lipgloss.Style{})
			if line > a {
				sb.WriteByte('\n')
			}
			sb.WriteString(strings.TrimRight(text, " "))
		}
		return sb.String()
	}
	content, _ := m.renderContent()
	rows := strings.Split(content, "\n")
	var b strings.Builder
	wrote := false
	for y := start.y; y <= end.y && y < len(rows); y++ {
		if y < 0 {
			continue
		}
		c0, c1, ok := rowRange(start, end, y)
		if !ok {
			continue
		}
		_, text := selectRow(rows[y], c0, c1, lipgloss.Style{})
		if wrote {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(text, " "))
		wrote = true
	}
	return b.String()
}

// selScrollInterval is the edge-autoscroll heartbeat while a selection drag is
// pinned at the transcript edge; selScrollStep lines per tick (~28 lines/s)
// tracks a held drag without outrunning the user's reading speed.
const (
	selScrollInterval = 70 * time.Millisecond
	selScrollStep     = 2
)

// selScrollTick schedules one edge-autoscroll heartbeat for drag generation gen.
func selScrollTick(gen int) tea.Cmd {
	return tea.Tick(selScrollInterval, func(time.Time) tea.Msg {
		return selScrollTickMsg{gen: gen}
	})
}

// screenToSel maps a screen cell to selection coordinates: inside the
// transcript region it is the content-line index (viewport offset + row), so
// the endpoint survives scrolling; below the transcript it is the raw screen
// row with below=true (legacy single-page behavior).
func (m Model) screenToSel(x, y int) (point, bool) {
	if vh := m.transcript.viewportHeight(); vh > 0 && y >= 0 && y < vh {
		return point{x, m.transcript.vp.YOffset() + y}, false
	}
	return point{x, y}, true
}

// dragToSel maps a drag/shift/release cell for a live selection: a
// content-anchored selection dragged below the transcript — input rows, status
// bar, all the way to the window bottom — pins to the last content line
// (select-to-end) instead of flipping to the screen-row fallback. The whole
// area under the last transcript row is a scroll-down zone, as generous as the
// top edge. A selection that started below the transcript keeps legacy
// behavior and never scrolls the transcript out from under the composer.
func (m Model) dragToSel(x, y int) (point, bool) {
	if !m.sel.below {
		if vh := m.transcript.viewportHeight(); vh > 0 && y >= vh {
			if n := len(m.transcript.contentLines()); n > 0 {
				return point{maxCol, n - 1}, false
			}
		}
	}
	return m.screenToSel(x, y)
}

// updateSelAutoscroll (re)arms edge-autoscroll from the selection cursor: pinned
// at the transcript's top row it scrolls up, at its bottom row down, but only
// while the cursor is inside the transcript region (drags into the input/status
// rows never scroll) and only when there is overflow to move through. Leaving
// the edge — or a clamped end — stops the loop. It returns the tick command to
// chain, or nil.
func (m *Model) updateSelAutoscroll() tea.Cmd {
	dir := 0
	if vh := m.transcript.viewportHeight(); vh > 0 && m.transcript.overflowing() && !m.sel.below {
		// The cursor is a content line; its screen row is the line minus the
		// viewport offset. Below-transcript drags never autoscroll.
		y := m.sel.cursor.y - m.transcript.vp.YOffset()
		switch {
		case y <= 0:
			dir = -1
		case y >= vh-1:
			dir = +1
		}
	}
	if dir == 0 {
		m.selScrollDir = 0
		return nil
	}
	if dir != m.selScrollDir {
		m.selScrollDir = dir
		return selScrollTick(m.selScrollGen)
	}
	return nil
}

// relayout re-sizes the transcript to the rows left after reserving the status
// bar (1 row), the current input editor height, and any open autocomplete popup.
// It hands the transcript the full width; the transcript itself spends one column
// on the scrollbar only while its content overflows (see transcript.reflow), so a
// short conversation uses the whole width and shows no bar, while a scrolling one
// reserves the gutter — and that decision re-runs on every streamed line, not just
// on resize. It is called on every resize and after any edit that changes the
// input height or menu row count.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	rows := m.height - 1 - m.input.Height() - m.menu.rows() - m.approvalRowsHeight()
	if m.running {
		rows-- // the working spinner occupies the row just above the input
		// The sub-agent panel reserves one status row per live sub-agent, plus the
		// wrapped output lines of the expanded row (if any); an empty panel reserves
		// nothing so the single-run layout is unchanged.
		rows -= m.subagents.lineCount(m.width)
	}
	if rows < 0 {
		rows = 0
	}
	m.transcript.setSize(m.width, rows)
	m.input.SetWidth(m.width)
}

// onScrollbar reports whether the terminal cell (x, y) is the transcript's
// scrollbar: the rightmost column (relayout reserves m.width-1 for content, so
// the bar sits at column m.width-1) within the transcript's visible rows, which
// start at the top of the screen (row 0). It gates click-to-drag so presses in
// the body or on other chrome are left alone. When the content fits there is no
// bar (relayout reclaims the column), so it always returns false.
func (m Model) onScrollbar(x, y int) bool {
	if m.width <= 0 || !m.transcript.overflowing() {
		return false
	}
	h := m.transcript.viewportHeight()
	return x == m.width-1 && y >= 0 && y < h
}

// transcriptHeight returns the fallback number of rows for the transcript before
// the first size message arrives: the total minus the status bar and a single
// input row, floored at zero so tiny terminals never produce a negative extent.
func transcriptHeight(total int) int {
	h := total - 2
	if h < 0 {
		h = 0
	}
	return h
}
