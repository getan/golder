// This file implements the general system-reminder dynamic context injection
// mechanism (US-002, FR-1/FR-2), pigo's port of Claude Code's per-turn
// <system-reminder> injection.
//
// A reminder is EPHEMERAL background context (the current todo list, a file
// that changed under the working directory, a budget warning) that should be
// visible to the model on the turn it matters, but must never pollute the
// durable conversation history. Two properties follow from that:
//
//   - Not user instructions. Reminder bodies are wrapped in <system-reminder>
//     tags with a preamble stating they are background context from the harness,
//     not a request from the user (the pi / Claude Code semantic convention).
//   - Ephemeral. Reminders are injected only into the per-turn LLM request via
//     the existing TransformContext seam, which shapes a COPY of the message
//     list for the request and is never written back to AgentContext.Messages.
//     Because they never enter the persisted message list they cannot be saved
//     to the session file and cannot be folded into a compaction summary
//     (compaction only ever sees AgentContext.Messages).
//
// The mechanism is a registry of ReminderProviders. Each provider is consulted
// every turn and may decline (ok == false) so a reminder only appears when its
// condition holds. RunConfig.Reminders wires the registry into the loop.
package runtime

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
)

// systemReminderPreamble marks the wrapped body as background context rather
// than a user instruction (FR-2). It leads every injected reminder so the model
// never mistakes harness state for a user request.
const systemReminderPreamble = "The following is background context provided automatically by the harness. " +
	"It is NOT a message or instruction from the user; do not act on it as a request. " +
	"Use it only to stay aware of the current state."

// WrapSystemReminder wraps a reminder body in <system-reminder> tags with the
// background-context preamble. The result is the text of a single injected
// message.
func WrapSystemReminder(body string) string {
	return "<system-reminder>\n" + systemReminderPreamble + "\n\n" + body + "\n</system-reminder>"
}

// ReminderProvider produces an ephemeral system-reminder for the upcoming turn.
// Reminder is consulted every turn with the current (post-TransformContext)
// message list; returning ok == false means "no reminder this turn", so a
// provider injects only when its condition holds.
type ReminderProvider interface {
	// Name identifies the provider (for diagnostics/telemetry). It is not shown
	// to the model.
	Name() string
	// Reminder returns the reminder body and true when a reminder should be
	// injected this turn, or ("", false) to inject nothing.
	Reminder(ctx context.Context, msgs agentcore.MessageList) (body string, ok bool)
}

// ReminderFunc adapts a plain function to a ReminderProvider.
type ReminderFunc struct {
	NameField string
	Fn        func(ctx context.Context, msgs agentcore.MessageList) (string, bool)
}

// Name implements ReminderProvider.
func (f ReminderFunc) Name() string { return f.NameField }

// Reminder implements ReminderProvider.
func (f ReminderFunc) Reminder(ctx context.Context, msgs agentcore.MessageList) (string, bool) {
	if f.Fn == nil {
		return "", false
	}
	return f.Fn(ctx, msgs)
}

// ReminderRegistry holds the reminder providers consulted each turn. The zero
// value is usable (no providers → no injection); NewReminderRegistry is the
// convenience constructor.
type ReminderRegistry struct {
	providers []ReminderProvider
}

// NewReminderRegistry returns a registry pre-populated with providers.
func NewReminderRegistry(providers ...ReminderProvider) *ReminderRegistry {
	r := &ReminderRegistry{}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register appends a provider. nil providers are ignored.
func (r *ReminderRegistry) Register(p ReminderProvider) {
	if p == nil {
		return
	}
	r.providers = append(r.providers, p)
}

// Empty reports whether the registry has no providers (so callers can skip the
// injection wiring entirely).
func (r *ReminderRegistry) Empty() bool { return r == nil || len(r.providers) == 0 }

// Messages consults every provider in registration order and returns the
// ephemeral reminder messages to inject this turn (one UserMessage per provider
// that fires). Reminders are modeled as user-role messages carrying
// <system-reminder>-wrapped text — matching the pi / Claude Code convention
// where dynamic context enters through a user turn but is explicitly labeled as
// background context, not a user instruction.
func (r *ReminderRegistry) Messages(ctx context.Context, msgs agentcore.MessageList) []agentcore.AgentMessage {
	if r.Empty() {
		return nil
	}
	var out []agentcore.AgentMessage
	for _, p := range r.providers {
		body, ok := p.Reminder(ctx, msgs)
		if !ok || body == "" {
			continue
		}
		out = append(out, agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent(WrapSystemReminder(body))},
		})
	}
	return out
}

