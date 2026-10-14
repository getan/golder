package reviewpolicy

// Tests for the classifier. The load-bearing cases are the failure paths: a
// decision model that is unreachable, absent, or contradicted must hand the
// call to the fallback, and with no fallback it must fail closed — never
// approve.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/getan/golder/internal/jev"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/shellfacts"
)

// spyFallback records what it was asked and answers with a fixed verdict.
type spyFallback struct {
	calls []string
	args  []json.RawMessage
	v     judge.Verdict
}

func (s *spyFallback) Classify(_ context.Context, tool string, args json.RawMessage) judge.Verdict {
	s.calls = append(s.calls, tool)
	s.args = append(s.args, args)
	return s.v
}

// jevServer answers an evaluation with the given per-question JSON bodies.
func jevServer(t *testing.T, answers map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		parts := make([]string, 0, len(answers))
		for id, body := range answers {
			parts = append(parts, `"`+id+`":`+body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{` + strings.Join(parts, ",") + `},"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cleanAnswers is a fully decisive, danger-free answer set.
func cleanAnswers() map[string]string {
	return map[string]string{
		QL:   `{"type":"choice","choice":"allow","probabilities":{"allow":0.97,"confirm":0.03},"confidence":0.95}`,
		QR:   `{"type":"score","score":0.5,"probabilities":{"0":0.5,"1":0.5},"confidence":0.9}`,
		QRev: `{"type":"noul","noul":0.01}`,
		QSec: `{"type":"noul","noul":0.01}`,
		QInt: `{"type":"noul","noul":0.95}`,
		QWhy: `{"type":"choice","choice":"read_only","probabilities":{"read_only":0.9,"unknown":0.1},"confidence":0.9}`,
	}
}

func newClassifier(t *testing.T, srv *httptest.Server, fallback judge.Classifier) *Classifier {
	t.Helper()
	cfg := jev.Config{Endpoint: srv.URL, APIKey: "test-key", MaxRetries: -1}
	return &Classifier{
		Client:   jev.New(cfg),
		Fallback: fallback,
		Config:   DefaultConfig(),
		Language: "en",
		Cwd:      "/work",
	}
}

func TestDecidedCallDoesNotReachTheFallback(t *testing.T) {
	// The whole point: a confident, clean answer is decided here, and the slow
	// reviewer is never called.
	srv := jevServer(t, cleanAnswers())
	fb := &spyFallback{v: judge.Verdict{Level: judge.Confirm, Source: "llm"}}
	c := newClassifier(t, srv, fb)

	v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls -la"}`))
	if v.Level != judge.Allow {
		t.Fatalf("level = %v, want allow (%q)", v.Level, v.Reasons)
	}
	if v.Source != Source {
		t.Errorf("source = %q, want %q", v.Source, Source)
	}
	if v.Failed {
		t.Error("a decided verdict was marked failed")
	}
	if len(fb.calls) != 0 {
		t.Errorf("the fallback was called %d times for a decided call", len(fb.calls))
	}
	if len(v.Reasons) == 0 || v.Reasons[0] == "" {
		t.Error("no rationale on the verdict")
	}
}

func TestEscalationReachesTheFallbackWithTheSameCall(t *testing.T) {
	// The arguments must travel intact: the fallback grades the SAME call, not
	// a summary of it, or its verdict would be about something else.
	answers := cleanAnswers()
	// A confirm verdict always escalates.
	answers[QL] = `{"type":"choice","choice":"confirm","probabilities":{"confirm":0.9,"allow":0.1},"confidence":0.9}`
	srv := jevServer(t, answers)
	fb := &spyFallback{v: judge.Verdict{Level: judge.Sandbox, Source: "llm"}}
	c := newClassifier(t, srv, fb)

	args := json.RawMessage(`{"command":"npm install"}`)
	v := c.Classify(context.Background(), "bash", args)
	if len(fb.calls) != 1 {
		t.Fatalf("fallback calls = %d, want 1", len(fb.calls))
	}
	if fb.calls[0] != "bash" {
		t.Errorf("fallback got tool %q", fb.calls[0])
	}
	if string(fb.args[0]) != string(args) {
		t.Errorf("fallback got args %s, want %s", fb.args[0], args)
	}
	if v.Level != judge.Sandbox || v.Source != "llm" {
		t.Errorf("verdict = %+v, want the fallback's", v)
	}
}

func TestUnreachableJudgeEscalates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	fb := &spyFallback{v: judge.Verdict{Level: judge.Sandbox, Source: "llm"}}
	c := newClassifier(t, srv, fb)

	v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if len(fb.calls) != 1 {
		t.Fatalf("an unreachable judge did not escalate (fallback calls = %d)", len(fb.calls))
	}
	if v.Source != "llm" {
		t.Errorf("verdict source = %q, want the fallback's", v.Source)
	}
}

func TestNoFallbackFailsClosed(t *testing.T) {
	// With nowhere to escalate, an unanswerable call must come back as Failed —
	// the shape every gate already treats as contain-or-ask. Returning Allow
	// here would make a broken judge the permissive path.
	answers := cleanAnswers()
	answers[QL] = `{"type":"choice","choice":"confirm","probabilities":{"confirm":0.9,"allow":0.1},"confidence":0.9}`
	srv := jevServer(t, answers)
	c := newClassifier(t, srv, nil)

	v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"npm install"}`))
	if !v.Failed {
		t.Fatalf("verdict = %+v, want Failed for an escalation with no fallback", v)
	}
	if v.Level == judge.Allow {
		t.Error("an unanswerable call was allowed")
	}

	// The same must hold when the judge itself is unreachable.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	c2 := newClassifier(t, dead, nil)
	if v := c2.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`)); !v.Failed {
		t.Errorf("verdict = %+v, want Failed when the judge is down and there is no fallback", v)
	}
}

