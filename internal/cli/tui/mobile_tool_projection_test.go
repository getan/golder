package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/redact"
)

func TestMobileTUIToolResultsAndDiffFollowLiveCards(t *testing.T) {
	m, adapter := testMobileModel(t)
	update := func(msg tea.Msg) {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	update(toolStartMsg{id: "read-1", name: "read", input: map[string]any{"path": "private-argument"}})
	running, err := adapter.Snapshot()
	if err != nil || len(running.Messages) != 1 || !strings.Contains(running.Messages[0].Text, "read · running") {
		t.Fatalf("running snapshot: %+v, %v", running, err)
	}
	update(toolEndMsg{id: "read-1", ok: true, result: "first line\n  nested 世界"})
	update(toolStartMsg{id: "grep-1", name: "grep"})
	update(toolEndMsg{id: "grep-1", ok: false, result: "search failed"})
	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new\n"
	update(toolStartMsg{id: "edit-1", name: "edit", input: map[string]any{"patch": "private-argument"}})
	update(toolEndMsg{id: "edit-1", ok: true, result: "Edited file.go\n" + diff, details: map[string]any{"diff": diff, "other": "private-metadata"}})

	snapshot, err := adapter.Snapshot()
	if err != nil || len(snapshot.Messages) != 3 {
		t.Fatalf("snapshot: %+v, %v", snapshot, err)
	}
	for i, want := range []string{"read · completed", "grep · failed or interrupted", "edit · completed"} {
		if snapshot.Messages[i].Role != "tool" || !strings.Contains(snapshot.Messages[i].Text, want) {
			t.Fatalf("message %d: %+v", i, snapshot.Messages[i])
		}
	}
	if !strings.Contains(snapshot.Messages[0].Text, "first line\n  nested 世界") || !strings.Contains(snapshot.Messages[1].Text, "search failed") {
		t.Fatalf("grouped tool results missing: %+v", snapshot.Messages)
	}
	edit := snapshot.Messages[2].Text
	if !strings.Contains(edit, "Result:\nEdited file.go") || !strings.Contains(edit, "Diff:\n"+diff) || strings.Count(edit, "+new") != 1 {
		t.Fatalf("result/diff missing or duplicated: %q", edit)
	}
	for _, message := range snapshot.Messages {
		if strings.Contains(message.Text, "private-argument") || strings.Contains(message.Text, "private-metadata") {
			t.Fatalf("input or unrelated metadata leaked: %+v", message)
		}
	}
	for _, block := range m.transcript.blocks {
		if block.group != nil && block.group.expanded {
			t.Fatal("mobile projection changed the local exploration group")
		}
	}
}

func TestMobileTUIToolProjectionFromReplayedSession(t *testing.T) {
	m, adapter := testMobileModel(t)
	diff := "--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n"
	history := agentcore.MessageList{
		agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, Content: agentcore.ContentList{
			agentcore.ToolCallContent{Type: agentcore.ContentTypeToolCall, ID: "read", Name: "read", Arguments: []byte(`{"path":"private-input"}`)},
			agentcore.ToolCallContent{Type: agentcore.ContentTypeToolCall, ID: "edit", Name: "edit", Arguments: []byte(`{}`)},
		}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "read", ToolName: "read", Content: agentcore.ContentList{agentcore.NewTextContent("replayed output")}},
		agentcore.ToolResultMessage{RoleField: agentcore.RoleToolResult, ToolCallID: "edit", ToolName: "edit", Content: agentcore.ContentList{agentcore.NewTextContent("Changed file\n" + diff)}, Details: map[string]any{"diff": diff}},
	}
	seedTranscript(&m.transcript, history)
	m.publishMobileSnapshot()
	snapshot, err := adapter.Snapshot()
	if err != nil || len(snapshot.Messages) != 2 || !strings.Contains(snapshot.Messages[0].Text, "replayed output") || !strings.Contains(snapshot.Messages[1].Text, "Diff:\n"+diff) {
		t.Fatalf("replayed snapshot: %+v, %v", snapshot, err)
	}
	if strings.Contains(snapshot.Messages[0].Text, "private-input") {
		t.Fatal("replayed tool arguments leaked")
	}
	snapshot.Messages[0].Text = "changed by caller"
	again, err := adapter.Snapshot()
	if err != nil || !strings.Contains(again.Messages[0].Text, "replayed output") {
		t.Fatalf("snapshot was not isolated for reconnect: %+v, %v", again, err)
	}
}

