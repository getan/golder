// This file defines LiveConfig, the mutable run configuration a control command
// may change mid-session. It was moved verbatim from cmd/golder (the former
// liveRunConfig) and exported so the run, repl, btw, status and goal
// subpackages can read and mutate it through the Host contract. The run closure
// reads it on every prompt, so a /model switch takes effect on the next turn.
// It carries no lock: it is read and written only on the REPL's single main
// goroutine (slash actions and the run are both invoked synchronously from the
// REPL loop, never concurrently).
package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/compaction"
	"github.com/getan/golder/internal/provider"
)

// LiveConfig is the mutable run configuration a control command may change
// mid-session.
type LiveConfig struct {
	Model        string
	ProviderName string
	Provider     provider.Provider
	BaseURL      string
	Protocol     string
	// ThinkingLevel is the reasoning-effort level applied to each turn. It is
	// seeded from the resolved config chain and read on every prompt.
	ThinkingLevel agentcore.ThinkingLevel
	// ContextWindow is the model's total context-token budget, used to gate
	// automatic compaction. When 0 the window is unknown and auto-compaction is
	// disabled; the REPL seeds it with a conservative default so long sessions
	// still compact rather than overflow.
	ContextWindow int
	// MaxContext is the parsed [compaction] max_context value (the zero value
	// is "unset"). CompactionSettings resolves it against the current
	// ContextWindow on every read, so a "N%" form keeps working across a
	// /model switch that changes the window. It can only lower the trigger
	// point, never raise it past the provider limit.
	MaxContext config.MaxContext

	// FetchedModels is the online model catalog pulled from the live provider's
	// endpoint by the bare-/model list (issue #566), sorted and
	// deduplicated. /model prefers it over the heuristic chain for ids it
	// contains (so a fetched id stays on the gateway that serves it) and reuses
	// it for numeric picks. The list is also persisted to a shared 24h disk
	// cache, so the network is consulted about once a day per provider rather
	// than once per session; this field is the session-lifetime copy, seeded
	// either from that cache or from a fresh fetch.
	FetchedModels []string
	FetchedAt     time.Time
	FetchedKey    string
}

// DefaultContextWindow is the fallback context-token budget used when a model's
// true window is unknown. It is deliberately large so auto-compaction only fires
// on genuinely long sessions (threshold = window - ReserveTokens), never on
// ordinary short exchanges.
const DefaultContextWindow = 1000000

// ResolveContextWindow resolves the context-token budget for a (provider,
// model) pair: the value models.dev publishes when its catalog knows the model,
// DefaultContextWindow otherwise. The catalog is read from the same 24h disk
// cache the reasoning ladder uses, so this never blocks on the network; a model
// the catalog does not cover (custom base URL, brand-new release) keeps the
// historical 1M fallback.
func ResolveContextWindow(providerName, model string) int {
	if w := provider.ContextWindowFor(providerName, model); w > 0 {
		return w
	}
	return DefaultContextWindow
}

// RefreshContextWindow re-resolves ContextWindow from the current provider and
// model. Call it after a /model or /provider switch, and after /resume applies
// a session header, so the status-bar percentage and the auto-compaction
// threshold track the model actually in use instead of the launch model.
func (l *LiveConfig) RefreshContextWindow() {
	l.ContextWindow = ResolveContextWindow(l.ProviderName, l.Model)
}

// CompactionSettings returns the compaction thresholds for this session: the
// defaults with the configured max_context lower bound applied. Every driver
// (main run, goal loop, side thread, manual /compact) reads this instead of
// the package defaults, so the one knob a user can set reaches all of them.
func (l *LiveConfig) CompactionSettings() compaction.CompactionSettings {
	if l == nil {
		return compaction.DefaultCompactionSettings
	}
	return CompactionSettingsFor(l.MaxContext, l.ContextWindow)
}

// CompactionSettingsFor resolves the compaction thresholds for a window, with
// the configured [compaction] max_context applied. Drivers that do not carry a
// LiveConfig (headless -p, the SDK) call it directly so the knob works there
// too. max_context only lowers the trigger point (see
// CompactionSettings.MaxContextTokens).
func CompactionSettingsFor(maxContext config.MaxContext, contextWindow int) compaction.CompactionSettings {
	settings := compaction.DefaultCompactionSettings
	if maxContext.IsSet() {
		settings.MaxContextTokens = maxContext.Resolve(contextWindow)
	}
	return settings
}

// FormatContextWindow renders a context-token budget compactly for model
// lists and switch confirmations: 200000 → "200K", 1048576 → "1.05M",
// 1000000 → "1M". A non-positive budget (the catalog does not know the model)
// returns "", so callers can omit the field rather than print a fake zero.
func FormatContextWindow(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	switch {
	case tokens < 1_000:
		return strconv.Itoa(tokens)
	case tokens < 999_950: // rounds below 1000.0K; 999950+ prints as 1M
		return decimal(float64(tokens)/1_000, 1) + "K"
	default:
		return decimal(float64(tokens)/1_000_000, 2) + "M"
	}
}

// decimal formats v with at most places decimals, trailing zeros trimmed:
// 204.8 → "204.8", 200.0 → "200", 1.05 → "1.05", 1.00 → "1".
func decimal(v float64, places int) string {
	s := strconv.FormatFloat(v, 'f', places, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}
