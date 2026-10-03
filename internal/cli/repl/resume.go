package repl

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/provider"
)

// runResume implements /resume: it swaps the active REPL session without
// leaving the program, mirroring the TUI's runSession.switchTo (persist
// current, load target, live model/provider follow the stored header). A bare
// /resume lists recent sessions; a 1-based number selects one.
func runResume(out io.Writer, deps *replDeps, arg string) {
	if strings.TrimSpace(arg) == "" {
		items, err := cli.RecentSessionsWithPreview(deps.store, 10)
		if err != nil {
			fmt.Fprintf(out, "resume: list sessions: %v\n", err)
			return
		}
		if len(items) == 0 {
			fmt.Fprintln(out, "No saved sessions yet.")
			return
		}
		fmt.Fprintln(out, "Recent sessions (/resume <n|id>):")
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
			fmt.Fprintf(out, "     %s · %s · %s\n", it.Header.ID, model, it.Header.UpdatedAt.Format("01-02 15:04"))
		}
		return
	}
	id, err := cli.ResolveResumeID(deps.store, arg)
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
