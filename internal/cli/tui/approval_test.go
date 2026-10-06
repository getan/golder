package tui

// Tests for the tool-call approval dialog: it is modal (every key is
// consumed), the three answers map to the right judge.ApprovalAnswer, and the
// run-goroutine reply channel receives exactly one value.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/seatbelt"
)

func testApprovalRequest() judge.ApprovalRequest {
	return judge.ApprovalRequest{
		Kind:          judge.ApprovalConfirm,
		Tool:          "bash",
		Summary:       "npm install",
		Justification: "needs the network",
		Risk:          "medium",
		Authorization: "high",
		Rationale:     "installs dependencies",
	}
}

// awaitAnswer reads one answer or reports a timeout, so a wiring bug fails the
// test instead of hanging.
func awaitAnswer(t *testing.T, ch chan judge.ApprovalAnswer) judge.ApprovalAnswer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("no answer delivered to the waiting gate")
		return judge.ApprovalAnswer{}
	}
}

func TestApprovalDialogAnswers(t *testing.T) {
	cases := []struct {
		name    string
		key     tea.KeyPressMsg
		approve bool
		always  bool
	}{
		{"y approves once", tea.KeyPressMsg{Code: 'y', Text: "y"}, true, false},
		{"p approves for the session", tea.KeyPressMsg{Code: 'p', Text: "p"}, true, true},
		{"esc denies", tea.KeyPressMsg{Code: tea.KeyEscape}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reply := make(chan judge.ApprovalAnswer, 1)
			m := NewModel(Options{})
			m.openApproval(testApprovalRequest(), reply)

			got, _ := m.Update(c.key)
			m = got.(Model)

			ans := awaitAnswer(t, reply)
			if !ans.Answered || ans.Approve != c.approve || ans.Always != c.always {
				t.Fatalf("answer = %+v, want approve=%v always=%v", ans, c.approve, c.always)
			}
			if m.approval.active {
				t.Error("the dialog should close after answering")
			}
		})
	}
}

// TestApprovalDialogIsModal locks the key ownership: while the dialog is open
// the composer must not receive input, and an unrelated key must neither
// dismiss nor answer the decision.
func TestApprovalDialogIsModal(t *testing.T) {
	reply := make(chan judge.ApprovalAnswer, 1)
	m := NewModel(Options{})
	m.openApproval(testApprovalRequest(), reply)

	got, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = got.(Model)
	if !m.approval.active {
		t.Fatal("an unrelated key must not close the dialog")
	}
	if v := m.input.Value(); v != "" {
		t.Errorf("typing leaked into the composer: %q", v)
	}
	select {
	case a := <-reply:
		t.Fatalf("unrelated key answered the request: %+v", a)
	default:
	}

	// Enter confirms the highlighted row (the first one: approve once).
	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = got.(Model)
	if ans := awaitAnswer(t, reply); !ans.Approve || ans.Always {
		t.Fatalf("Enter should approve once, got %+v", ans)
	}
}

// TestApprovalDialogArrowSelection verifies ↑↓ move the highlight and wrap.
func TestApprovalDialogArrowSelection(t *testing.T) {
	reply := make(chan judge.ApprovalAnswer, 1)
	m := NewModel(Options{})
	m.openApproval(testApprovalRequest(), reply)

	got, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = got.(Model)
	if m.approval.selected != 1 {
		t.Fatalf("down should select the second row, got %d", m.approval.selected)
	}
	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = got.(Model)
	if m.approval.selected != 0 {
		t.Fatalf("up should wrap back to the first row, got %d", m.approval.selected)
	}
	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = got.(Model)
	if m.approval.selected != len(toolApprovalRows)-1 {
		t.Fatalf("up at the top should wrap to the last row, got %d", m.approval.selected)
	}
}

// TestApprovalViewShowsContext verifies the dialog shows what is being
// approved: the call preview, the model's reason, and the reviewer's verdict.
func TestApprovalViewShowsContext(t *testing.T) {
	reply := make(chan judge.ApprovalAnswer, 1)
	m := NewModel(Options{})
	m.openApproval(testApprovalRequest(), reply)
	view := m.approvalView(100)
	for _, want := range []string{"needs approval", "npm install", "needs the network", "installs dependencies", "Approve once", "Approve for this session", "Deny"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}
}

