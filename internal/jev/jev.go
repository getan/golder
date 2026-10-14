// Package jev calls TypeSafe's System One model — a decision model that answers
// typed questions with calibrated probabilities instead of generating text.
//
// Why this shape. The tool-call reviewer used to ask the conversation model for
// strict JSON, then parse it, then retry when the model wrote prose around the
// object. That costs a full generation, and every failure mode is a parsing
// failure. A decision model answers in one pass with no prose: the question
// types are the request schema, so there is nothing to parse and nothing to
// retry on formatting.
//
// What this package is careful about:
//
//   - Concurrency. Tool batches run in parallel, so several reviews can be in
//     flight at once. The upstream documents 429/529 replies under load, so
//     calls pass a limiter and retry with backoff rather than stampeding.
//   - Failure is loud. Every error path returns an error; there is no "assume
//     safe". A caller that cannot get a decision must fall back to a slower
//     reviewer or ask the user — never proceed.
//   - The key never reaches a log. It travels in one header and is never
//     formatted into an error, a warning, or a plan's Raw text.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultEndpoint is the documented evaluation endpoint.
const DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"

// DefaultModel is TypeSafe's flagship alias.
const DefaultModel = "jev-latest"

// Endpoint reports the evaluation endpoint this client was built for. It is the
// key the process-wide circuit breaker uses, so a caller can tell which gateway
// a decision came from.
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// Limits from the upstream documentation, enforced here so a programming
// mistake becomes a local error instead of a 422 after a round trip.
const (
	// MaxChoiceOptions is the documented cap on a Choice's criteria.
	MaxChoiceOptions = 255
	// MinScoreLevels and MaxScoreLevels bound a Score's rubric.
	MinScoreLevels = 2
	MaxScoreLevels = 10
)

// Config configures a Client.
type Config struct {
	// Endpoint overrides the evaluation endpoint (tests, or a gateway).
	Endpoint string
	// APIKey authenticates the request. An empty key is refused by Evaluate
	// rather than sent, because a 401 wastes a round trip and reads as an
	// upstream fault.
	APIKey string
	// APIKeyFunc resolves the key per call, and takes precedence over APIKey.
	// It exists because the credential store refreshes tokens: reading the key
	// once at construction would pin a stale value for the life of the process.
	APIKeyFunc func(context.Context) string
	// Model is the model id or alias. Empty means DefaultModel.
	Model string
	// HTTPClient carries the transport. The caller builds it, which is how the
	// proxy routing stays in the provider package instead of being duplicated
	// here: this client only needs a client, not the knowledge of which
	// providers route through what.
	HTTPClient *http.Client
	// Timeout bounds one call, overriding HTTPClient.Timeout when non-zero.
	Timeout time.Duration
	// MaxConcurrent bounds simultaneous calls. Zero means DefaultConcurrency.
	// It exists because tool batches review in parallel and the upstream
	// documents rate limits under load.
	MaxConcurrent int
	// MaxRetries bounds retry attempts for a retryable failure. Zero means
	// DefaultRetries. Negative disables retrying (tests).
	MaxRetries int
	// Backoff overrides the base backoff. Zero means DefaultBackoff.
	Backoff time.Duration
	// Now and Sleep are seams for tests. Nil means the real clock.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) bool
}

const (
	// DefaultConcurrency keeps a batch from stampeding a rate-limited service.
	// Four is a compromise: enough that a five-call batch barely serializes,
	// low enough that a burst is not what causes the 429.
	DefaultConcurrency = 4
	// DefaultRetries is the number of retries AFTER the first attempt.
	DefaultRetries = 3
	// DefaultBackoff is the base of the exponential backoff.
	DefaultBackoff = 300 * time.Millisecond
	// DefaultTimeout bounds one evaluation.
	DefaultTimeout = 10 * time.Second
	// maxBackoff caps the wait so a caller's deadline stays meaningful.
	maxBackoff = 8 * time.Second
)

// Client calls the evaluation endpoint. It is safe for concurrent use.
type Client struct {
	endpoint string
	keyFunc  func(context.Context) string
	model    string
	http     *http.Client

	// sem bounds concurrent calls. It is a channel so a caller's context
	// cancels while waiting for a slot instead of parking on a mutex.
	sem chan struct{}

	retries int
	backoff time.Duration
	now     func() time.Time
	sleep   func(context.Context, time.Duration) bool
}

