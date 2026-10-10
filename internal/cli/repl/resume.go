package repl

import (
	"fmt"
	"io"
	"os"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/provider"
)

// runResume implements /resume: it swaps the active REPL session without
// leaving the program, mirroring the TUI's runSession.switchTo (persist
// current, load target, live model/provider follow the stored header). A bare
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
	sysPrompt := h.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = deps.agentCtx.SystemPrompt
	}
	if h.Provider != "" && h.Provider != deps.live.ProviderName {
		prov, name, err := provider.ResolveProvider(h.Model, "", "", h.Provider, os.Getenv)
		if err != nil {
			fmt.Fprintf(out, "resume: resolve session provider: %v\n", err)
			return
		}
		deps.live.Provider = prov
		deps.live.ProviderName = name
		deps.live.BaseURL = ""
		deps.live.Protocol = ""
	}
	if h.Model != "" {
		deps.live.Model = h.Model
	}
	// The resumed session may run a different model than the launch one;
	// re-resolve the context budget so the gauge and auto-compaction follow.
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
}

// resumeAllHint renders the flag that would widen this list, or "" when the
// list is already unfiltered.
func resumeAllHint(scope cli.ResumeScope) string {
	if scope.All {
		return ""
	}
	return " or " + cli.AllSessionsFlag
}