func TestMobileToolProjectionRedactsAndStripsControls(t *testing.T) {
	r, err := redact.New(redact.Config{Mode: redact.Redact})
	if err != nil {
		t.Fatal(err)
	}
	secret := "known-sensitive-fixture-12345"
	r.AddKnown(secret)
	card := &toolCard{
		name:     "edit",
		state:    cardSuccess,
		response: parseToolResult("output " + secret[:8] + "\x1b[31m" + secret[8:] + "\x1b[0m"),
		diff:     "--- a/file\n+++ b/file\n+" + secret + "\n\x1b]52;c;YQ==\a",
	}
	budget := &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan, redactor: r}
	text := budget.mobileToolText(card)
	if strings.Contains(text, secret) || strings.ContainsAny(text, "\x1b\a") || strings.Count(text, "[REDACTED]") != 2 {
		t.Fatalf("unsafe output: %q", text)
	}
	if !strings.Contains(text, "Diff:\n--- a/file") {
		t.Fatalf("safe diff was lost: %q", text)
	}
}

func TestMobileToolProjectionBoundsAndLocalShell(t *testing.T) {
	card := &toolCard{name: "bash", state: cardSuccess, response: parseToolResult(strings.Repeat("界", 5000))}
	budget := &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan}
	text := budget.mobileToolText(card)
	if !utf8.ValidString(text) || !strings.Contains(text, "Result truncated") || len(text) > mobileToolSectionBytes+200 {
		t.Fatalf("invalid or unbounded output: bytes=%d text=%q", len(text), text)
	}
	r, err := redact.New(redact.Config{Mode: redact.Redact})
	if err != nil {
		t.Fatal(err)
	}
	card.response = parseToolResult(strings.Repeat("x", mobileToolScanBytes+20))
	card.diff = "--- a/file\n+++ b/file\n+safe\n"
	budget = &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan, redactor: r}
	text = budget.mobileToolText(card)
	if !strings.Contains(text, "Result omitted: too large to safely redact") || strings.Contains(text, strings.Repeat("x", 100)) || !strings.Contains(text, "+safe") {
		t.Fatalf("unsafe oversized result handling: %q", text)
	}
	card = &toolCard{name: "shell", state: cardSuccess, response: parseToolResult("local-private-output")}
	text = budget.mobileToolText(card)
	if strings.Contains(text, "local-private-output") || !strings.Contains(text, "Local shell output remains on the Mac") {
		t.Fatalf("local-only shell output: %q", text)
	}
}

func TestMobileTUIToolBudgetPrefersRecentCards(t *testing.T) {
	m, _ := testMobileModel(t)
	for i := range 10 {
		card := &toolCard{name: "bash", state: cardSuccess, response: parseToolResult(fmt.Sprintf("card-%d:", i) + strings.Repeat("x", mobileToolSectionBytes))}
		m.transcript.blocks = append(m.transcript.blocks, transcriptBlock{role: roleTool, card: card})
	}
	snapshot, _ := m.mobileSnapshot()
	if len(snapshot.Messages) != 10 {
		t.Fatalf("got %d messages", len(snapshot.Messages))
	}
	if !strings.Contains(snapshot.Messages[0].Text, "Result omitted: mobile snapshot limit") || !strings.Contains(snapshot.Messages[9].Text, "card-9:") {
		t.Fatalf("budget did not prioritize recent cards: first=%q last=%q", snapshot.Messages[0].Text, snapshot.Messages[9].Text)
	}
}

