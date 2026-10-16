package repl

import (
	"fmt"
	"io"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
)

// runResume implements /resume: it swaps the active REPL session without
// leaving the program, mirroring the TUI's runSession.switchTo (persist
// current, load target, keep serving with the live run configuration). A bare
// /resume lists the current project's recent sessions; a 1-based number selects
// one, --all widens the list to every project, and a literal id always resolves
// globally.
func runResume(out io.Writer, deps *replDeps, arg string) {
	scope, sel := cli.ParseResumeArg(arg, deps.cwd)
	if sel == "" {
		items, err := cli.RecentSessionsWithPreview(deps.store, scope, cli.ResumeListN)
		if err != nil {
			fmt.Fprintf(out, "resume: list sessions: %v\n", err)
			return
		}
		if len(items) == 0 {
			fmt.Fprintln(out, cli.ResumeEmptyMessage(deps.store, scope))
			return
		}
		fmt.Fprintf(out, "Recent sessions in %s (/resume <n|id>%s):\n", cli.ResumeScopeLabel(scope), resumeAllHint(scope))
		for i, it := range items {
			title := it.Preview
			if title == "" {
				title = it.Header.Model
			}
			if title == "" {
				title = "(empty session)"
			}
			model := it.Header.Model
			if model == "" {
				model = "?"
			}
			fmt.Fprintf(out, "  %d. %s\n", i+1, title)
			fmt.Fprintf(out, "     %s · %s · %s · %s\n", it.Header.ID, model, cli.SessionDirDisplay(it.Header), it.Header.UpdatedAt.Format("01-02 15:04"))
		}
		// A project-scoped list can be shorter than the store; say so once, so
		// the sessions of other projects are not silently invisible.
		if hint := cli.ResumeHiddenHint(deps.store, scope, len(items)); hint != "" {
			fmt.Fprintf(out, "  (%s)\n", hint)
		}
		return
	}
	id, err := cli.ResolveResumeID(deps.store, arg, deps.cwd)
	if err != nil {
		fmt.Fprintf(out, "resume: %v\n", err)
		return
	}
	if id == deps.header.ID {
		fmt.Fprintf(out, "Already on session %s.\n", id)
		return
	}
	cli.PersistTurn(out, deps)
	h, entries, err := deps.store.LoadEntries(id)
	if err != nil {
		fmt.Fprintf(out, "resume: %v\n", err)
		return
	}
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	// The guide and environment are rebuilt from this binary; the incoming
	// session's own user-authored inputs are carried over.
	sysPrompt := cli.ResumeSystemPrompt(h, deps.prompt)
	// The live run configuration stays with this process, exactly as it does for
	// a launch on this session (see the TUI's switchTo): the session's recorded
	// Model/Provider are its history, listed above, not a configuration to adopt.
	// Re-resolving the context budget keeps the gauge and auto-compaction honest
	// about the model actually in use.
	deps.live.RefreshContextWindow()
	deps.header = h
	deps.agentCtx = &agentcore.AgentContext{SystemPrompt: sysPrompt, Messages: msgs, Tools: deps.agentCtx.Tools}
	deps.persisted = len(msgs)
	deps.curLeaf = ""
	if len(entries) > 0 {
		deps.curLeaf = entries[len(entries)-1].ID
	}
	deps.hookDeps.SessionID = h.ID
	replayTranscript(out, msgs)
	fmt.Fprintf(out, "Resumed session %s (%s).\n", id, deps.live.Model)
	// The pane now holds a different conversation: a host that resumed it would
	// otherwise reopen the one this golder no longer runs.
	deps.hostStart()
}

// resumeAllHint renders the flag that would widen this list, or "" when the
// list is already unfiltered.
func resumeAllHint(scope cli.ResumeScope) string {
	if scope.All {
		return ""
	}
	return " or " + cli.AllSessionsFlag
}
