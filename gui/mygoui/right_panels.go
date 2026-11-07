package mygoui

import "github.com/egoist/mygo/ui"

// rightPanel identifies a requested automation view, not its process or lease.
// A view may remain requested while temporarily displaced by another view.
type rightPanel uint8

const (
	rightPanelNone rightPanel = iota
	rightPanelComputer
	rightPanelBrowser
	rightPanelTerminal
)

func (a *App) rightPanelRequested(panel rightPanel) bool {
	switch panel {
	case rightPanelComputer:
		return a.computerOpen
	case rightPanelBrowser:
		return a.browserPanelOpen
	case rightPanelTerminal:
		return a.terminalOpen
	default:
		return false
	}
}

// rightPanels returns at most two views in priority order. An explicit user
// selection can displace the lowest-priority view, without closing it.
func (a *App) rightPanels() []rightPanel {
	var requested []rightPanel
	for _, panel := range []rightPanel{rightPanelComputer, rightPanelBrowser, rightPanelTerminal} {
		if a.rightPanelRequested(panel) {
			requested = append(requested, panel)
		}
	}
	if len(requested) <= 2 {
		return requested
	}
	if a.rightPreferred == rightPanelTerminal {
		return []rightPanel{rightPanelComputer, rightPanelTerminal}
	}
	return requested[:2]
}

func (a *App) rightPanelVisible(panel rightPanel) bool {
	if a.filesOpen || a.changesOpen {
		return false
	}
	for _, visible := range a.rightPanels() {
		if visible == panel {
			return true
		}
	}
	return false
}

func (a *App) hasRightPanel() bool {
	return a.filesOpen || a.changesOpen || len(a.rightPanels()) > 0
}

// revealRequestedPanel handles a click on an obscured view. Clicking an
// already visible view is left to its normal hide/close action.
func (a *App) revealRequestedPanel(panel rightPanel) bool {
	if !a.rightPanelRequested(panel) || a.rightPanelVisible(panel) {
		return false
	}
	a.filesOpen = false
	a.closeChangesPanel()
	a.rightPreferred = panel
	return true
}

func (a *App) rightPanelView(c *ui.Context) {
	if a.filesOpen {
		a.fileExplorerPanel(c)
		return
	}
	if a.changesOpen {
		a.changesPanel(c)
		return
	}
	panels := a.rightPanels()
	if len(panels) == 1 {
		a.automationPanelView(c, panels[0])
	} else if len(panels) == 2 {
		clampPanelSplit(&a.rightStackSplit, 50)
		ui.SplitVerticalPercent(c, &a.rightStackSplit,
			func() { a.automationPanelView(c, panels[0]) },
			func() { a.automationPanelView(c, panels[1]) },
		).Fill().MinHeight(0).MinWidth(0)
	}
}

func (a *App) automationPanelView(c *ui.Context, panel rightPanel) {
	switch panel {
	case rightPanelComputer:
		a.computerPanel(c)
	case rightPanelBrowser:
		a.browserPanel(c)
	case rightPanelTerminal:
		a.terminalPanel(c)
	}
}
