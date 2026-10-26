package tui

// The approval dialog: the TUI's answer to "may this tool call run?".
//
// golder's TUI has no stdin for the REPL's y/N prompt, so a call the gate
// cannot settle on its own would otherwise fail closed. The dialog fills that
// gap: a compact prompt rendered just above the input line (same slot as the
// slash menu, so the transcript stays visible), answered with a single
// keystroke.
//
// Two questions remain. A capability request is the consent gate's one
// combined question: the rows grant the whole Needs set at one lifetime. A
// trust request is the trust manager's question about the folder; it is asked
// once at launch and, mid-run, when a tool first needs the folder.
//
// The dialog is modal: while it is open the model routes every key to it, so
// typing can not accidentally dismiss a decision. Exactly one dialog is open
// at a time, and a second call needing approval while one is pending waits in
// a queue (Model.approvalQueue) that opens the next dialog the moment the
// current one is answered — needed now that one assistant message can run
// several calls in parallel, each of which may ask. A queued request whose run
// ends unanswered is released fail-closed, so a gate never waits forever.

import (
	"strconv"
	"strings"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/trust"
)

// approvalRequestMsg is sent from the run goroutine to the UI when a tool call
// needs the user's decision. The run goroutine blocks on reply until the model
// answers (or the run is cancelled, which closes the dialog with Answered=false).
type approvalRequestMsg struct {
	req   judge.ApprovalRequest
	reply chan judge.ApprovalAnswer
}

// approvalRow is one option: a label, its single-key shortcut, and the value
// the dialog reports when it is chosen.
type approvalRow struct {
	label string
	key   string
	value string
}

// Consent-dialog values: one decision grants the whole Needs set at this
// lifetime.
const (
	approveDeny     = "deny"
	consentOnce     = "consent-once"
	consentSession  = "consent-session"
	consentRemember = "consent-remember"
)

// Trust-dialog values. approveOnce/approveSession/approveReadsHere are the
// mid-run trust gate's answers; trustRemember/trustOnce/trustReject are the
// first-run dialog's, which also has a persisted "don't trust" row.
const (
	approveOnce      = "once"
	approveSession   = "session"
	approveReadsHere = "reads-here"

	trustRemember = "trust"
	trustOnce     = "once"
	trustReject   = "reject"
)

// trustApprovalRows is the first-run trust question, in the same dialog shape.
//
// The keys follow the ladder every other dialog uses — y = just this once,
// p = this session, s = saved — because they used to mean something else here
// and the mismatch was the dangerous kind: this dialog had p for REMEMBER, so a
// person who had learned "p means this session" from the tool dialog wrote a
// decision to disk while meaning to try it out. A key whose meaning depends on
// which dialog is open is a key people get wrong, and the cost here is a
// permission they did not intend to keep.
//
// Esc stays the cancel key: "not now" (untrusted for this session, nothing
// saved), deliberately different from the persisted "don't trust" row.
var trustApprovalRows = []approvalRow{
	{"Trust this folder (remembered)", "s", trustRemember},
	{"Trust for this session", "p", trustOnce},
	{"Don't trust (remembered)", "n", trustReject},
}

// approvalDialog is the modal state. kind selects how a chosen value is
// applied: "tool" replies to the waiting run goroutine, "trust" runs the
// first-launch decision.
type approvalDialog struct {
	active bool
	kind   string
	// title and detail are the rendered header; rows are the options.
	title  string
	detail []string
	rows   []approvalRow
	// trustDir is the directory a trust question is about
	// (judge.ApprovalTrust); set only for those requests, and used to name the
	// folder in the transcript note that each session-scoped answer prints.
	trustDir string
	// reply is non-nil for a tool approval: the waiting gate reads the answer.
	reply chan judge.ApprovalAnswer
	// selected highlights a row, so ↑↓ and Enter work as well as the
	// single-key shortcuts (the two input styles share one row model).
	selected int
	// consent is set for the unified capability dialog; consentNeeds is the
	// exact set an approval grants.
	consent      bool
	consentNeeds []judge.Need
}

// openApproval arms the dialog for one tool request. The caller keeps the
// reply channel; answering sends exactly one value.
func (m *Model) openApproval(req judge.ApprovalRequest, reply chan judge.ApprovalAnswer) {
	m.menu.close()
	d := approvalDialog{
		active: true,
		kind:   "tool",
		title:  approvalTitle(req),
		detail: approvalDetail(req),
		reply:  reply,
	}
	if req.Kind == judge.ApprovalConsent {
		d.consent = true
		d.consentNeeds = req.Needs
		d.rows = consentApprovalRows(req.Needs)
	} else {
		d.rows = trustGateRows(req.Tool)
		d.trustDir = req.TrustDir
	}
	m.approval = d
}