func TestNilClientBehavesAsIfAbsent(t *testing.T) {
	// Enabling the decision model is the only thing that should change
	// behaviour: with no client configured, every call must go to the fallback
	// exactly as it did before this layer existed.
	fb := &spyFallback{v: judge.Verdict{Level: judge.Allow, Source: "llm"}}
	c := &Classifier{Fallback: fb, Config: DefaultConfig(), Language: "en"}
	v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if len(fb.calls) != 1 {
		t.Fatalf("fallback calls = %d, want 1", len(fb.calls))
	}
	if v.Source != "llm" {
		t.Errorf("source = %q, want the fallback's", v.Source)
	}
}

func TestFactsAreSentForBashAndNotForNativeTools(t *testing.T) {
	// The state is the judge's whole world. A shell call must carry the parsed
	// facts (that is the point of the layer); a native tool has no argv, and
	// sending a bogus empty one would invite a judgement about nothing.
	var seen []State
	answers := cleanAnswers()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State json.RawMessage `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var st State
		if err := json.Unmarshal(req.State, &st); err != nil {
			t.Errorf("state did not decode as a State: %v", err)
		}
		seen = append(seen, st)
		parts := make([]string, 0, len(answers))
		for id, body := range answers {
			parts = append(parts, `"`+id+`":`+body)
		}
		_, _ = w.Write([]byte(`{"answers":{` + strings.Join(parts, ",") + `}}`))
	}))
	t.Cleanup(srv.Close)
	c := newClassifier(t, srv, nil)

	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"cat /etc/hosts > /tmp/out"}`))
	c.Classify(context.Background(), "apply_patch", json.RawMessage(`{"patch":"*** Begin Patch"}`))

	if len(seen) != 2 {
		t.Fatalf("states seen = %d, want 2", len(seen))
	}
	shell, native := seen[0], seen[1]
	if shell.Facts == nil {
		t.Fatal("a bash call carried no parsed facts")
	}
	if shell.Command != "cat /etc/hosts > /tmp/out" {
		t.Errorf("command = %q, want the original text", shell.Command)
	}
	// The access list is what makes a redirect visible to the judge.
	var sawRedirect bool
	for _, a := range shell.Access {
		if a.Path == "/tmp/out" && a.Op&shellfacts.Write != 0 {
			sawRedirect = true
		}
	}
	if !sawRedirect {
		t.Errorf("access = %+v, want the redirect target with a write operation", shell.Access)
	}
	if shell.Cwd != "/work" {
		t.Errorf("cwd = %q, want /work", shell.Cwd)
	}
	if native.Facts != nil {
		t.Error("a native tool call carried shell facts")
	}
	if !strings.Contains(string(native.Arguments), "Begin Patch") {
		t.Errorf("native arguments = %s, want them passed through", native.Arguments)
	}
}

