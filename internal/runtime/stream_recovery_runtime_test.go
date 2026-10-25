package runtime

// Runtime regression for user-visible stream recovery around LOCAL tool calls.
//
// The requirement: a stream that dies while a local tool call is still being
// streamed must not end the turn. The provider layer re-issues the request, the
// caller keeps everything it already has (earlier turns, executed tool calls,
// their results), and the retried attempt's tool call runs exactly once. The
// cut attempt's half-arrived call must never run, because its arguments were
// never complete — executing it is executing a tool nobody asked for.
//
// Everything below runs the real loop (StartRun → agentLoop → runLoop →
// streamAssistantResponse → ExecuteToolCalls) against a real OpenAI-compatible
// driver resolved by the public resolver, so the provider wrapper's retry policy
// (provider.withStreamRetry, applied at that boundary and not by the driver) is
// engaged exactly the way it is in production. The only fake is the wire: an
// httptest SSE endpoint that replays one scripted attempt per request.
//
// Baseline note: the pre-recovery provider treats "a tool call was forwarded"
// as final (streamretry.go replayableCut), so every test here that expects a
// recovery is red on that baseline by design. The assertions that must hold
// either way — a cut attempt's tool call is never executed — are green there and
// stay green here: they are the guard rail, not the bug.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

// Tool call ids used by the scripts below. They are distinct on purpose: an id
// in the history or in an execution event is then unambiguous proof of which
// attempt the loop acted on.
const (
	// recoveryCallFirst is the local tool call of the turn that completes
	// normally, before the stream is cut.
	recoveryCallFirst = "call_1_first"
	// recoveryCallCut is the call the dying attempt was still streaming. Its
	// arguments are deliberately truncated, so acting on it is never right.
	recoveryCallCut = "call_2_cut"
	// recoveryCallRetried is the whole call the re-issued attempt returns.
	recoveryCallRetried = "call_3_retried"
	// recoveryCutArgs is a tool call's arguments as they look mid-stream: the
	// JSON is still open, so nothing downstream may parse or run it.
	recoveryCutArgs = `{"step":"tw`
)

// sseEnding is how one scripted attempt stops producing frames.
type sseEnding int

const (
	// sseComplete ends the attempt the way the wire does: finish_reason, [DONE].
	sseComplete sseEnding = iota
	// sseAbort drops the socket mid-response, as a dying gateway does: the
	// client's read fails with a truncated-body (EOF) error.
	sseAbort
	// sseIdle keeps the socket open and silent, so the transport's idle
	// watchdog (GOLDER_STREAM_IDLE_TIMEOUT) is what ends the attempt.
	sseIdle
)

// sseAttempt is one scripted response: the frames it sends, then its ending.
type sseAttempt struct {
	frames      []string
	end         sseEnding
	afterFrames func()
}

// fakeSSEEndpoint is a real HTTP server speaking the OpenAI Chat Completions SSE
// wire. It plays one scripted attempt per request, in order, and records every
// request body so a test can see what the loop actually sent on each attempt.
type fakeSSEEndpoint struct {
	server *httptest.Server
	stop   chan struct{}

	mu       sync.Mutex
	scripts  []sseAttempt
	attempts int
	bodies   []string
}

func newFakeSSEEndpoint(t *testing.T, scripts ...sseAttempt) *fakeSSEEndpoint {
	t.Helper()
	f := &fakeSSEEndpoint{scripts: scripts, stop: make(chan struct{})}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		// Release any attempt that is holding the socket open on purpose (sseIdle)
		// before Close waits for it.
		close(f.stop)
		f.server.Close()
	})
	return f
}

