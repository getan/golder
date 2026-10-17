package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Benchmarks for the fold's cost. Motion reporting (xterm 1003) sends an event
// for every cell the pointer crosses, so the per-event path has to be cheap: the
// hover update decides a segment from the same layout the renderer paints, and a
// frame re-renders only when the decision changes.

// benchModel is a model with a full transcript and a populated status bar, which
// is what a motion event lands on in a real session.
func benchModel(b *testing.B) Model {
	b.Helper()
	m := NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	m.statusBar.SetGit(gitInfoMsg{branch: "master", dirty: 2, ahead: 1, ok: true})
	m.statusBar.SetMode("auto")
	m.statusBar.SetTelemetry(telemetryEventView{util: 0.42, window: 200000, tokens: 84000})
	m.statusBar.SetTask("running: Read")
	for i := 0; i < 60; i++ {
		m.transcript.addUser(fmt.Sprintf("question %d: how does the fold behave?", i))
		m.transcript.addSystem(fmt.Sprintf("answer %d: the tail fields collapse to slivers", i))
	}
	m.relayout()
	return m
}

// BenchmarkStatusBarRender is the bar's own cost per frame: the fold layout, the
// cell canvas, and the styled row it turns into.
func BenchmarkStatusBarRender(b *testing.B) {
	s := foldFixture()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Render(120)
	}
}

// BenchmarkStatusBarSegmentAt is the hit test a hover runs: one layout pass and a
// column lookup, no allocation intended.
func BenchmarkStatusBarSegmentAt(b *testing.B) {
	s := foldFixture()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.segmentAt(20, 120)
	}
}

// BenchmarkModelStatusHoverMotion is what one motion event costs end to end: the
// Update path that decides whether the pointer changed segment.
func BenchmarkModelStatusHoverMotion(b *testing.B) {
	m := benchModel(b)
	msg := tea.MouseMotionMsg{X: 20, Y: m.height - 1}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(msg)
		if _, ok := next.(Model); !ok {
			b.Fatal("Update returned a foreign model")
		}
	}
}

// BenchmarkModelStatusHoverMotionOffBar is the same event when the pointer is
// anywhere else: it must do nothing at all.
func BenchmarkModelStatusHoverMotionOffBar(b *testing.B) {
	m := benchModel(b)
	msg := tea.MouseMotionMsg{X: 20, Y: 3}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(msg)
		if _, ok := next.(Model); !ok {
			b.Fatal("Update returned a foreign model")
		}
	}
}

// BenchmarkModelView is the frame the renderer asks for after an update: the whole
// shell (transcript, input, status bar), which is the cost an event pays only when
// the model actually changed.
func BenchmarkModelView(b *testing.B) {
	m := benchModel(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}
