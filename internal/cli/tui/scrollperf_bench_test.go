package tui

// Benchmarks for the scroll path. A wheel event is not just a viewport offset:
// Bubble Tea calls View() after EVERY message it dispatches (tea.go, eventLoop),
// so one two-finger flick on macOS pays for one full shell render per wheel
// event the terminal reports — plus the transcript's own lazy-seed work when the
// flick reaches the top of the materialized window. These benchmarks measure
// exactly that: one wheel event (Update + View) against a transcript that looks
// like a real session, and the burst a fast flick produces.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/ui"
)

// scrollTurns is how many turns the fixture replays. A working session is
// usually a few dozen turns; the numbers here are chosen so the transcript
// overflows the viewport by a wide margin (which is when the scrollbar, the
// wheel path and the lazy seed are all live).
const scrollTurns = 60

// bashResult is a tool result shaped like real command output: enough lines that
// the card is a meaningful part of the frame, and long enough that re-rendering
// it is not free.
func bashResult(i int) string {
	var b strings.Builder
	for j := 0; j < 12; j++ {
		fmt.Fprintf(&b, "internal/cli/tui/scrollperf.go:%d: some diagnostic line %d\n", i*10+j, j)
	}
	return b.String()
}

// markdownAnswer is an assistant turn with the markdown a real answer carries
// (headings, a list, inline code), which is what makes renderMarkdown the
// expensive part of a frame.
func markdownAnswer(i int) string {
	return fmt.Sprintf("Here is what I found for step %d:\n\n"+
		"- the first thing (`scrollToRow`) is fine\n"+
		"- the second thing is not\n\n"+
		"```go\nfunc f%d() error { return nil }\n```\n\n"+
		"That is the shape of it.", i, i)
}

// scrollFixture builds a model holding a session the size of a real transcript:
// the banner, then turns of user text, a markdown answer and a finished bash
// card. It is sized like a full-screen terminal before returning, because every
// cost below depends on the geometry.
func scrollFixture(tb testing.TB, turns int) Model {
	tb.Helper()
	m := NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	m.statusBar.SetGit(gitInfoMsg{branch: "master", dirty: 2, ahead: 1, ok: true})
	m.statusBar.SetMode("auto")
	m.statusBar.SetTelemetry(telemetryEventView{util: 0.42, window: 200000, tokens: 84000})
	m.transcript.addBanner(renderBannerFrame(m.theme, m.bannerOptions(), m.cwd, logoFrames))
	for i := 0; i < turns; i++ {
		m.transcript.addUser(fmt.Sprintf("question %d: why does the frame cost so much when I scroll?", i))
		m.transcript.finalizeTurn(agentcore.AssistantMessage{
			Content: agentcore.ContentList{agentcore.NewTextContent(markdownAnswer(i))},
		})
		card := &toolCard{
			id:    fmt.Sprintf("call_%d", i),
			name:  "bash",
			input: map[string]any{"command": fmt.Sprintf("rg -n 'scroll' internal/cli/tui | head -%d", i)},
			state: cardSuccess,
		}
		card.complete(true, bashResult(i), nil)
		m.transcript.addToolCard(card)
	}
	m.relayout()
	return m
}

// resumedHistory builds the message list a resumed session replays: a user turn,
// an assistant answer with a bash call, and that call's result — three messages
// per turn, which is what a real session file looks like.
func resumedHistory(turns int) agentcore.MessageList {
	msgs := make(agentcore.MessageList, 0, turns*3)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		args, err := json.Marshal(map[string]string{
			"command": fmt.Sprintf("rg -n 'scroll' internal/cli/tui | head -%d", i),
		})
		if err != nil {
			panic(err)
		}
		msgs = append(msgs,
			agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf("question %d: why is the frame expensive?", i))},
			},
			agentcore.AssistantMessage{
				RoleField: agentcore.RoleAssistant,
				Content: agentcore.ContentList{
					agentcore.NewTextContent(markdownAnswer(i)),
					agentcore.NewToolCallContent(callID, "bash", args),
				},
			},
			agentcore.ToolResultMessage{
				RoleField:  agentcore.RoleToolResult,
				ToolCallID: callID,
				ToolName:   "bash",
				Content:    agentcore.ContentList{agentcore.NewTextContent(bashResult(i))},
			},
		)
	}
	return msgs
}