func (f *fakeSSEEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	n := f.attempts
	f.attempts++
	f.bodies = append(f.bodies, string(body))
	script := sseAttempt{end: sseAbort}
	if n < len(f.scripts) {
		script = f.scripts[n]
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range script.frames {
		fmt.Fprintf(w, "data: %s\n\n", frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if script.afterFrames != nil {
		script.afterFrames()
	}
	switch script.end {
	case sseIdle:
		// Silence. The client's idle watchdog is what has to notice.
		select {
		case <-r.Context().Done():
		case <-f.stop:
		case <-time.After(30 * time.Second): // never hang the test's cleanup
		}
	case sseAbort:
		// Abort mid-response: the client sees the body end before the response
		// does, which is the dropped-connection shape.
		panic(http.ErrAbortHandler)
	default:
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// attemptCount is how many times the driver reached the wire.
func (f *fakeSSEEndpoint) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// requestBody returns the raw JSON body of request i (0-based, in arrival order).
func (f *fakeSSEEndpoint) requestBody(t *testing.T, i int) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.bodies) {
		t.Fatalf("no request %d; the driver made %d request(s)", i, len(f.bodies))
	}
	return f.bodies[i]
}

// jsonString renders s as a JSON string literal.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// sseToolCallFrame is one streamed tool-call delta, the shape the Chat wire uses
// while a call is still arriving.
func sseToolCallFrame(id, name, args string) string {
	return fmt.Sprintf(
		`{"id":"chatcmpl-fake","model":"gpt-faux","choices":[{"delta":{"tool_calls":[{"index":0,"id":%s,"type":"function","function":{"name":%s,"arguments":%s}}]}}]}`,
		jsonString(id), jsonString(name), jsonString(args))
}

// sseTextFrame is one streamed text delta.
func sseTextFrame(text string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-fake","model":"gpt-faux","choices":[{"delta":{"content":%s}}]}`, jsonString(text))
}

// sseFinishFrame is the response's own stop reason.
func sseFinishFrame(reason string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-fake","model":"gpt-faux","choices":[{"finish_reason":%s}]}`, jsonString(reason))
}

// completeToolAttempt scripts an attempt that returns one whole local tool call.
func completeToolAttempt(id, name, args string) sseAttempt {
	return sseAttempt{
		frames: []string{sseToolCallFrame(id, name, args), sseFinishFrame("tool_calls")},
		end:    sseComplete,
	}
}

// completeTextAttempt scripts an attempt that answers in text and stops.
func completeTextAttempt(text string) sseAttempt {
	return sseAttempt{
		frames: []string{sseTextFrame(text), sseFinishFrame("stop")},
		end:    sseComplete,
	}
}

// cutToolAttempt scripts an attempt that dies while a local tool call is still
// being streamed: the call's arguments never close and the wire never reaches
// its terminator.
func cutToolAttempt(end sseEnding, id, name, partialArgs string) sseAttempt {
	return sseAttempt{frames: []string{sseToolCallFrame(id, name, partialArgs)}, end: end}
}

// recoveryRecordingTool is a local tool that records every call id it executes.
// Recording (rather than counting) is what makes a re-run of an already
// completed call visible: the id shows up twice.
type recoveryRecordingTool struct {
	name string

	mu   sync.Mutex
	runs []string
}

func newRecoveryRecordingTool(name string) *recoveryRecordingTool {
	return &recoveryRecordingTool{name: name}
}

func (r *recoveryRecordingTool) Name() string        { return r.name }
func (r *recoveryRecordingTool) Description() string { return "records every execution" }
func (r *recoveryRecordingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (r *recoveryRecordingTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

func (r *recoveryRecordingTool) Execute(_ context.Context, id string, _ json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	r.mu.Lock()
	r.runs = append(r.runs, id)
	r.mu.Unlock()
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent("ran " + id)},
	}, nil
}

// executed returns the call ids the tool actually ran, in execution order.
func (r *recoveryRecordingTool) executed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.runs))
	copy(out, r.runs)
	return out
}

