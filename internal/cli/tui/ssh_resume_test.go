package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
)

func TestSSHResumeDefersRenderingUntilSizeAndMaterializesOnDemand(t *testing.T) {
	clearMotionEnv(t)
	t.Setenv("SSH_CONNECTION", "fixture")
	withEmptyTrustStore(t)
	opts := Options{Approve: true, Model: "gpt-4.1", ProviderName: "openai"}
	s, _, err := newRunSessionWithStore(newTestStore(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	const messages = 392
	history := make([]agentcore.Message, 0, messages)
	for i := range messages {
		text := fmt.Sprintf("entry-%03d %s", i, strings.Repeat("body ", 30))
		if i%2 == 0 {
			history = append(history, userMsg(text))
		} else {
			history = append(history, agentcore.AssistantMessage{Content: agentcore.ContentList{agentcore.NewTextContent(text)}})
		}
	}
	start := time.Now()
	m := NewModel(opts).withSession(s, history)
	seedTime := time.Since(start)
	if len(m.transcript.blocks) != messages || m.transcript.matEnd != 0 || len(m.transcript.prefixLines) != 0 {
		t.Fatal("SSH must retain every source block without rendering at width zero")
	}
	for _, b := range m.transcript.blocks {
		if b.role == roleBanner {
			t.Fatal("SSH must not seed a block-font splash")
		}
	}
	start = time.Now()
	m = apply(t, m, tea.WindowSizeMsg{Width: 211, Height: 53})
	sizeTime := time.Since(start)
	if got := m.transcript.matEnd - m.transcript.matStart; got != 1 {
		t.Fatalf("first SSH layout materialized %d blocks instead of one", got)
	}
	if !strings.Contains(stripANSI(m.View().Content), "entry-391") {
		t.Fatal("first sized frame must show the most recent message")
	}
	if !m.seedMoreActive {
		t.Fatal("a short first block must arm budgeted screen filling")
	}
	m = apply(t, m, runeKey('x'))
	if m.input.Value() != "x" {
		t.Fatal("input waited for history materialization")
	}
	for steps := 0; m.seedMoreActive && steps < 100; steps++ {
		m = apply(t, m, seedMoreMsg{gen: m.seedMoreGen})
	}
	if m.seedMoreActive || m.transcript.matStart == 0 {
		t.Fatal("initial screen fill must stop without eagerly materializing all history")
	}
	// Older messages are still available; scrolling materializes, not truncates.
	m.transcript.follow = false
	for steps := 0; m.transcript.matStart > 0 && steps < messages; steps++ {
		m.transcript.vp.GotoTop()
		m.transcript.update(nil)
	}
	body := stripANSI(strings.Join(m.transcript.contentLines(), "\n"))
	if m.transcript.matStart != 0 || !strings.Contains(body, "entry-000") || !strings.Contains(body, "entry-391") {
		t.Fatal("deferred SSH history was lost or could not be scrolled back")
	}
	t.Logf("synthetic 392-message SSH seed=%s first-size-update=%s", seedTime, sizeTime)
}

func TestNoQueryPaletteDefaultsDarkWithoutChangingLocalPalette(t *testing.T) {
	previous := syntaxDark()
	t.Cleanup(func() { SetMarkdownDark(previous) })
	SetMarkdownDark(false)
	prepareTerminalPalette(false)
	if syntaxDark() {
		t.Fatal("local palette was changed by an inactive SSH policy")
	}
	prepareTerminalPalette(true)
	if !syntaxDark() {
		t.Fatal("query-free startup must use the default dark Markdown palette")
	}
}