// trustGateRows returns the rows a mid-run trust question offers, ordered by
// what each answer remembers. The labels cannot be the tool dialog's generic
// "Approve for this session": in a trust dialog that value relaxes one call
// shape, while here it is a grant for the whole directory, and the middle rung
// (local reads) exists only for the read tools - on a command it would be the
// same answer with a narrower-sounding name.
func trustGateRows(tool string) []approvalRow {
	allTools := approvalRow{"Allow all tools in this folder (this session)", "p", approveSession}
	deny := approvalRow{"Deny", "esc", approveDeny}
	if trust.LocalReadTools[tool] {
		return []approvalRow{
			{"Allow this read only", "y", approveOnce},
			{"Allow reads in this folder (this session)", "a", approveReadsHere},
			allTools,
			deny,
		}
	}
	return []approvalRow{
		{"Allow this once", "y", approveOnce},
		allTools,
		deny,
	}
}

// openTrustApproval arms the dialog for the first-run trust question, so the
// launch prompt and a mid-run approval look and behave the same. The chosen
// value is applied by applyTrustChoice.
func (m *Model) openTrustApproval() {
	m.menu.close()
	m.approval = approvalDialog{
		active: true,
		kind:   "trust",
		title:  "Do you trust this folder?",
		detail: approvalTrustDetail(m.cwd),
		rows:   trustApprovalRows,
	}
}

// answerApproval applies a chosen value and closes the dialog. A decision is
// delivered at most once: a second key press after the dialog closed is
// ignored by the active check. Answering a tool request also opens the next
// queued request (parallel calls can each need a decision), so the user works
// through them without the batch stalling.
func (m *Model) answerApproval(value string) {
	if !m.approval.active {
		return
	}
	d := m.approval
	if d.consent {
		m.approval = approvalDialog{}
		if value == approveDeny || value == "" {
			m.deliverApproval(d, judge.ApprovalAnswer{Answered: true})
			return
		}
		ans := judge.ApprovalAnswer{Answered: true, Approve: true, Needs: d.consentNeeds}
		switch value {
		case consentSession:
			ans.Scope = judge.ScopeSession
		case consentRemember:
			ans.Scope = judge.ScopeRemembered
		default:
			ans.Scope = judge.ScopeOnce
		}
		for _, n := range d.consentNeeds {
			if n.Cap == judge.CapUnconfined {
				ans.Exec = judge.ExecUnconfined
			}
		}
		m.deliverApproval(d, ans)
		return
	}
	m.approval = approvalDialog{}
	if d.kind == "trust" {
		if m.session != nil {
			// The row values (trust/once/reject) match decideTrust's cases
			// directly; "" is its cancel case.
			m.transcript.addSystem(m.session.decideTrust(m.opts, value))
		}
		return
	}
	// The mid-run trust gate's rows. A value outside this set is a denial:
	// Answered=true with Approve=false is an explicit "no", which the trust
	// gate records as such.
	ans := judge.ApprovalAnswer{Answered: true}
	switch value {
	case approveOnce:
		ans.Approve = true
	case approveSession:
		ans.Approve, ans.Always = true, true
		// In a trust question this answer reaches the whole directory, so say
		// so.
		if d.trustDir != "" && m.session != nil {
			m.transcript.addSystem("All tools in " + displayPath(d.trustDir) +
				" run without asking for this session.")
		}
	case approveReadsHere:
		// The gate registers the grant (it owns the trust manager); the answer
		// only names the intent. The note spells out the scope so "reads
		// allowed" is not read as "everything allowed".
		ans.Approve, ans.SessionReads = true, true
		if m.session != nil {
			where := displayPath(d.trustDir)
			if where == "" {
				where = "this folder"
			}
			m.transcript.addSystem("Reads in " + where +
				" will not ask again this session; commands and network reads still will.")
		}
	}
	m.deliverApproval(d, ans)
}

// consentApprovalRows is the single capability dialog. The keys describe the
// lifetime, never an individual subsystem: one answer covers every Need shown.
func consentApprovalRows(needs []judge.Need) []approvalRow {
	if len(needs) == 0 {
		return []approvalRow{
			{"Approve this call once", "y", consentOnce},
			{"Deny", "esc", approveDeny},
		}
	}
	// A lifetime longer than one call needs something that can actually be
	// recorded. Network is per call now, and an unconfined run is never
	// remembered, so a request whose only needs are those two offers once/deny.
	onlyOnce := false
	hasScopedPath := false
	for _, n := range needs {
		switch n.Cap {
		case judge.CapUnconfined:
			onlyOnce = true
		case judge.CapReadOutside, judge.CapWriteOutside:
			hasScopedPath = true
		}
	}
	if !hasScopedPath {
		onlyOnce = true
	}
	rows := []approvalRow{{"Allow this call once", "y", consentOnce}}
	if !onlyOnce {
		rows = append(rows,
			approvalRow{"Allow these scopes for this session", "a", consentSession},
			approvalRow{"Remember these scopes", "s", consentRemember},
		)
	}
	return append(rows, approvalRow{"Deny", "esc", approveDeny})
}

