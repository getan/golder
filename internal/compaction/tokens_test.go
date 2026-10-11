package compaction

import (
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func userMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

func assistantMsg(text string, usage *agentcore.Usage, stop string) agentcore.AssistantMessage {
	return agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
		Usage:      usage,
		StopReason: stop,
	}
}

func TestEstimateTokensText(t *testing.T) {
	// 8 chars / 4 = 2 tokens.
	if got := EstimateTokens(userMsg("abcdefgh")); got != 2 {
		t.Fatalf("text estimate: got %d, want 2", got)
	}
	// 9 chars -> ceil(9/4) = 3.
	if got := EstimateTokens(userMsg("abcdefghi")); got != 3 {
		t.Fatalf("ceil estimate: got %d, want 3", got)
	}
	// empty -> 0.
	if got := EstimateTokens(userMsg("")); got != 0 {
		t.Fatalf("empty estimate: got %d, want 0", got)
	}
}

func TestEstimateTokensImage(t *testing.T) {
	m := agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewImageContent("data", "image/png")},
	}
	// estimatedImageChars (4800) / 4 = 1200.
	if got := EstimateTokens(m); got != 1200 {
		t.Fatalf("image estimate: got %d, want 1200", got)
	}
}

func TestEstimateTokensAssistantToolCall(t *testing.T) {
	args := json.RawMessage(`{"path":"a.go"}`) // 15 chars
	m := agentcore.AssistantMessage{
		RoleField: agentcore.RoleAssistant,
		Content: agentcore.ContentList{
			agentcore.NewToolCallContent("id1", "read", args), // name "read" = 4 chars
		},
	}
	// (4 + 15) / 4 = ceil(19/4) = 5.
	if got := EstimateTokens(m); got != 5 {
		t.Fatalf("toolcall estimate: got %d, want 5", got)
	}
}

func TestEstimateTokensUnknownRoleZero(t *testing.T) {
	if got := EstimateTokens(agentcore.ToolResultMessage{}); got != 0 {
		t.Fatalf("empty tool result: got %d, want 0", got)
	}
}

func TestEstimateContextTokensNoUsageFallsBackToEstimate(t *testing.T) {
	msgs := []agentcore.Message{
		userMsg("abcdefgh"),                   // 2
		assistantMsg("abcd", nil, "end_turn"), // 1
	}
	est := EstimateContextTokens(msgs)
	if est.LastUsageIndex != -1 {
		t.Fatalf("LastUsageIndex: got %d, want -1", est.LastUsageIndex)
	}
	if est.Tokens != 3 {
		t.Fatalf("Tokens: got %d, want 3", est.Tokens)
	}
	if est.TrailingTokens != 3 || est.UsageTokens != 0 {
		t.Fatalf("trailing/usage: got %d/%d, want 3/0", est.TrailingTokens, est.UsageTokens)
	}
}

func TestEstimateContextTokensPrefersUsage(t *testing.T) {
	usage := &agentcore.Usage{InputTokens: 1000, OutputTokens: 200}
	msgs := []agentcore.Message{
		userMsg("first"),
		assistantMsg("reply", usage, "end_turn"),
		userMsg("abcdefgh"), // trailing: 2 tokens
	}
	est := EstimateContextTokens(msgs)
	if est.LastUsageIndex != 1 {
		t.Fatalf("LastUsageIndex: got %d, want 1", est.LastUsageIndex)
	}
	if est.UsageTokens != 1200 {
		t.Fatalf("UsageTokens: got %d, want 1200", est.UsageTokens)
	}
	if est.TrailingTokens != 2 {
		t.Fatalf("TrailingTokens: got %d, want 2", est.TrailingTokens)
	}
	if est.Tokens != 1202 {
		t.Fatalf("Tokens: got %d, want 1202", est.Tokens)
	}
}

func TestEstimateContextTokensCountsCacheTokens(t *testing.T) {
	// Anthropic-wire usage reports cached prompt content separately from
	// input_tokens, so context accounting must fold it in (parity with pi).
	usage := &agentcore.Usage{
		InputTokens:      10,
		OutputTokens:     5,
		CacheReadTokens:  900,
		CacheWriteTokens: 100,
	}
	msgs := []agentcore.Message{assistantMsg("reply", usage, "end_turn")}
	est := EstimateContextTokens(msgs)
	if est.UsageTokens != 1015 {
		t.Fatalf("UsageTokens: got %d, want 1015", est.UsageTokens)
	}
	if est.Tokens != 1015 {
		t.Fatalf("Tokens: got %d, want 1015", est.Tokens)
	}
}

