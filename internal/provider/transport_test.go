package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// jsonDecoder is a trivial Decoder: each payload is a JSON object with a "text"
// field yielding a StreamTextEvent, or {"done":true} yielding a StreamDoneEvent.
type jsonDecoder struct {
	finished bool
}

func (d *jsonDecoder) Decode(payload []byte) ([]StreamEvent, error) {
	var m struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
		Bad  bool   `json:"bad"`
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, err
	}
	if m.Bad {
		return nil, fmt.Errorf("decoder rejected payload")
	}
	if m.Done {
		return []StreamEvent{StreamDoneEvent{Message: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn}}}, nil
	}
	return []StreamEvent{StreamTextEvent{Partial: agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant}}}, nil
}

func (d *jsonDecoder) Finish() ([]StreamEvent, error) {
	d.finished = true
	return nil, nil
}

// sseServer returns an httptest server that writes the given SSE body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("server write: %v", err)
		}
	}))
}

func newReqFn(url string) func(context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	}
}

// transportStream runs a transport over a stubbed response so a test can hand it
// a body that ends the way a cut connection does, without a socket.
func transportStream(t *testing.T, ctx context.Context, body io.ReadCloser, dec Decoder) *AssistantMessageEventStream {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	})}
	stream, err := StreamRequest(ctx, TransportConfig{
		Client:     client,
		NewRequest: newReqFn("http://golder.test/stream"),
		Decoder:    dec,
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}
	return stream
}

// collectTransport drains a transport stream and returns its events and terminal
// message.
func collectTransport(t *testing.T, stream *AssistantMessageEventStream) ([]AssistantMessageEvent, agentcore.AssistantMessage) {
	t.Helper()
	var events []AssistantMessageEvent
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	msg, _ := stream.Result(context.Background())
	return events, msg
}

func transportErrorEvent(events []AssistantMessageEvent) *StreamErrorEvent {
	for i := range events {
		if se, ok := events[i].(StreamErrorEvent); ok {
			return &se
		}
	}
	return nil
}

// A transport error after the wire's own end marker is lost HTTP framing, not a
// lost answer. Each wire says "the response is complete" in its own dialect —
// Chat finish_reason, Anthropic message_delta stop_reason, Gemini finishReason —
// and the driver must finish normally instead of reporting a cut.
func TestTransportKeepsAnAnswerCompleteBeforeATransportError(t *testing.T) {
	cases := []struct {
		name string
		dec  Decoder
		body string
	}{
		{
			name: "chat finish_reason",
			dec:  NewOpenAIDecoder(),
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"the whole answer\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		},
		{
			// The last frame is complete but its blank-line terminator never
			// arrived: the buffered payload still carries the end marker.
			name: "chat unterminated final frame",
			dec:  NewOpenAIDecoder(),
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"the whole answer\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
		},
		{
			name: "anthropic stop_reason",
			dec:  NewAnthropicDecoder(),
			body: "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"the whole answer\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n",
		},
		{
			name: "gemini finishReason",
			dec:  NewGeminiDecoder(),
			body: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"the whole answer\"}]},\"finishReason\":\"STOP\"}]}\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &truncatedBody{data: []byte(tc.body)}
			stream := transportStream(t, context.Background(), body, tc.dec)
			events, msg := collectTransport(t, stream)

			if se := transportErrorEvent(events); se != nil {
				t.Fatalf("transport reported a cut over a complete answer: %v", se.Err)
			}
			if got := textOf(msg); got != "the whole answer" {
				t.Errorf("text = %q, want the complete answer", got)
			}
			if msg.StopReason == agentcore.StopReasonError {
				t.Errorf("stop reason = error, want a normal end")
			}
		})
	}
}

// The counterpart: a cut before any end marker is a real cut and must stay one.
// Losing this would turn every mid-answer drop into a silently truncated turn.
func TestTransportStillReportsATransportErrorBeforeTheEndMarker(t *testing.T) {
	body := &truncatedBody{data: []byte(
		"data: {\"choices\":[{\"delta\":{\"content\":\"half an answer\"}}]}\n\n")}
	stream := transportStream(t, context.Background(), body, NewOpenAIDecoder())
	events, msg := collectTransport(t, stream)

	se := transportErrorEvent(events)
	if se == nil {
		t.Fatal("a cut before finish_reason must still be reported")
	}
	if !isTruncatedStreamErr(se.Err) {
		t.Errorf("failure = %v, want the reader's truncation", se.Err)
	}
	// The terminal error message starts empty: the front-end keeps what already
	// streamed, which is what the text events carry.
	var streamed string
	for _, ev := range events {
		if te, ok := ev.(StreamTextEvent); ok {
			streamed = textOf(te.Partial)
		}
	}
	if streamed != "half an answer" {
		t.Errorf("streamed text = %q, want the partial answer kept", streamed)
	}
	if msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}