// newRecoveryRunConfig resolves the provider through the PUBLIC resolver against
// the fake endpoint. ResolveProvider is the boundary that applies the cut-stream
// retry wrapper (provider.ResolveProvider → withStreamRetry), so driving the
// loop through this config exercises the real retry policy rather than a driver
// on its own.
func newRecoveryRunConfig(t *testing.T, endpoint *fakeSSEEndpoint, tools ...agentcore.AgentTool) RunConfig {
	t.Helper()
	p, name, err := provider.ResolveProvider("gpt-faux", endpoint.server.URL, "openai", "", func(string) string { return "" })
	if err != nil {
		t.Fatalf("provider.ResolveProvider: %v", err)
	}
	if p == nil {
		t.Fatal("provider.ResolveProvider returned a nil provider")
	}
	if name != "openai" {
		t.Fatalf("resolved provider name = %q, want %q", name, "openai")
	}
	reg := agenttool.NewToolRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("register tool %s: %v", tl.Name(), err)
		}
	}
	return RunConfig{
		LoopConfig: LoopConfig{
			Model:    "gpt-faux",
			Provider: name,
			APIKey:   "sk-test-not-a-real-key",
			Stream:   provider.StreamFnFromProvider(p),
		},
		Batch: agenttool.BatchConfig{
			ToolExecutorConfig: agenttool.ToolExecutorConfig{Registry: reg},
		},
	}
}

// newRecoveryAgentCtx starts a conversation with one user turn and advertises
// the local tools in the request, so the provider sees them as local.
func newRecoveryAgentCtx(tools ...agentcore.AgentTool) *agentcore.AgentContext {
	return &agentcore.AgentContext{
		SystemPrompt: "you are a test agent",
		Messages: agentcore.MessageList{
			agentcore.UserMessage{
				RoleField: agentcore.RoleUser,
				Content:   agentcore.ContentList{agentcore.NewTextContent("find the answer")},
			},
		},
		Tools: tools,
	}
}

// recoveryExecutionIDs lists the tool call ids the loop started, in order, as
// the front-end sees them.
func recoveryExecutionIDs(events []agentcore.AgentEvent) []string {
	var ids []string
	for _, ev := range events {
		if e, ok := ev.(agentcore.ToolExecutionStartEvent); ok {
			ids = append(ids, e.ToolCallID)
		}
	}
	return ids
}

// recoveryAssistantMessages returns the assistant messages of a history.
func recoveryAssistantMessages(msgs agentcore.MessageList) []agentcore.AssistantMessage {
	var out []agentcore.AssistantMessage
	for _, m := range msgs {
		if a, ok := m.(agentcore.AssistantMessage); ok {
			out = append(out, a)
		}
	}
	return out
}

// assertToolCallPairing checks the history invariant a provider request depends
// on: every tool call appears exactly once and is answered by exactly one tool
// result, and no tool result refers to a call that is not in the history. A
// recovery that replays what it already has breaks this first — with a duplicate
// call, a duplicate result, an orphan result, or a dangling call.
func assertToolCallPairing(t *testing.T, msgs agentcore.MessageList) {
	t.Helper()
	calls := map[string]int{}
	results := map[string]int{}
	for i, m := range msgs {
		switch v := m.(type) {
		case agentcore.AssistantMessage:
			for _, c := range v.ToolCalls() {
				calls[c.ID]++
				if calls[c.ID] > 1 {
					t.Errorf("tool call id %q appears %d times in history (message %d): a recovered turn must not duplicate a call", c.ID, calls[c.ID], i)
				}
			}
		case agentcore.ToolResultMessage:
			if v.ToolCallID == "" {
				t.Errorf("tool result at message %d has no tool call id", i)
				continue
			}
			results[v.ToolCallID]++
			if results[v.ToolCallID] > 1 {
				t.Errorf("tool result for %q appears %d times in history (message %d): a recovered turn must not re-run a finished tool", v.ToolCallID, results[v.ToolCallID], i)
			}
		}
	}
	for id, n := range calls {
		if results[id] == 0 {
			t.Errorf("tool call %q is dangling: no tool result in history", id)
		}
		if results[id] > 1 {
			t.Errorf("tool call %q has %d results, want exactly 1", id, results[id])
		}
		_ = n
	}
	for id := range results {
		if calls[id] == 0 {
			t.Errorf("tool result for %q has no matching tool call in history", id)
		}
	}
}

// assertNoToolCallID proves a call id never entered the history as a call or a
// result: the cut attempt's half-arrived call leaves nothing behind.
func assertNoToolCallID(t *testing.T, msgs agentcore.MessageList, id string) {
	t.Helper()
	for i, m := range msgs {
		switch v := m.(type) {
		case agentcore.AssistantMessage:
			for _, c := range v.ToolCalls() {
				if c.ID == id {
					t.Errorf("history message %d carries the cut attempt's tool call %q, want it absent", i, id)
				}
			}
		case agentcore.ToolResultMessage:
			if v.ToolCallID == id {
				t.Errorf("history message %d carries a tool result for the cut attempt's call %q, want it absent", i, id)
			}
		}
	}
}

