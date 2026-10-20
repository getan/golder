package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFilterUnchangedMotion(t *testing.T) {
	m := apply(t, NewModel(Options{ProviderName: "test", Model: "sonnet-4"}), tea.WindowSizeMsg{Width: 120, Height: 40})
	motion := tea.MouseMotionMsg{X: 20, Y: 5}
	if filterUnchangedMotion(m, motion) != nil {
		t.Error("plain hover in the transcript must not force another frame")
	}
	if filterUnchangedMotion(timingModel{Model: m}, motion) != nil {
		t.Error("timing diagnostics must not change filtering")
	}
	// Find a real segment using the same layout the bar paints/hit-tests.
	var over tea.MouseMotionMsg
	found := false
	for x := 0; x < m.width; x++ {
		if m.statusSegmentAt(x, m.height-1) != 0 {
			over = tea.MouseMotionMsg{X: x, Y: m.height - 1}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no status-bar segment")
	}
	if filterUnchangedMotion(m, over) == nil {
		t.Fatal("entering a segment must expand it")
	}
	m = apply(t, m, over)
	if filterUnchangedMotion(m, over) != nil {
		t.Error("motion within the same focused segment must not force another frame")
	}
	if filterUnchangedMotion(m, motion) == nil {
		t.Error("leaving the status bar must clear its hover")
	}
}

func TestMotionFilterPreservesInteractiveEvents(t *testing.T) {
	motion := tea.MouseMotionMsg{X: 20, Y: 5}
	for _, m := range []Model{
		{draggingScrollbar: true},
		{sel: selection{active: true}},
		{selDragging: true},
	} {
		if filterUnchangedMotion(m, motion) == nil {
			t.Error("selection/scrollbar motion must reach Update")
		}
	}
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: 'a', Text: "a"},
		tea.PasteMsg{Content: "hello"},
		tea.MouseClickMsg{}, tea.MouseReleaseMsg{}, tea.MouseWheelMsg{},
		tea.ModeReportMsg{}, tea.BackgroundColorMsg{}, tea.WindowSizeMsg{},
	} {
		if filterUnchangedMotion(Model{}, msg) == nil {
			t.Errorf("filter must not drop %T", msg)
		}
	}
	if filterUnchangedMotion(timingTestModel{}, motion) == nil {
		t.Error("unknown models must not be filtered")
	}
}
