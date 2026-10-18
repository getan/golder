package tui

// The TUI's gate wiring: which gates a turn runs under, and who answers their
// questions.
//
// The gates themselves are the session's, assembled once by run.Gates — the
// same definition the REPL, /goal, /btw, headless and every sub-agent use, so
// the TUI can no longer gate a tool differently from the REPL. It used to: the
// trust gate here consulted the side-effect tool map directly, which left the
// read half of StrictReads ungated in the TUI while the REPL asked about it,
// so a read plus a fetch of what it read were two questions in the REPL and
// none here. This file contributes only the TUI-specific answer channels.

import (
	"context"

	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/judge"
)

// sessionGates assembles this session's gate chain around the TUI's answer
// channels. rs is the remote-control session, or nil when none is running.
func (s *runSession) sessionGates(rs *remoteSession) *run.Gates {
	return run.NewGates(run.GatesOpts{
		Trust:      s.trust,
		Cwd:        s.hookDeps.ProjectDir,
		Perms:      s.perms,
		Notes:      s.notes,
		Sandboxed:  run.SandboxGate(),
		Classifier: s.reviewer,
		Confirm:    s.confirmApproval,
		// The paired browser answers trust questions while local dialogs answer
		// the rest: the user is looking at the browser, and that was the first
		// choice before this wiring existed.
		TrustConfirm: s.trustConfirm(rs),
		// Every prompt the gates put to the person passes through this, so the
		// multiplexer around the pane can say the pane is waiting for a human —
		// and say it is not, the moment the answer lands. A nil session (no
		// multiplexer) makes it a no-op.
		OnPrompt:   s.hostPrompt,
		ExtraRoots: run.ReadableExtraRoots(),
	})
}

// reviewer builds the session's classifier for one chain. It is a factory
// rather than a value because a /model switch changes the reviewer between
// runs. The trust it reports is the launch decision (--approve, or the saved
// store at start-up) OR the live store: --approve grants session trust for the
// run without writing the manager, so reading only the manager would tell the
// reviewer this directory is untrusted on a run the user explicitly approved,
// while reading only the launch snapshot would hide a mid-session
// "trust this folder" from the gates' own view of the same directory.
func (s *runSession) reviewer() judge.Classifier {
	trusted := s.trusted || (s.trust != nil && s.trust.IsTrusted(s.hookDeps.ProjectDir))
	return run.NewReviewer(s.live.Model, s.live.ProviderName, s.live.Provider, s.creds, s.header.ID, s.hookDeps.ProjectDir, trusted)
}

// trustConfirm answers a trust question: the paired browser when remote control
// has a client, the local approval dialog otherwise. A browser question that
// could not be delivered — the client vanished, or the run was interrupted —
// is answered as a denial, so the gate fails closed instead of parking the run.
func (s *runSession) trustConfirm(rs *remoteSession) func(context.Context, judge.ApprovalRequest) judge.ApprovalAnswer {
	return func(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer {
		if rs != nil && rs.hasClient() {
			d, remote := rs.bridge.Confirm(ctx, req.Tool, req.Summary)
			if remote {
				return judge.ApprovalAnswer{Approve: d.Approve, Always: d.Always, Answered: true}
			}
			// The browser could not deliver the question (the client vanished
			// or the run was interrupted). That is "nobody answered", not a
			// refusal: recording it as a denial would silence the next question
			// for the same tool and shape.
			return judge.ApprovalAnswer{}
		}
		return s.confirmApproval(ctx, req)
	}
}
