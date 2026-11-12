package computeruse

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestPaneOperatorActionsAndClosePause(t *testing.T) {
	ctx := context.Background()
	backend := &semanticTestBackend{}
	s := New(backend)
	if err := s.Start(ctx, Target{ID: "window", Application: "Calculator\x1b[2J", Window: "main"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	pane, err := NewPane(s, &output, 73, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pane.Render(ctx, 80, 20); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b") {
		t.Fatal("untrusted window name emitted terminal control characters")
	}
	if _, err := pane.OperatorCommand(ctx, "click @e1"); err == nil {
		t.Fatal("click before observation succeeded")
	}
	if _, err := pane.OperatorCommand(ctx, "takeover"); err != nil {
		t.Fatal(err)
	}
	if _, err := pane.OperatorCommand(ctx, "observe"); err != nil {
		t.Fatal(err)
	}
	if _, err := pane.OperatorCommand(ctx, "click @e1"); err != nil || backend.called != 1 {
		t.Fatalf("operator click failed: calls=%d err=%v", backend.called, err)
	}
	if err := pane.Close(); err != nil || !s.Status().Paused {
		t.Fatalf("closing pane did not pause: status=%+v err=%v", s.Status(), err)
	}
	if _, err := pane.OperatorCommand(ctx, "resume"); err == nil || !s.Status().Paused {
		t.Fatal("closed pane allowed the operator to resume")
	}
}

type paneBlockingBackend struct {
	semanticTestBackend
	started chan struct{}
}

func (b *paneBlockingBackend) Act(ctx context.Context, _ Target, _, _, _, _ string) error {
	close(b.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestPaneEmergencyPauseInterruptsInFlightAction(t *testing.T) {
	ctx := context.Background()
	backend := &paneBlockingBackend{started: make(chan struct{})}
	s := New(backend)
	if err := s.Start(ctx, Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	pane, err := NewPane(s, &bytes.Buffer{}, 42, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pane.OperatorCommand(ctx, "takeover"); err != nil {
		t.Fatal(err)
	}
	if _, err := pane.OperatorCommand(ctx, "observe"); err != nil {
		t.Fatal(err)
	}
	clickDone := make(chan error, 1)
	go func() {
		_, err := pane.OperatorCommand(ctx, "click @e1")
		clickDone <- err
	}()
	<-backend.started
	pauseDone := make(chan error, 1)
	go func() {
		_, err := pane.OperatorCommand(ctx, "pause")
		pauseDone <- err
	}()
	select {
	case err := <-clickDone:
		if err == nil {
			t.Fatal("in-flight action was not canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("pause waited behind the blocked action")
	}
	if err := <-pauseDone; err != nil || !s.Status().Paused {
		t.Fatalf("pause failed: status=%+v err=%v", s.Status(), err)
	}
}