func TestLanguageFollowsTheConversation(t *testing.T) {
	var seenLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answers := cleanAnswers()
		parts := make([]string, 0, len(answers))
		for id, body := range answers {
			parts = append(parts, `"`+id+`":`+body)
		}
		_, _ = w.Write([]byte(`{"answers":{` + strings.Join(parts, ",") + `}}`))
	}))
	t.Cleanup(srv.Close)
	c := newClassifier(t, srv, nil)
	c.Language = "" // detect from the conversation
	defer func() { _ = seenLang }()

	v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if v.Level != judge.Allow {
		t.Fatalf("level = %v, want allow", v.Level)
	}
	// With no conversation the detector defaults to English, so the rationale
	// must be the English wording rather than an empty string.
	if !strings.Contains(v.Reasons[0], "read") && !strings.Contains(v.Reasons[0], "visible") {
		t.Errorf("rationale = %q, want the English wording", v.Reasons[0])
	}
}

func TestObserverSeesEveryCall(t *testing.T) {
	answers := cleanAnswers()
	answers[QL] = `{"type":"choice","choice":"deny","probabilities":{"deny":0.95,"allow":0.05},"confidence":0.95}`
	srv := jevServer(t, answers)
	var records []CallRecord
	c := newClassifier(t, srv, nil)
	c.Observer = func(rec CallRecord) { records = append(records, rec) }

	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf /"}`))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Routed != "decided" || records[0].Level != judge.Deny {
		t.Errorf("record = %+v, want a decided deny", records[0])
	}
}

func TestObserverDoesNotReceiveTheAPIKey(t *testing.T) {
	// Telemetry ends up in logs; the key must never be in it.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(dead.Close)
	var records []CallRecord
	c := newClassifier(t, dead, nil)
	c.Observer = func(rec CallRecord) { records = append(records, rec) }

	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	// The error is carried, but only as the transport error the client built.
	if records[0].Err == nil {
		t.Error("a failed call produced no error in the record")
	}
	if strings.Contains(records[0].Err.Error(), "test-key") {
		t.Errorf("the record leaked the key: %v", records[0].Err)
	}
}

func TestStatsCountRouting(t *testing.T) {
	srv := jevServer(t, cleanAnswers())
	var stats Stats
	c := newClassifier(t, srv, nil)
	c.Observer = stats.Observe

	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	decided, escalated, failed, _ := stats.Snapshot()
	if decided != 1 || escalated != 0 || failed != 0 {
		t.Errorf("stats = decided %d escalated %d failed %d, want 1/0/0", decided, escalated, failed)
	}

	// A call whose answer is ambiguous escalates, and with no fallback that is
	// also a failure — both counters must move.
	answers := cleanAnswers()
	answers[QL] = `{"type":"choice","choice":"confirm","probabilities":{"confirm":0.9,"allow":0.1},"confidence":0.9}`
	srv2 := jevServer(t, answers)
	var stats2 Stats
	c2 := newClassifier(t, srv2, nil)
	c2.Observer = stats2.Observe
	c2.Classify(context.Background(), "bash", json.RawMessage(`{"command":"npm install"}`))
	decided2, escalated2, failed2, _ := stats2.Snapshot()
	if decided2 != 0 || escalated2 != 1 {
		t.Errorf("stats = decided %d escalated %d, want 0/1", decided2, escalated2)
	}
	if failed2 != 0 {
		// The judge answered; only the policy declined to decide. That is an
		// escalation, not a failure, and conflating them would hide a broken
		// upstream behind a healthy-looking metric.
		t.Errorf("failed = %d, want 0 for a policy escalation with a working judge", failed2)
	}
}

