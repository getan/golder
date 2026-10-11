// Package compaction implements context-window token accounting and the
// decision of when a long session must be compacted, mirroring pi's
// harness/compaction/compaction.ts.
//
// This file (US-001) covers the token side: estimating a message's token
// footprint from a character heuristic, deriving the current context-token
// usage (preferring provider-reported Usage over estimation), and the
// ShouldCompact threshold check. Cut-point finding (US-002) and summarization
// (US-003) live in sibling files.
package compaction

import (
	"encoding/json"
	"fmt"

	"github.com/getan/golder/internal/agentcore"
)

// CompactionSettings holds the thresholds and retention knobs for compaction,
// mirroring pi's CompactionSettings.
type CompactionSettings struct {
	// Enabled gates automatic compaction decisions.
	Enabled bool
	// ReserveTokens is reserved for the summary prompt and its output, and is
	// also the floor under HeadroomPercent: the usable window is
	// contextWindow - max(ReserveTokens, HeadroomPercent of the window).
	ReserveTokens int
	// HeadroomPercent keeps a slice of the window free beyond ReserveTokens, as
	// a percentage (10 = full at 90%). A fixed reserve cannot serve every
	// window: 16k is most of a 32k window but a rounding error on a 1M one,
	// where it let a conversation climb to 98% before compacting. 0 keeps the
	// fixed reserve only (pi's behavior).
	HeadroomPercent int
	// MaxContextTokens, when > 0, lowers the window the trigger is computed
	// from (the [compaction] max_context key): a user on a metered or
	// quality-sensitive setup can compact earlier than the model's hard
	// ceiling. It never raises the window — a value at or above the provider
	// limit is clamped to it, so the knob can only make compaction earlier.
	MaxContextTokens int
	// KeepRecentTokens is the approximate recent-context token budget to retain
	// after compaction (consumed by FindCutPoint in US-002).
	KeepRecentTokens int
}

// DefaultCompactionSettings matches pi's DEFAULT_COMPACTION_SETTINGS (reserve
// 16k, keep 20k) plus a 10% headroom. The percentage aligns the trigger with
// what the rest of the industry does — Codex compacts at 90% of the window and
// Claude Code around 90-95% — and with golder's own low-budget ladder, whose
// first notice fires at 10% remaining. Without it, a 1M-window model only
// compacted at 98.4% full, which is later than the point where a single fat
// turn can still overflow the window.
var DefaultCompactionSettings = CompactionSettings{
	Enabled:          true,
	ReserveTokens:    16384,
	HeadroomPercent:  10,
	KeepRecentTokens: 20000,
}

// EffectiveWindow returns the window the compaction decision counts against:
// the provider window, lowered to MaxContextTokens when that is set. A
// non-positive contextWindow (unknown) returns MaxContextTokens, or 0 when
// neither is known.
func (s CompactionSettings) EffectiveWindow(contextWindow int) int {
	if s.MaxContextTokens > 0 && (contextWindow <= 0 || s.MaxContextTokens < contextWindow) {
		return s.MaxContextTokens
	}
	if contextWindow <= 0 {
		return 0
	}
	return contextWindow
}

// EffectiveReserve returns the tokens held back before compaction triggers: the
// larger of the fixed ReserveTokens and HeadroomPercent of the effective
// window. A non-positive window returns ReserveTokens.
func (s CompactionSettings) EffectiveReserve(contextWindow int) int {
	reserve := s.ReserveTokens
	if window := s.EffectiveWindow(contextWindow); s.HeadroomPercent > 0 && window > 0 {
		if percent := window * s.HeadroomPercent / 100; percent > reserve {
			reserve = percent
		}
	}
	return reserve
}

// UsableTokens returns the context-token ceiling at which compaction triggers:
// contextWindow minus the effective reserve. It is the single definition of
// "full enough", shared by ShouldCompact, the loop's decision, and the
// threshold /status and /memory print. A non-positive window returns 0.
func (s CompactionSettings) UsableTokens(contextWindow int) int {
	window := s.EffectiveWindow(contextWindow)
	if window <= 0 {
		return 0
	}
	return window - s.EffectiveReserve(contextWindow)
}

// estimatedImageChars is the fixed character budget attributed to an image
// block, matching pi's ESTIMATED_IMAGE_CHARS.
const estimatedImageChars = 4800

// charsPerToken is the conservative characters-per-token divisor pi uses.
const charsPerToken = 4

