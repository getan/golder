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
// at a time, and a second call needing approval while one is pending waits in
// a queue (Model.approvalQueue) that opens the next dialog the moment the
// current one is answered — needed now that one assistant message can run
// several calls in parallel, each of which may ask. A queued request whose run
// ends unanswered is released fail-closed, so a gate never waits forever.

import (
	"strconv"
	"strings"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/grants"
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
// the dialog reports when it is chosen. Values are interpreted per dialog kind
// ("tool" vs "trust").
type approvalRow struct {
	label string
	key   string
	value string
}

// Tool-approval values.
const (
	approveOnce            = "once"
	approveSession         = "session"
	approveWritable        = "writable"
	approveWritablePersist = "writable-persist"
	approveReadDir         = "read-dir"
	approveReadPersist     = "read-persist"
	approveWriteDir        = "write-dir"
	approveWritePersist    = "write-persist"
	approveReadsHere       = "reads-here"
	approveDeny            = "deny"
	// approveUnconfined is the denial dialog's "run this one command outside the
	// sandbox" answer. It is a one-call decision, which is why it has no session
	// or remembered twin.
	approveUnconfined = "unconfined"
	// Network answers are separate values because they carry a lifetime: a
	// one-call grant must not be confused with a session or remembered rule.
	approveNetwork         = "network-once"
	approveNetworkSession  = "network-session"
	approveNetworkRemember = "network-remember"
	// approveContained is the escalation dialog's default: keep the call in the
	// sandbox. approveDirect is the deliberate second answer: run this one call
	// outside it. The two are separate values because they are separate
	// decisions — a reader who wants the command to run should not have to
	// choose "leave the sandbox" to get it.
	approveContained = "contained"
	approveDirect    = "direct"
	// Consent answers: one decision grants the whole Needs set at this scope.
	consentOnce     = "consent-once"
	consentSession  = "consent-session"
	consentRemember = "consent-remember"
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
	// writableRoot is the escalation path the "allow writes" row grants; set
	// only when that row is present.
	writableRoot string
	// readRoot is the directory the "allow reading" row grants; set only when
	// that row is present (a read-scope request whose candidate directory is
	// not too broad to hand over).
	readRoot string
	// writeRoot is the directory the "allow writing" rows grant; set only when
	// those rows are present (a boundary request about a write, whose candidate
	// directory is not too broad to hand over).
	writeRoot string
	// trustDir is the directory a trust question is about (judge.ApprovalTrust);
	// set only for those requests, and used to name the folder in the
	// transcript note that each session-scoped answer prints.
	trustDir string
	// reply is non-nil for a tool approval: the waiting gate reads the answer.
	reply chan judge.ApprovalAnswer
	// combined marks a request that also asks the network question. The dialog
	// stays open across both stages: stage 0 chooses the path answer, stage 1
	// chooses network access, and only then is one ApprovalAnswer delivered.
	combined            bool
	stage               int
	pathValue           string
	networkKey          string
	networkRememberable bool
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
	if req.Kind == judge.ApprovalConsent {
		m.approval = approvalDialog{
			active:       true,
			kind:         "tool",
			consent:      true,
			consentNeeds: req.Needs,
			title:        approvalTitle(req),
			detail:       approvalDetail(req),
			rows:         consentApprovalRows(req.Needs),
			reply:        reply,
		}
		return
	}
	root := inferWritableRoot(req, m.cwd)
	rows := toolApprovalRows
	if req.Kind == judge.ApprovalTrust {
		rows = trustGateRows(req.Tool)
	}
	if req.Kind == judge.ApprovalEscalation {
		// An escalation's default answer is containment, not escape: the
		// reviewer already graded the call runnable inside the sandbox, so
		// "keep it there" is the safe row and it leads (y, and Enter on the
		// default selection). Leaving the sandbox stays possible — the reviewer
		// is not always right — but it is a deliberate second key whose label
		// says what it costs. There is no "for this session" row: a remembered
		// escape is not a decision to make in one keystroke.
		rows = []approvalRow{{"Run it in the sandbox (recommended)", "y", approveContained}}
		if root != "" {
			rows = append(rows,
				approvalRow{"Allow writes to " + displayPath(root) + " (this session)", "w", approveWritable},
				approvalRow{"Always allow writes to " + displayPath(root) + " (remembered)", "s", approveWritablePersist},
			)
		}
		rows = append(rows,
			approvalRow{"Run it outside the sandbox anyway", "e", approveDirect},
			approvalRow{"Deny", "esc", approveDeny},
		)
	}
	if req.Kind == judge.ApprovalNetwork {
		// The network question used to fall through to the generic tool rows,
		// so its title was "needs approval" and the answer had no lifetime. The
		// key names exactly what a standing answer would cover; when it is empty
		// (a local host, an IP, a command the parser cannot pin down) only the
		// one-call answer is offered.
		key := strings.TrimSpace(req.NetworkKey)
		rows = []approvalRow{{"Allow network for this call only", "y", approveNetwork}}
		if key != "" && req.NetworkRememberable {
			rows = append(rows,
				approvalRow{"Allow " + key + " for this session", "a", approveNetworkSession},
				approvalRow{"Always allow " + key + " (remembered)", "s", approveNetworkRemember},
			)
		}
		rows = append(rows, approvalRow{"Deny network", "esc", approveDeny})
	}
	// The boundary question is about reading or about writing, and the rows say
	// which: the two carry different grants ("allow reading /x" is not "allow
	// writing /x"), so showing the read rows for a write question would offer a
	// keystroke that does not do what it says. The gate names the side in
	// BoundaryWrite; everything else about the dialog is shared.
	boundaryDir := strings.TrimSpace(req.BoundaryDir)
	readRoot, writeRoot := boundaryDir, ""
	if req.Kind == judge.ApprovalBoundary && req.BoundaryWrite {
		readRoot, writeRoot = "", boundaryDir
	}
	if req.Kind == judge.ApprovalReAdmit {
		// The escape was refused by review; the one thing this dialog can do is
		// bring the blocked path back inside the sandbox — the smaller answer
		// than running the call bare, and the reason the question is asked at
		// all. The read rows carry the boundary dialog's read-side grants (same
		// keys, same lifetimes); this dialog adds the write row, because a
		// blocked command may have been refused for writing, and a write grant
		// carries read. Permanence is not offered for writes in one keystroke,
		// so the session row's note names /boundary writable add for that.
		if p := strings.TrimSpace(req.ReadRoot); p != "" {
			// The gate extracted this path when it refused the escape, so it is
			// already the candidate for both sides. Using it verbatim — rather
			// than re-extracting from the summary — keeps the rows and the
			// answer naming the same path the gate offered.
			readRoot, root = p, p
			readRow := approvalRow{"Allow reading " + displayPath(p) + " (this folder, this session)", "a", approveReadDir}
			rememberedRow := approvalRow{"Always allow reading " + displayPath(p) + " (this folder, remembered)", "s", approveReadPersist}
			writeRow := approvalRow{"Allow writing " + displayPath(p) + " (this session; reading included)", "w", approveWritable}
			denyRow := approvalRow{"Deny", "esc", approveDeny}
			rows = []approvalRow{readRow, rememberedRow, denyRow}
			if req.NeedsWrite {
				// The command needed to write there, so the row that unblocks it
				// leads: the first row is the one a reader who trusts the dialog
				// will take, and a read grant would leave the retry failing. The
				// read rows stay right below — narrower is still a choice, and
				// the title says which side was denied.
				rows = []approvalRow{writeRow, readRow, rememberedRow, denyRow}
			}
			if req.UnconfineOffer {
				// The command already ran, so it was already reviewed: what the
				// sandbox refused is a path. When no grant can fix it — a
				// system-protected path, or a credential file the floor will not
				// hand over — running this one command outside the sandbox is the
				// answer that works, so it is offered here and never as the
				// default. Inserted before Deny so the destructive row stays last.
				escapeRow := approvalRow{
					"Request one unconfined run outside the sandbox (the gate decides)",
					"e", approveUnconfined,
				}
				rows = append(rows[:len(rows)-1], escapeRow, rows[len(rows)-1])
			}
		} else {
			rows = []approvalRow{{"Deny", "esc", approveDeny}}
		}
	} else if readRoot != "" {
		// A read-scope question offers the directory the call read: the
		// directory itself when it named one, the file's own directory
		// otherwise — and never one the gate judged too broad to grant. It
		// sits beside the one-read answer, so a batch of reads in one tree is
		// one keystroke.
		// The last two rows are the same grant at two lifetimes, and the labels
		// say which: "this session" for the one that dies with the process,
		// "remembered" for the one written to the managed store. A user who
		// does not want a permanent grant must be able to see that they are
		// choosing one, so neither label is abbreviated.
		//
		// The key is `s` because the shortcut match is case-insensitive
		// (EqualFold), so `a`/`A` cannot be told apart.
		rows = []approvalRow{
			{"Allow this read once", "y", approveOnce},
			{"Allow reading " + displayPath(readRoot) + " and below (this session)", "a", approveReadDir},
			{"Always allow reading " + displayPath(readRoot) + " and below (remembered)", "s", approveReadPersist},
			{"Deny", "esc", approveDeny},
		}
	}
	if writeRoot != "" {
		// The write side of the same question, with the same three lifetimes as
		// the read rows above and the same key ladder (y once, a this session, s
		// remembered). The labels say "writing" rather than "reading" because the
		// grant differs: a directory allowed for reading is not thereby writable.
		rows = []approvalRow{
			{"Allow this write once", "y", approveOnce},
			{"Allow writing " + displayPath(writeRoot) + " and below (this session)", "a", approveWriteDir},
			{"Always allow writing " + displayPath(writeRoot) + " and below (remembered)", "s", approveWritePersist},
			{"Deny", "esc", approveDeny},
		}
	}
	m.approval = approvalDialog{
		active:       true,
		kind:         "tool",
		title:        approvalTitle(req),
		detail:       approvalDetail(req),
		rows:         rows,
		writableRoot: root,
		readRoot:     readRoot,
		writeRoot:    writeRoot,
		trustDir:     req.TrustDir,
		reply:        reply,
		combined: req.NetworkRequest &&
			(req.Kind == judge.ApprovalBoundary ||
				req.Kind == judge.ApprovalReAdmit ||
				req.Kind == judge.ApprovalEscalation),
		networkKey:          req.NetworkKey,
		networkRememberable: req.NetworkRememberable,
	}
}

// trustGateRows returns the rows a mid-run trust question offers, ordered by
// what each answer remembers. The labels cannot be the tool dialog's generic
// "Approve for this session": in a tool dialog that value relaxes one call
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
	if d.combined {
		m.answerCombinedApproval(d, value)
		return
	}
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
		case approveUnconfined:
			// A denial's escape request: it asks the permission gate to decide
			// whether this one command may run without the sandbox. The gate
			// asks the reviewer under auto and the person again under ask.
			ans.Approve, ans.Unconfined = true, true
			if m.session != nil {
				m.transcript.addSystem("Requesting one unconfined run; the permission gate decides.")
			}
		case approveNetwork:
			ans.Approve, ans.Network = true, true
		case approveNetworkSession:
			ans.Approve, ans.Network, ans.NetworkSession = true, true, true
		case approveNetworkRemember:
			ans.Approve, ans.Network, ans.NetworkRemember = true, true, true
		case approveContained:
			// "Keep it in the sandbox": an approval of the call, not of the
			// escape. The gate re-contains it (the reviewer had already graded
			// it runnable under containment), so nothing leaves the sandbox.
			ans.Approve, ans.Contained = true, true
		case approveDirect:
			// The escape, granted deliberately: this one call runs outside the
			// sandbox. The transcript says so, because "the reviewer let it
			// through" and "I let it through" must not look alike later.
			ans.Approve = true
			if m.session != nil {
				m.transcript.addSystem("This call runs outside the sandbox (approved by you).")
			}
		case approveSession:
			ans.Approve, ans.Always = true, true
			// In a trust question this answer reaches the whole directory, so
			// say so; a tool dialog's "for this session" relaxes one call shape
			// and carries no trustDir, so it stays silent.
			if d.trustDir != "" && m.session != nil {
				m.transcript.addSystem("All tools in " + displayPath(d.trustDir) +
					" run without asking for this session.")
			}
		case approveReadsHere:
			// The gate registers the grant (it owns the trust manager); the
			// answer only names the intent. The note spells out the scope so
			// "reads allowed" is not read as "everything allowed".
			ans.Approve, ans.SessionReads = true, true
			if m.session != nil {
				where := displayPath(d.trustDir)
				if where == "" {
					where = "this folder"
				}
				m.transcript.addSystem("Reads in " + where +
					" will not ask again this session; commands and network reads still will.")
			}
		case approveWritable:
			// Register the path first; only a successful registration turns
			// the choice into an approval. On failure the call is denied (the
			// gate fails closed on an explicit negative), never silently run
			// unisolated.
			if root := d.writableRoot; root != "" {
				if _, err := grants.AddWritableRoot(root, grants.Session, "write dialog"); err != nil {
					if m.session != nil {
						m.transcript.addSystem("Cannot grant write access to " + root + ": " + err.Error())
					}
				} else {
					ans.Approve, ans.WritableRoot = true, root
					if m.session != nil {
						m.transcript.addSystem("Writes to " + displayPath(root) +
							" are allowed inside the sandbox for this session only. To make it" +
							" permanent, choose \"remembered\" next time, or run" +
							" /boundary writable add " + root)
					}
				}
			}
		case approveWritablePersist:
			if root := d.writableRoot; root != "" {
				ans.Approve, ans.WritableRoot = true, root
				path, err := cli.AddWritablePath(root)
				if err != nil {
					if m.session != nil {
						m.transcript.addSystem("Writes to " + displayPath(root) +
							" are allowed inside the sandbox for this session only; remembering it failed: " + err.Error())
					}
					// The session grant still stands, so the call runs contained.
					// The remembered write failed, so fall back to the session
					// grant the user asked for rather than losing it: the call
					// they approved still runs, contained, and the message above
					// says why it will not survive a restart.
					_, _ = grants.AddWritableRoot(root, grants.Session, "write dialog")
				} else {
					ans.Persisted = true
					if m.session != nil {
						m.transcript.addSystem("Writes to " + displayPath(path) +
							" are now remembered across sessions. Manage it with /boundary writable list|add|rm.")
					}
				}
			}
		case approveWriteDir:
			// The gate registers the grant (it owns GrantWritableRoot, which
			// registers the sandbox's writable root and the table's row together),
			// so this answer only names the directory — exactly as the read rows do.
			if dir := d.writeRoot; dir != "" {
				ans.Approve, ans.WriteRoot = true, dir
				if m.session != nil {
					m.transcript.addSystem("Writing to " + displayPath(dir) +
						" is allowed for this session only. To make it permanent, choose" +
						" \"remembered\" next time, or run /boundary writable add " + dir)
				}
			}
		case approveWritePersist:
			// Persist first, approve second, for the reason the read rows give: a
			// store write that fails must not leave the user believing the grant
			// will survive. On failure the call is still approved for this session
			// (the registries the gate owns are already in place), and the note
			// says what happened.
			if dir := d.writeRoot; dir != "" {
				ans.Approve, ans.WriteRoot = true, dir
				root, err := cli.AddWritablePath(dir)
				if err != nil {
					if m.session != nil {
						m.transcript.addSystem("Writing to " + displayPath(dir) +
							" is allowed for this session only; remembering it failed: " + err.Error())
					}
				} else {
					ans.Persisted = true
					if m.session != nil {
						m.transcript.addSystem("Writing to " + displayPath(root) +
							" is now remembered across sessions. Manage it with /boundary writable list|add|rm.")
					}
				}
			}
		case approveReadDir:
			// The gate registers the grant (it owns the read-root registry);
			// the answer only names the directory. A registration the registry
			// refuses (too broad after all) leaves the answer without it, and
			// the gate falls back to the one-read grant.
			if dir := d.readRoot; dir != "" {
				ans.Approve, ans.ReadRoot = true, dir
				if m.session != nil {
					m.transcript.addSystem("Reading " + displayPath(dir) +
						" is allowed for this session only. To make it permanent, choose" +
						" \"remembered\" next time, or run /boundary readable add " + dir)
				}
			}
		case approveReadPersist:
			// Persist first, approve second: a write that fails must not leave
			// the user believing the grant will survive. On failure the call is
			// still approved for this session (the in-memory registry is the
			// grant the gate already owns), and the note says what happened.
			if dir := d.readRoot; dir != "" {
				ans.Approve, ans.ReadRoot = true, dir
				root, err := cli.AddReadablePath(dir)
				if err != nil {
					if m.session != nil {
						m.transcript.addSystem("Reading " + displayPath(dir) +
							" is allowed for this session only; remembering it failed: " + err.Error())
					}
				} else {
					ans.Persisted = true
					if m.session != nil {
						m.transcript.addSystem("Reading " + displayPath(root) +
							" is now remembered across sessions. Manage it with /boundary readable list|add|rm.")
					}
				}
			}
		}
		m.deliverApproval(d, ans)
	}
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
	onlyOnce := false
	for _, n := range needs {
		if n.Cap == judge.CapUnconfined {
			onlyOnce = true
		}
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

// answerCombinedApproval walks a path+network request through its two stages
// without closing the dialog. Stage 0 records the path choice; stage 1 records
// the network choice and delivers one ApprovalAnswer, so the gates cannot ask
// the same call twice.
func (m *Model) answerCombinedApproval(d approvalDialog, value string) {
	if d.stage == 0 {
		if value == approveDeny {
			m.approval = approvalDialog{}
			m.deliverApproval(d, judge.ApprovalAnswer{Answered: true})
			return
		}
		d.pathValue = value
		d.stage = 1
		d.selected = 0
		d.rows = networkApprovalRows(d.networkKey, d.networkRememberable)
		d.title = "Network access for this call"
		d.detail = []string{"The path decision is recorded. This is the network half of the same prompt."}
		m.approval = d
		return
	}
	m.approval = approvalDialog{}
	ans := combinedPathAnswer(d)
	switch value {
	case approveNetwork:
		ans.Network = true
	case approveNetworkSession:
		ans.Network, ans.NetworkSession = true, true
	case approveNetworkRemember:
		ans.Network, ans.NetworkRemember = true, true
	default:
		// The network half was denied. This is one combined decision: do not
		// deliver a path/containment approval whose retry would fail the
		// network guard.
		m.deliverApproval(d, judge.ApprovalAnswer{Answered: true})
		return
	}
	m.deliverApproval(d, ans)
}

// combinedPathAnswer replays only the boundary rows a combined request can
// carry. Registration stays in the boundary gate (it owns the read-root and
// writable-root callbacks), which is why this returns the grant shape rather
// than writing it twice.
func combinedPathAnswer(d approvalDialog) judge.ApprovalAnswer {
	ans := judge.ApprovalAnswer{Answered: true}
	switch d.pathValue {
	case approveOnce:
		ans.Approve = true
	case approveReadDir:
		ans.Approve, ans.ReadRoot = true, d.readRoot
	case approveReadPersist:
		ans.Approve, ans.ReadRoot, ans.Persisted = true, d.readRoot, true
	case approveWriteDir:
		ans.Approve, ans.WriteRoot = true, d.writeRoot
	case approveWritePersist:
		ans.Approve, ans.WriteRoot, ans.Persisted = true, d.writeRoot, true
	case approveWritable:
		// The re-admission dialog's write row grants one path, not a boundary
		// directory; d.writableRoot is that path.
		ans.Approve, ans.WritableRoot = true, d.writableRoot
	case approveWritablePersist:
		ans.Approve, ans.WritableRoot, ans.Persisted = true, d.writableRoot, true
	case approveContained:
		ans.Approve, ans.Contained = true, true
	case approveDirect:
		ans.Approve = true
	}
	return ans
}

func networkApprovalRows(key string, rememberable bool) []approvalRow {
	rows := []approvalRow{{"Allow network for this call only", "y", approveNetwork}}
	if key != "" && rememberable {
		rows = append(rows,
			approvalRow{"Allow " + key + " for this session", "a", approveNetworkSession},
			approvalRow{"Always allow " + key + " (remembered)", "s", approveNetworkRemember},
		)
	}
	return append(rows, approvalRow{"Deny network", "esc", approveDeny})
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
	for _, granted := range grants.WritableRoots() {
		if coveredBy(p, granted) {
			return ""
		}
	}
	return p
}

// escalationPathCandidate extracts the first plausible path token from an
// escalation request: the parsing itself lives in judge, because the REPL's
// prompt offers the same grant and the two must agree on what it covers.
func escalationPathCandidate(req judge.ApprovalRequest, cwd string) string {
	if req.Kind != judge.ApprovalEscalation {
		return ""
	}
	return judge.GrantablePathFromText(req.Summary, req.Justification, cwd)
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
	case judge.ApprovalNoSandbox:
		return tool + " cannot be sandboxed — approving runs it outside the sandbox"
	case judge.ApprovalReviewFailed:
		return tool + " needs approval (automatic review was unavailable)"
	case judge.ApprovalEscalation:
		return tool + " asks to run outside the sandbox"
	case judge.ApprovalReAdmit:
		// Which side was denied comes first in the title: the rows name the
		// grants and the path, but a reader needs to know whether the command
		// was writing or reading before the rows mean anything to them.
		if req.NeedsWrite {
			return tool + " was denied writing outside the sandbox"
		}
		return tool + " was denied reading outside the sandbox"
	case judge.ApprovalBoundary:
		if req.NetworkRequest {
			if req.BoundaryWrite {
				return tool + " wants to write outside the workspace and reach the network"
			}
			return tool + " wants to read outside the workspace and reach the network"
		}
		if req.BoundaryWrite {
			return tool + " wants to write outside the workspace"
		}
		return tool + " wants to read outside the workspace"
	case judge.ApprovalNetwork:
		if req.NetworkKey != "" {
			return tool + " wants network access to " + req.NetworkKey
		}
		return tool + " wants network access"
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
	out := []string{judge.DecisionLine(req.Tool, req.Mode, req.Level, judge.AskDisposition(req.Kind, "en"))}
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
		// The (risk, auth) suffix is part of a reviewer's verdict. A consent
		// question (read-scope, trust) has no verdict behind it, so it renders
		// as its own sentence instead of "Reviewer:"; and since a suffix with
		// no risk grade reads as punctuation without content, it is built from
		// Risk alone.
		rating := ""
		if req.Risk != "" {
			rating = " (" + req.Risk + ", auth: " + req.Authorization + ")"
		}
		switch req.Kind {
		case judge.ApprovalBoundary, judge.ApprovalTrust:
			out = append(out, r+rating)
		default:
			out = append(out, "Reviewer: "+r+rating)
		}
	}
	if req.NetworkRequest {
		key := strings.TrimSpace(req.NetworkKey)
		if key == "" {
			out = append(out, "Network access will be asked next; this call can only be approved for one use.")
		} else {
			out = append(out, "Network access will be asked next ("+key+").")
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
