package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// TestIsStreamCutErr pins the match: the three ways a stream ends early, and
// nothing else — a 400 or a plain EOF is the wire's real answer, not a cut.
func TestIsStreamCutErr(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"truncation sentinel", io.ErrUnexpectedEOF, true},
		{"truncation text", &fallbackTestErr{"unexpected EOF"}, true},
		{"transport read error", &fallbackTestErr{"read error: transport: unexpected EOF"}, true},
		{"idle", errStreamIdle, true},
		{"wrapped idle", fmt.Errorf("no data for 5m0s: %w", errStreamIdle), true},
		{"idle wording only", &fallbackTestErr{"no data for 5m0s"}, false},
		{"stall", errStreamStall, true},
		{"wrapped stall", fmt.Errorf("no output for 6m0s: %w", errStreamStall), true},
		{"silent end", errStreamNoTerminal, true},
		{"wrapped silent end", fmt.Errorf("commandcode: %w", errStreamNoTerminal), true},
		{"clean eof", io.EOF, false},
		{"client error", &fallbackTestErr{"400 invalid tools"}, false},
		{"nil", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isStreamCutErr(c.err); got != c.want {
				t.Errorf("isStreamCutErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// The sentence a person reads must say which failure it was, so the three cases
// stay distinguishable — and leave every other error alone.
func TestStreamCutErrorNamesEachCase(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"truncation", io.ErrUnexpectedEOF, "closed before the answer finished"},
		{"idle", errStreamIdle, "went quiet"},
		{"stall", errStreamStall, "stopped producing"},
		{"silent end", errStreamNoTerminal, "without a final event"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := streamCutError("opencode-go", c.err).Error()
			if !strings.Contains(got, "opencode-go: stream cut") {
				t.Errorf("streamCutError = %q, want it to name the provider", got)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("streamCutError = %q, want %q", got, c.want)
			}
			if !strings.Contains(got, "retry the turn") {
				t.Errorf("streamCutError = %q, want the remedy", got)
			}
		})
	}

	// Anything that is not a cut is passed through untouched: the wire's own
	// answer is what the user needs to see.
	plain := &fallbackTestErr{"400 model_coming_soon"}
	if got := streamCutError("opencode-go", plain); got != plain {
		t.Errorf("streamCutError(non-cut) = %v, want it unchanged", got)
	}
}

// TestStreamCutErrorNamesTheProviderOnce: the drivers prefix their errors with the
// provider, and the sentence already carries it. Naming the gateway twice in one
// line is the kind of noise that makes an error look like machinery talking.
func TestStreamCutErrorNamesTheProviderOnce(t *testing.T) {
	err := &fallbackTestErr{"commandcode: unexpected EOF"}
	got := streamCutError("commandcode", err).Error()
	want := "commandcode: stream cut — the connection closed before the answer finished (unexpected EOF); retry the turn"
	if got != want {
		t.Errorf("streamCutError = %q, want %q", got, want)
	}
}

// TestStreamCutRetriedErrorSaysTheRetryIsSpent: when the second attempt was cut
// the same way, "retry the turn" alone leaves the user wondering whether a retry
// had even run. It did.
func TestStreamCutRetriedErrorSaysTheRetryIsSpent(t *testing.T) {
	got := streamCutRetriedError("commandcode", &fallbackTestErr{"commandcode: unexpected EOF"}, 1).Error()
	if !strings.Contains(got, "already retried once") {
		t.Errorf("streamCutRetriedError = %q, want it to say the retry was made", got)
	}
	if strings.Contains(got, "commandcode: unexpected EOF") {
		t.Errorf("streamCutRetriedError = %q, want the provider named once", got)
	}
}

