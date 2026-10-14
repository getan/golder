package provider

// Tests for the empty-SSE-frame filter (sse_guard.go).
//
// The first test is a reproduction: it pins the failure the filter removes, so
// the fix cannot be mistaken for a workaround for something else. The control
// half of each case runs the stream WITHOUT the filter and requires it to fail.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/openai/openai-go/option"
)

// rawSSEResponse builds a 200 text/event-stream response from a RAW body, so a
// test can put bytes in it that sseResponse cannot express: sseResponse always
// writes well-formed `data: …\n\n` frames, while the bug lives in the bytes
// between frames.
func rawSSEResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// goodStream is a minimal well-formed Responses stream: one text delta and the
// authoritative completed frame.
func goodStream() string {
	return "data: " + deltaFrame("hi") + "\n\n" +
		"data: " + completedFrame("hi", "resp_1", "m", 1, 1) + "\n\n"
}

// streamOnce runs one Responses stream over body and reports the final text.
// guard selects whether the SDK's client carries the empty-frame filter.
func streamOnce(t *testing.T, body string, guard bool) (string, error) {
	t.Helper()
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return rawSSEResponse(body), nil
	})
	client := &http.Client{Transport: rt}
	if guard {
		client = guardSSEFrames(client)
	}
	d := NewOpenAIResponsesProvider("openai", "https://example.test/v1", nil)
	d.clientOpts = []option.RequestOption{option.WithHTTPClient(client)}

	stream, err := d.StreamCompletion(context.Background(), CompletionRequest{
		Model:  "m",
		Config: StreamConfig{APIKey: "k"},
	})
	if err != nil {
		return "", err
	}
	for range stream.Events() {
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	final, resErr := stream.Result(ctx)
	if resErr != nil {
		return "", resErr
	}
	if final.StopReason == agentcore.StopReasonError {
		return "", errors.New(final.ErrorMessage)
	}
	return agentcore.ContentToText(final.Content), nil
}

// TestContentlessSSEFramesAreDropped is the point of the guard.
//
// A contentless frame — a keep-alive, a leading or doubled blank line, an empty
// `data:` field — makes the SDK dispatch an event whose Data is empty, and it
// then runs json.Unmarshal on that empty input. Go's error for empty input is
// "unexpected end of JSON input", and it fails the WHOLE run: the assistant
// message ends up empty (no text, no tool calls), carrying only that parse
// error. That is the failure seen live against a Responses relay.
func TestContentlessSSEFramesAreDropped(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"leading blank line", "\n" + goodStream()},
		{"doubled separator", strings.Replace(goodStream(), "\n\n", "\n\n\n", 1)},
		{"trailing extra blank", goodStream() + "\n"},
		{"empty data field", "data:\n\n" + goodStream()},
		{"whitespace-only data field", "data: \n\n" + goodStream()},
		{"comment then blank", ": ping\n\n" + goodStream()},
		{"event field without data", "event: response.in_progress\n\n" + goodStream()},
		{"blank line between every event", strings.ReplaceAll(goodStream(), "\n\n", "\n\n\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Control: without the filter the stream must fail, with exactly the
			// error the session recorded. If this ever stops failing, the case no
			// longer reproduces anything and the test is lying about its premise.
			_, ctrlErr := streamOnce(t, c.body, false)
			if ctrlErr == nil {
				t.Fatalf("control stream succeeded; the case does not reproduce the bug")
			}
			if !strings.Contains(ctrlErr.Error(), "unexpected end of JSON input") {
				t.Fatalf("control error = %q, want the empty-input parse error", ctrlErr)
			}

			text, err := streamOnce(t, c.body, true)
			if err != nil {
				t.Fatalf("guarded stream failed: %v", err)
			}
			if text != "hi" {
				t.Fatalf("guarded stream text = %q, want %q", text, "hi")
			}
		})
	}
}