// equalStrings compares two string slices.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestStreamRecoveryRuntimeHealthyTurnsStayUntouched is the guard rail the
// recovery tests are measured against: whatever the recovery policy becomes, a
// stream that never breaks stays completely untouched — one request per turn, no
// retry notice, no extra tool run. It is green before and after the fix, and it
// is what proves the fake wire below is a faithful one.
func TestStreamRecoveryRuntimeHealthyTurnsStayUntouched(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "75ms")
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "2m")
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")

	endpoint := newFakeSSEEndpoint(t,
		completeToolAttempt(recoveryCallFirst, "record", `{"step":"one"}`),
		completeTextAttempt("all done"),
	)
	tool := newRecoveryRecordingTool("record")
	cfg := newRecoveryRunConfig(t, endpoint, tool)
	agentCtx := newRecoveryAgentCtx(tool)

	events := collectEvents(t, StartRun(context.Background(), agentCtx, cfg))

	if got := endpoint.attemptCount(); got != 2 {
		t.Errorf("provider requests = %d, want 2: a healthy stream must not be re-issued", got)
	}
	if got := tool.executed(); !equalStrings(got, []string{recoveryCallFirst}) {
		t.Errorf("tool executions = %v, want [%s]", got, recoveryCallFirst)
	}
	assertToolCallPairing(t, agentCtx.Messages)
	if got := countKind(eventKinds(events), agentcore.EventTurnStart); got != 2 {
		t.Errorf("turns = %d, want 2: %v", got, eventKinds(events))
	}
	finals := recoveryAssistantMessages(agentCtx.Messages)
	if len(finals) == 0 || finals[len(finals)-1].StopReason != agentcore.StopReasonEndTurn {
		t.Fatalf("run did not end on a natural answer: %+v", finals)
	}
	if got := strings.TrimSpace(agentcore.ContentToText(finals[len(finals)-1].Content)); got != "all done" {
		t.Errorf("final text = %q, want %q", got, "all done")
	}
	for _, ev := range events {
		if n, ok := ev.(agentcore.ProviderNoticeEvent); ok && n.Kind == provider.StreamNoticeRetry {
			t.Errorf("a healthy stream reported a retry notice (attempt %d of %d): %q", n.Attempt, n.MaxAttempts, n.Reason)
		}
	}
}