// The transport wire names the same condition: without this, a cut stream on
// the Chat or Messages wire reported the reader's bare "unexpected EOF" behind a
// generic "read error" prefix.
func TestClassifyTransportErrorNamesATruncatedStream(t *testing.T) {
	got := classifyTransportError(io.ErrUnexpectedEOF)
	if got == nil || !strings.Contains(got.Error(), "stream truncated") {
		t.Fatalf("classifyTransportError(unexpected EOF) = %v, want a truncation", got)
	}
	if !strings.Contains(got.Error(), "unexpected EOF") {
		t.Errorf("classified = %q, want the underlying error kept", got)
	}
}

// truncatedBody serves the given bytes and then fails the read the way a cut
// connection does: net/http reports io.ErrUnexpectedEOF when a body ends before
// the response it belongs to, and that is the exact error the transcript used to
// print as "unexpected EOF".
type truncatedBody struct {
	data []byte
}

func (b *truncatedBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *truncatedBody) Close() error { return nil }

// truncatedSSE builds a 200 SSE response whose body carries frames and then
// dies mid-response.
func truncatedSSE(frames ...string) *http.Response {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &truncatedBody{data: []byte(b.String())},
	}
}

// The driver reports the reader's error as it arrives; naming it is
// streamretry.go's job (see streamretry_test.go), and content that already
// streamed must survive either way.
func TestResponsesDriverReportsATruncatedStreamVerbatim(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return truncatedSSE(deltaFrame("half an answer")), nil
	})
	d := newResponsesTestDriver("https://api.openai.test/v1", rt)

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:   "gpt-4o",
		Context: LlmContext{Messages: agentcore.MessageList{userMsg("hi")}},
		Config:  StreamConfig{APIKey: "sk-test"},
	})
	if err != nil {
		t.Fatalf("StreamCompletion returned early error: %v", err)
	}

	var partialText string
	var failure error
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case StreamTextEvent:
			partialText = textOf(e.Partial)
		case StreamErrorEvent:
			failure = e.Err
		}
	}
	if partialText != "half an answer" {
		t.Errorf("partial text = %q, want the streamed content kept", partialText)
	}
	if failure == nil || !isTruncatedStreamErr(failure) {
		t.Errorf("failure = %v, want the reader's truncation", failure)
	}
}

// A body that stops producing without ever erroring must not hang the SDK
// stream: the guard's idle watchdog ends it (see sse_guard.go). The window is
// shortened through the same knob the transport reads.
func TestSSEGuardIdleWatchdogFires(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "20ms")
	guard := newSSEStreamGuard(context.Background(), io.NopCloser(&stallingBody{}))
	defer guard.Close()

	start := time.Now()
	_, err := io.ReadAll(guard)
	if err == nil {
		t.Fatal("reading a silent body returned no error, want the idle watchdog")
	}
	if !isStreamCutErr(err) {
		t.Errorf("err = %v, want a cut stream", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("watchdog took %s, want it to fire on the configured window", elapsed)
	}
}

// Keep-alive comments keep the idle window satisfied while the model produces
// nothing; only the content-stall watchdog catches that, which is why both
// exist.
func TestSSEGuardStallWatchdogFiresOnKeepAlives(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "20ms")
	// The keep-alives keep the idle window happy; it is the first-token budget
	// that must expire, so it is the one shortened here.
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "40ms")
	guard := newSSEStreamGuard(context.Background(), io.NopCloser(&keepAliveBody{}))
	defer guard.Close()

	_, err := io.ReadAll(guard)
	if err == nil {
		t.Fatal("reading a keep-alive-only body returned no error, want the stall watchdog")
	}
	if !errors.Is(err, errStreamStall) {
		t.Errorf("err = %v, want the content-stall sentinel (not idle: keep-alives did arrive)", err)
	}
	// Nothing was produced at all, so the failure is in the first-token phase and
	// must say that rather than claiming the output stopped mid-answer.
	got := streamCutError("opencode-go", err).Error()
	for _, want := range []string{"no answer started", "keep-alives", "last line"} {
		if !strings.Contains(got, want) {
			t.Errorf("streamCutError = %q, want %q", got, want)
		}
	}
}

// stallingBody delivers nothing and never returns.
type stallingBody struct{}