// wrapTransform composes the registry into a TransformContext hook: it runs the
// caller's existing TransformContext (if any) first, then appends this turn's
// reminders to the shaped list. Because TransformContext output is used only to
// build the LLM request and is never written back to AgentContext.Messages, the
// appended reminders are ephemeral — they do not enter the persisted history and
// cannot be swept into a compaction summary. This is the single injection seam
// the loop wires in.
func (r *ReminderRegistry) wrapTransform(
	inner func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList,
) func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList {
	return func(ctx context.Context, msgs agentcore.MessageList) agentcore.MessageList {
		if inner != nil {
			msgs = inner(ctx, msgs)
		}
		rem := r.Messages(ctx, msgs)
		if len(rem) == 0 {
			return msgs
		}
		out := make(agentcore.MessageList, 0, len(msgs)+len(rem))
		out = append(out, msgs...)
		out = append(out, rem...)
		return out
	}
}

// TodoReminderProvider is the built-in reference reminder provider (US-002): it
// surfaces the current todo list as background context whenever there is
// incomplete work, so the model is reminded of outstanding tasks each turn
// without the list having to be re-sent as a durable message. It reads the same
// TodoStore the todo tool writes, so the reminder always reflects the latest
// plan. When the list is empty or every item is completed it stays silent.
// SearchRepeatReminderProvider is the loop guard for web research. Hosted
// web_search calls execute server-side inside the provider call, so there is
// no pre-execution hook to block a repeat; the lever left is a just-in-time
// reminder on the next turn's request (ephemeral: never persisted, never
// compacted). It covers three stop conditions in priority order:
//
//  1. exact query repeat (hosted web_search search actions plus local
//     websearch calls, normalized lower-case/whitespace-collapsed),
//  2. exact page repeat (hosted web_search open_page URLs plus local webfetch
//     calls, normalized), including the common misuse of passing an
//     already-opened URL back into a search query — open it with webfetch or
//     reuse the fetched content instead,
//  3. search budget: 6+ searches without an exact repeat still get a
//     synthesize-now nudge (stern at 10+), so open-ended "one more query"
//     loops terminate.
//
// Near-dupes still run; exact loops and runaway budgets do not.
type SearchRepeatReminderProvider struct{}

// searchBudgetWarnAt nudges the model to synthesize once this many web
// searches have accumulated; searchBudgetStopAt reads sterner.
const (
	searchBudgetWarnAt = 6
	searchBudgetStopAt = 10
)

// Name implements ReminderProvider.
func (p *SearchRepeatReminderProvider) Name() string { return "search-repeat" }