func TestEstimateContextTokensSkipsAbortedAndErrorUsage(t *testing.T) {
	good := &agentcore.Usage{InputTokens: 500, OutputTokens: 0}
	bad := &agentcore.Usage{InputTokens: 9999, OutputTokens: 0}
	msgs := []agentcore.Message{
		assistantMsg("ok", good, "end_turn"),
		assistantMsg("aborted", bad, agentcore.StopReasonAborted),
		assistantMsg("errored", bad, agentcore.StopReasonError),
	}
	est := EstimateContextTokens(msgs)
	if est.LastUsageIndex != 0 {
		t.Fatalf("LastUsageIndex: got %d, want 0 (should skip aborted/error)", est.LastUsageIndex)
	}
	if est.UsageTokens != 500 {
		t.Fatalf("UsageTokens: got %d, want 500", est.UsageTokens)
	}
}

func TestEstimateContextTokensSkipsZeroUsage(t *testing.T) {
	zero := &agentcore.Usage{InputTokens: 0, OutputTokens: 0}
	msgs := []agentcore.Message{
		userMsg("abcdefgh"), // 2
		assistantMsg("reply", zero, "end_turn"),
	}
	est := EstimateContextTokens(msgs)
	// zero usage is ignored, so falls back to full estimation.
	if est.LastUsageIndex != -1 {
		t.Fatalf("LastUsageIndex: got %d, want -1", est.LastUsageIndex)
	}
}

// TestEstimateContextTokensUsesWindowedMax reproduces the gateway that reported
// one growing 1M-window conversation at two levels, alternating between ~87%
// and ~95%: the figure must track the high level, or auto-compaction keeps
// deferring off the low one (the reported "95% and still no compact").
func TestEstimateContextTokensUsesWindowedMax(t *testing.T) {
	high, low := &agentcore.Usage{InputTokens: 953_908}, &agentcore.Usage{InputTokens: 870_000}
	msgs := []agentcore.Message{
		assistantMsg("a", high, "tool_use"),
		assistantMsg("b", low, "tool_use"),
		assistantMsg("c", high, "tool_use"),
		assistantMsg("d", low, "tool_use"),
	}
	est := EstimateContextTokens(msgs)
	if est.UsageTokens != 953_908 {
		t.Fatalf("UsageTokens: got %d, want the high reading 953908", est.UsageTokens)
	}
	if est.LastUsageIndex != 0 && est.LastUsageIndex != 2 {
		t.Fatalf("LastUsageIndex: got %d, want a high-reading index (0 or 2)", est.LastUsageIndex)
	}
}

// TestEstimateContextTokensDropsOlderSpike pins the artifact guard: a single
// reading several times its neighbours (980k beside 383k) is gateway noise, not
// a context that grew and shrank, and must not drag the estimate to a third of
// the window.
func TestEstimateContextTokensDropsOlderSpike(t *testing.T) {
	spike, normal := &agentcore.Usage{InputTokens: 980_460}, &agentcore.Usage{InputTokens: 383_762}
	msgs := []agentcore.Message{
		assistantMsg("a", normal, "tool_use"),
		assistantMsg("b", spike, "tool_use"),
		assistantMsg("c", normal, "tool_use"),
	}
	est := EstimateContextTokens(msgs)
	if est.UsageTokens != 383_762 {
		t.Fatalf("UsageTokens: got %d, want the normal reading 383762 (spike dropped)", est.UsageTokens)
	}
}

// TestEstimateContextTokensIgnoresStaleReadingsAfterCompaction pins the other
// half of the oscillation bug: once a compaction rewrites the context, the
// readings carried by the retained tail described the pre-compaction request.
// Counting them again would report the old size on the very next turn and
// re-trigger compaction forever (or, before this fix, immediately after a
// successful manual /compact).
func TestEstimateContextTokensIgnoresStaleReadingsAfterCompaction(t *testing.T) {
	stale := &agentcore.Usage{InputTokens: 950_000}
	msgs := []agentcore.Message{
		agentcore.CompactionMessage{RoleField: agentcore.RoleCompaction, Summary: "## Goal\nx", KeptCount: 2},
		userMsg("recent question"),
		assistantMsg("recent answer", stale, "end_turn"),
	}
	est := EstimateContextTokens(msgs)
	if est.LastUsageIndex != -1 {
		t.Fatalf("LastUsageIndex: got %d, want -1 (stale tail reading must be ignored)", est.LastUsageIndex)
	}
	// Falls back to character estimation: well under the stale 950k.
	if est.Tokens >= 10_000 {
		t.Fatalf("Tokens: got %d, want a small full estimate", est.Tokens)
	}

	// A reading produced after the checkpoint's tail is fresh and must be used.
	fresh := &agentcore.Usage{InputTokens: 120_000}
	msgs = append(msgs, userMsg("next question"), assistantMsg("next answer", fresh, "tool_use"))
	est = EstimateContextTokens(msgs)
	if est.UsageTokens != 120_000 {
		t.Fatalf("UsageTokens: got %d, want the fresh 120000", est.UsageTokens)
	}
}