// TestFirstContentTimeoutWidensPerAttempt: the first-token budget is not one
// number repeated. A request that is genuinely slow gets more room on each retry,
// so it is not cut three times at the same mark, while a later attempt still ends
// the turn instead of hanging.
func TestFirstContentTimeoutWidensPerAttempt(t *testing.T) {
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "1m")
	base := context.Background()
	for attempt, want := range map[int]time.Duration{
		0: time.Minute,
		1: 2 * time.Minute,
		2: 3 * time.Minute,
		9: 3 * time.Minute,
	} {
		if got := firstContentTimeout(withStreamAttempt(base, attempt)); got != want {
			t.Errorf("attempt %d budget = %s, want %s", attempt, got, want)
		}
	}
	// A malformed setting falls back to the default rather than to no budget.
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "soon")
	if got := firstContentTimeout(base); got != 3*time.Minute {
		t.Errorf("malformed setting = %s, want the 3m default", got)
	}
}

// The reported failure: a POST that dies before any response arrives, which
// net/http wraps as `Post "https://…": EOF`. It produced nothing, so it is a
// cut and must be retried instead of ending the turn with the raw wrapper.
func TestConnectFailureClassification(t *testing.T) {
	postEOF := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://api.example/v1/responses", Err: err}
	}
	retryable := []error{
		postEOF(io.EOF),
		postEOF(errors.New("connection reset by peer")),
		postEOF(errors.New("dial tcp 1.2.3.4:443: connect: connection refused")),
		errors.New("net/http: TLS handshake timeout"),
		errors.New("read tcp: i/o timeout"),
	}
	for _, err := range retryable {
		if !isConnectFailure(err) || !isStreamCutErr(err) {
			t.Errorf("%v must count as a connect failure", err)
		}
		if got := streamCutReason(err); got != StreamCutConnect {
			t.Errorf("reason for %v = %q, want %q", err, got, StreamCutConnect)
		}
	}
	// A response that ended early is the truncation path's, not a connect
	// failure; a bare io.EOF is the transport's normal end of stream; and the
	// user's own interrupt is never retried.
	notRetryable := []error{
		io.ErrUnexpectedEOF,
		io.EOF,
		errors.New("unexpected EOF"),
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("commandcode: missing API key (set COMMANDCODE_API_KEY)"),
		errors.New("transport: upstream 400: bad request"),
	}
	for _, err := range notRetryable {
		if isConnectFailure(err) {
			t.Errorf("%v must not count as a connect failure", err)
		}
	}
}

// The sentence names the provider, the attempt count and the transport's own
// text, instead of the bare wrapper the user saw.
func TestStreamConnectErrorSentence(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://api.commandcode.ai/provider/v1/responses", Err: io.EOF}
	got := streamConnectError("commandcode", err, 3).Error()
	for _, want := range []string{"commandcode", "3 attempts", "api.commandcode.ai", "EOF", "retry the turn"} {
		if !strings.Contains(got, want) {
			t.Errorf("sentence = %q, want %q", got, want)
		}
	}
	// A configuration error is returned unchanged: retrying it would only delay
	// the message that says how to fix it.
	cfg := errors.New("commandcode: missing API key (set COMMANDCODE_API_KEY)")
	if got := streamConnectError("commandcode", cfg, 3); got != cfg {
		t.Errorf("config error = %v, want it unchanged", got)
	}
}

func (b *stallingBody) Read([]byte) (int, error) {
	time.Sleep(time.Hour)
	return 0, io.EOF
}

// keepAliveBody delivers SSE comments (frames the guard drops for being
// contentless) but no `data:` frame, so bytes keep arriving while the model
// produces nothing.
type keepAliveBody struct{}

func (b *keepAliveBody) Read(p []byte) (int, error) {
	for {
		time.Sleep(5 * time.Millisecond)
		n := copy(p, []byte(": keep-alive\n\n"))
		if n > 0 {
			return n, nil
		}
	}
}