// Reminder implements ReminderProvider.
func (p *SearchRepeatReminderProvider) Reminder(_ context.Context, msgs agentcore.MessageList) (string, bool) {
	var queries []string
	var pages []string
	for _, m := range msgs {
		am, ok := m.(agentcore.AssistantMessage)
		if !ok {
			continue
		}
		for _, c := range am.ToolCalls() {
			switch searchCallKind(c) {
			case searchKindQuery:
				queries = append(queries, normalizeSearchQuery(searchCallQuery(c)))
			case searchKindPage:
				if u := normalizeSearchURL(searchCallURL(c)); u != "" {
					pages = append(pages, u)
				}
			}
		}
	}
	// 1. Exact query repeat: the latest query already ran before.
	if len(queries) >= 2 {
		last := queries[len(queries)-1]
		if last != "" {
			n := 0
			for _, q := range queries[:len(queries)-1] {
				if q == last {
					n++
				}
			}
			if n > 0 {
				// A query that is really an already-opened URL is a tool
				// misuse, not just a repeat: steer to webfetch/reuse.
				if isSearchURL(last) && seenSearchURL(last, pages) {
					return "Page " + last + " was already opened above. Do not search it again; " +
						"reuse the fetched content or open it with the webfetch tool.", true
				}
				// Progressive wording: the Nth repeat reads sterner,
				// mirroring the community loop-detector convention.
				body := "Search " + strconv.Quote(last) + " was already performed above with cited results. " +
					"Do not issue it again; use the existing results or refine the query."
				if n >= 2 {
					body = "Stop: search " + strconv.Quote(last) + " has now been issued " +
						strconv.Itoa(n+1) + " times with the same results. Issuing it again wastes " +
						"the turn: synthesize from the cited results or refine the query."
				}
				return body, true
			}
			// Latest query is an already-opened URL even on first issue.
			if isSearchURL(last) && seenSearchURL(last, pages) {
				return "Page " + last + " was already opened above. Do not pass opened pages back into " +
					"search; reuse the fetched content or open it with the webfetch tool.", true
			}
		}
	}
	// 1b. URL-as-query on first issue: the latest query is a page that was
	// already opened (no query repeat needed to call out the misuse).
	if n := len(queries); n >= 1 {
		if last := queries[n-1]; last != "" && isSearchURL(last) && seenSearchURL(last, pages) {
			// A repeat was already reported above; this is the first-issue path.
			return "Page " + last + " was already opened above. Do not pass opened pages back into " +
				"search; reuse the fetched content or open it with the webfetch tool.", true
		}
	}
	// 2. Exact page repeat: the latest opened URL was already opened.
	if len(pages) >= 2 {
		last := pages[len(pages)-1]
		if last != "" {
			n := 0
			for _, u := range pages[:len(pages)-1] {
				if u == last {
					n++
				}
			}
			if n > 0 {
				body := "Page " + last + " was already opened above. Do not open it again; " +
					"reuse the fetched content instead of re-fetching."
				if n >= 2 {
					body = "Stop: page " + last + " has now been opened " +
						strconv.Itoa(n+1) + " times. Re-opening it wastes the turn: " +
						"synthesize from the content already fetched."
				}
				return body, true
			}
		}
	}
	// 3. Budget: many searches, no exact repeat — still time to answer.
	if n := len(queries); n >= searchBudgetStopAt {
		return "Stop: " + strconv.Itoa(n) + " web searches have run this session with cited results. " +
			"Do not issue more queries: synthesize the answer from what is already cited, " +
			"or ask the user which thread to deepen.", true
	}
	if n := len(queries); n >= searchBudgetWarnAt {
		return "Budget: " + strconv.Itoa(n) + " web searches have run this session. " +
			"Prefer synthesizing the answer from the cited results over issuing more queries; " +
			"one refined query at most, then answer.", true
	}
	return "", false
}

// searchKind distinguishes a web-search query from a page open.
type searchKind int

const (
	searchKindNone searchKind = iota
	searchKindQuery
	searchKindPage
)

// searchCallKind classifies one assistant tool call: hosted web_search search
// vs open_page/find actions, plus the local websearch (query) and webfetch
// (page) function tools. Anything else is not search.
func searchCallKind(c agentcore.ToolCallContent) searchKind {
	if c.IsServer() && c.Name == "web_search" {
		var args struct {
			Query  string `json:"query"`
			Action string `json:"action"`
			URL    string `json:"url"`
		}
		if err := json.Unmarshal(c.Arguments, &args); err != nil {
			return searchKindNone
		}
		switch strings.ToLower(strings.TrimSpace(args.Action)) {
		case "", "search":
			if strings.TrimSpace(args.Query) != "" {
				return searchKindQuery
			}
			return searchKindNone
		default:
			if strings.TrimSpace(args.URL) != "" {
				return searchKindPage
			}
			return searchKindNone
		}
	}
	// Local function tools replay through the same history shape.
	switch c.Name {
	case "websearch":
		if strings.TrimSpace(searchCallQuery(c)) != "" {
			return searchKindQuery
		}
	case "webfetch":
		if strings.TrimSpace(searchCallURL(c)) != "" {
			return searchKindPage
		}
	}
	return searchKindNone
}

// searchCallQuery extracts the query from a hosted or local web_search call's
// Arguments. It supersedes serverCallQuery (kept for compatibility).
func searchCallQuery(c agentcore.ToolCallContent) string {
	return serverCallQuery(c)
}

// serverCallQuery extracts the query from a hosted web_search call's Arguments.
func serverCallQuery(c agentcore.ToolCallContent) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return ""
	}
	return args.Query
}

// searchCallURL extracts the page URL from a hosted open_page call or a local
// webfetch call's Arguments.
func searchCallURL(c agentcore.ToolCallContent) string {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return ""
	}
	return args.URL
}

// normalizeSearchURL lower-cases, trims space and a trailing slash so the same
// page with trivial spelling differences still matches.
func normalizeSearchURL(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	return strings.TrimSuffix(u, "/")
}