func consentNeedLabel(n judge.Need) string {
	switch n.Cap {
	case judge.CapReadOutside:
		return "read " + displayPath(n.Path)
	case judge.CapWriteOutside:
		return "write " + displayPath(n.Path)
	case judge.CapNetwork:
		if n.Key != "" {
			return "network " + n.Key
		}
		return "network access"
	case judge.CapUnconfined:
		return "run outside the sandbox"
	default:
		return "capability"
	}
}

// deliverApproval sends exactly one answer and advances the queue.
func (m *Model) deliverApproval(d approvalDialog, ans judge.ApprovalAnswer) {
	select {
	case d.reply <- ans:
	default:
		// The run was cancelled and nobody is listening; the dialog is already
		// closed, so the gate falls back to failing closed.
	}
	m.openNextQueuedApproval()
}

// openNextQueuedApproval opens the oldest queued approval request, if any. It
// is called after a decision is delivered so a batch of calls that each need
// approval is worked through one dialog at a time (the alternative — answering
// the later ones fail-closed — silently denied calls the user never saw).
func (m *Model) openNextQueuedApproval() {
	for len(m.approvalQueue) > 0 {
		next := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		// A request whose run already ended — cancelled while it waited, or
		// interrupted with the dialog open — must not open a dialog for a
		// decision nobody is waiting on: answer it fail-closed and move on.
		if !m.running {
			select {
			case next.reply <- judge.ApprovalAnswer{}:
			default:
			}
			continue
		}
		m.openApproval(next.req, next.reply)
		return
	}
}

// dismissApprovalQueue releases every queued request fail-closed and clears
// the queue. The run has ended: its gates are gone, so a queued dialog would
// ask about calls that can no longer run.
func (m *Model) dismissApprovalQueue() {
	for _, req := range m.approvalQueue {
		select {
		case req.reply <- judge.ApprovalAnswer{}:
		default:
		}
	}
	m.approvalQueue = nil
}

// displayPath shortens a path under $HOME to "~/…" for the row label; the
// grant itself always uses the full path.
func displayPath(p string) string {
	return cli.DisplayHomePath(p)
}

// approvalKey handles one key press while the dialog is open and reports
// whether it consumed the key. Every key except Ctrl+C is consumed while the
// dialog is active (typing must not leak into the composer behind a pending
// decision). Ctrl+C is never swallowed: it falls through to the shell's
// interrupt/quit path, cancelling the run mid-flight — which resolves the
// dialog fail-closed via context cancellation — or arming quit while idle.
// A pending decision must never make the keyboard unresponsive.
func (m *Model) approvalKey(key string) bool {
	if !m.approval.active {
		return false
	}
	if key == "ctrl+c" {
		return false
	}
	switch key {
	case "up":
		m.approval.selected--
		if m.approval.selected < 0 {
			m.approval.selected = len(m.approval.rows) - 1
		}
	case "down":
		m.approval.selected++
		if m.approval.selected >= len(m.approval.rows) {
			m.approval.selected = 0
		}
	case "enter":
		m.answerApproval(m.approval.rows[m.approval.selected].value)
	case "esc":
		if m.approval.kind == "trust" {
			// Cancel: untrusted for this session, nothing persisted. An empty
			// value is decideTrust's "no answer" case.
			m.answerApproval("")
		} else {
			m.answerApproval(approveDeny)
		}
	default:
		// Single-key shortcuts: match the row whose key equals the press
		// (case-insensitive so a shifted key still works).
		for _, row := range m.approval.rows {
			if strings.EqualFold(row.key, key) {
				m.answerApproval(row.value)
				break
			}
		}
	}
	return true
}

