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
	"strings"

	"github.com/getan/golder/internal/judge"
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
	approveOnce    = "once"
	approveSession = "session"
	approveDeny    = "deny"
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
	// reply is non-nil for a tool approval: the waiting gate reads the answer.
	reply chan judge.ApprovalAnswer
	// selected highlights a row, so ↑↓ and Enter work as well as the
	// single-key shortcuts (the two input styles share one row model).
	selected int
}

// openApproval arms the dialog for one tool request. The caller keeps the
// reply channel; answering sends exactly one value.
func (m *Model) openApproval(req judge.ApprovalRequest, reply chan judge.ApprovalAnswer) {
	m.approval = approvalDialog{
		active: true,
		kind:   "tool",
		title:  approvalTitle(req),
		detail: approvalDetail(req),
		rows:   toolApprovalRows,
		reply:  reply,
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
		}
		select {
		case d.reply <- ans:
		default:
			// The run was cancelled and nobody is listening; the dialog is
			// already closed, so the gate falls back to failing closed.
		}
	}
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
