package reviewpolicy

// The classifier: the wiring that turns the policy into a judge.Classifier.
//
// It sits where the slow reviewer sits today, so every consumer of that
// interface — the permission gate, the sandbox router, the read-scope gate —
// gets the cheap judge without changing. The chain it implements:
//
//	native readers → local read-only rule, no model call
//	facts  → one parsed plan, or nil for a non-shell tool
//	Jev    → one call, every question at once
//	policy → decide, or escalate
//	LLM    → the fallback, for anything escalated or unanswerable
//
// Two rules, both about failure:
//
//  1. Everything that is not a confident decision goes to the fallback. Absent
//     facts, an unreachable judge, a narrowed margin, a contradiction between
//     the verdict and a danger signal — all of it escalates. The cheap judge
//     accelerates the confident middle; it does not become the only opinion.
//
//  2. With no fallback, a failure fails CLOSED. A Verdict with Failed set is
//     what the gates already treat as "contain or ask", never as an approval,
//     so a run whose only judge is unreachable degrades to the behavior it had
//     before any reviewer existed rather than to no review at all.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/jev"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/shellfacts"
)

// Source names this classifier in a Verdict, so a card or a log line can say
// which judge answered.
const Source = "jev"

// Classifier routes tool calls through the decision model.
type Classifier struct {
	// Client is the decision model. Nil disables the cheap path entirely and
	// every call goes to Fallback.
	Client *jev.Client
	// Fallback is the slow reviewer. Nil means an escalation has nowhere to go,
	// so it fails closed instead — which is the right default for a caller that
	// did not wire one.
	Fallback judge.Classifier
	// Config holds the thresholds.
	Config Config
	// Language pins the rationale's language. Empty detects it from the
	// conversation, the same way the slow reviewer does.
	Language string
	// Cwd is the working directory the state reports, so the judge can tell a
	// path inside the workspace from one outside it.
	Cwd string
	// ExtraRoots are directories outside the workspace the session treats as its
	// own (the skills directory golder advertises in the system prompt). They
	// travel with Cwd because both answer the same question — is this path the
	// user's own or a boundary crossing — and the reviewer must see the same
	// answer the boundary gate enforces.
	ExtraRoots []string
	// WritableAt reports whether a path is already writable for this session
	// (the sandbox's writable-root registry). It travels with the rest of the
	// boundary facts for the same reason: the reviewer must see the answer the
	// gate enforces, and a write the registry already admits is not a question.
	WritableAt func(path string) bool
	// Observer, when set, receives one record per call. It runs on the calling
	// goroutine, so it must not block: it is for telemetry, not for prompting.
	Observer func(CallRecord)
	// Now is a test seam.
	Now func() time.Time
}

// Process-wide circuit breaker.
//
// It is package-level rather than a Classifier field because the classifier is
// rebuilt per turn: drivers build the session's gate chain through a factory, so
// a per-instance flag would be discarded and re-created on the next turn — the
// cheap path would pay one failed network call per turn, forever. The failure it
// guards against is permanent for the process (TypeSafe answers HTTP 451 "not
// available in your region" to a whole region), so the memory has to outlive any
// single instance.
//
// Keyed by endpoint so a user who switches gateways mid-session (or runs two
// sessions against different endpoints in one process, as tests do) does not
// inherit an unrelated endpoint's verdict.
var disabledEndpoints = struct {
	mu     sync.Mutex
	reason map[string]string
}{reason: map[string]string{}}

func (c *Classifier) isDisabled() bool {
	if c.Client == nil {
		return false
	}
	disabledEndpoints.mu.Lock()
	defer disabledEndpoints.mu.Unlock()
	_, off := disabledEndpoints.reason[c.Client.Endpoint()]
	return off
}

// disableFrom turns the cheap path off for this endpoint for the rest of the
// process, keeping the first reason so a status line can explain it.
//
// The disable is deliberately sticky rather than timed: a 451 does not clear,
// and a rejected key does not fix itself mid-run. Turning the feature back on is
// a restart, which is also when a proxy or key change takes effect.
func (c *Classifier) disable(err error) {
	if c.Client == nil {
		return
	}
	disabledEndpoints.mu.Lock()
	defer disabledEndpoints.mu.Unlock()
	if _, already := disabledEndpoints.reason[c.Client.Endpoint()]; already {
		return
	}
	disabledEndpoints.reason[c.Client.Endpoint()] = err.Error()
}