// [DONE] ends the Chat wire. A gateway that keeps the socket open afterwards
// must not leave the turn waiting for a close (or an idle watchdog) on an answer
// that is already complete.
func TestTransportEndsAChatStreamAtDoneMarker(t *testing.T) {
	pr, pw := io.Pipe()
	frames := "data: {\"choices\":[{\"delta\":{\"content\":\"done already\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	go func() {
		_, _ = pw.Write([]byte(frames)) // never closed: only [DONE] can end this stream
	}()
	defer pw.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := transportStream(t, ctx, pr, NewOpenAIDecoder())
	events, msg := collectTransport(t, stream)

	if se := transportErrorEvent(events); se != nil {
		t.Fatalf("stream did not end at [DONE]: %v", se.Err)
	}
	if got := textOf(msg); got != "done already" {
		t.Errorf("text = %q, want the complete answer", got)
	}
}

// TestTransportSSEParsing verifies data accumulation, blank-line flush, [DONE]
// discard, and ":" keep-alive handling.
func TestTransportSSEParsing(t *testing.T) {
	body := ": keep-alive comment\n" +
		"data: {\"text\":\"hi\"}\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n" +
		"data: {\"done\":true}\n" +
		"\n"
	srv := sseServer(t, body)
	defer srv.Close()

	dec := &jsonDecoder{}
	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    dec,
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}

	var kinds []string
	for ev := range stream.Events() {
		kinds = append(kinds, ev.EventKind())
	}
	final, resErr := stream.Result(context.Background())
	if resErr != nil {
		t.Fatalf("result error: %v", resErr)
	}
	// text (from first data) then done; [DONE] payload must be discarded.
	if len(kinds) != 2 || kinds[0] != StreamEventText || kinds[1] != StreamEventDone {
		t.Errorf("event kinds = %v, want [text done]", kinds)
	}
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("final stop reason = %q, want end_turn", final.StopReason)
	}
	if !dec.finished {
		t.Errorf("decoder Finish() was not called on clean EOF")
	}
}

// TestTransportDecodeErrorRidesStream confirms a decode failure becomes a
// terminal error event, not a returned error.
func TestTransportDecodeErrorRidesStream(t *testing.T) {
	body := "data: {\"bad\":true}\n\n"
	srv := sseServer(t, body)
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    &jsonDecoder{},
	})
	if err != nil {
		t.Fatalf("decode failure must NOT be a returned error: %v", err)
	}
	final, _ := stream.Result(context.Background())
	if final.StopReason != agentcore.StopReasonError {
		t.Errorf("expected terminal error message, got stopReason=%q", final.StopReason)
	}
}

// TestTransportEarlyBuildFailure verifies a request-build failure is a returned
// error (the only early-error case).
func TestTransportEarlyBuildFailure(t *testing.T) {
	_, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: func(ctx context.Context) (*http.Request, error) {
			return nil, fmt.Errorf("cannot build")
		},
		Decoder: &jsonDecoder{},
	})
	if err == nil {
		t.Fatal("request-build failure must return an error")
	}
}

// TestTransportMissingConfig checks required-field validation.
func TestTransportMissingConfig(t *testing.T) {
	if _, err := StreamRequest(context.Background(), TransportConfig{Decoder: &jsonDecoder{}}); err == nil {
		t.Error("missing NewRequest must error")
	}
	if _, err := StreamRequest(context.Background(), TransportConfig{NewRequest: newReqFn("http://x")}); err == nil {
		t.Error("missing Decoder must error")
	}
}

// TestTransportRetryOn503 verifies the connect retry path honors a retryable
// status and eventually succeeds without replaying a consumed stream.
func TestTransportRetryOn503(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "5s")
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"done\":true}\n\n"))
	}))
	defer srv.Close()

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    &jsonDecoder{},
	})
	if err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	final, _ := stream.Result(context.Background())
	if final.StopReason != agentcore.StopReasonEndTurn {
		t.Errorf("final stop reason = %q, want end_turn", final.StopReason)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("expected 2 attempts (1 failed + 1 ok), got %d", got)
	}
}

// TestTransportRetryExhausted verifies a persistently retryable status returns
// an early error after exhausting retries.
func TestTransportRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest:        newReqFn(srv.URL),
		Decoder:           &jsonDecoder{},
		MaxConnectRetries: 1,
	})
	if err == nil {
		t.Fatal("exhausted retries must return an error")
	}
}

// TestTransportIdleWatchdog verifies the idle watchdog fires a terminal error
// when the server stalls without sending data.
func TestTransportIdleWatchdog(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "100ms")
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-hold // never send any data
	}))
	defer srv.Close()
	defer close(hold)

	stream, err := StreamRequest(context.Background(), TransportConfig{
		NewRequest: newReqFn(srv.URL),
		Decoder:    &jsonDecoder{},
	})
	if err != nil {
		t.Fatalf("StreamRequest: %v", err)
	}

	done := make(chan agentcore.AssistantMessage, 1)
	go func() {
		final, _ := stream.Result(context.Background())
		done <- final
	}()
	select {
	case final := <-done:
		if final.StopReason != agentcore.StopReasonError {
			t.Errorf("idle watchdog must produce terminal error, got %q", final.StopReason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle watchdog did not fire")
	}
}

// TestRetryAfterParsing covers seconds and absent header parsing.
func TestRetryAfterParsing(t *testing.T) {
	h := http.Header{}
	if d := retryAfter(h); d != 0 {
		t.Errorf("absent Retry-After = %v, want 0", d)
	}
	h.Set("Retry-After", "3")
	if d := retryAfter(h); d != 3*time.Second {
		t.Errorf("Retry-After 3 = %v, want 3s", d)
	}
}
