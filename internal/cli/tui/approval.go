package tui

// The approval dialog: the TUI's answer to "may this tool call run?".
//
// golder's TUI has no stdin for the REPL's y/N prompt, so a call the
// permission gate cannot settle on its own used to fail closed. This dialog
// fills that gap the way codex does: a compact prompt rendered just above the
// input line (same slot as the slash menu, so the transcript stays visible),
// answered with a single keystroke.
//
// The dialog is modal: while it is open the model routes every key to it, so
// typing can not accidentally dismiss a decision. Exactly one dialog is open
// at a time; a second call needing approval while one is pending fails closed
// (the gate sees an unanswered request) rather than queueing behind a
// possibly-interrupted run.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/seatbelt"
)

// approvalRequestMsg is sent from the run goroutine to the UI when a tool call
// needs the user's decision. The run goroutine blocks on reply until the model
// answers (or the run is cancelled, which closes the dialog with Answered=false).
type approvalRequestMsg struct {
	req   judge.ApprovalRequest
	reply chan judge.ApprovalAnswer
}

// approvalRow is one option: a label, its single-key shortcut, and the value
// the dialog reports when it is chosen. Values are interpreted per dialog kind
// ("tool" vs "trust").
type approvalRow struct {
	label string
	key   string
	value string
}

// Tool-approval values.
const (
	approveOnce     = "once"
	approveSession  = "session"
	approveWritable = "writable"
	approveDeny     = "deny"
)

// Trust-question values (the first-run dialog; the same three-way choice the
// REPL's prompt offers).
const (
	trustRemember = "trust"
	trustOnce     = "once"
	trustReject   = "reject"
)

// toolApprovalRows mirrors codex: y approves once, p approves for the session
// (remembered per tool+command), Esc denies.
var toolApprovalRows = []approvalRow{
	{"Approve once", "y", approveOnce},
	{"Approve for this session", "p", approveSession},
	{"Deny", "esc", approveDeny},
}

// trustApprovalRows is the first-run trust question, in the same dialog shape:
// p remembers the decision, y trusts just this session, n remembers distrust.
// Esc is the dialog's cancel key: it means "not now" (untrusted for this
// session, nothing saved), which is deliberately different from the persisted
// "don't trust" row.
var trustApprovalRows = []approvalRow{
	{"Trust this folder", "p", trustRemember},
	{"Trust once (this session only)", "y", trustOnce},
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
	// writableRoot is the escalation path the "allow writes" row grants; set
	// only when that row is present.
	writableRoot string
	// reply is non-nil for a tool approval: the waiting gate reads the answer.
	reply chan judge.ApprovalAnswer
	// selected highlights a row, so ↑↓ and Enter work as well as the
	// single-key shortcuts (the two input styles share one row model).
	selected int
}

// openApproval arms the dialog for one tool request. The caller keeps the
// reply channel; answering sends exactly one value.
func (m *Model) openApproval(req judge.ApprovalRequest, reply chan judge.ApprovalAnswer) {
	root := inferWritableRoot(req, m.cwd)
	rows := toolApprovalRows
	if root != "" {
		// The escalation's blocker is a path the sandbox denies: offer to
		// re-admit just that path (read+write, this session) instead of
		// unisolating the whole command. Inserted before Deny so Esc keeps
		// its meaning and the negative option stays last.
		rows = []approvalRow{
			toolApprovalRows[0],
			toolApprovalRows[1],
			{"Allow writes to " + displayPath(root) + " (this session)", "w", approveWritable},
			toolApprovalRows[2],
		}
	}
	m.approval = approvalDialog{
		active:       true,
		kind:         "tool",
		title:        approvalTitle(req),
		detail:       approvalDetail(req),
		rows:         rows,
		writableRoot: root,
		reply:        reply,
	}
}

