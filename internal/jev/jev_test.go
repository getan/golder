package jev

// Tests for the Jev client. The load-bearing ones are the failure paths: a
// reviewer that cannot get a decision must return an error, never a default
// that reads as approval.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJev serves the evaluation endpoint. handler receives the decoded request
// and returns the HTTP status plus body.
func fakeJev(t *testing.T, handler func(req evaluateRequest) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req evaluateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		status, body := handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// choiceAnswer builds a choice answer body.
func choiceAnswer(choice string, probs map[string]float64) string {
	b, _ := json.Marshal(map[string]any{
		"type": "choice", "choice": choice, "probabilities": probs, "confidence": 0.9,
	})
	return string(b)
}

func newTestClient(t *testing.T, srv *httptest.Server, cfg Config) *Client {
	t.Helper()
	cfg.Endpoint = srv.URL
	cfg.APIKey = "test-key"
	if cfg.Sleep == nil {
		cfg.Sleep = func(context.Context, time.Duration) bool { return true }
	}
	return New(cfg)
}

func TestEvaluateHappyPath(t *testing.T) {
	var gotReq evaluateRequest
	srv := fakeJev(t, func(req evaluateRequest) (int, string) {
		gotReq = req
		return http.StatusOK, `{"model":"jev-1.13.0","answers":{` +
			`"level":` + choiceAnswer("allow", map[string]float64{"allow": 0.97, "confirm": 0.03}) +
			`},"usage":{"input_tokens":100,"output_tokens":20}}`
	})
	c := newTestClient(t, srv, Config{Model: "jev-latest"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.Evaluate(ctx, "ls -la", map[string]Question{
		"level": {Type: Choice, Instructions: "Is this safe?", Criteria: map[string]any{
			"allow": "read-only", "confirm": "needs a human",
		}},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// The model travels in the request: the default would otherwise be sent
	// silently and a caller's choice ignored.
	if gotReq.Model != "jev-latest" {
		t.Errorf("request model = %q, want jev-latest", gotReq.Model)
	}
	if gotReq.State != "ls -la" {
		t.Errorf("request state = %v, want the command", gotReq.State)
	}
	a, ok := res.Answers["level"]
	if !ok {
		t.Fatal("no answer for the question id")
	}
	if a.Choice != "allow" {
		t.Errorf("choice = %q, want allow", a.Choice)
	}
	if got := a.Margin(); got < 0.9 {
		t.Errorf("margin = %v, want the decisive answer's margin", got)
	}
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 20 {
		t.Errorf("usage = %+v, want 100/20", res.Usage)
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", res.Attempts)
	}
}

func TestAllThreeQuestionTypesInOneCall(t *testing.T) {
	// The point of the request shape: one state, several typed questions, one
	// round trip, each answer decoded by its own type.
	srv := fakeJev(t, func(req evaluateRequest) (int, string) {
		if len(req.Questions) != 3 {
			t.Errorf("questions sent = %d, want 3", len(req.Questions))
		}
		return http.StatusOK, `{"model":"m","answers":{` +
			`"level":` + choiceAnswer("sandbox", map[string]float64{"allow": 0.1, "sandbox": 0.9}) + `,` +
			`"risk":{"type":"score","score":2.4,"probabilities":{"0":0.1,"1":0.4,"2":0.5},"confidence":0.8},` +
			`"touches_secrets":{"type":"noul","noul":0.02}` +
			`},"usage":{"input_tokens":1,"output_tokens":1}}`
	})
	c := newTestClient(t, srv, Config{})
	res, err := c.Evaluate(context.Background(), map[string]any{"cmd": "x"}, map[string]Question{
		"level":           {Type: Choice, Instructions: "Verdict?", Criteria: map[string]any{"allow": nil, "sandbox": nil, "deny": nil}},
		"risk":            {Type: Score, Instructions: "How risky?", Criteria: []any{"low", "medium", "high"}},
		"touches_secrets": {Type: Noul, Instructions: "Does it touch secrets?"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := res.Answers["level"].Choice; got != "sandbox" {
		t.Errorf("level = %q, want sandbox", got)
	}
	if got := res.Answers["risk"].Score; got != 2.4 {
		t.Errorf("risk = %v, want 2.4", got)
	}
	if got := res.Answers["touches_secrets"].Noul; got != 0.02 {
		t.Errorf("noul = %v, want 0.02", got)
	}
}

func TestLocalValidationRefusesBadQuestions(t *testing.T) {
	var calls int32
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusOK, `{"answers":{"x":{"type":"noul","noul":0.5}}}`
	})
	client := newTestClient(t, srv, Config{})
	cases := []struct {
		name string
		q    Question
	}{
		{"no type", Question{Instructions: "x"}},
		{"unknown type", Question{Type: "ranking", Instructions: "x"}},
		{"choice without options", Question{Type: Choice, Instructions: "x", Criteria: map[string]any{}}},
		{"choice with the wrong criteria shape", Question{Type: Choice, Instructions: "x", Criteria: []any{"a"}}},
		{"score with one level", Question{Type: Score, Instructions: "x", Criteria: []any{"only"}}},
		{"score with eleven levels", Question{Type: Score, Instructions: "x",
			Criteria: []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}}},
		{"no instructions", Question{Type: Noul}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := client.Evaluate(context.Background(), "s", map[string]Question{"q": c.q}); err == nil {
				t.Error("a malformed question was accepted")
			}
		})
	}
	// Validation is local: a schema mistake must not cost a round trip.
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("made %d HTTP calls for locally-invalid questions", n)
	}
}

func TestMissingKeyFailsWithoutCallingOut(t *testing.T) {
	var calls int32
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		atomic.AddInt32(&calls, 1)
		return http.StatusOK, `{"answers":{}}`
	})
	c := New(Config{Endpoint: srv.URL, APIKey: ""})
	if _, err := c.Evaluate(context.Background(), "s", map[string]Question{
		"q": {Type: Noul, Instructions: "x"},
	}); err == nil {
		t.Fatal("a call without a key succeeded")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("called the endpoint %d times without a key", n)
	}
}

func TestRetryOn429ThenSucceed(t *testing.T) {
	var attempts int32
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			return http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`
		}
		return http.StatusOK, `{"answers":{"q":{"type":"noul","noul":0.9}}}`
	})
	c := newTestClient(t, srv, Config{})
	res, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", res.Attempts)
	}
	if got := res.Answers["q"].Noul; got != 0.9 {
		t.Errorf("noul = %v, want 0.9", got)
	}
}

func TestOverloadedIsRetried(t *testing.T) {
	var attempts int32
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		atomic.AddInt32(&attempts, 1)
		return statusOverloaded, `{"error":{"message":"overloaded"}}`
	})
	c := newTestClient(t, srv, Config{MaxRetries: 2})
	_, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	if err == nil {
		t.Fatal("a persistently overloaded endpoint produced a result")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 + 2 retries)", got)
	}
}

func TestNonRetryableErrorIsNotRetried(t *testing.T) {
	var attempts int32
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		atomic.AddInt32(&attempts, 1)
		return http.StatusUnprocessableEntity, `{"error":{"message":"bad question"}}`
	})
	c := newTestClient(t, srv, Config{})
	var apiErr *Error
	_, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	if err == nil {
		t.Fatal("a 422 produced a result")
	}
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a *jev.Error", err)
	}
	if apiErr.Retryable {
		t.Error("a 422 was marked retryable")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 for a non-retryable status", got)
	}
}

func TestMalformedAnswersAreRefused(t *testing.T) {
	// A defaulted answer would become a decision the model never made, so every
	// malformed shape must fail the call instead of yielding a zero value.
	bodies := []string{
		`{"model":"m","answers":{},"usage":{}}`,
		`{"model":"m","answers":{"q":{"type":"choice"}},"usage":{}}`,
		`{"model":"m","answers":{"q":{"type":"choice","choice":"a"}},"usage":{}}`,
		`{"model":"m","answers":{"q":{"type":"score"}},"usage":{}}`,
		`{"model":"m","answers":{"q":{"type":"noul","noul":1.5}},"usage":{}}`,
		`{"model":"m","answers":{"q":{"type":"mystery"}},"usage":{}}`,
		`not json`,
	}
	for _, body := range bodies {
		srv := fakeJev(t, func(evaluateRequest) (int, string) { return http.StatusOK, body })
		c := newTestClient(t, srv, Config{})
		res, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
		if err == nil {
			t.Errorf("body %q produced %+v, want an error", body, res)
		}
	}
}

func TestContextCancellationStopsWaitingForASlot(t *testing.T) {
	// The limiter must honor the caller's context: a cancelled run cannot be
	// parked waiting for a concurrency slot it will never use.
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		started.Done()
		<-release
		return http.StatusOK, `{"answers":{"q":{"type":"noul","noul":0.1}}}`
	})
	c := newTestClient(t, srv, Config{MaxConcurrent: 1})
	// Occupy the single slot.
	go func() {
		_, _ = c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	}()
	started.Wait()
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Evaluate(ctx, "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}}); err == nil {
		t.Error("a cancelled call returned without an error")
	}
	close(release)
}

func TestConcurrencyIsBounded(t *testing.T) {
	var inFlight, peak int32
	var mu sync.Mutex
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		n := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return http.StatusOK, `{"answers":{"q":{"type":"noul","noul":0.5}}}`
	})
	const limit = 2
	c := newTestClient(t, srv, Config{MaxConcurrent: limit})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if peak > limit {
		t.Errorf("peak concurrency = %d, want at most %d", peak, limit)
	}
}

func TestErrorNeverCarriesTheKey(t *testing.T) {
	// An error message ends up in logs and transcripts; the key must not.
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		return http.StatusInternalServerError, `{"error":{"message":"boom"}}`
	})
	c := newTestClient(t, srv, Config{MaxRetries: -1})
	_, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	if err == nil {
		t.Fatal("a 500 produced a result")
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Errorf("error message leaked the key: %v", err)
	}
}

func TestResponseBodyIsBounded(t *testing.T) {
	// A hostile or broken endpoint must not be able to stream unbounded data
	// into memory; the read is capped.
	huge := `{"model":"m","answers":{"q":{"type":"noul","noul":0.5}},"pad":"` +
		strings.Repeat("x", 2<<20) + `"}`
	srv := fakeJev(t, func(evaluateRequest) (int, string) { return http.StatusOK, huge })
	c := newTestClient(t, srv, Config{})
	if _, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}}); err == nil {
		t.Error("an oversized body was parsed successfully")
	}
}