// TestStreamRecoveryRuntimeResumesALocalToolCallCutMidArguments is the headline
// requirement: a stream cut while a local tool call is still arriving must not
// end the turn. The retried attempt's whole call runs once, the turn before it
// keeps its executed tool and result, the cut attempt's call never runs, and the
// run goes on to a natural end instead of handing the prompt back.
func TestStreamRecoveryRuntimeResumesALocalToolCallCutMidArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  sseEnding
	}{
		{"transport EOF", sseAbort},
		{"idle timeout", sseIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The idle window is a seam, so the test uses milliseconds instead of
			// waiting out the real 90s default; the first-token window stays wide so
			// the cut is unambiguously the mid-answer one.
			t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "75ms")
			t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "2m")
			t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")

			endpoint := newFakeSSEEndpoint(t,
				completeToolAttempt(recoveryCallFirst, "record", `{"step":"one"}`),
				cutToolAttempt(tc.end, recoveryCallCut, "record", recoveryCutArgs),
				completeToolAttempt(recoveryCallRetried, "record", `{"step":"two"}`),
				completeTextAttempt("all done"),
			)
			tool := newRecoveryRecordingTool("record")
			cfg := newRecoveryRunConfig(t, endpoint, tool)
			agentCtx := newRecoveryAgentCtx(tool)

			events := collectEvents(t, StartRun(context.Background(), agentCtx, cfg))

			// The turn's stream was re-issued inside the SAME turn: four requests
			// (first turn, cut attempt, its retry, final turn) rather than three. A
			// new loop turn would also work, but it would show the user two turns
			// for one answer, so the recovery is pinned to the provider boundary.
			if got := endpoint.attemptCount(); got != 4 {
				t.Errorf("provider requests = %d, want 4 (turn 1, cut attempt, its retry, final turn)", got)
			}

			// Exactly the two whole calls ran, each once, in order. The cut
			// attempt's call is not among them.
			if got := tool.executed(); !equalStrings(got, []string{recoveryCallFirst, recoveryCallRetried}) {
				t.Errorf("tool executions = %v, want [%s %s]", got, recoveryCallFirst, recoveryCallRetried)
			}
			if got := recoveryExecutionIDs(events); !equalStrings(got, []string{recoveryCallFirst, recoveryCallRetried}) {
				t.Errorf("tool_execution_start events = %v, want [%s %s]", got, recoveryCallFirst, recoveryCallRetried)
			}

			// The cut attempt's half-arrived call left nothing behind: it never
			// ran, and the history does not carry it.
			for _, id := range tool.executed() {
				if id == recoveryCallCut {
					t.Errorf("the cut attempt's tool call %q was executed, want it dropped", recoveryCallCut)
				}
			}
			assertNoToolCallID(t, agentCtx.Messages, recoveryCallCut)
			assertToolCallPairing(t, agentCtx.Messages)

			// History: the completed tool and its result survived the cut, and the
			// recovered call sits in history with its WHOLE arguments — the partial
			// ones were not merged into it.
			var sawFirstResult bool
			for _, m := range agentCtx.Messages {
				if r, ok := m.(agentcore.ToolResultMessage); ok && r.ToolCallID == recoveryCallFirst {
					sawFirstResult = true
				}
			}
			if !sawFirstResult {
				t.Error("the tool result completed before the cut is missing from history; a recovery must keep it")
			}
			for _, a := range recoveryAssistantMessages(agentCtx.Messages) {
				for _, c := range a.ToolCalls() {
					if c.ID != recoveryCallRetried {
						continue
					}
					if got := strings.TrimSpace(string(c.Arguments)); got != `{"step":"two"}` {
						t.Errorf("recovered call arguments = %s, want the whole {\"step\":\"two\"} (not the cut attempt's partial)", got)
					}
				}
			}

			// The retry carried the finished tool call and its result: nothing the
			// user already has is thrown away to make the re-issue work.
			if endpoint.attemptCount() > 2 {
				retryBody := endpoint.requestBody(t, 2)
				if !strings.Contains(retryBody, recoveryCallFirst) {
					t.Errorf("the retried request dropped the already-completed tool call %q: %s", recoveryCallFirst, retryBody)
				}
			}

			// The turn continued instead of handing the prompt back: three turns,
			// and the run ends on its own answer rather than on a failure.
			if got := countKind(eventKinds(events), agentcore.EventTurnStart); got != 3 {
				t.Errorf("turns = %d, want 3 (tool turn, recovered tool turn, final answer): %v", got, eventKinds(events))
			}
			finals := recoveryAssistantMessages(agentCtx.Messages)
			if len(finals) == 0 {
				t.Fatal("no assistant message in history")
			}
			for _, a := range finals {
				if a.StopReason == agentcore.StopReasonError || a.StopReason == agentcore.StopReasonAborted {
					t.Errorf("assistant message stopped as %q (%q); the cut should have been recovered", a.StopReason, a.ErrorMessage)
				}
			}
			last := finals[len(finals)-1]
			if last.StopReason != agentcore.StopReasonEndTurn {
				t.Errorf("final stop reason = %q, want %q", last.StopReason, agentcore.StopReasonEndTurn)
			}
			if got := strings.TrimSpace(agentcore.ContentToText(last.Content)); got != "all done" {
				t.Errorf("final text = %q, want %q", got, "all done")
			}

			// The recovery reached the front-end as a provider notice, which is how
			// a user learns a retry happened; without it the re-issue is invisible.
			notices := 0
			for _, ev := range events {
				if n, ok := ev.(agentcore.ProviderNoticeEvent); ok && n.Kind == provider.StreamNoticeRetry && n.Attempt >= 1 {
					notices++
				}
			}
			if notices == 0 {
				t.Errorf("no provider retry notice in the event stream; the cut was not announced: %v", eventKinds(events))
			}
		})
	}
}

