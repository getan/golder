package provider

import (
	"strings"

	"github.com/getan/golder/internal/agentcore"
)

// This file owns the one place where model ids meet wire reasoning-effort
// values.
//
// golder's unified ThinkingLevel ladder (off < minimal < low < medium < high <
// xhigh < max) is wider than any single gateway's. Every wire driver — Chat
// Completions (`reasoning_effort`) and Responses (`reasoning.effort`) — calls
// WireReasoningEffort, so a model is mapped identically no matter which
// protocol carries its request: the two OpenAI wires accept the same effort
// names, and a model that speaks Responses never needs a chat-specific rule.
//
// A family declares the rungs it accepts, lowest to highest. A requested level
// above the family's top clamps down to its top rung (a model with a real max
// keeps it); a level below its bottom clamps up to the bottom rung. Unknown
// families get the conservative ladder both wires document, and off/empty
// omits the field so the gateway keeps its own default.

// reasoningLadders pins the families whose ladder differs from the default.
// Matching is a case-insensitive substring of the model id; the first match
// wins, so keep more specific families above more generic ones. Adding a
// gateway's quirk is one table entry, nothing else.
var reasoningLadders = []struct {
	match  string
	ladder []agentcore.ThinkingLevel
}{
	// DeepSeek (first-party and the opencode-go gateway) accepts the full
	// ladder. Measured on opencode-go via reasoning-token counts: low < medium
	// ≈ high < xhigh < max, so max stays max.
	{"deepseek", []agentcore.ThinkingLevel{
		agentcore.ThinkingMinimal, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax,
	}},
	// Muse spark accepts minimal|low|medium|high|xhigh. "max" is advertised in
	// its error message's supported list but rejected on the wire, so xhigh is
	// the highest usable rung.
	{"muse", []agentcore.ThinkingLevel{
		agentcore.ThinkingMinimal, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh,
	}},
}

// defaultReasoningLadder is the conservative common denominator: low/medium/
// high, the rungs both OpenAI wires document. minimal clamps up to low (the
// Responses wire does not list it) and xhigh/max clamp down to high.
var defaultReasoningLadder = []agentcore.ThinkingLevel{
	agentcore.ThinkingLow, agentcore.ThinkingMedium, agentcore.ThinkingHigh,
}

// reasoningRank orders the unified levels for clamping. The zero value is
// unused: off (0) is handled before any lookup.
var reasoningRank = map[agentcore.ThinkingLevel]int{
	agentcore.ThinkingMinimal: 1,
	agentcore.ThinkingLow:     2,
	agentcore.ThinkingMedium:  3,
	agentcore.ThinkingHigh:    4,
	agentcore.ThinkingXHigh:   5,
	agentcore.ThinkingMax:     6,
}

// WireReasoningEffort maps the unified level onto the effort value to send for
// model. Both the Chat Completions and the Responses driver call it, so they
// agree by construction. An empty return means "omit the field": off/unset
// (or an unknown level) leaves the gateway's default behavior untouched.
func WireReasoningEffort(model string, level agentcore.ThinkingLevel) string {
	if level == "" || level == agentcore.ThinkingOff {
		return ""
	}
	rank, ok := reasoningRank[level]
	if !ok {
		return ""
	}

	ladder := defaultReasoningLadder
	id := strings.ToLower(model)
	for _, family := range reasoningLadders {
		if strings.Contains(id, family.match) {
			ladder = family.ladder
			break
		}
	}

	// Walk up while the rung is at or below the request; the last one wins, so
	// a request above the ladder's top lands on the top rung and one below its
	// bottom lands on the bottom rung.
	pick := ladder[0]
	for _, rung := range ladder {
		if reasoningRank[rung] > rank {
			break
		}
		pick = rung
	}
	return string(pick)
}