// approvalView renders the dialog in the slash-menu slot: a title, the call
// preview, the reviewer's reasoning, and the options with their keys.
func (m Model) approvalView(width int) string {
	if !m.approval.active {
		return ""
	}
	rowWidth := width - 2
	if rowWidth < 1 {
		rowWidth = width
	}
	dim := func(s string) string {
		return TruncateToWidth(m.theme.System.Render("  "+TruncateToWidth(s, rowWidth)), width)
	}
	var lines []string
	lines = append(lines, TruncateToWidth(m.theme.MenuHeader.Render("  "+TruncateToWidth(m.approval.title, rowWidth)), width))
	for _, d := range m.approval.detail {
		lines = append(lines, dim(d))
	}
	for i, row := range m.approval.rows {
		tag := "  (" + row.key + ")"
		line := TruncateToWidth(row.label, max(0, width-2-len(tag)))
		if i == m.approval.selected {
			lines = append(lines, TruncateToWidth(m.theme.Accent.Render("› "+line)+m.theme.System.Render(tag), width))
		} else {
			lines = append(lines, TruncateToWidth(m.theme.System.Render("  "+line)+m.theme.System.Render(tag), width))
		}
	}
	return strings.Join(lines, "\n")
}

// approvalTitle is the dialog's headline, phrased by why the call needs an
// answer so the user knows what they are weighing.
func approvalTitle(req judge.ApprovalRequest) string {
	tool := req.Tool
	if tool == "" {
		tool = "tool"
	}
	// A question a sub-agent asked says so, before anything else: the person is
	// being asked to consent to a call their own conversation did not make, and
	// "who is asking" is the first thing that decides how they read the rest.
	if req.Origin != "" {
		tool = tool + " (from " + req.Origin + ")"
	}
	switch req.Kind {
	case judge.ApprovalConsent:
		n := len(req.Needs)
		switch n {
		case 0:
			return tool + " needs approval"
		case 1:
			return tool + " needs 1 capability"
		default:
			return tool + " needs " + strconv.Itoa(n) + " capabilities"
		}
	case judge.ApprovalTrust:
		return tool + " in an untrusted folder"
	default:
		return tool + " needs approval"
	}
}

// approvalDetail renders the body lines above the options for a tool approval:
// the command preview, the model's justification, and the reviewer's verdict.
func approvalDetail(req judge.ApprovalRequest) []string {
	// The decision line leads: tool · mode · grade → who settles it. The mode is
	// the fact a person forgets between two dialogs — the same "confirm" is
	// theirs to answer under ask and already decided under auto — so it is
	// printed above the command, in the same words the notes and /permissions
	// use.
	out := []string{judge.DecisionLineWithSource(req.Tool, req.Mode, req.Level, req.Source, judge.AskDisposition(req.Kind, "en"))}
	if req.Kind == judge.ApprovalConsent {
		for _, n := range req.Needs {
			out = append(out, "• "+consentNeedLabel(n))
		}
	}
	if req.Origin != "" {
		// Named before the preview, because the preview is a file tool's path or
		// a shell command, and both read differently when a delegated child
		// chose them.
		out = append(out, "Requested by "+req.Origin)
	}
	if s := strings.TrimSpace(req.Summary); s != "" {
		lines := wrapLines(s, 76)
		if len(lines) > 3 { // keep the dialog compact: preview, not the script
			lines = append(lines[:3], "…")
		}
		for _, l := range lines {
			out = append(out, "$ "+l)
		}
	}
	if j := strings.TrimSpace(req.Justification); j != "" {
		out = append(out, "Reason: "+j)
	}
	if r := strings.TrimSpace(req.Rationale); r != "" {
		if req.Kind == judge.ApprovalTrust {
			out = append(out, r)
		} else {
			out = append(out, "Reviewer: "+r)
		}
	}
	return out
}

// approvalTrustDetail explains the first-run trust question, naming both halves
// of what an untrusted directory costs: side-effect tools ask, and under the
// fork's StrictReads default so do the read tools.
func approvalTrustDetail(cwd string) []string {
	return []string{
		cwd,
		"golder runs side-effect tools (bash, edit) and reads files (read, grep, webfetch) here.",
		"Without trust, all of them ask first; project hooks load only when trusted.",
	}
}

// wrapLines soft-wraps s into lines of at most width runes, breaking on
// whitespace where possible. It is the dialog's own helper: the transcript's
// markdown wrapper is far heavier than a prompt preview needs.
func wrapLines(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		rest := strings.TrimSpace(paragraph)
		for rest != "" {
			if len(rest) <= width {
				out = append(out, rest)
				break
			}
			cut := strings.LastIndex(rest[:width], " ")
			if cut <= 0 {
				cut = width
			}
			out = append(out, strings.TrimRight(rest[:cut], " "))
			rest = strings.TrimLeft(rest[cut:], " ")
		}
	}
	return out
}

// approvalRowsHeight is how many rows the dialog occupies, so relayout can
// reserve them (the same contract as the slash menu).
func (m Model) approvalRowsHeight() int {
	if !m.approval.active {
		return 0
	}
	return 1 + len(m.approval.detail) + len(m.approval.rows)
}
