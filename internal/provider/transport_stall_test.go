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
	if !strings.Contains(streamCutError("opencode-go", got).Error(), "stopped producing") {
		t.Errorf("streamCutError = %q, want the stall named", streamCutError("opencode-go", got))
	}
	if msg, _ := stream.Result(context.Background()); msg.StopReason != agentcore.StopReasonError {
		t.Errorf("stop reason = %q, want error", msg.StopReason)
	}
}
