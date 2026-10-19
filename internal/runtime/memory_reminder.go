package runtime

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/memory"
)

// defaultMemoryReminderMaxChars is the per-turn character budget for the
// injected memory body. It is deliberately modest so relevant memory context
// does not crowd out the live conversation in the model's window.
const defaultMemoryReminderMaxChars = 600

// defaultMemoryReminderLimit is the number of search hits fetched per turn
// before budget trimming.
const defaultMemoryReminderLimit = 5

// MemoryReminderProvider injects relevant persistent memory as ephemeral
// background context on each turn (issue #478). It derives a query from the
// most recent user message, runs a BM25 search over the memory store, and
// surfaces the top-ranked snippets — budget-capped and deduped so identical
// context is never repeated turn after turn.
//
// The returned body is RAW plain text; the reminder registry wraps it in
// <system-reminder> tags and injects it only into the per-turn LLM request, so
// it never enters persisted history.
type MemoryReminderProvider struct {
	// Store is the persistent memory database. When nil the provider never
	// fires.
	Store *memory.Store

	// MaxChars caps the injected body length. <=0 uses
	// defaultMemoryReminderMaxChars.
	MaxChars int

	// Limit caps the number of search hits considered. <=0 uses
	// defaultMemoryReminderLimit.
	Limit int

	// Scope and ScopeID, when non-empty, focus the search on a single memory
	// scope (e.g. the current project id) via SearchOptions.
	Scope   string
	ScopeID string

	// Disabled turns the reminder off without unwiring it: the shipped default
	// (config.DefaultMemoryRecall is false), so the store and the memory_search
	// tool stay available while no turn injects anything.
	Disabled bool
	// MinUnits is the smallest query worth searching, in units. A unit is a
	// latin word or a CJK bigram — the granularity the store's unicode61
	// tokenizer cannot provide for Chinese, where it keeps a whole run as one
	// token. Default 2: "继续", "ok" and "好的" recall nothing.
	MinUnits int
	// MinOverlap is how many distinct query units a hit must contain before it
	// is injected. The store's score floor is relative and always keeps the top
	// hit, so this is the guard that answers "is that top hit actually about
	// the question", not merely "is there some match".
	MinOverlap int
	// ReconcileInterval throttles the index reconcile: the walk is linear in
	// the number of memory files (one checkpoint per compaction, so it grows),
	// and it used to run on every turn. 0 means every turn; negative means the
	// reminder never reconciles (the memory_search tool still does).
	ReconcileInterval time.Duration

	// mu guards lastBody so Reminder is safe to call across turns.
	mu            sync.Mutex
	lastBody      string
	lastReconcile time.Time
}

// Name implements ReminderProvider.
func (p *MemoryReminderProvider) Name() string { return "memory" }

// Reminder implements ReminderProvider. It fires when the latest user message
// yields a search query that matches stored memory, returning a concise,
// budget-capped body of ranked snippets. It stays silent when the store is nil,
// there is no user text to search on, the search errors or returns nothing, or
// the produced body is identical to the one injected on the previous firing
// turn (dedupe).
func (p *MemoryReminderProvider) Reminder(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
	if p == nil || p.Store == nil {
		return "", false
	}
	if p.Disabled {
		return "", false
	}

	query := latestUserText(msgs)
	if strings.TrimSpace(query) == "" {
		return "", false
	}
	units := recallUnits(query)
	if len(units) < p.minUnits() {
		recordRecall(RecallStats{
			LastQuery:  query,
			LastReason: "query too short to recall on",
			LastAt:     time.Now(),
		})
		return "", false
	}

	limit := p.Limit
	if limit <= 0 {
		limit = defaultMemoryReminderLimit
	}

	reconcile := p.reconcileDue(time.Now())
	results, err := p.Store.Search(query, memory.SearchOptions{
		Scope:          p.Scope,
		ScopeID:        p.ScopeID,
		Limit:          limit,
		ReconcileFirst: reconcile,
	})
	if err != nil || len(results) == 0 {
		recordRecall(RecallStats{LastQuery: query, LastReason: "no match", LastAt: time.Now()})
		return "", false
	}

	// Relevance guard: keep only hits that share enough of the query's own
	// units. Without it the relative score floor guarantees a top hit exists
	// whenever anything matches at all, however little it has to do with the
	// question.
	minOverlap := p.minOverlap()
	if minOverlap > 0 {
		kept := results[:0]
		for _, r := range results {
			if recallOverlap(units, r) >= minOverlap {
				kept = append(kept, r)
			}
		}
		results = kept
	}
	if len(results) == 0 {
		recordRecall(RecallStats{
			LastQuery:  query,
			LastReason: "matches shared too little of the query",
			LastAt:     time.Now(),
		})
		return "", false
	}

	body := p.buildBody(results)
	if body == "" {
		return "", false
	}

	// Dedupe: never re-inject the identical body on a subsequent firing turn.
	p.mu.Lock()
	defer p.mu.Unlock()
	if body == p.lastBody {
		recordRecall(RecallStats{
			LastQuery:  query,
			LastReason: "same body as last turn",
			LastAt:     time.Now(),
		})
		return "", false
	}
	p.lastBody = body
	paths := make([]string, 0, len(results))
	for _, r := range results {
		paths = append(paths, r.Path)
	}
	recordRecall(RecallStats{
		Fires: 1, LastQuery: query, LastReason: "injected", LastAt: time.Now(),
		LastPaths: paths, LastFired: true,
	})
	return body, true
}

func (p *MemoryReminderProvider) minUnits() int {
	if p.MinUnits <= 0 {
		return 1
	}
	return p.MinUnits
}

func (p *MemoryReminderProvider) minOverlap() int {
	if p.MinOverlap < 0 {
		return 0
	}
	return p.MinOverlap
}