// New builds a client. A nil HTTPClient gets a default with Timeout applied.
func New(cfg Config) *Client {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	concurrency := cfg.MaxConcurrent
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	retries := cfg.MaxRetries
	if cfg.MaxRetries == 0 {
		retries = DefaultRetries
	}
	if retries < 0 {
		retries = 0
	}
	backoff := cfg.Backoff
	if backoff <= 0 {
		backoff = DefaultBackoff
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	// Copy before setting Timeout: an *http.Client is shared (clientForURL hands
	// out one per route), so mutating the caller's would change unrelated calls.
	hc := *httpClient
	if hc.Timeout == 0 {
		hc.Timeout = timeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	return &Client{
		endpoint: endpoint,
		keyFunc:  keySource(cfg),
		model:    model,
		http:     &hc,
		sem:      make(chan struct{}, concurrency),
		retries:  retries,
		backoff:  backoff,
		now:      now,
		sleep:    sleep,
	}
}

// keySource folds the two key spellings into one resolver. A fixed key becomes
// a function that ignores its context, so Evaluate has a single path.
func keySource(cfg Config) func(context.Context) string {
	if cfg.APIKeyFunc != nil {
		return func(ctx context.Context) string { return strings.TrimSpace(cfg.APIKeyFunc(ctx)) }
	}
	key := strings.TrimSpace(cfg.APIKey)
	return func(context.Context) string { return key }
}

// QuestionType names the three primitives.
type QuestionType string

const (
	// Choice picks one option from a named set.
	Choice QuestionType = "choice"
	// Score places the state on an ordered rubric.
	Score QuestionType = "score"
	// Noul answers a yes/no question with a probability.
	Noul QuestionType = "noul"
)

// Question is one typed question. The zero value is invalid and Evaluate
// refuses it, so a programming mistake surfaces before a round trip.
type Question struct {
	Type QuestionType
	// Instructions is what to decide. It may be a string or, for a question
	// that needs data alongside it, a map/array — the upstream documents
	// structured instructions, and passing an object keeps the data next to the
	// question instead of concatenated into prose.
	Instructions any
	// Criteria is the option set for a Choice (option → description), the
	// ordered rubric for a Score, or {true, false} descriptions for a Noul.
	Criteria any
}

// Validate reports a malformed question. It runs locally so a schema mistake
// costs nothing, rather than a 422 after a network round trip.
func (q Question) Validate() error {
	switch q.Type {
	case Choice:
		opts, ok := q.Criteria.(map[string]any)
		if !ok {
			return fmt.Errorf("choice question needs a map of options, got %T", q.Criteria)
		}
		if len(opts) == 0 {
			return errors.New("choice question has no options")
		}
		if len(opts) > MaxChoiceOptions {
			return fmt.Errorf("choice question has %d options, the maximum is %d", len(opts), MaxChoiceOptions)
		}
	case Score:
		levels, ok := q.Criteria.([]any)
		if !ok {
			return fmt.Errorf("score question needs an ordered array of levels, got %T", q.Criteria)
		}
		if len(levels) < MinScoreLevels || len(levels) > MaxScoreLevels {
			return fmt.Errorf("score question has %d levels, want between %d and %d",
				len(levels), MinScoreLevels, MaxScoreLevels)
		}
	case Noul:
		// Criteria is optional: a yes/no proposition needs no rubric.
	case "":
		return errors.New("question has no type")
	default:
		return fmt.Errorf("unknown question type %q", q.Type)
	}
	if q.Instructions == nil {
		return errors.New("question has no instructions")
	}
	return nil
}

// Answer is one decoded answer. Exactly one of the three fields is meaningful,
// according to Type.
type Answer struct {
	Type AnswerType
	// Choice is the winning option.
	Choice string
	// Probabilities maps every option or level to its probability.
	Probabilities map[string]float64
	// Confidence is how certain the model is (Choice and Score only). For a
	// Noul the probability itself carries that meaning.
	Confidence float64
	// Score is the probability-weighted position on the rubric.
	Score float64
	// Noul is the probability that a yes/no proposition holds.
	Noul float64
}

// AnswerType mirrors QuestionType on the response side.
type AnswerType string

const (
	AnswerChoice AnswerType = "choice"
	AnswerScore  AnswerType = "score"
	AnswerNoul   AnswerType = "noul"
)

// Top returns the highest-probability option and its probability.
func (a Answer) Top() (string, float64) {
	best, bestP := "", 0.0
	for k, p := range a.Probabilities {
		// Ties break by name so the answer is deterministic: two options at the
		// same probability must not flip between runs.
		if p > bestP || (p == bestP && k < best) {
			best, bestP = k, p
		}
	}
	return best, bestP
}

// RunnerUp returns the second-highest probability, which is what tells a caller
// whether the answer was decisive: 0.9/0.05 is a verdict, 0.45/0.40 is not.
func (a Answer) RunnerUp() (string, float64) {
	best, _ := a.Top()
	second, secondP := "", 0.0
	for k, p := range a.Probabilities {
		if k == best {
			continue
		}
		if p > secondP || (p == secondP && k < second) {
			second, secondP = k, p
		}
	}
	return second, secondP
}

// Margin is how far the winner is ahead of the runner-up.
func (a Answer) Margin() float64 {
	_, top := a.Top()
	_, second := a.RunnerUp()
	return top - second
}

// Usage is the request's token accounting.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Result is one evaluation: the answers plus what the call cost.
type Result struct {
	Model   string
	Answers map[string]Answer
	Usage   Usage
	// Latency is how long the call took, for the telemetry the reviewer already
	// reports.
	Latency time.Duration
	// Attempts counts tries including the first, so a caller can see retries.
	Attempts int
}

// evaluateRequest is the wire shape.
type evaluateRequest struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// evaluateResponse is the wire shape of a success.
type evaluateResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   Usage                      `json:"usage"`
}

type answerWire struct {
	Type          AnswerType         `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Score         float64            `json:"score"`
	Noul          float64            `json:"noul"`
}

// Error is an upstream failure with the parts a caller needs to decide whether
// to retry. It never carries the API key.
type Error struct {
	StatusCode int
	Message    string
	// Retryable reports whether the status is one the upstream documents as
	// transient (429 Too Many Requests, 529 Overloaded).
	Retryable bool
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("jev: upstream %d", e.StatusCode)
	}
	return fmt.Sprintf("jev: upstream %d: %s", e.StatusCode, e.Message)
}

// Evaluate answers every question against one state, in one call.
//
// questions must be non-empty; each is validated locally first. The returned
// Result carries every answer under the key it was asked with.
func (c *Client) Evaluate(ctx context.Context, state any, questions map[string]Question) (*Result, error) {
	if len(questions) == 0 {
		return nil, errors.New("jev: no questions")
	}
	if c.keyFunc == nil || c.keyFunc(ctx) == "" {
		return nil, errors.New("jev: no API key configured")
	}
	wire := make(map[string]question, len(questions))
	for id, q := range questions {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("jev: question with an empty id")
		}
		if err := q.Validate(); err != nil {
			return nil, fmt.Errorf("jev: question %q: %w", id, err)
		}
		wire[id] = question{Type: string(q.Type), Instructions: q.Instructions, Criteria: q.Criteria}
	}
	body, err := json.Marshal(evaluateRequest{Model: c.model, State: state, Questions: wire})
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}

	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	start := c.now()
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			wait := c.backoffFor(attempt)
			if !c.sleep(ctx, wait) {
				return nil, ctx.Err()
			}
		}
		res, err := c.once(ctx, body)
		if err == nil {
			res.Attempts = attempt + 1
			res.Latency = c.now().Sub(start)
			return res, nil
		}
		lastErr = err
		var apiErr *Error
		if errors.As(err, &apiErr) && !apiErr.Retryable {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("jev: gave up after %d attempts: %w", c.retries+1, lastErr)
}

// backoffFor returns the wait before the given attempt, with jitter so two
// concurrent reviews that hit a 429 together do not retry in lockstep.
//
// The cap is applied AFTER the jitter, so maxBackoff is a real upper bound: a
// jittered wait can never exceed it. Adding jitter to an already-capped value
// would let a caller's deadline be overshot by up to a quarter of the cap.
func (c *Client) backoffFor(attempt int) time.Duration {
	wait := c.backoff << (attempt - 1)
	// Jitter: up to 25% either way.
	jitter := time.Duration(rand.Int63n(int64(wait)/2 + 1))
	wait = wait - time.Duration(int64(wait)/4) + jitter
	if wait > maxBackoff {
		wait = maxBackoff
	}
	return wait
}

// acquire takes a concurrency slot, honoring the context.
func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) release() { <-c.sem }

// once performs exactly one HTTP call.
func (c *Client) once(ctx context.Context, body []byte) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The key is read here, per attempt, so a refresh between the first try and
	// a retry is picked up rather than replayed.
	req.Header.Set("Authorization", "Bearer "+c.keyFunc(ctx))

	resp, err := c.http.Do(req)
	if err != nil {
		// A transport failure is retryable: the common causes (a proxy restart,
		// a dropped connection) clear on their own.
		return nil, fmt.Errorf("jev: %w", err)
	}
	defer resp.Body.Close()

	// Bound the read: a hostile or broken endpoint must not stream unbounded
	// data into memory. The documented response is a few hundred bytes.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// A geo-block is not a transient failure and not a bad request: TypeSafe
		// refuses whole regions, so the remedy is routing rather than retrying.
		// Naming the fix here — where the status is known — is what turns an
		// opaque 451 into one command the user can run.
		if resp.StatusCode == statusUnavailableForLegalReasons {
			return nil, &Error{
				StatusCode: resp.StatusCode,
				Message: "this region is not served by TypeSafe; route the decision model " +
					"through a proxy with `/proxy typesafe on` (after /proxy url <address>), " +
					"or turn the fast path off with GOLDER_JEV=off",
			}
		}
		return nil, &Error{
			StatusCode: resp.StatusCode,
			Message:    errorMessage(raw),
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == statusOverloaded,
		}
	}
	var parsed evaluateResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("jev: parse response: %w", err)
	}
	answers, err := decodeAnswers(parsed.Answers)
	if err != nil {
		return nil, err
	}
	return &Result{Model: parsed.Model, Answers: answers, Usage: parsed.Usage}, nil
}

// statusOverloaded is TypeSafe's documented "temporarily overloaded" status.
const statusOverloaded = 529

// statusUnavailableForLegalReasons is the region block. It is a hard refusal —
// retrying cannot clear it — so it is reported with the remedy instead of being
// retried into a timeout.
const statusUnavailableForLegalReasons = 451

// maxResponseBytes bounds one response body.
const maxResponseBytes = 1 << 20

// maxErrorChars bounds the upstream text carried into an error. The message
// ends up in a transcript and possibly a log, so it is summarized rather than
// passed through: an upstream that answers with a wall of text must not decide
// how much of it the user reads.
const maxErrorChars = 300

// errorMessage extracts the upstream's message without echoing the whole body.
// Both the JSON and the fallback path are bounded — bounding only the fallback
// would leave the ordinary case (a well-formed error object) unbounded.
func errorMessage(raw []byte) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		if body.Error.Type != "" {
			return clamp(body.Error.Type+": "+body.Error.Message, maxErrorChars)
		}
		return clamp(body.Error.Message, maxErrorChars)
	}
	return clamp(strings.TrimSpace(string(raw)), maxErrorChars)
}

// clamp truncates text to n bytes, marking the cut. It cuts on a rune boundary
// so a multi-byte character is never split into invalid UTF-8.
func clamp(text string, n int) string {
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// decodeAnswers converts the wire answers into typed ones, refusing a malformed
// or missing answer rather than defaulting it. A defaulted answer would become
// a decision the model never made.
func decodeAnswers(raw map[string]json.RawMessage) (map[string]Answer, error) {
	if len(raw) == 0 {
		return nil, errors.New("jev: response carried no answers")
	}
	out := make(map[string]Answer, len(raw))
	for id, body := range raw {
		var wire answerWire
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, fmt.Errorf("jev: answer %q: %w", id, err)
		}
		a := Answer{
			Type:          wire.Type,
			Choice:        wire.Choice,
			Probabilities: wire.Probabilities,
			Confidence:    wire.Confidence,
			Score:         wire.Score,
			Noul:          wire.Noul,
		}
		switch wire.Type {
		case AnswerChoice:
			if a.Choice == "" || len(a.Probabilities) == 0 {
				return nil, fmt.Errorf("jev: answer %q is a choice with no options", id)
			}
		case AnswerScore:
			if len(a.Probabilities) == 0 {
				return nil, fmt.Errorf("jev: answer %q is a score with no distribution", id)
			}
		case AnswerNoul:
			if a.Noul < 0 || a.Noul > 1 {
				return nil, fmt.Errorf("jev: answer %q has probability %v outside [0,1]", id, a.Noul)
			}
		default:
			return nil, fmt.Errorf("jev: answer %q has unknown type %q", id, wire.Type)
		}
		out[id] = a
	}
	return out, nil
}

// sleepCtx waits, reporting false when the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
