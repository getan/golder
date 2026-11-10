package remotecontrol

import (
	"errors"

	"github.com/getan/golder/internal/judge"
)

// NewApprovalID returns an opaque identifier scoped by the owning adapter to
// the lifetime of the original, immutable gate request.
func NewApprovalID() (string, error) { return randHex(16) }

// ProjectApproval exposes only reviewed fields and only choices that can be
// represented without broadening the original gate. Unsupported capabilities
// remain local-only; they must not be collapsed into a generic approval.
func ProjectApproval(id string, req judge.ApprovalRequest) MobileApproval {
	view := MobileApproval{
		ID: id, Kind: string(req.Kind), Tool: req.Tool, Summary: req.Summary,
		Justification: req.Justification, Rationale: req.Rationale, Source: req.Source,
		Level: req.Level.String(), Mode: req.Mode, Origin: req.Origin, TrustDir: req.TrustDir,
	}
	supported := id != "" && req.Tool != "" && req.Summary != ""
	if req.Kind != judge.ApprovalConsent && req.Kind != judge.ApprovalTrust {
		supported = false
	}
	if req.Kind == judge.ApprovalTrust && req.TrustDir == "" {
		supported = false
	}
	for _, n := range req.Needs {
		capability := ""
		switch n.Cap {
		case judge.CapReadOutside:
			capability = "read outside workspace"
		case judge.CapWriteOutside:
			capability = "write outside workspace"
		case judge.CapNetwork:
			capability = "network"
		case judge.CapUnconfined:
			capability = "outside sandbox"
		default:
			capability = "local confirmation required"
			supported = false
		}
		view.Needs = append(view.Needs, MobileNeed{Capability: capability, Path: n.Path, Directory: n.Dir, Target: n.Key})
	}
	if supported {
		view.Choices = []MobileChoice{{ID: "deny", Label: "Deny"}, {ID: "once", Label: "Allow this call once"}}
	}
	return view
}

// ApprovalAnswer constructs the gate's native answer from the original request,
// not client-supplied capabilities or paths. Long-lived grants are not offered.
func ApprovalAnswer(req judge.ApprovalRequest, choice string) (judge.ApprovalAnswer, error) {
	view := ProjectApproval("valid", req)
	if len(view.Choices) == 0 {
		return judge.ApprovalAnswer{}, errors.New("approval requires the local operator")
	}
	switch choice {
	case "deny":
		return judge.ApprovalAnswer{Answered: true}, nil
	case "once":
		answer := judge.ApprovalAnswer{Answered: true, Approve: true, Scope: judge.ScopeOnce, Needs: append([]judge.Need(nil), req.Needs...)}
		for _, need := range req.Needs {
			if need.Cap == judge.CapUnconfined {
				answer.Exec = judge.ExecUnconfined
			}
		}
		return answer, nil
	default:
		return judge.ApprovalAnswer{}, errors.New("invalid approval choice")
	}
}