// resumedFixture is a model launched against an existing session: the whole
// history is replayed through seedTranscript in one batch, which is the case the
// lazy seed exists for (only the newest window is materialized up front).
func resumedFixture(tb testing.TB, turns int) Model {
	tb.Helper()
	m := NewModel(Options{Model: "claude-opus", ProviderName: "commandcode"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	m.statusBar.SetGit(gitInfoMsg{branch: "master", dirty: 2, ahead: 1, ok: true})
	m.transcript.addBanner(renderBannerFrame(m.theme, m.bannerOptions(), m.cwd, logoFrames))
	seedTranscript(&m.transcript, resumedHistory(turns))
	m.relayout()
	return m
}

// benchModelViewFrame is the whole per-event cost Bubble Tea pays: the Update
// that decides the scroll, then the View the renderer asks for.
func benchScrollEvent(b *testing.B, msg tea.Msg) {
	m := scrollFixture(b, scrollTurns)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(msg)
		if mm, ok := next.(Model); ok {
			_ = mm.View()
		}
	}
}

// BenchmarkScrollWheelUpEvent is one wheel-up event: the scroll itself plus the
// frame that follows it. At 60fps a frame has 16.6ms; the terminal reports a
// trackpad flick far faster than that, so this number times the event rate is
// the CPU the scroll costs.
func BenchmarkScrollWheelUpEvent(b *testing.B) {
	benchScrollEvent(b, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
}

// BenchmarkScrollWheelDownEvent is the same event scrolling back down.
func BenchmarkScrollWheelDownEvent(b *testing.B) {
	benchScrollEvent(b, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
}

// BenchmarkScrollMotionEvent is one mouse-motion event. Cell-motion reporting
// sends these while a button is held, so a drag across the window pays this per
// cell crossed.
func BenchmarkScrollMotionEvent(b *testing.B) {
	benchScrollEvent(b, tea.MouseMotionMsg{X: 20, Y: 3})
}

// BenchmarkScrollUpdateOnly is the Update half of a wheel event on its own, so
// the frame's share of the per-event cost can be read off against
// BenchmarkScrollViewOnly.
func BenchmarkScrollUpdateOnly(b *testing.B) {
	m := scrollFixture(b, scrollTurns)
	msg := tea.MouseWheelMsg{Button: tea.MouseWheelUp}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next, _ := m.Update(msg)
		if _, ok := next.(Model); !ok {
			b.Fatal("Update returned a foreign model")
		}
	}
}

// BenchmarkScrollViewOnly is the frame half: what the renderer asks for after
// every message Bubble Tea dispatches.
func BenchmarkScrollViewOnly(b *testing.B) {
	m := scrollFixture(b, scrollTurns)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}

// BenchmarkScrollFlickBurst is a fast flick: 24 wheel events, each followed by
// the frame the renderer asks for. It reports the wall time the whole burst
// costs the event loop — the number that decides whether the content on screen
// keeps up with the fingers.
func BenchmarkScrollFlickBurst(b *testing.B) {
	m := scrollFixture(b, scrollTurns)
	// Start below the top so the burst scrolls into materialized history and
	// does not sit on the lazy-seed boundary.
	m.transcript.jumpToBottom()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for k := 0; k < 24; k++ {
			next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			if mm, ok := next.(Model); ok {
				_ = mm.View()
			}
		}
	}
}

// BenchmarkScrollStreamingFrame is the frame that runs while an answer streams:
// the wheel is the input the user reaches for mid-run, and every delta re-renders
// the streaming markdown block on top of it.
func BenchmarkScrollStreamingFrame(b *testing.B) {
	m := scrollFixture(b, scrollTurns)
	m.transcript.appendDelta("A partial answer that is still arriving, ")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.transcript.appendDelta("more text ")
		next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		if mm, ok := next.(Model); ok {
			_ = mm.View()
		}
	}
}

// BenchmarkResumedSeedAndScroll is the resumed-session path, which is where the
// scroll used to freeze: the whole history lands in one batch (the replay), only
// its newest window is rendered up front, and every scroll event that reaches the
// top of that window materializes more of it. Both numbers are reported per
// iteration — the seed and a 200-event scroll-to-the-top-of-the-window flick — so
// a regression in either shows up as one number moving.
func BenchmarkResumedSeedAndScroll(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		seedStart := time.Now()
		m := resumedFixture(b, 900)
		seed := time.Since(seedStart)

		scrollStart := time.Now()
		for k := 0; k < 200 && m.transcript.matStart > 0; k++ {
			next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
			mm, ok := next.(Model)
			if !ok {
				b.Fatal("Update returned a foreign model")
			}
			m = mm
			_ = m.View()
		}
		b.ReportMetric(float64(seed.Nanoseconds())/1e6, "seed-ms")
		b.ReportMetric(float64(time.Since(scrollStart).Nanoseconds())/1000, "scroll-µs")
	}
}

// markdownOf builds a markdown answer of roughly the given size, shaped like a
// real one: prose, a list, and fenced code blocks (which is what makes chroma
// highlighting run).
func markdownOf(bytes int) string {
	var b strings.Builder
	section := "## What I found\n\n" +
		"The scroll path rebuilds the whole frame for every event the terminal reports. " +
		"That is `Model.View`, called after every message, and it walks the transcript.\n\n" +
		"- the first item names `scrollToRow`\n" +
		"- the second item names the status bar\n\n" +
		"```go\nfunc renderBlock(blk transcriptBlock, width int) string {\n\treturn blk.text\n}\n```\n\n"
	for b.Len() < bytes {
		b.WriteString(section)
	}
	return b.String()
}

// BenchmarkMarkdownBlockRender is the cost the transcript pays when it renders
// one assistant answer — the operation the streaming path repeats for every delta
// and the lazy seed repeats for every block it materializes.
//
// It drives the cached glamour renderer directly because the transcript's own
// renderMarkdown is gated on ui.Enabled(), which is false under `go test` (stdout
// is a pipe there) — so a benchmark that went through renderMarkdown would
// measure the gate instead of the render, i.e. exactly the numbers a real
// terminal never sees.
func BenchmarkMarkdownBlockRender(b *testing.B) {
	r := rendererFor(120)
	if r == nil {
		b.Fatal("no markdown renderer")
	}
	for _, size := range []int{2 << 10, 8 << 10, 32 << 10} {
		src := markdownOf(size)
		b.Run(fmt.Sprintf("%dKB", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := r.Render(src); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMarkdownGateCost is the per-call cost of the color gate itself:
// ui.Enabled() stats stdout on every markdown render, which is one syscall per
// block per frame.
func BenchmarkMarkdownGateCost(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ui.Enabled()
	}
}
