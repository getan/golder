package appsession

import (
	"testing"

	"github.com/getan/golder/internal/judge"
)

func TestTerminalApprovalsAreNeverSessionCached(t *testing.T) {
	if got := approvalKey(judge.ApprovalRequest{Tool: "terminal", Summary: `{"pane":"p","action":"send"}`}); got != "" {
		t.Fatalf("terminal approval could be cached: %q", got)
	}
	if got := approvalKey(judge.ApprovalRequest{Tool: "bash", Summary: "go test"}); got == "" {
		t.Fatal("ordinary approval caching was unexpectedly disabled")
	}
}
