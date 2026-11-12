package computeruse

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"
)

type operatorFrameTestBackend struct {
	frameTextTestBackend
	localClicks, localTypes int
}

func (b *operatorFrameTestBackend) OperatorClickFrame(context.Context, Target, string, float64, float64) error {
	b.localClicks++
	return nil
}

func (b *operatorFrameTestBackend) OperatorTypeTextFrame(_ context.Context, _ Target, _, text string) error {
	b.localTypes++
	b.text = text
	return nil
}

func TestPaneTakeoverExcludesAgentAndInvalidatesLocalLeasesOnResume(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	b := &operatorFrameTestBackend{frameTextTestBackend: frameTextTestBackend{frameBoundTestBackend: frameBoundTestBackend{png: data.Bytes()}}}
	s := New(b)
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	p, err := NewPane(s, io.Discard, 9, true)
	if err != nil {
		t.Fatal(err)
	}
	response := paneResponse(ctx, p, PaneRequest{Action: "click_at", FrameID: "fake", X: new(float64), Y: new(float64), Sequence: 1})
	if response.Error == "" || b.localClicks != 0 {
		t.Fatal("manual input allowed before takeover")
	}
	response = paneResponse(ctx, p, PaneRequest{Action: "takeover", Sequence: 2})
	if response.Error != "" || !response.Status.Paused || !response.Status.OperatorControl {
		t.Fatalf("takeover failed: %+v", response)
	}
	if _, err := s.Capture(ctx); !errors.Is(err, ErrPaused) {
		t.Fatalf("Agent capture allowed during takeover: %v", err)
	}
	local, err := s.operatorContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response = paneResponse(ctx, p, PaneRequest{Action: "frame"})
	if response.Error != "" || response.Frame == nil {
		t.Fatalf("manual preview failed: %+v", response)
	}
	x, y := 20.0, 10.0
	response = paneResponse(ctx, p, PaneRequest{Action: "click_at", FrameID: response.Frame.ID, X: &x, Y: &y, Sequence: 3})
	if response.Error != "" || b.localClicks != 1 || b.called != 0 {
		t.Fatalf("local click dispatch: %+v", response)
	}
	response = paneResponse(ctx, p, PaneRequest{Action: "frame"})
	if response.Frame == nil {
		t.Fatalf("manual frame missing: %+v", response)
	}
	frameID := response.Frame.ID
	response = paneResponse(ctx, p, PaneRequest{Action: "type_text", FrameID: frameID, Text: "人工输入🙂", Sequence: 4})
	if response.Error != "" || b.localTypes != 1 || b.text != "人工输入🙂" || strings.Contains(response.Text, "人工输入") {
		t.Fatalf("manual typing or privacy failure: %+v", response)
	}
	response = paneResponse(ctx, p, PaneRequest{Action: "resume", Sequence: 5})
	if response.Error != "" || response.Status.Paused || response.Status.OperatorControl {
		t.Fatalf("resume failed: %+v", response)
	}
	if err := s.TypeTextFrame(ctx, frameID, "old frame"); !errors.Is(err, ErrNoInput) {
		t.Fatalf("operator frame survived handoff: %v", err)
	}
	if err := s.TypeTextFrame(local, frameID, "old context"); !errors.Is(err, ErrPaused) {
		t.Fatalf("operator context survived resume: %v", err)
	}
	response = paneResponse(ctx, p, PaneRequest{Action: "type_text", FrameID: frameID, Text: "blocked", Sequence: 6})
	if response.Error == "" || b.localTypes != 1 {
		t.Fatal("pane input was allowed during Agent control")
	}
}

func TestTakeoverCancelsAgentAndWaitsForOperationLock(t *testing.T) {
	s := New(&testBackend{})
	ctx := context.Background()
	if err := s.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	agentDone := make(chan error, 1)
	go func() {
		agentDone <- s.Do(ctx, true, func(ctx context.Context, _ Backend, _ Target) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return noInputError(ctx.Err())
		})
	}()
	<-started
	if err := s.takeOver(); err != nil {
		t.Fatal(err)
	}
	<-canceled
	local, err := s.operatorContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	timeout, cancel := context.WithTimeout(local, 10*time.Millisecond)
	defer cancel()
	called := false
	err = s.Do(timeout, true, func(context.Context, Backend, Target) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("operator overlapped the Agent: called=%t err=%v", called, err)
	}
	close(release)
	if err := <-agentDone; !errors.Is(err, ErrNoInput) {
		t.Fatalf("known cancellation lost no-input state: %v", err)
	}
	s.Pause()
	if _, err := s.operatorContext(ctx); err == nil || s.Status().OperatorControl {
		t.Fatal("pause did not revoke operator control")
	}
}

func TestReadOnlySessionCannotBeTakenOver(t *testing.T) {
	s := New(&testBackend{})
	if err := s.Start(context.Background(), Target{ID: "target"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.takeOver(); !errors.Is(err, ErrNotAllowed) || s.Status().OperatorControl {
		t.Fatalf("read-only takeover: %v", err)
	}
}
