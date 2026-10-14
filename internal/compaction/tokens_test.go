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
// compacted at 98.4% full: the percentage headroom sets the trigger at 80% of the
// window, and the fixed reserve still wins on small windows where 20% would hold
// back less than a summary plus a turn needs.
func TestShouldCompactPercentHeadroom(t *testing.T) {
	s := DefaultCompactionSettings

	// 1M window: 200k headroom dominates the 16k reserve, so 800k is the
	// trigger. A reading at 850k must compact.
	if got, want := s.UsableTokens(1_000_000), 800_000; got != want {
		t.Fatalf("UsableTokens(1M) = %d, want %d", got, want)
	}
	if !ShouldCompact(850_000, 1_000_000, s) {
		t.Fatal("850k of a 1M window must compact")
	}
	if ShouldCompact(790_000, 1_000_000, s) {
		t.Fatal("790k of a 1M window must not compact")
	}

	// 128k window: 20% (25,600) beats the 16k reserve, so the percentage governs
	// and the trigger sits at 102,400 — the 80% the default is chosen for.
	if got, want := s.UsableTokens(128_000), 128_000-25_600; got != want {
		t.Fatalf("UsableTokens(128k) = %d, want %d", got, want)
	}

	// 32k window: 20% is 6,400, below the 16k reserve, so the fixed floor still
	// governs and keeps half the window usable.
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
	// Trigger = 400k - 80k (20% headroom) = 320k, well below the 800k a 1M
	// window would give.
	if got, want := s.UsableTokens(1_000_000), 320_000; got != want {
		t.Fatalf("UsableTokens with max_context = %d, want %d", got, want)
	}
	if !ShouldCompact(330_000, 1_000_000, s) {
		t.Fatal("330k must compact when max_context is 400k")
	}

	// A max_context above the provider window is clamped to it.
	s.MaxContextTokens = 2_000_000
	if got, want := s.UsableTokens(1_000_000), 800_000; got != want {
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
	if DefaultCompactionSettings.HeadroomPercent != 20 {
		t.Fatalf("HeadroomPercent: got %d, want 20 (compact at 80%%)", DefaultCompactionSettings.HeadroomPercent)
	}
	if DefaultCompactionSettings.KeepRecentTokens != 65536 {
		t.Fatalf("KeepRecentTokens: got %d, want 65536 (64k)", DefaultCompactionSettings.KeepRecentTokens)
	}
	if !DefaultCompactionSettings.Enabled {
		t.Fatal("default settings should be enabled")
	}
}

// TestKeepTokensClampsForSmallWindows pins the clamp that keeps auto-compaction
// working on a window smaller than the configured tail.
//
// Without it, a 64k tail on a 64k window is a tail the cut can never reach:
// FindCutPoint walks back, never accumulates the budget, keeps every message, and
// the summarization range is empty — so the run reports "nothing to compact"
// while sitting above its own trigger, and the next fat turn overflows the
// provider's window instead. The clamp leaves half the usable window for the
// summary and the work that follows the cut.
func TestKeepTokensClampsForSmallWindows(t *testing.T) {
	s := DefaultCompactionSettings

	// A window large enough for the configured tail: no clamping, so the 64k
	// default is what a 200k model actually keeps.
	if got := s.KeepTokens(200_000); got != 65536 {
		t.Errorf("KeepTokens(200k) = %d, want the configured 65536", got)
	}

	// Below that, the tail is half the usable window — derived from the same
	// number the trigger uses, so the two cannot drift apart in this test.
	for _, window := range []int{128_000, 64_000, 32_000} {
		usable := s.UsableTokens(window)
		if got, want := s.KeepTokens(window), usable/2; got != want {
			t.Errorf("KeepTokens(%d) = %d, want half the usable window (%d)", window, got, want)
		}
		// The property that matters: a cut must be possible, so the tail has to
		// leave a prefix behind.
		if got := s.KeepTokens(window); got >= usable {
			t.Errorf("window %d: tail %d must stay under the usable window %d, or nothing can be summarized", window, got, usable)
		}
	}

	// A tail configured small keeps its exact value: the clamp only lowers.
	small := s
	small.KeepRecentTokens = 1000
	if got := small.KeepTokens(64_000); got != 1000 {
		t.Errorf("KeepTokens = %d, want the configured 1000 (the clamp must not raise)", got)
	}

	// An unknown window leaves the configured tail alone; the caller decides
	// whether compacting is meaningful without a window.
	if got := s.KeepTokens(0); got != 65536 {
		t.Errorf("KeepTokens(0) = %d, want the configured tail", got)
	}
}
