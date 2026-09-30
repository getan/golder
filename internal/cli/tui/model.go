package tui

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/memstatus"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/cli/status"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/memory"
	"github.com/smallnest/pigo/internal/runtime"
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
	// model persists the conversation to ~/.pigo/sessions after each turn ends.
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
// prompt-template dirs (~/.pigo/{commands,prompts}) and the pre-loaded skills;
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
	m.transcript.addBanner(renderBanner(m.theme, m.opts, m.cwd))
	seedTranscript(&m.transcript, history)
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
	if len(m.live.FetchedModels) > 0 {
		m.menu.openPicker(m.live.FetchedModels, m.live.Model)
		m.transcript.addSystem("Select a model (↑↓ + Enter, Esc cancels):")
		m.relayout()
		return m, nil
	}
	m.transcript.addSystem("Fetching models…")
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
	m.menu.openPicker(msg.ids, current)
	m.transcript.addSystem("Select a model (↑↓ + Enter, Esc cancels):")
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
	m.transcript.addBanner(renderBanner(m.theme, m.opts, m.cwd))
	seedTranscript(&m.transcript, msgs)
	m.transcript.addSystem(fmt.Sprintf("Resumed session %s (%s).", id, m.live.Model))
	return m, nil
}

// Init implements tea.Model. It kicks off the async git probe so the status bar
// can show the branch/dirty state as soon as it resolves; the alt-screen is
// requested declaratively via the AltScreen field on the View returned by View.
func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchGitCmd(m.cwd), m.input.Focus(), func() tea.Msg {
		return tea.RequestBackgroundColor()
	})
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
			if m.onScrollbar(msg.X, msg.Y) {
				m.draggingScrollbar = true
				m.transcript.scrollToRow(msg.Y)
				return m, nil
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

	case toolStartMsg:
		// Create a rich tool-call card, index it by id for the later end event, and
		// append it as an ordered transcript block so it renders inline (#389).
		card := &toolCard{id: msg.id, name: msg.name, input: msg.input, state: cardRunning}
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
			if msg.ok {
				card.state = cardSuccess
			} else {
				card.state = cardWarn
			}
			// A tool that reported a diff (edit) gets a colored Diff section; the
			// diff is also embedded in the result text, so strip it there to keep
			// the card from showing the change twice (#560).
			result := msg.result
			if diff, ok := ui.DiffFromDetails(msg.details); ok {
				card.diff = diff
				result = stripDiffTail(result)
			}
			card.response = parseToolResult(result)
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
			m.session.compacted = true
		}
		m.transcript.addSystem("(context compacted)")
		return m, m.pumpNext()

	case rebuildDoneMsg:
		// A manual /rebuild finished: clear the pinned "Preparing conversation
		// context…" spinner (no run is pumping, so stop it and drop out of the
		// running state) and report the outcome. rebuild() already applied the
		// rebuilt messages and set session.compacted on success.
		m.spinner.unpin()
		m.spinner.stop()
		m.running = false
		if msg.err != nil {
			m.transcript.addSystem("rebuild failed: " + msg.err.Error() + " (context left unchanged)")
		} else {
			m.transcript.addSystem(msg.summary)
		}
		m.relayout()
		return m, nil

	case runEndMsg:
		m.running = false
		m.runCh = nil
		m.spinner.stop()
		// The run is over: any still-open sub-agent rows are stale (their tasks ended
		// with the run), so clear the panel to reclaim its height.
		m.subagents = subagentPanel{}
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
			if err := m.session.persist(); err != nil {
				m.transcript.addSystem("Session save failed: " + err.Error())
			}
		}
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
		// clearing it afterward; with no selection it keeps its interrupt-or-quit
		// role. Copying works even mid-run, so grabbing streamed output never
		// interrupts the run.
		if !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			if text != "" {
				return m, tea.SetClipboard(text)
			}
			return m, nil
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
	raw := strings.TrimSpace(m.input.Value())
	prompt := strings.TrimSpace(m.expandImages(m.expandPastes(m.input.Value())))
	if prompt == "" {
		return m, nil
	}
	// Record the input (as typed) into the browse history, then exit browse mode.
	m.recordHistory(raw)
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
	// /exit and /quit terminate the TUI, mirroring the REPL loop which intercepts
	// them before slash resolution. They register only as no-op /help builtins, so
	// without this the registry would resolve them to an empty action.
	if line == "/exit" || line == "/quit" {
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
	// /memory is intercepted before registry resolution (like /rebuild): it
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
	// /session is intercepted before registry resolution (like /memory): it prints
	// the conversation summary (session id, message count, estimated tokens, model/
	// provider, created time, compaction count) from the live context — state a
	// slash Action closure cannot reach. The rendering is shared with the REPL.
	if line == "/session" {
		m.transcript.addUser(line)
		m.input.Clear()
		m.menu.close()
		if m.session == nil {
			m.transcript.addSystem("(session unavailable: no active session)")
			m.relayout()
			return m, nil
		}
		var buf bytes.Buffer
		m.session.renderSession(&buf)
		m.transcript.addSystem(strings.TrimRight(buf.String(), "\n"))
		m.relayout()
		return m, nil
	}
	// /rebuild is intercepted before registry resolution (like /exit): it
	// reconstructs the shared context from a persisted checkpoint (or falls back
	// to compaction) and replaces the message list in place — work a slash Action
	// closure cannot do. It reuses the compacting-indicator: the spinner is armed
	// and pinned to "Preparing conversation context…" while the rebuild runs off
	// the tea loop, and rebuildDoneMsg clears it and reports the result.
	if line == "/rebuild" {
		m.transcript.addUser(line)
		m.input.Clear()
		m.menu.close()
		if m.session == nil {
			m.transcript.addSystem("(rebuild unavailable: no active session)")
			m.relayout()
			return m, nil
		}
		m.spinner.begin(time.Now(), m.thinkingLabel())
		m.spinner.pin("Preparing conversation context")
		m.running = true
		m.relayout()
		return m, tea.Batch(m.session.rebuildCmd(), m.tickSpinner())
	}
	// /remote-control is intercepted before registry resolution (like /rebuild):
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

// recordHistory appends an submitted input to the browse history (skipping a
// consecutive duplicate, like a shell) and resets the browse cursor to the live
// draft, so the next ↑ starts from the most recent entry and any stashed draft is
// dropped. A blank entry is never stored.
func (m *Model) recordHistory(entry string) {
	entry = strings.TrimSpace(entry)
	if entry != "" && (len(m.history) == 0 || m.history[len(m.history)-1] != entry) {
		m.history = append(m.history, entry)
	}
	m.histIdx = len(m.history)
	m.histDraft = ""
}

// historyPrev recalls the previous submitted input into the composer, but only
// when the caret is on the first line — otherwise ↑ moves the caret within a
// multi-line draft. The first recall stashes the live draft so historyNext can
// restore it, and the cursor lands past the newest entry (len(history)) initially.
func (m Model) historyPrev(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if len(m.history) == 0 || m.input.Line() != 0 {
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

	content := m.applySelection(m.renderContent())

	// MouseModeCellMotion enables click/release/wheel events. Without it the
	// alt-screen swallows the wheel (no native scrollback), so history could only
	// be reached via PgUp/PgDn; enabling it lets the wheel scroll the transcript
	// and drives both scrollbar drag and mouse text selection.
	return tea.View{Content: content, AltScreen: true, MouseMode: tea.MouseModeCellMotion}
}

// renderContent builds the full-screen shell string (transcript, autocomplete
// popup, input editor, status bar) without any selection overlay. View wraps it
// with applySelection for display, and selectedText reuses it to extract the
// copied text from the exact rows the user sees.
func (m Model) renderContent() string {
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
	if sized {
		// The viewport pads its content to exactly the rows relayout reserved.
		b.WriteString(m.transcript.view())
		b.WriteByte('\n')
	} else {
		for i := 0; i < rows; i++ {
			b.WriteByte('\n')
		}
	}
	// The working spinner sits on its own row just above the input while a run is
	// in flight (relayout reserves the row so the transcript shrinks to fit). The
	// sub-agent status panel, when any `task` sub-agents are live, renders on the
	// rows just ABOVE the spinner: one line each, elapsed refreshed every tick.
	if m.running {
		if panel := m.subagents.view(m.theme, width, time.Now()); panel != "" {
			b.WriteString(panel)
			b.WriteByte('\n')
		}
		if line := m.spinner.view(width); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	// The autocomplete popup, when open, renders just above the input line as an
	// overlay (it contributes no rows while idle, so the empty-shell layout is
	// unchanged).
	if menu := m.menu.view(width); menu != "" {
		b.WriteString(menu)
		b.WriteByte('\n')
	}
	b.WriteString(input)
	b.WriteByte('\n')
	// The status bar is the final line, pinned to the very bottom of the shell
	// below the input editor.
	b.WriteString(status)
	return b.String()
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
	rows := strings.Split(m.renderContent(), "\n")
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
	rows := m.height - 1 - m.input.Height() - m.menu.rows()
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