// TestStreamRecoveryRuntimeRunsARetriedToolCallExactlyOnce covers the retry that
// answers with the call the cut attempt already streamed in full. Whether the
// model repeats its own call id or mints a new one, the loop must run it once.
func TestStreamRecoveryRuntimeRunsARetriedToolCallExactlyOnce(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "75ms")
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "2m")
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")

	// The repeated call is complete on the wire but the attempt dies before the
	// response's own terminator, so it is still a cut attempt's call.
	const same = "call_same_id"
	endpoint := newFakeSSEEndpoint(t,
		completeToolAttempt(recoveryCallFirst, "record", `{"step":"one"}`),
		cutToolAttempt(sseAbort, same, "record", `{"step":"two"}`),
		completeToolAttempt(same, "record", `{"step":"two"}`),
		completeTextAttempt("all done"),
	)
	tool := newRecoveryRecordingTool("record")
	cfg := newRecoveryRunConfig(t, endpoint, tool)
	agentCtx := newRecoveryAgentCtx(tool)

	events := collectEvents(t, StartRun(context.Background(), agentCtx, cfg))

	if got := endpoint.attemptCount(); got != 4 {
		t.Errorf("provider requests = %d, want 4", got)
	}
	if got := tool.executed(); !equalStrings(got, []string{recoveryCallFirst, same}) {
		t.Errorf("tool executions = %v, want [%s %s] — the repeated call must run exactly once", got, recoveryCallFirst, same)
	}
	assertToolCallPairing(t, agentCtx.Messages)
	if got := countKind(eventKinds(events), agentcore.EventTurnStart); got != 3 {
		t.Errorf("turns = %d, want 3: %v", got, eventKinds(events))
	}
	finals := recoveryAssistantMessages(agentCtx.Messages)
	if len(finals) == 0 || finals[len(finals)-1].StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("run did not end on a natural answer: %+v", finals)
	}
}

// TestStreamRecoveryRuntimeStopsAfterTheRetryBudgetWithoutRunningTheCutTool is
// the failing side of the same policy: when the budget is spent the turn ends
// with an error, and no incomplete tool call was ever executed.
func TestStreamRecoveryRuntimeStopsAfterTheRetryBudgetWithoutRunningTheCutTool(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "75ms")
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "2m")
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "2")

	endpoint := newFakeSSEEndpoint(t,
		completeToolAttempt(recoveryCallFirst, "record", `{"step":"one"}`),
		cutToolAttempt(sseAbort, recoveryCallCut, "record", recoveryCutArgs),
		cutToolAttempt(sseAbort, recoveryCallCut, "record", recoveryCutArgs),
		cutToolAttempt(sseAbort, recoveryCallCut, "record", recoveryCutArgs),
	)
	tool := newRecoveryRecordingTool("record")
	cfg := newRecoveryRunConfig(t, endpoint, tool)
	agentCtx := newRecoveryAgentCtx(tool)

	events := collectEvents(t, StartRun(context.Background(), agentCtx, cfg))

	// The budget is spent on the wire: the cut turn is attempted once and
	// retried twice, so four requests in total — and then it stops.
	if got := endpoint.attemptCount(); got != 4 {
		t.Errorf("provider requests = %d, want 4 (turn 1 + 1 attempt + 2 retries)", got)
	}
	// Only the completed tool ran; the cut call ran zero times despite three
	// attempts streaming it.
	if got := tool.executed(); !equalStrings(got, []string{recoveryCallFirst}) {
		t.Errorf("tool executions = %v, want [%s]: an unexecuted cut tool call must never run", got, recoveryCallFirst)
	}
	if got := recoveryExecutionIDs(events); !equalStrings(got, []string{recoveryCallFirst}) {
		t.Errorf("tool_execution_start events = %v, want [%s]", got, recoveryCallFirst)
	}
	assertNoToolCallID(t, agentCtx.Messages, recoveryCallCut)
	assertToolCallPairing(t, agentCtx.Messages)

	// The run ends, and it says it failed rather than pretending it finished.
	finals := recoveryAssistantMessages(agentCtx.Messages)
	if len(finals) == 0 {
		t.Fatal("no assistant message in history")
	}
	last := finals[len(finals)-1]
	if last.StopReason != agentcore.StopReasonError {
		t.Errorf("final stop reason = %q, want %q", last.StopReason, agentcore.StopReasonError)
	}
	if strings.TrimSpace(last.ErrorMessage) == "" {
		t.Error("exhausted retry budget produced no error message; the turn ends without saying why")
	}
	if got := countKind(eventKinds(events), agentcore.EventTurnStart); got != 2 {
		t.Errorf("turns = %d, want 2 (the cut turn never reaches a third): %v", got, eventKinds(events))
	}
}