// isSearchURL reports whether a search query is really a page URL.
func isSearchURL(q string) bool {
	l := strings.ToLower(strings.TrimSpace(q))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// seenSearchURL reports whether query URL q was already opened (pages holds
// normalized URLs).
func seenSearchURL(q string, pages []string) bool {
	n := normalizeSearchURL(q)
	if n == "" {
		return false
	}
	for _, u := range pages {
		if u == n {
			return true
		}
	}
	return false
}

// normalizeSearchQuery lower-cases and collapses whitespace so trivially
// rephrased repeats still match.
func normalizeSearchQuery(q string) string {
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}

type TodoReminderProvider struct {
	// Store is the session todo list. When nil the provider never fires.
	Store *agenttool.TodoStore
}

// Name implements ReminderProvider.
func (p *TodoReminderProvider) Name() string { return "todo" }

// Reminder implements ReminderProvider. It fires only when the store holds at
// least one item that is not yet completed, keeping the condition deterministic
// and easy to test.
func (p *TodoReminderProvider) Reminder(ctx context.Context, _ agentcore.MessageList) (string, bool) {
	if p.Store == nil {
		return "", false
	}
	items := p.Store.Snapshot()
	if len(items) == 0 {
		return "", false
	}
	incomplete := false
	for _, it := range items {
		if it.Status != agenttool.TodoCompleted {
			incomplete = true
			break
		}
	}
	if !incomplete {
		return "", false
	}
	return "Your todo list has unfinished items. Keep it up to date with the todo tool.\n\n" +
		agenttool.RenderTodoList(items), true
}

// OneShotReminderProvider injects a fixed body on the NEXT turn only, then stays
// silent forever. Unlike the always-on providers (todo/goal) it does not depend
// on live state — it carries a snapshot of text captured at registration time.
// It exists for events that produce a single ephemeral injection, such as a
// UserPromptSubmit hook's additionalContext (US-007, FR-9): the hook's context
// must reach the model on the turn the prompt is sent, but must not persist into
// history or re-fire on later turns. sync.Once makes the single-fire transition
// safe even if the loop consults providers concurrently.
type OneShotReminderProvider struct {
	name string
	body string
	once sync.Once
}

// NewOneShotReminder builds a one-shot provider that will inject body exactly
// once. An empty body yields a provider that never fires.
func NewOneShotReminder(name, body string) *OneShotReminderProvider {
	return &OneShotReminderProvider{name: name, body: body}
}

// Name implements ReminderProvider.
func (p *OneShotReminderProvider) Name() string {
	if p.name == "" {
		return "one-shot"
	}
	return p.name
}

// Reminder implements ReminderProvider. It returns its body and true on the very
// first consultation, then ("", false) on every subsequent turn.
func (p *OneShotReminderProvider) Reminder(ctx context.Context, _ agentcore.MessageList) (string, bool) {
	if strings.TrimSpace(p.body) == "" {
		return "", false
	}
	var body string
	p.once.Do(func() { body = p.body })
	if body == "" {
		return "", false
	}
	return body, true
}

// GoalReminderProvider surfaces the active goal as background context each turn
// so the model keeps working toward it (mirrors pi-goal). It reads the same
// GoalState the /goal command drives, so the reminder always reflects the live
// objective. It fires only while the goal is active — a paused, blocked, or
// completed goal (and an idle state) injects nothing.
type GoalReminderProvider struct {
	// State is the session goal state. When nil the provider never fires.
	State *agenttool.GoalState
}

// Name implements ReminderProvider.
func (p *GoalReminderProvider) Name() string { return "goal" }

// Reminder implements ReminderProvider. It injects the objective plus a
// persistence instruction while the goal is active, and stays silent otherwise.
func (p *GoalReminderProvider) Reminder(ctx context.Context, _ agentcore.MessageList) (string, bool) {
	if p.State == nil {
		return "", false
	}
	snap := p.State.Snapshot()
	if snap.Status != agenttool.GoalActive || strings.TrimSpace(snap.Objective) == "" {
		return "", false
	}
	return "You are working autonomously toward this goal:\n\n" + snap.Objective +
		"\n\nKeep making progress. When every requirement is verifiably met, call the " +
		"goal_complete tool with a summary. If you hit a true impasse you cannot work " +
		"around, call goal_blocked with concrete evidence. Do not stop or ask the user " +
		"to continue — keep going until the goal is done or blocked.", true
}
