package tui

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/judge"
)

func consentTUIRequest() judge.ApprovalRequest {
	return judge.ApprovalRequest{
		Kind: judge.ApprovalConsent,
		Tool: "bash", Summary: "cat /tmp/out && curl example.com",
		Mode: "ask", Level: judge.Confirm, Source: "local", Rationale: "needs your decision",
		Needs: []judge.Need{
			{Cap: judge.CapReadOutside, Path: "/tmp/out", Dir: "/tmp"},
			{Cap: judge.CapNetwork, Key: "example.com"},
			{Cap: judge.CapUnconfined},
		},
	}
}

func TestConsentDialogShowsOneCombinedQuestion(t *testing.T) {
	m := NewModel(Options{})
	reply := make(chan judge.ApprovalAnswer, 1)
	m.openApproval(consentTUIRequest(), reply)
	view := m.approvalView(100)
	for _, want := range []string{"needs 3 capabilities", "via local", "read /tmp/out", "network example.com", "outside the sandbox"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog missing %q:\n%s", want, view)
		}
	}
	if len(m.approval.rows) != 2 {
		t.Fatalf("an escape request must offer once/deny only, got %+v", m.approval.rows)
	}
	m.answerApproval(consentOnce)
	ans := <-reply
	if !ans.Approve || !ans.Answered || ans.Scope != judge.ScopeOnce || len(ans.Needs) != 3 || ans.Exec != judge.ExecUnconfined {
		t.Fatalf("answer = %+v", ans)
	}
}

func TestConsentDialogOffersSessionWithoutEscape(t *testing.T) {
	m := NewModel(Options{})
	req := consentTUIRequest()
	req.Needs = req.Needs[:2]
	reply := make(chan judge.ApprovalAnswer, 1)
	m.openApproval(req, reply)
	if len(m.approval.rows) != 4 {
		t.Fatalf("rows = %+v, want once/session/remember/deny", m.approval.rows)
	}
	m.answerApproval(consentSession)
	ans := <-reply
	if !ans.Approve || ans.Scope != judge.ScopeSession || ans.Exec != judge.ExecSandboxed {
		t.Fatalf("answer = %+v", ans)
	}
}
