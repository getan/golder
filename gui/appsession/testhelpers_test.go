package appsession

import (
	"github.com/getan/golder/internal/judge"
)

func judgeRequest(tool, summary string) judge.ApprovalRequest {
	return judge.ApprovalRequest{Tool: tool, Summary: summary}
}