// openTrustApproval arms the dialog for the first-run trust question, so the
// launch prompt and a mid-run approval look and behave the same. The chosen
// value is applied by applyTrustChoice.
func (m *Model) openTrustApproval() {
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
// ignored by the active check.
func (m *Model) answerApproval(value string) {
	if !m.approval.active {
		return
	}
	d := m.approval
	m.approval = approvalDialog{}
	switch d.kind {
	case "trust":
		if m.session != nil {
			// The row values (trust/once/reject) match decideTrust's cases
			// directly; "" is its cancel case.
			m.transcript.addSystem(m.session.decideTrust(m.opts, value))
		}
	default:
		ans := judge.ApprovalAnswer{Answered: true}
		switch value {
		case approveOnce:
			ans.Approve = true
		case approveSession:
			ans.Approve, ans.Always = true, true
		case approveWritable:
			// Register the path first; only a successful registration turns
			// the choice into an approval. On failure the call is denied (the
			// gate fails closed on an explicit negative), never silently run
			// unisolated.
			if root := d.writableRoot; root != "" {
				if _, err := seatbelt.AddWritableRoot(root); err != nil {
					if m.session != nil {
						m.transcript.addSystem("Cannot grant write access to " + root + ": " + err.Error())
					}
				} else {
					ans.Approve, ans.WritableRoot = true, root
					if m.session != nil {
						m.transcript.addSystem("Writes to " + displayPath(root) +
							" are allowed inside the sandbox for this session. Persist it with: /permissions writable add " + root)
					}
				}
			}
		}
		select {
		case d.reply <- ans:
		default:
			// The run was cancelled and nobody is listening; the dialog is
			// already closed, so the gate falls back to failing closed.
		}
	}
}

// inferWritableRoot picks the path an escalation is trying to reach, or ""
// when there is no confident candidate (the row is then hidden — a guess
// would grant access the user did not choose). Only paths outside what the
// sandbox already writes (the project, temp dirs, the system runtime and
// golder's own state) and not already granted qualify.
func inferWritableRoot(req judge.ApprovalRequest, cwd string) string {
	p := escalationPathCandidate(req, cwd)
	if p == "" {
		return ""
	}
	for _, granted := range seatbelt.WritableRoots() {
		if coveredBy(p, granted) {
			return ""
		}
	}
	return p
}

// escalationPathCandidate extracts the first plausible path token from an
// escalation request. Per-command grants feed both the dialog row and the
// "already granted" shortcut in confirmApproval, so the parsing lives here.
func escalationPathCandidate(req judge.ApprovalRequest, cwd string) string {
	if req.Kind != judge.ApprovalEscalation {
		return ""
	}
	for _, raw := range append(pathTokens(req.Summary), pathTokens(req.Justification)...) {
		p, err := seatbelt.NormalizeWritableRoot(raw)
		if err != nil {
			continue
		}
		if plausibleWritableRoot(p, cwd) {
			return p
		}
	}
	return ""
}

// pathTokens splits s into shell-ish tokens that name an absolute or ~ path.
// It is deliberately lexical: quotes, =, ':' and shell punctuation break
// tokens, and a surviving token must start with "/" or "~/". Commands are
// free-form, so a missed path only hides the row (fail closed), never grants.
func pathTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '\'', '"', '`', '=', ':', '(', ')', ',', ';':
			return true
		}
		return false
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimLeft(f, ">|<")
		f = strings.TrimRight(f, ".,;:)]}")
		if strings.HasPrefix(f, "~/") || (strings.HasPrefix(f, "/") && f != "/") {
			out = append(out, f)
		}
	}
	return out
}

// plausibleWritableRoot filters the candidates whose grant would be
// meaningless or dangerous: paths the command can already write, the system
// runtime (read-only by design), and golder's own state.
func plausibleWritableRoot(p, cwd string) bool {
	inside := func(dir string) bool {
		return dir != "" && coveredBy(p, dir)
	}
	if inside(cwd) {
		return false
	}
	for _, dir := range []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp"} {
		if inside(dir) {
			return false
		}
	}
	for _, dir := range []string{
		"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/etc",
		"/System", "/Library", "/Applications", "/dev", "/proc", "/var",
	} {
		if inside(dir) {
			return false
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if inside(filepath.Join(home, ".golder")) || p == home {
			return false
		}
	}
	if gh := strings.TrimSpace(os.Getenv("GOLDER_HOME")); gh != "" && inside(gh) {
		return false
	}
	return true
}

// coveredBy reports whether p is root itself or lives under it.
func coveredBy(p, root string) bool {
	if root == "" {
		return false
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// displayPath shortens a path under $HOME to "~/…" for the row label; the
// grant itself always uses the full path.
func displayPath(p string) string {
	return cli.DisplayHomePath(p)
}

// approvalKey handles one key press while the dialog is open and reports
// whether it consumed the key. Every key is consumed while the dialog is
// active (typing must not leak into the composer behind a pending decision).
func (m *Model) approvalKey(key string) bool {
	if !m.approval.active {
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
		return m.theme.System.Render("  " + TruncateToWidth(s, rowWidth))
	}
	var lines []string
	lines = append(lines, m.theme.MenuHeader.Render("  "+TruncateToWidth(m.approval.title, rowWidth)))
	for _, d := range m.approval.detail {
		lines = append(lines, dim(d))
	}
	for i, row := range m.approval.rows {
		line := TruncateToWidth(row.label, rowWidth-2)
		tag := "  (" + row.key + ")"
		if i == m.approval.selected {
			lines = append(lines, m.theme.Accent.Render("› "+line)+m.theme.System.Render(tag))
		} else {
			lines = append(lines, m.theme.System.Render("  "+line)+m.theme.System.Render(tag))
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
	switch req.Kind {
	case judge.ApprovalNoSandbox:
		return tool + " needs approval (sandbox unavailable — approving runs it unsandboxed)"
	case judge.ApprovalReviewFailed:
		return tool + " needs approval (automatic review was unavailable)"
	case judge.ApprovalEscalation:
		return tool + " asks to run outside the sandbox (reviewer graded it sandbox)"
	default:
		return tool + " needs approval"
	}
}

// approvalDetail renders the body lines above the options for a tool approval:
// the command preview, the model's justification, and the reviewer's verdict.
func approvalDetail(req judge.ApprovalRequest) []string {
	var out []string
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
		rating := ""
		if req.Risk != "" || req.Authorization != "" {
			rating = " (" + req.Risk + ", auth: " + req.Authorization + ")"
		}
		out = append(out, "Reviewer: "+r+rating)
	}
	return out
}

// approvalTrustDetail explains the first-run trust question.
func approvalTrustDetail(cwd string) []string {
	return []string{
		cwd,
		"golder runs side-effect tools (bash, edit) here; project hooks load only when trusted.",
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