func TestShouldCompactThresholdBoundaries(t *testing.T) {
	s := CompactionSettings{Enabled: true, ReserveTokens: 16384}
	window := 200000
	usable := window - s.ReserveTokens // 183616

	tests := []struct {
		name          string
		contextTokens int
		want          bool
	}{
		{"far below", 1000, false},
		{"equal to usable", usable, false}, // strictly greater required
		{"one over usable", usable + 1, true},
		{"far over", window * 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldCompact(tt.contextTokens, window, s); got != tt.want {
				t.Fatalf("ShouldCompact(%d): got %v, want %v", tt.contextTokens, got, tt.want)
			}
		})
	}
}

// TestShouldCompactPercentHeadroom pins the fix for a 1M-window model that only
// compacted at 98.4% full: with the default 10% headroom the trigger sits at
// 90% of the window, and the fixed reserve still wins on small windows where
// 10% would hold back less than the summary needs.
func TestShouldCompactPercentHeadroom(t *testing.T) {
	s := DefaultCompactionSettings

	// 1M window: 100k headroom dominates the 16k reserve, so 900k is the
	// trigger. A reading at 950k (the reported 95%) must compact.
	if got, want := s.UsableTokens(1_000_000), 900_000; got != want {
		t.Fatalf("UsableTokens(1M) = %d, want %d", got, want)
	}
	if !ShouldCompact(950_000, 1_000_000, s) {
		t.Fatal("950k of a 1M window must compact")
	}
	if ShouldCompact(890_000, 1_000_000, s) {
		t.Fatal("890k of a 1M window must not compact")
	}

	// 128k window: 10% (12,800) is below the 16k reserve, so the fixed reserve
	// still governs — parity with the pre-existing behavior.
	if got, want := s.UsableTokens(128_000), 128_000-16384; got != want {
		t.Fatalf("UsableTokens(128k) = %d, want %d", got, want)
	}

	// 32k window: reserve 16k would leave 16k usable; the floor must not be
	// raised by the percentage form.
	if got, want := s.UsableTokens(32_000), 32_000-16384; got != want {
		t.Fatalf("UsableTokens(32k) = %d, want %d", got, want)
	}
}

// TestShouldCompactMaxContextLowersWindow pins max_context: it lowers the
// window the trigger is computed from, and a value above the provider limit is
// clamped (it can never raise the trigger).
func TestShouldCompactMaxContextLowersWindow(t *testing.T) {
	s := DefaultCompactionSettings
	s.MaxContextTokens = 400_000
	// Trigger = 400k - 40k (10% headroom) = 360k, well below the 900k a 1M
	// window would give.
	if got, want := s.UsableTokens(1_000_000), 360_000; got != want {
		t.Fatalf("UsableTokens with max_context = %d, want %d", got, want)
	}
	if !ShouldCompact(370_000, 1_000_000, s) {
		t.Fatal("370k must compact when max_context is 400k")
	}

	// A max_context above the provider window is clamped to it.
	s.MaxContextTokens = 2_000_000
	if got, want := s.UsableTokens(1_000_000), 900_000; got != want {
		t.Fatalf("clamped UsableTokens = %d, want %d", got, want)
	}
}

func TestShouldCompactDisabled(t *testing.T) {
	s := CompactionSettings{Enabled: false, ReserveTokens: 16384}
	if ShouldCompact(1_000_000, 200000, s) {
		t.Fatal("disabled settings must never compact")
	}
}

func TestShouldCompactUnknownWindow(t *testing.T) {
	s := CompactionSettings{Enabled: true, ReserveTokens: 16384}
	if ShouldCompact(1_000_000, 0, s) {
		t.Fatal("unknown (0) context window must never compact")
	}
}

func TestDefaultCompactionSettings(t *testing.T) {
	if DefaultCompactionSettings.ReserveTokens != 16384 {
		t.Fatalf("ReserveTokens: got %d, want 16384", DefaultCompactionSettings.ReserveTokens)
	}
	if DefaultCompactionSettings.HeadroomPercent != 10 {
		t.Fatalf("HeadroomPercent: got %d, want 10", DefaultCompactionSettings.HeadroomPercent)
	}
	if DefaultCompactionSettings.KeepRecentTokens != 20000 {
		t.Fatalf("KeepRecentTokens: got %d, want 20000", DefaultCompactionSettings.KeepRecentTokens)
	}
	if !DefaultCompactionSettings.Enabled {
		t.Fatal("default settings should be enabled")
	}
}