// CallRecord is what one call cost and how it was answered, for telemetry.
type CallRecord struct {
	Tool string
	// Routed is "decided" or "escalated".
	Routed string
	// Level is the decided level, empty when escalated.
	Level judge.Level
	// Escalation names why the call went to the fallback.
	Escalation string
	// Attempts and Latency describe the decision-model call.
	Attempts int
	Latency  time.Duration
	// Err is the transport failure, if any. It never contains the API key.
	Err error
}

// Classify implements judge.Classifier. It is safe for concurrent use: the
// client bounds its own concurrency and this type holds no mutable state.
func (c *Classifier) Classify(ctx context.Context, tool string, args json.RawMessage) judge.Verdict {
	start := c.now()
	lang := c.language(ctx)

	// Native file readers are settled by the same deterministic rule the local
	// grader uses. This is the JEV route's entry, so a configured decision model
	// does not turn `read`/`grep`/`find` into a model call. The gate still runs
	// the static floor and boundary before/after this verdict.
	if v, ok := judge.LocalReadOnlyVerdict(tool, args, lang); ok {
		c.observe(CallRecord{Tool: tool, Routed: "decided", Level: v.Level})
		return v
	}

	facts := c.factsFor(tool, args)

	if c.Client == nil || c.isDisabled() {
		// No decision model configured: behave exactly as if this layer were not
		// here, so enabling the feature is the only thing that changes behavior.
		// The same path serves a model that already failed hard this run.
		reason := "no decision model configured"
		if c.isDisabled() {
			reason = "the decision model was disabled earlier in this run"
		}
		return c.escalate(ctx, tool, args, CallRecord{Tool: tool, Routed: "escalated", Escalation: reason}, lang)
	}

	network := judge.NetworkRequested(tool, args)
	direct := judge.EscalationRequested(tool, args)
	res, err := c.Client.Evaluate(ctx, c.state(tool, args, facts), QuestionsFor(network, direct))
	rec := CallRecord{Tool: tool}
	if res != nil {
		rec.Attempts = res.Attempts
		rec.Latency = res.Latency
	}
	if err != nil {
		// A failed call is not a verdict. It is the strongest signal that this
		// layer cannot answer, so it goes up with the reason attached.
		rec.Routed, rec.Escalation, rec.Err = "escalated", "the decision model failed", err
		if isHardFailure(err) {
			c.disable(err)
		}
		return c.escalate(ctx, tool, args, rec, lang)
	}
	if rec.Latency == 0 {
		rec.Latency = c.now().Sub(start)
	}

	out := Decide(Request{Facts: facts, Answers: res.Answers, Config: c.Config, Lang: lang, NetworkRequested: network, DirectRequested: direct})
	if out.Route == RouteEscalate {
		rec.Routed, rec.Escalation = "escalated", out.Escalation
		return c.escalate(ctx, tool, args, rec, lang)
	}

	rec.Routed, rec.Level = "decided", out.Level
	c.observe(rec)
	return judge.Verdict{
		Level:   out.Level,
		Network: out.Network,
		Direct:  out.Direct,
		// The level came from the judge, so it is not a static verdict.
		Source: Source,
		// The rationale is the sentence the user reads. It is assembled from
		// the reason label, because a decision model returns labels and this is
		// the layer that owns the wording.
		Reasons: []string{out.Rationale},
		Risk:    out.Risk,
		Lang:    lang,
	}
}

// escalate hands the call to the fallback, or fails closed when there is none.
// The tool and its arguments travel as parameters rather than through the
// record: a record is telemetry, and a telemetry type must not be the thing a
// security decision depends on being populated.
func (c *Classifier) escalate(ctx context.Context, tool string, args json.RawMessage, rec CallRecord, lang string) judge.Verdict {
	if c.Fallback != nil {
		c.observe(rec)
		return c.Fallback.Classify(ctx, tool, args)
	}
	c.observe(rec)
	// Failed is what makes the gates contain or ask instead of allowing. An
	// escalation with nowhere to go must not be read as one.
	return judge.Verdict{
		Level:   judge.Confirm,
		Failed:  true,
		Source:  Source,
		Reasons: []string{"no reviewer could decide this call; failing closed"},
		Lang:    lang,
	}
}

// factsFor parses the call when it is a shell command, and returns nil
// otherwise. A non-shell tool has no argv to inspect; its arguments are the
// reviewer's business, and the policy treats nil facts as "nothing to distrust"
// rather than as a reason to skip.
func (c *Classifier) factsFor(tool string, args json.RawMessage) *shellfacts.Plan {
	if !strings.EqualFold(strings.TrimSpace(tool), "bash") {
		return nil
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil
	}
	cmd := strings.TrimSpace(a.Command)
	if cmd == "" {
		return nil
	}
	return shellfacts.Parse(cmd)
}

