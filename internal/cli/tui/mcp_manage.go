package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/cli/extensioncmd"
	"github.com/getan/golder/internal/extensions"
)

func (m Model) confirmMCPRemove(id string) (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		m.transcript.addSystem("Finish the current run before removing an MCP server.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	edit, err := m.opts.Extensions.PrepareMCPEdit(id)
	if err != nil {
		m.transcript.addSystem(extensions.DisplayText(err.Error(), 1024))
		return m, nil
	}
	m.mcpRemoval = &edit
	info := []pickInfoLine{{Label: "Source", Value: "mcp:" + id}, {Label: "Effect", Value: "Remove configuration; retain named credentials and local login state."}}
	m.menu.openPickerDetailed([]pickItem{
		{Title: "Cancel", Value: extensionValue("back"), Info: info},
		{Title: "Confirm remove", StatusTone: pickWarn, Value: extensionValue("mcp-remove-confirm"), Info: info},
	}, "", "extensions")
	m.menu.pickHint = "↑↓ Select · Enter · Esc Close"
	m.relayout()
	return m, nil
}

func (m Model) removeConfirmedMCP() (tea.Model, tea.Cmd) {
	if m.mcpRemoval == nil {
		return m, nil
	}
	edit := *m.mcpRemoval
	m.mcpRemoval = nil
	manager := m.opts.Extensions
	return m.startSessionOp("/mcp remove "+edit.ID, "Removing MCP source", func(ctx context.Context) tea.Cmd {
		return func() tea.Msg {
			if err := ctx.Err(); err != nil {
				return extensionDoneMsg{err: err}
			}
			err := manager.RemoveMCP(edit)
			return extensionDoneMsg{response: extensioncmd.Response{Text: "Removed mcp:" + edit.ID + "; credentials and login state were retained."}, err: err}
		}
	})
}