// TestMalformedSSEStillFails: the filter must not turn a genuinely broken stream
// into a silent success. A truncated JSON payload is not a contentless frame —
// it carries bytes — so it must still surface as an error.
//
// The exact parser message is deliberately not asserted. The SDK appends a line
// feed to every data value, so whether a truncated payload reports "unexpected
// end of JSON input" or a stray-newline error depends on where the cut lands;
// what must hold is that the failure reaches the caller either way.
func TestMalformedSSEStillFails(t *testing.T) {
	bodies := []string{
		`data: {"type":"response.output_text.delta","delta":"h` + "\n\n",
		"data: {\"a\":\n\n",
		"data: not json at all\n\n",
	}
	for _, body := range bodies {
		text, err := streamOnce(t, body, true)
		if err == nil {
			t.Fatalf("%q: a malformed payload was accepted as %q", body, text)
		}
	}
}

// TestEmptySSEFrameFilterKeepsContent is the other half of correctness: a frame
// that DOES carry content survives, whichever field order or spacing it uses.
func TestEmptySSEFrameFilterKeepsContent(t *testing.T) {
	bodies := []string{
		"data: {\"a\":1}\n\n",
		"event: x\ndata: {\"a\":1}\n\n",
		"data:{\"a\":1}\n\n",         // no space after the colon
		"data: {\"a\":1}\nid: 7\n\n", // another field after data
		"data: one\ndata: two\n\n",   // multi-line data
		"data: [DONE]\n\n",           // the terminator
		"data: {\"a\":1}\r\n\r\n",    // CRLF line endings
	}
	for _, body := range bodies {
		got, err := io.ReadAll(newEmptySSEFrameFilter(io.NopCloser(strings.NewReader(body))))
		if err != nil {
			t.Fatalf("%q: read: %v", body, err)
		}
		if got := string(got); !strings.Contains(got, "data:") {
			t.Fatalf("%q: a content-carrying frame was dropped; got %q", body, got)
		}
	}
}

// TestEmptySSEFrameFilterDropsOnlyEmptyFrames checks the exact bytes that
// survive, so a future change cannot start eating real separators: dropping the
// blank line BETWEEN two content-carrying frames would merge them into one
// undecodable event.
func TestEmptySSEFrameFilterDropsOnlyEmptyFrames(t *testing.T) {
	in := "\n\ndata: a\n\ndata: b\n\n\n"
	want := "data: a\n\ndata: b\n\n"
	got, err := io.ReadAll(newEmptySSEFrameFilter(io.NopCloser(strings.NewReader(in))))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != want {
		t.Fatalf("filtered = %q, want %q", got, want)
	}
}

// TestGuardSSEFramesCopiesTheClient pins that guarding never mutates the client
// it was handed: clientForURL hands out cached clients shared by every request
// in the process, so stamping the transport in place would filter unrelated
// traffic and leak the wrapper into other providers.
func TestGuardSSEFramesCopiesTheClient(t *testing.T) {
	orig := &http.Client{Timeout: 3 * time.Second}
	guarded := guardSSEFrames(orig)
	if guarded == orig {
		t.Fatal("guardSSEFrames returned the same client")
	}
	if _, isGuard := orig.Transport.(*sseGuardTransport); isGuard {
		t.Fatal("guardSSEFrames mutated the caller's client")
	}
	if _, isGuard := guarded.Transport.(*sseGuardTransport); !isGuard {
		t.Fatal("guarded client has no filter transport")
	}
	if guarded.Timeout != orig.Timeout {
		t.Fatal("guardSSEFrames dropped the client's settings")
	}
	if guardSSEFrames(nil) != nil {
		t.Fatal("guardSSEFrames(nil) should stay nil")
	}
}

// TestGuardSSEFramesLeavesOtherBodiesAlone: a JSON (non-SSE) error response must
// pass through untouched, or the driver's error reporting loses the body it
// shows the user.
func TestGuardSSEFramesLeavesOtherBodiesAlone(t *testing.T) {
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(400, `{"error":{"message":"nope"}}`), nil
	})
	client := guardSSEFrames(&http.Client{Transport: rt})
	resp, err := client.Get("https://example.test/v1/responses")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != `{"error":{"message":"nope"}}` {
		t.Fatalf("body = %q, want it untouched", body)
	}
}