// State is the material the decision model reads. It is a package-level type
// rather than an anonymous map so the shape is documented and a test can assert
// it: what the judge sees decides what it can decide.
type State struct {
	Tool string `json:"tool"`
	// Command is the shell command text, unchanged. It is kept beside the
	// parsed form because a parse can be wrong about a construct, and the
	// original text is the tiebreaker a reader can check.
	Command string `json:"command,omitempty"`
	// Arguments carries a native tool's arguments verbatim.
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Cwd is the directory the call runs in, so a path inside the workspace can
	// be told from one outside it.
	Cwd string `json:"cwd,omitempty"`
	// Facts is the parsed command, absent for a non-shell tool. Its presence is
	// what lets the judge reason about what will run instead of what the text
	// says — the computed program name, the nested command, the redirect.
	Facts *shellfacts.Plan `json:"facts,omitempty"`
	// Access lists the paths the call touches, when they can be determined.
	Access []shellfacts.AccessSummary `json:"access,omitempty"`
	// Auth says what the session already knows about those paths: whether each
	// lands inside the workspace, whether a consent question is in front of this
	// call, and what the authorization table has decided (granted or refused) per
	// capability. Without it the model grades the same text as though no answer
	// had ever been given: it re-asks what the user just approved, or reads a
	// pending question as settled.
	//
	// The flags come from the boundary gate's own predicates, never from a second
	// reading of the arguments, so the reviewer cannot be told something the gate
	// does not believe.
	Auth []judge.AuthFact `json:"auth,omitempty"`
	// Outliving lists paths whose change lands OUTSIDE the edit: `.git/hooks/*`
	// runs on the next git command (and git commands are the ones the read-only
	// fast path waves through all day), `.git/config` is the same door
	// (core.hooksPath, core.sshCommand, credential helpers), and AGENTS.md is
	// injected into every later session's prompt — a persistent injection channel
	// that needs no execution at all.
	//
	// It is a FACT, not a verdict, and that is the point of sending it: a person
	// asking for a pre-commit hook is asking for something legitimate, so a hard
	// deny on the name would take a reasonable request off the table. A hook body
	// looks like any other script until a reader knows when it runs, which is
	// exactly what a file's contents do not say.
	Outliving []string `json:"outliving,omitempty"`
}

func (c *Classifier) state(tool string, args json.RawMessage, facts *shellfacts.Plan) State {
	st := State{Tool: tool, Cwd: c.Cwd}
	st.Auth = judge.AuthFacts(tool, args, c.Cwd, c.ExtraRoots, c.WritableAt)
	st.Outliving = judge.OutlivingPaths(tool, args)
	if facts != nil {
		st.Command = facts.Raw
		st.Facts = facts
		st.Access = facts.Summary()
		return st
	}
	if len(args) > 0 {
		st.Arguments = args
	}
	return st
}

// language detects the rationale's language the way the slow reviewer does.
func (c *Classifier) language(ctx context.Context) string {
	if c.Language != "" {
		return c.Language
	}
	return judge.ConversationLanguage(agentcore.MessageSnapshotFromContext(ctx))
}

func (c *Classifier) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Classifier) observe(rec CallRecord) {
	if c.Observer != nil {
		c.Observer(rec)
	}
}

// isHardFailure reports whether an error will not clear by being retried. The
// client already refuses to retry these; this decides whether to stop asking at
// all, which is the stronger response.
func isHardFailure(err error) bool {
	var apiErr *jev.Error
	if errors.As(err, &apiErr) {
		// Retryable=false covers 429/529's opposite: a 451 region block, a 401
		// rejected key, a 422 refused request. None of them improves by asking
		// again a second later.
		return !apiErr.Retryable
	}
	// A local misconfiguration (no key, a malformed question) is equally
	// permanent for this process.
	return errors.Is(err, errNoKey) || strings.Contains(err.Error(), "no API key")
}

var errNoKey = errors.New("no API key")

// Stats counts how the classifier routed calls, for a status command.
type Stats struct {
	mu        sync.Mutex
	decided   int
	escalated int
	failed    int
	latency   time.Duration
}

// Observe is an Observer that keeps counters.
func (s *Stats) Observe(rec CallRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case rec.Err != nil:
		s.failed++
		s.escalated++
	case rec.Routed == "decided":
		s.decided++
	default:
		s.escalated++
	}
	s.latency += rec.Latency
}

// Snapshot returns the counters: decided, escalated, failed, and the total time
// spent in the decision model.
func (s *Stats) Snapshot() (decided, escalated, failed int, total time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decided, s.escalated, s.failed, s.latency
}