func TestMobileTUIBoundsExplorationGroups(t *testing.T) {
	m, _ := testMobileModel(t)
	group := &exploreGroup{}
	for i := range 205 {
		group.cards = append(group.cards, &toolCard{name: "read", state: cardSuccess, response: parseToolResult(fmt.Sprintf("result-%d", i))})
	}
	m.transcript.blocks = []transcriptBlock{{role: roleTool, group: group}}
	snapshot, _ := m.mobileSnapshot()
	if !snapshot.Truncated || len(snapshot.Messages) != 200 || snapshot.Messages[0].ID != "0:5" || snapshot.Messages[199].ID != "0:204" {
		t.Fatalf("unbounded or misordered group: truncated=%v count=%d", snapshot.Truncated, len(snapshot.Messages))
	}
	if !strings.Contains(snapshot.Messages[0].Text, "result-5") || !strings.Contains(snapshot.Messages[199].Text, "result-204") || group.expanded {
		t.Fatal("group contents or local expansion changed")
	}
}

func TestMobileToolCacheInvalidatesForResultsBudgetsAndRedaction(t *testing.T) {
	r, err := redact.New(redact.Config{Mode: redact.Redact})
	if err != nil {
		t.Fatal(err)
	}
	secret := "known-cache-fixture-12345"
	card := &toolCard{name: "read", state: cardSuccess, response: parseToolResult(secret)}
	cache := &mobileToolCache{}
	project := func(output int) (string, *mobileToolBudget) {
		cache.begin()
		budget := &mobileToolBudget{output: output, scan: mobileToolTotalScan, redactor: r}
		text := cache.project(card, budget)
		cache.prune()
		return text, budget
	}
	first, firstBudget := project(mobileToolOutputBytes)
	again, secondBudget := project(mobileToolOutputBytes)
	if first != again || firstBudget.output != secondBudget.output || firstBudget.scan != secondBudget.scan || !strings.Contains(first, secret) {
		t.Fatalf("cached result or budget changed: %q %q", first, again)
	}
	r.AddKnown(secret)
	redacted, _ := project(mobileToolOutputBytes)
	if strings.Contains(redacted, secret) || !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("cache survived redaction policy change: %q", redacted)
	}
	card.response = parseToolResult("replacement result")
	replaced, _ := project(mobileToolOutputBytes)
	if !strings.Contains(replaced, "replacement result") || strings.Contains(replaced, "[REDACTED]") {
		t.Fatalf("cache survived new result: %q", replaced)
	}
	limited, _ := project(4)
	if !strings.Contains(limited, "Result truncated") || strings.Contains(limited, "replacement result") {
		t.Fatalf("cache ignored changed budget: %q", limited)
	}
	cache.begin()
	cache.prune()
	if len(cache.entries) != 0 {
		t.Fatal("cache retained a card outside the snapshot")
	}
}

func BenchmarkMobileToolProjection(b *testing.B) {
	r, err := redact.New(redact.Config{Mode: redact.Redact})
	if err != nil {
		b.Fatal(err)
	}
	card := &toolCard{name: "edit", state: cardSuccess, response: parseToolResult(strings.Repeat("result line\n", 4000)), diff: strings.Repeat("+changed line\n", 4000)}
	b.ResetTimer()
	for range b.N {
		budget := &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan, redactor: r}
		_ = budget.mobileToolText(card)
	}
}

func BenchmarkMobileToolProjectionCached(b *testing.B) {
	r, err := redact.New(redact.Config{Mode: redact.Redact})
	if err != nil {
		b.Fatal(err)
	}
	card := &toolCard{name: "edit", state: cardSuccess, response: parseToolResult(strings.Repeat("result line\n", 4000)), diff: strings.Repeat("+changed line\n", 4000)}
	cache := &mobileToolCache{}
	cache.begin()
	cache.project(card, &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan, redactor: r})
	cache.prune()
	b.ResetTimer()
	for range b.N {
		budget := &mobileToolBudget{output: mobileToolOutputBytes, scan: mobileToolTotalScan, redactor: r}
		cache.begin()
		_ = cache.project(card, budget)
		cache.prune()
	}
}
