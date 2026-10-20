package tui

import tea "charm.land/bubbletea/v2"

// Bubble Tea calls View after EVERY dispatched event, even when Update returns
// an unchanged model and no command. All-motion mouse reporting (needed for the
// status bar's hover) can therefore queue hundreds of redundant frames ahead of
// a keypress. Drop only motions known to leave the model unchanged, before the
// event loop renders them. Never coalesce/drop keys, terminal replies or drags.
func filterUnchangedMotion(model tea.Model, msg tea.Msg) tea.Msg {
	motion, ok := msg.(tea.MouseMotionMsg)
	if !ok {
		return msg
	}
	if traced, ok := model.(timingModel); ok {
		model = traced.Model
	}
	m, ok := model.(Model)
	if !ok || m.draggingScrollbar || m.sel.active || m.selDragging {
		return msg
	}
	if m.statusSegmentAt(motion.X, motion.Y) == m.statusBar.hover {
		return nil
	}
	return msg
}