func TestBashWithUnparsableCommandEscalates(t *testing.T) {
	// The facts layer is why this layer can be trusted at all: a command it
	// cannot read goes up rather than being judged from its text.
	srv := jevServer(t, cleanAnswers())
	fb := &spyFallback{v: judge.Verdict{Level: judge.Confirm, Source: "llm"}}
	c := newClassifier(t, srv, fb)

	v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls \"unterminated"}`))
	if len(fb.calls) != 1 {
		t.Fatalf("an unparsable command did not escalate (fallback calls = %d)", len(fb.calls))
	}
	if v.Level == judge.Allow {
		t.Error("an unparsable command was allowed")
	}
}

func TestEmptyCommandDoesNotCrash(t *testing.T) {
	srv := jevServer(t, cleanAnswers())
	c := newClassifier(t, srv, nil)
	for _, args := range []string{`{}`, `{"command":""}`, `{"command":"   "}`, `not json`, ``} {
		v := c.Classify(context.Background(), "bash", json.RawMessage(args))
		// Whatever the shape, the outcome must be a real verdict and never a
		// panic or a silent allow of something unseen.
		if v.Source == "" {
			t.Errorf("args %q produced a verdict with no source: %+v", args, v)
		}
	}
}

func TestConcurrentClassifyIsSafe(t *testing.T) {
	// Tool batches grade concurrently; the contract says so explicitly.
	srv := jevServer(t, cleanAnswers())
	var stats Stats
	c := newClassifier(t, srv, nil)
	c.Observer = stats.Observe

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 4; j++ {
				c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls -la"}`))
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if decided, _, _, _ := stats.Snapshot(); decided != 32 {
		t.Errorf("decided = %d, want 32", decided)
	}
}

func TestEscalationErrorIsWrappedNotSwallowed(t *testing.T) {
	var records []CallRecord
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(dead.Close)
	fb := &spyFallback{v: judge.Verdict{Level: judge.Sandbox, Source: "llm"}}
	c := newClassifier(t, dead, fb)
	c.Observer = func(rec CallRecord) { records = append(records, rec) }

	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if len(records) != 1 || records[0].Err == nil {
		t.Fatalf("records = %+v, want one with the transport error", records)
	}
	if !strings.Contains(records[0].Escalation, "failed") {
		t.Errorf("escalation = %q, want it to name the judge failure", records[0].Escalation)
	}
	// The fallback still decided, so the caller gets its verdict.
	if records[0].Err == nil {
		t.Error("the failure was swallowed")
	}
}

// TestHardFailureDisablesTheCheapPath pins the response to a PERMANENT failure.
//
// The failure that motivated it is real: TypeSafe answers HTTP 451 ("not
// available in your region") to a whole region. Retrying that on every gated
// call would add a failed round trip to every tool call for the life of the
// process, with no chance of a different answer — so the cheap path switches
// itself off and the fallback handles the rest of the run.
func TestHardFailureDisablesTheCheapPath(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(451)
		_, _ = w.Write([]byte(`{"title":"Typesafe is not available in your region.","status":451}`))
	}))
	t.Cleanup(srv.Close)

	fb := &spyFallback{v: judge.Verdict{Level: judge.Sandbox, Source: "llm"}}
	c := newClassifier(t, srv, fb)

	// The first call discovers the block.
	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("endpoint calls = %d, want 1", got)
	}
	if disabled, reason := c.Disabled(); !disabled {
		t.Error("a 451 did not disable the cheap path")
	} else if !strings.Contains(reason, "region") {
		t.Errorf("reason = %q, want it to name the region block", reason)
	}

	// Every later call must skip the network entirely and go to the fallback.
	for i := 0; i < 5; i++ {
		v := c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
		if v.Source != "llm" {
			t.Fatalf("call %d did not reach the fallback: %+v", i, v)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("endpoint calls = %d after the disable, want 1", got)
	}
	if len(fb.calls) != 6 {
		t.Errorf("fallback calls = %d, want 6 (the first failure plus five)", len(fb.calls))
	}
}

// TestRetryableFailureDoesNotDisable: a transient failure must not switch the
// cheap path off permanently, or one 429 would cost the fast path for the rest
// of the session.
func TestRetryableFailureDoesNotDisable(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	fb := &spyFallback{v: judge.Verdict{Level: judge.Allow, Source: "llm"}}
	c := newClassifier(t, srv, fb)

	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if disabled, _ := c.Disabled(); disabled {
		t.Error("a retryable failure disabled the cheap path")
	}
	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("endpoint calls = %d, want the cheap path to keep trying", got)
	}
}

// TestRejectedKeyDisablesToo: a 401 cannot fix itself mid-run either.
func TestRejectedKeyDisablesToo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	fb := &spyFallback{v: judge.Verdict{Level: judge.Confirm, Source: "llm"}}
	c := newClassifier(t, srv, fb)
	c.Classify(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if disabled, _ := c.Disabled(); !disabled {
		t.Error("a rejected key did not disable the cheap path")
	}
}