// TestApprovalTitleEscalation pins the escalation-conflict headline: the
// dialog must say the call wants out of the sandbox, not just "needs
// approval".
func TestApprovalTitleEscalation(t *testing.T) {
	req := testApprovalRequest()
	req.Kind = judge.ApprovalEscalation
	title := approvalTitle(req)
	if !strings.Contains(title, "outside the sandbox") || !strings.Contains(title, "sandbox") {
		t.Errorf("escalation title = %q", title)
	}
}

// TestApprovalDialogWritableRow: an escalation whose command names an outside
// path offers the per-path grant, and choosing it registers the path and
// answers with the containment grant (Approve + WritableRoot).
func TestApprovalDialogWritableRow(t *testing.T) {
	defer seatbelt.SetWritableRoots(nil)
	t.Setenv("HOME", t.TempDir())
	req := judge.ApprovalRequest{
		Kind:          judge.ApprovalEscalation,
		Tool:          "bash",
		Summary:       "command: rm -rf /Volumes/KIOXIA/old-build",
		Justification: "the sandbox denies writes to /Volumes/KIOXIA/old-build",
	}
	reply := make(chan judge.ApprovalAnswer, 1)
	m := NewModel(Options{})
	m.openApproval(req, reply)
	if len(m.approval.rows) != 4 {
		t.Fatalf("rows = %d, want 4 with the writable grant", len(m.approval.rows))
	}
	label := m.approval.rows[2].label
	if !strings.Contains(label, "/Volumes/KIOXIA/old-build") || !strings.Contains(label, "this session") {
		t.Fatalf("writable row label = %q", label)
	}
	got, _ := m.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})
	_ = got
	ans := awaitAnswer(t, reply)
	if !ans.Approve || ans.WritableRoot != "/Volumes/KIOXIA/old-build" {
		t.Fatalf("answer = %+v", ans)
	}
	if roots := seatbelt.WritableRoots(); len(roots) != 1 || roots[0] != "/Volumes/KIOXIA/old-build" {
		t.Fatalf("registry = %v", roots)
	}
	// The path is granted now: the row disappears for the next request (and
	// confirmApproval settles it without a dialog).
	m2 := NewModel(Options{})
	m2.openApproval(req, make(chan judge.ApprovalAnswer, 1))
	if len(m2.approval.rows) != 3 || m2.approval.writableRoot != "" {
		t.Fatalf("already-granted path must not offer the row: %+v", m2.approval.rows)
	}
	s := &runSession{cwd: t.TempDir()} // no approvalCh: a dialog would fail closed
	if ans := s.confirmApproval(context.Background(), req); !ans.Approve || ans.WritableRoot != "/Volumes/KIOXIA/old-build" {
		t.Fatalf("pre-granted escalation should be contained without a dialog, got %+v", ans)
	}
}

// TestInferWritableRoot pins the candidate filter: only outside paths make the
// row, and every ambiguous or already-writable candidate hides it.
func TestInferWritableRoot(t *testing.T) {
	defer seatbelt.SetWritableRoots(nil)
	// A lexical home outside the OS temp dir: a t.TempDir() home would sit
	// under os.TempDir() (which the filter rightly rejects) and mask the
	// tilde expansion case.
	home := "/golder-test-home"
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "work", "proj")
	escalation := judge.ApprovalRequest{Kind: judge.ApprovalEscalation, Tool: "bash"}
	cases := []struct {
		name    string
		summary string
		just    string
		want    string
	}{
		{"absolute flag path", "command: cargo build --target-dir /Volumes/KIOXIA/rust-target", "", "/Volumes/KIOXIA/rust-target"},
		{"tilde path", "command: cp a ~/backup/dir", "", filepath.Join(home, "backup", "dir")},
		{"justification only", "command: rm -rf build", "needs to clean /Volumes/KIOXIA/cache", "/Volumes/KIOXIA/cache"},
		{"project path skipped", "command: ls " + cwd + "/sub", "", ""},
		{"system path skipped", "command: cat /usr/share/foo", "", ""},
		{"tmp skipped", "command: mkdir -p /tmp/work", "", ""},
		{"bare home skipped", "command: rm -rf ~", "", ""},
		{"no path", "command: git fetch origin", "needs the network", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := escalation
			req.Summary, req.Justification = c.summary, c.just
			if got := inferWritableRoot(req, cwd); got != c.want {
				t.Fatalf("inferWritableRoot = %q, want %q", got, c.want)
			}
		})
	}
	// A confirm-tier request never grows the row, even with a path.
	req := escalation
	req.Kind = judge.ApprovalConfirm
	req.Summary = "command: rm -rf /Volumes/KIOXIA/old-build"
	if got := inferWritableRoot(req, cwd); got != "" {
		t.Fatalf("confirm-tier request offered a path: %q", got)
	}
}