// ceilDiv returns ceil(a / b) for non-negative a and positive b.
func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// contentListChars sums the character footprint of a content list, counting
// text/thinking/toolCall blocks by their text length and each image block as a
// fixed estimatedImageChars, mirroring pi's estimateTextAndImageContentChars
// plus its assistant-block handling.
func contentListChars(content agentcore.ContentList) int {
	chars := 0
	for _, block := range content {
		switch c := block.(type) {
		case agentcore.TextContent:
			chars += len(c.Text)
		case agentcore.ThinkingContent:
			chars += len(c.Thinking)
		case agentcore.ToolCallContent:
			// name + serialized arguments, matching pi's toolCall accounting.
			chars += len(c.Name)
			if len(c.Arguments) > 0 {
				chars += len(c.Arguments)
			} else {
				// nil/empty RawMessage serializes to "null" downstream.
				b, _ := json.Marshal(json.RawMessage(c.Arguments))
				chars += len(b)
			}
		case agentcore.ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

// EstimateTokens returns a conservative token estimate for one message using
// the same character heuristic as pi's estimateTokens (ceil(chars / 4)).
func EstimateTokens(msg agentcore.Message) int {
	switch m := msg.(type) {
	case agentcore.UserMessage:
		return ceilDiv(contentListChars(m.Content), charsPerToken)
	case agentcore.AssistantMessage:
		return ceilDiv(contentListChars(m.Content), charsPerToken)
	case agentcore.ToolResultMessage:
		return ceilDiv(contentListChars(m.Content), charsPerToken)
	case agentcore.CompactionMessage:
		// A compaction checkpoint replays as its summary text; estimate from it.
		return ceilDiv(len(m.Summary), charsPerToken)
	default:
		return 0
	}
}

// calculateContextTokens derives total context tokens from a provider usage
// block. Input/output are always counted; cache read/write are added for
// providers that report them separately (Anthropic-wire, where input_tokens
// excludes cached content). OpenAI-wire usage already includes cache hits in
// InputTokens and leaves the cache fields zero, so this folds like pi/codex
// without double-counting either wire format.
func calculateContextTokens(u agentcore.Usage) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// assistantUsage returns a usable Usage from an assistant message, skipping
// aborted/error responses and zero-token usage, mirroring pi's getAssistantUsage.
func assistantUsage(msg agentcore.Message) (agentcore.Usage, bool) {
	a, ok := msg.(agentcore.AssistantMessage)
	if !ok || a.Usage == nil {
		return agentcore.Usage{}, false
	}
	if a.StopReason == agentcore.StopReasonAborted || a.StopReason == agentcore.StopReasonError {
		return agentcore.Usage{}, false
	}
	if calculateContextTokens(*a.Usage) <= 0 {
		return agentcore.Usage{}, false
	}
	return *a.Usage, true
}

// usageSampleWindow is how many of the most recent valid usage readings
// EstimateContextTokens considers. The estimate takes the largest of them
// rather than blindly the newest: gateway streams can report the same
// conversation at two different levels a few percent apart (an upstream
// cache/prefix accounting split, seen in the wild as readings alternating
// between ~87% and ~95% of a 1M window for one growing conversation), and a
// run that keeps re-reading the lower one both under-reports the window and
// defers compaction. Multiple readings of one growing conversation differ only
// in what they count, never in being "newer truth", so the maximum is the safe
// figure. Four readings is roughly one assistant turn's worth (a turn emits one
// reading per tool round-trip), so the window still tracks fresh figures
// instead of an ancient peak.
const usageSampleWindow = 4

// usageOutlierFactor guards the windowed maximum against a lone artifact: a
// reading more than this many times the next-highest sample is a gateway
// accounting glitch (one instance in the wild reported 980k for a conversation
// whose neighbours all sat near 383k), not a context that grew and shrank.
// Trusting it would compact at a fraction of the window. A genuine mid-turn
// surge (a large paste) is not lost: it is carried by the messages the trailing
// estimate counts after the surviving base reading, so the figure stays
// representative while the artifact ages out of the window.
const usageOutlierFactor = 2

// usageSample is one candidate provider reading: the message index it came
// from and the token figure it reported.
type usageSample struct {
	index  int
	tokens int
}

// ContextUsageEstimate reports the derived context-token usage for a message
// list, mirroring pi's ContextUsageEstimate.
type ContextUsageEstimate struct {
	// Tokens is the estimated total context tokens.
	Tokens int
	// UsageTokens is the peak tokens reported by the sampled assistant usage
	// blocks (the largest of the last usageSampleWindow readings).
	UsageTokens int
	// TrailingTokens is the estimated tokens after that usage block.
	TrailingTokens int
	// LastUsageIndex is the index of the message that provided the peak usage,
	// or -1 when no usage exists.
	LastUsageIndex int
}

// EstimateContextTokens computes context-token usage for messages, preferring
// provider-reported Usage over estimation: it takes the largest of the last
// usageSampleWindow valid assistant Usage readings and estimates only the
// messages that follow it. When no usage is available it estimates every
// message. This mirrors pi's estimateContextTokens with a windowed maximum
// instead of the single newest reading, so a gateway that reports one
// conversation at two levels cannot leave the run on the lower one.
//
// Two kinds of reading are never used. A reading older than the newest
// compaction checkpoint described a context that no longer exists (its prefix
// was summarized away), and one carried by the tail that checkpoint kept
// verbatim described the pre-compaction request — reading either back would
// report the old size and re-trigger compaction on the very next turn.
// CompactionMessage.KeptCount is what marks that tail; a checkpoint written
// before the field existed (KeptCount 0) only excludes the prefix, so a legacy
// session settles after at most one extra compaction.
func EstimateContextTokens(msgs []agentcore.Message) ContextUsageEstimate {
	// boundary is the highest index whose usage readings describe an older
	// context: the newest checkpoint (its prefix is gone) plus the tail it
	// kept (whose readings predate the rewrite). -1 means nothing is stale.
	boundary := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if c, ok := msgs[i].(agentcore.CompactionMessage); ok {
			boundary = i
			if c.KeptCount > 0 {
				boundary = i + c.KeptCount
			}
			break
		}
	}

	var samples []usageSample
	for i := len(msgs) - 1; i > boundary && len(samples) < usageSampleWindow; i-- {
		u, ok := assistantUsage(msgs[i])
		if !ok {
			continue
		}
		samples = append(samples, usageSample{index: i, tokens: calculateContextTokens(u)})
	}

	// Discard a lone towering peak (see usageOutlierFactor), then take the
	// highest reading left as the base to grow from.
	for len(samples) > 1 {
		bestIdx := 0
		for k := 1; k < len(samples); k++ {
			if samples[k].tokens > samples[bestIdx].tokens {
				bestIdx = k
			}
		}
		second := -1
		for k, s := range samples {
			if k != bestIdx && s.tokens > second {
				second = s.tokens
			}
		}
		if second > 0 && samples[bestIdx].tokens > usageOutlierFactor*second {
			samples = append(samples[:bestIdx], samples[bestIdx+1:]...)
			continue
		}
		break
	}

	if len(samples) == 0 {
		estimated := 0
		for _, m := range msgs {
			estimated += EstimateTokens(m)
		}
		return ContextUsageEstimate{
			Tokens:         estimated,
			UsageTokens:    0,
			TrailingTokens: estimated,
			LastUsageIndex: -1,
		}
	}

	base := samples[0]
	for _, s := range samples[1:] {
		if s.tokens > base.tokens {
			base = s
		}
	}
	trailing := 0
	for i := base.index + 1; i < len(msgs); i++ {
		trailing += EstimateTokens(msgs[i])
	}
	return ContextUsageEstimate{
		Tokens:         base.tokens + trailing,
		UsageTokens:    base.tokens,
		TrailingTokens: trailing,
		LastUsageIndex: base.index,
	}
}

// ShouldCompact reports whether context usage has exceeded the usable window:
// contextTokens > settings.UsableTokens(contextWindow) — pi's
// contextTokens > contextWindow - reserveTokens, with the reserve widened to
// max(ReserveTokens, HeadroomPercent of the window). Disabled settings or a
// non-positive contextWindow (unknown) never trigger compaction.
func ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings) bool {
	if !settings.Enabled {
		return false
	}
	if contextWindow <= 0 {
		return false
	}
	return contextTokens > settings.UsableTokens(contextWindow)
}

// ReserveDescription names which knob set the reserve for a window, for the
// status displays that print the trigger point: the percentage headroom when it
// dominates, the fixed reserve otherwise.
func (s CompactionSettings) ReserveDescription(contextWindow int) string {
	window := s.EffectiveWindow(contextWindow)
	if window > 0 && s.HeadroomPercent > 0 &&
		window*s.HeadroomPercent/100 > s.ReserveTokens {
		return fmt.Sprintf("%d%% headroom", s.HeadroomPercent)
	}
	return fmt.Sprintf("reserve: %d", s.ReserveTokens)
}