// reconcileDue reports whether this turn should reconcile the index, and
// records the time when it says yes.
func (p *MemoryReminderProvider) reconcileDue(now time.Time) bool {
	interval := p.ReconcileInterval
	if interval < 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if interval > 0 && !p.lastReconcile.IsZero() && now.Sub(p.lastReconcile) < interval {
		return false
	}
	p.lastReconcile = now
	return true
}

// RecallStats is the last per-turn recall decision plus a running count, so the
// report can answer "why did that fire" and "has it fired at all" without the
// transcript carrying a line per turn.
type RecallStats struct {
	Fires      int
	Skips      int
	LastQuery  string
	LastReason string
	LastPaths  []string
	LastFired  bool
	LastAt     time.Time
}

var recallLog struct {
	mu    sync.Mutex
	stats RecallStats
}

// recordRecall merges one decision into the package snapshot. Both counters are
// cumulative for the process; the rest describes the latest turn.
func recordRecall(s RecallStats) {
	recallLog.mu.Lock()
	defer recallLog.mu.Unlock()
	if s.LastFired {
		recallLog.stats.Fires += s.Fires
	} else {
		recallLog.stats.Skips++
	}
	recallLog.stats.LastQuery = s.LastQuery
	recallLog.stats.LastReason = s.LastReason
	recallLog.stats.LastPaths = append([]string(nil), s.LastPaths...)
	recallLog.stats.LastFired = s.LastFired
	recallLog.stats.LastAt = s.LastAt
}

// MemoryRecallStats returns the process's recall counters and the last decision.
func MemoryRecallStats() RecallStats {
	recallLog.mu.Lock()
	defer recallLog.mu.Unlock()
	out := recallLog.stats
	out.LastPaths = append([]string(nil), out.LastPaths...)
	return out
}

// recallUnits splits a query into the units the guard counts: runs of latin
// letters, digits and `_-` of two or more runes, plus bigrams over a CJK run.
// The bigram choice is deliberate: unicode61 keeps 继续 or 记忆的召回 as ONE
// token (no dictionary), so a per-token count would call any Chinese sentence a
// one-unit query and silently disable recall for it.
func recallUnits(text string) []string {
	var units []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) >= 2 {
			units = append(units, string(word))
		}
		word = word[:0]
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			units = append(units, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case isWordRune(r):
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	// Dedupe, so "记忆……记忆" counts once.
	seen := make(map[string]bool, len(units))
	out := units[:0]
	for _, u := range units {
		u = strings.ToLower(u)
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func isWordRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '_' || r == '-'
}

func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF || // CJK Unified Ideographs
		r >= 0x3400 && r <= 0x4DBF || // Extension A
		r >= 0xF900 && r <= 0xFAFF || // Compatibility Ideographs
		r >= 0x3040 && r <= 0x30FF || // Hiragana / Katakana
		r >= 0xAC00 && r <= 0xD7AF // Hangul syllables
}

// recallOverlap counts how many of the query's units appear in the hit's path
// or snippet. It is a containment test, not a second BM25: the store already
// ranked, and this only asks whether the ranking was about the question.
func recallOverlap(units []string, r memory.SearchResult) int {
	hay := strings.ToLower(r.Path + "\n" + r.Snippet)
	n := 0
	for _, u := range units {
		if strings.Contains(hay, u) {
			n++
		}
	}
	return n
}

// buildBody renders the ranked results into a concise, budget-capped body.
// MEMORY.md index files (and free-type entries) are ordered first as they are
// the most useful high-level context.
func (p *MemoryReminderProvider) buildBody(results []memory.SearchResult) string {
	maxChars := p.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMemoryReminderMaxChars
	}

	// Stable sort so index-type / MEMORY.md hits float to the top while
	// preserving the underlying BM25 order among equals.
	ordered := make([]memory.SearchResult, len(results))
	copy(ordered, results)
	sort.SliceStable(ordered, func(i, j int) bool {
		return memoryRank(ordered[i]) < memoryRank(ordered[j])
	})

	const heading = "Relevant memory:"
	var b strings.Builder
	b.WriteString(heading)
	for _, r := range ordered {
		snippet := strings.TrimSpace(strings.ReplaceAll(r.Snippet, "\n", " "))
		if snippet == "" {
			continue
		}
		line := "\n- " + r.Path + ": " + snippet
		// Enforce the budget: stop before exceeding maxChars, and never emit a
		// heading-only body.
		if b.Len()+len(line) > maxChars {
			if b.Len() > len(heading) {
				break
			}
			// The very first line already overflows: hard-truncate it so we
			// still surface something within budget.
			room := maxChars - b.Len()
			if room <= 0 {
				break
			}
			if room < len(line) {
				line = line[:room]
			}
			b.WriteString(line)
			break
		}
		b.WriteString(line)
	}

	if b.Len() <= len(heading) {
		return ""
	}
	return b.String()
}

// memoryRank returns a sort key that floats MEMORY.md index files and free-type
// entries to the front (rank 0) ahead of everything else (rank 1).
func memoryRank(r memory.SearchResult) int {
	if strings.HasSuffix(r.Path, "MEMORY.md") || r.Type == memory.TypeFree {
		return 0
	}
	return 1
}

// latestUserText returns the flattened text of the most recent user message in
// msgs, or "" if there is none. Reminder messages are user-role too, but they
// carry the <system-reminder> preamble; those are skipped so the search query
// reflects the genuine user request rather than previously injected context.
func latestUserText(msgs agentcore.MessageList) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		um, ok := msgs[i].(agentcore.UserMessage)
		if !ok {
			continue
		}
		text := agentcore.ContentToText(um.Content)
		if strings.Contains(text, "<system-reminder>") {
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		return text
	}
	return ""
}