// TestConfirmApprovalRemembersDenials: after the user denies a call, a retry
// of the same call is blocked from memory instead of reopening the dialog.
func TestConfirmApprovalRemembersDenials(t *testing.T) {
	s := &runSession{approvalCh: make(chan tea.Msg, 1)}
	go func() {
		msg := (<-s.approvalCh).(approvalRequestMsg)
		msg.reply <- judge.ApprovalAnswer{Answered: true}
	}()
	ans := s.confirmApproval(context.Background(), testApprovalRequest())
	if ans.Approve || !ans.Answered {
		t.Fatalf("first answer = %+v, want an explicit denial", ans)
	}
	// The retry must be answered from the denial memory without a dialog.
	ans = s.confirmApproval(context.Background(), testApprovalRequest())
	if ans.Approve || !ans.Answered {
		t.Fatalf("memorized denial = %+v, want deny without a dialog", ans)
	}
	select {
	case msg := <-s.approvalCh:
		t.Fatalf("a memorized denial must not reopen the dialog: %+v", msg)
	default:
	}
}

// TestTrustGateAsksLocally drives the trust gate end to end with a stubbed
// UI: an untrusted directory must produce an approval request, and approving
// with "always" must trust the directory for the rest of the session.
func TestTrustGateAsksLocally(t *testing.T) {
	home := withEmptyTrustStore(t)
	session := newRemoteTestSession(t)
	session.trusted = false
	session.hookDeps.ProjectDir = t.TempDir()

	// Wire a fake UI: the run goroutine's Confirm callback must see the
	// request and answer "approve for the session".
	ch := make(chan tea.Msg, 4)
	session.approvalCh = ch
	gate := session.trustApprovalGate(nil)
	if gate == nil {
		t.Fatal("trust gate should be installed when a trust manager exists")
	}

	type outcome struct {
		dec *agentcore.BeforeToolCallDecision
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		dec := gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"echo hi"}`)})
		done <- outcome{dec: dec}
	}()

	// The UI side: receive the request and answer "Approved for session".
	select {
	case msg := <-ch:
		reqMsg, ok := msg.(approvalRequestMsg)
		if !ok {
			t.Fatalf("unexpected message %T", msg)
		}
		if reqMsg.req.Kind != judge.ApprovalTrust {
			t.Errorf("kind = %q, want trust", reqMsg.req.Kind)
		}
		reqMsg.reply <- judge.ApprovalAnswer{Approve: true, Always: true, Answered: true}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not ask for approval")
	}

	select {
	case out := <-done:
		if out.dec != nil {
			t.Errorf("approved call should pass, got %+v", out.dec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not return after approval")
	}
	if !session.trust.IsTrusted(session.hookDeps.ProjectDir) {
		t.Error("approve-for-session should trust the directory for the session")
	}
	_ = home
}

// TestTrustGateDeniesWithoutAnswer verifies a cancelled run (no UI answer)
// blocks the call rather than allowing it by silence.
func TestTrustGateDeniesWithoutAnswer(t *testing.T) {
	withEmptyTrustStore(t)
	session := newRemoteTestSession(t)
	session.trusted = false
	session.hookDeps.ProjectDir = t.TempDir()
	session.approvalCh = make(chan tea.Msg, 1)

	gate := session.trustApprovalGate(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *agentcore.BeforeToolCallDecision, 1)
	go func() {
		done <- gate(ctx, agentcore.AgentToolCall{Name: "bash", Arguments: []byte(`{"command":"echo hi"}`)})
	}()
	// Consume the request but never answer, then cancel (the run was
	// interrupted with the dialog open).
	select {
	case <-session.approvalCh:
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not ask for approval")
	}
	cancel()
	select {
	case dec := <-done:
		if dec == nil || !dec.Block {
			t.Errorf("unanswered trust question must block, got %+v", dec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gate did not return after cancellation")
	}
}