func TestTopAndRunnerUpAreDeterministic(t *testing.T) {
	a := Answer{Probabilities: map[string]float64{"allow": 0.4, "confirm": 0.4, "deny": 0.2}}
	// A tie must not flip between runs: the same input yields the same answer.
	for i := 0; i < 20; i++ {
		if got, _ := a.Top(); got != "allow" {
			t.Fatalf("Top() = %q on a tie, want the deterministic choice \"allow\"", got)
		}
	}
	if m := a.Margin(); m != 0 {
		t.Errorf("Margin() = %v, want 0 for a tie", m)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	c := New(Config{Backoff: 100 * time.Millisecond})
	var prev time.Duration
	for attempt := 1; attempt <= 8; attempt++ {
		got := c.backoffFor(attempt)
		if got > maxBackoff {
			t.Errorf("attempt %d waited %v, above the cap %v", attempt, got, maxBackoff)
		}
		if prev != 0 && got < prev/4 {
			t.Errorf("attempt %d waited %v, far below the previous %v", attempt, got, prev)
		}
		prev = got
	}
}

func TestEvaluateNeverReturnsNilResultOnError(t *testing.T) {
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		return http.StatusUnauthorized, `{"error":{"message":"nope"}}`
	})
	c := newTestClient(t, srv, Config{MaxRetries: -1})
	res, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	if err == nil {
		t.Fatal("a 401 produced a result")
	}
	// A caller that ignores the error must not accidentally get an empty-but-
	// valid Result; nil plus an error is the only safe shape.
	if res != nil {
		t.Errorf("result = %+v with an error, want nil", res)
	}
}

func TestEmptyQuestionSetIsRefused(t *testing.T) {
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		return http.StatusOK, `{"answers":{}}`
	})
	c := newTestClient(t, srv, Config{})
	if _, err := c.Evaluate(context.Background(), "s", nil); err == nil {
		t.Error("an empty question set produced a result")
	}
}

func TestErrorBodyIsSummarized(t *testing.T) {
	long := strings.Repeat("y", 5000)
	srv := fakeJev(t, func(evaluateRequest) (int, string) {
		return http.StatusBadRequest, fmt.Sprintf(`{"error":{"message":%q}}`, long)
	})
	c := newTestClient(t, srv, Config{MaxRetries: -1})
	_, err := c.Evaluate(context.Background(), "s", map[string]Question{"q": {Type: Noul, Instructions: "x"}})
	if err == nil {
		t.Fatal("a 400 produced a result")
	}
	if len(err.Error()) > 1000 {
		t.Errorf("error message is %d bytes, want it summarized", len(err.Error()))
	}
}
