package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// truncatedBody serves the given bytes and then fails the read the way a cut
// connection does: net/http reports io.ErrUnexpectedEOF when a body ends before
// the response it belongs to (a dead chunked stream, or a short Content-Length),
// and that is the exact error the transcript used to print as "unexpected EOF".
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

// A connection that drops before any content arrives is retried once: nothing
// was shown, so the retry cannot duplicate output, and the second attempt is
// what the user actually wanted. Before this, the turn simply ended with
// "error: unexpected EOF".
func TestResponsesDriverRetriesATruncatedStreamWhenNothingWasShown(t *testing.T) {
	var calls int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return truncatedSSE(`{"type":"response.created","response":{"id":"resp_cut"}}`), nil
		}
		return sseResponse(completedFrame("recovered", "resp_ok", "gpt-4o", 5, 3)), nil
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
	msg := drain(t, stream)

	if calls != 2 {
		t.Errorf("requests = %d, want 2 (the cut attempt and one retry)", calls)
	}
	if got := textOf(msg); got != "recovered" {
		t.Errorf("final text = %q, want the retry's answer", got)
	}
	if msg.StopReason == agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want the retry to have completed the turn", msg.StopReason)
	}
}

// A truncation that lands after content has been streamed cannot be retried —
// resending would duplicate what the user already read. It must at least say
// what happened: the failure is a dropped connection, not a configuration
// mistake, and the reader's bare "unexpected EOF" says none of that.
func TestResponsesDriverSurfacesATruncatedStreamAfterContent(t *testing.T) {
	var calls int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
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
	var failure string
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case StreamTextEvent:
			partialText = textOf(e.Partial)
		case StreamErrorEvent:
			failure = e.Message.ErrorMessage
		}
	}
	if calls != 1 {
		t.Errorf("requests = %d, want 1 (content was already streamed, so no retry)", calls)
	}
	if partialText != "half an answer" {
		t.Errorf("partial text = %q, want the streamed content kept", partialText)
	}
	if !strings.Contains(failure, "stream truncated") {
		t.Errorf("failure = %q, want it to name the truncation", failure)
	}
	if !strings.Contains(failure, "unexpected EOF") {
		t.Errorf("failure = %q, want the underlying reader error kept", failure)
	}
	if !strings.Contains(failure, "openai") {
		t.Errorf("failure = %q, want it to name the provider", failure)
	}
}

// The transport wire names the same condition: without this, a cut stream on
// the Chat or Messages wire reported the reader's bare "unexpected EOF" behind
// a generic "read error" prefix.
func TestClassifyTransportErrorNamesATruncatedStream(t *testing.T) {
	got := classifyTransportError(io.ErrUnexpectedEOF)
	if got == nil || !strings.Contains(got.Error(), "stream truncated") {
		t.Fatalf("classifyTransportError(unexpected EOF) = %v, want a truncation", got)
	}
	if !strings.Contains(got.Error(), "unexpected EOF") {
		t.Errorf("classified = %q, want the underlying error kept", got)
	}
}

// TestIsTruncatedStreamErr pins the match: the sentinel and the string, and
// nothing else — a 400 or a plain EOF is not a truncation.
func TestIsTruncatedStreamErr(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{io.ErrUnexpectedEOF, true},
		{&fallbackTestErr{"unexpected EOF"}, true},
		{&fallbackTestErr{"read error: transport: unexpected EOF"}, true},
		{io.EOF, false},
		{&fallbackTestErr{"400 invalid tools"}, false},
		{nil, false},
	} {
		if got := isTruncatedStreamErr(c.err); got != c.want {
			t.Errorf("isTruncatedStreamErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