// TestStreamRecoveryRuntimeDoesNotRunACutToolCallAfterCancel pins the interrupt:
// a user who cancels while the cut attempt's tool call is arriving gets an
// aborted turn with no tool run and no phantom result — and no retry storm.
func TestStreamRecoveryRuntimeDoesNotRunACutToolCallAfterCancel(t *testing.T) {
	// The attempt would idle forever; the cut that matters is the user's cancel.
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "5s")
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "10s")
	t.Setenv("GOLDER_STREAM_MAX_RETRIES", "4")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cut := cutToolAttempt(sseIdle, recoveryCallCut, "record", recoveryCutArgs)
	// Tool deltas are intentionally buffered until a successful terminal.
	// Cancel at the wire boundary, not on a UI event that must never appear.
	cut.afterFrames = cancel
	endpoint := newFakeSSEEndpoint(t,
		completeToolAttempt(recoveryCallFirst, "record", `{"step":"one"}`),
		cut,
	)
	tool := newRecoveryRecordingTool("record")
	cfg := newRecoveryRunConfig(t, endpoint, tool)
	agentCtx := newRecoveryAgentCtx(tool)

	stream := StartRun(ctx, agentCtx, cfg)
	var events []agentcore.AgentEvent
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	if _, err := stream.Result(context.Background()); err != nil {
		t.Fatalf("stream result: %v", err)
	}

	// The interrupt is not turned into another attempt.
	if got := endpoint.attemptCount(); got > 2 {
		t.Errorf("provider requests = %d, want at most 2: a cancelled turn must not be retried", got)
	}
	// The completed tool still ran exactly once; the cut call never ran.
	if got := tool.executed(); !equalStrings(got, []string{recoveryCallFirst}) {
		t.Errorf("tool executions = %v, want [%s]", got, recoveryCallFirst)
	}
	if got := recoveryExecutionIDs(events); !equalStrings(got, []string{recoveryCallFirst}) {
		t.Errorf("tool_execution_start events = %v, want [%s]", got, recoveryCallFirst)
	}
	// No phantom result was fabricated for a call that never ran.
	for _, m := range agentCtx.Messages {
		if r, ok := m.(agentcore.ToolResultMessage); ok && r.ToolCallID == recoveryCallCut {
			t.Errorf("a tool result was recorded for the cut attempt's call %q, which never executed", recoveryCallCut)
		}
	}

	// The turn ended as the user's own interrupt, not as a provider failure.
	finals := recoveryAssistantMessages(agentCtx.Messages)
	if len(finals) == 0 {
		t.Fatal("no assistant message in history")
	}
	last := finals[len(finals)-1]
	// Cancellation may precede the new assistant's first visible event, in
	// which case there is no new message to persist. If one was finalized it
	// must describe an interrupt, never a provider failure.
	if len(finals) > 1 && last.StopReason != agentcore.StopReasonAborted {
		t.Errorf("final stop reason = %q, want %q", last.StopReason, agentcore.StopReasonAborted)
	}
	// A dangling call is healed before the next request (healResumedContext), so
	// the invariant pinned here is only that nothing was duplicated.
	seen := map[string]int{}
	for _, a := range recoveryAssistantMessages(agentCtx.Messages) {
		for _, c := range a.ToolCalls() {
			seen[c.ID]++
			if seen[c.ID] > 1 {
				t.Errorf("tool call %q appears %d times in history after a cancel", c.ID, seen[c.ID])
			}
		}
	}
}
