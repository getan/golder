package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// TestDefaultIdleTimeoutIsNinetySeconds pins the wait a person sees before a
// silent gateway is declared cut. Five minutes read as a hang; ninety seconds is
// short enough to retry inside a coffee sip, long enough for a slow request to
// emit its first event.
func TestDefaultIdleTimeoutIsNinetySeconds(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "")
	if got := idleTimeout(); got != 90*time.Second {
		t.Fatalf("idleTimeout() = %s, want 90s", got)
	}
}

// nopDecoder accepts each framed payload and produces nothing; the keep-alive
// test never sends a payload, so it only has to satisfy the interface.
type nopDecoder struct{}

func (nopDecoder) Decode([]byte) ([]StreamEvent, error) { return nil, nil }
func (nopDecoder) Finish() ([]StreamEvent, error)       { return nil, nil }

// sseKeepAliveBody emits SSE comments forever: bytes keep arriving, so the idle
// watchdog is satisfied, but no data frame is ever produced. This is the shape a
// gateway uses when it holds a request open without working on it.
type sseKeepAliveBody struct {
	closed chan struct{}
	once   bool
}

func newSSEKeepAliveBody() *sseKeepAliveBody {
	return &sseKeepAliveBody{closed: make(chan struct{})}
}

func (b *sseKeepAliveBody) Read(p []byte) (int, error) {
	select {
	case <-b.closed:
		return 0, io.EOF
	case <-time.After(5 * time.Millisecond):
	}
	return copy(p, ": keep-alive\n\n"), nil
}

func (b *sseKeepAliveBody) Close() error {
	if !b.once {
		b.once = true
		close(b.closed)
	}
	return nil
}

// TestTransportStallIgnoresKeepAlives pins the difference between the two
// watchdogs on the Chat/Messages/Gemini wires: keep-alive comments may reset the
// idle window (they prove the socket is alive) but must not reset the
// content-stall window (they prove nothing was produced). Resetting stall on
// every blank line let a keep-alive-happy gateway hold a turn open forever.
func TestTransportStallIgnoresKeepAlives(t *testing.T) {
	t.Setenv("GOLDER_STREAM_IDLE_TIMEOUT", "50ms")
	t.Setenv("GOLDER_STREAM_FIRST_CONTENT_TIMEOUT", "120ms")
	body := newSSEKeepAliveBody()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := NewAssistantMessageEventStream(0)
	go pump(ctx, stream, resp, nopDecoder{})

	var got error
	for ev := range stream.Events() {
		if se, ok := ev.(StreamErrorEvent); ok {
			got = se.Err
		}
	}
	if !errors.Is(got, errStreamStall) {
		t.Fatalf("watchdog error = %v, want errStreamStall (keep-alives must not reset stall)", got)
	}
	// The keep-alive-only body never produced a frame, so the first-token phase is
	// the one that expired and the sentence describes that phase.
	rendered := streamCutError("opencode-go", got).Error()
	for _, want := range []string{"no answer started", "0 content frames", "keep-alives"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("streamCutError = %q, want %q", rendered, want)
		}
	}
	if msg, _ := stream.Result(context.Background()); msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}
