package computeruse

import (
	"context"
	"io"
	"net"
	"testing"
)

func TestOperatorPaneProtocolRevokesOnDisconnect(t *testing.T) {
	ctx := context.Background()
	backend := &semanticTestBackend{}
	s := New(backend)
	if err := s.Start(ctx, Target{ID: "w", Application: "Calculator"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	pane, err := NewPane(s, io.Discard, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	server, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeOperatorPane(ctx, server, pane) }()
	client := NewPaneClient(clientConn)
	response, err := client.Request(ctx, PaneRequest{Action: "observe", Sequence: 1})
	if err != nil || response.Error != "" || response.Text == "" {
		t.Fatalf("observe: response=%+v err=%v", response, err)
	}
	response, err = client.Request(ctx, PaneRequest{Action: "click", Ref: "@e1", Sequence: 2})
	if err != nil || response.Error != "" || backend.called != 1 {
		t.Fatalf("click: response=%+v calls=%d err=%v", response, backend.called, err)
	}
	response, err = client.Request(ctx, PaneRequest{Action: "status", Ref: "@e1"})
	if err != nil || response.Error == "" {
		t.Fatalf("invalid arguments accepted: response=%+v err=%v", response, err)
	}
	response, err = client.Request(ctx, PaneRequest{Action: "pause", Sequence: 4})
	if err != nil || response.Error != "" || !response.Status.Paused {
		t.Fatalf("pause: response=%+v err=%v", response, err)
	}
	response, err = client.Request(ctx, PaneRequest{Action: "resume", Sequence: 3})
	if err != nil || response.Error == "" || !response.Status.Paused {
		t.Fatalf("stale resume reversed a newer pause: response=%+v err=%v", response, err)
	}
	_ = client.Close()
	if err := <-done; err != nil || !s.Status().Paused {
		t.Fatalf("disconnect did not pause: status=%+v err=%v", s.Status(), err)
	}
}

func TestOperatorPaneResumeRevalidatesTheOriginalWindow(t *testing.T) {
	target := Target{ID: "calculator"}
	backend := &settlingResumeBackend{target: target}
	s := New(backend)
	if err := s.Start(context.Background(), target, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	s.Pause()
	backend.failures = 1
	pane, err := NewPane(s, io.Discard, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	response := paneResponse(context.Background(), pane, PaneRequest{Action: "resume", Sequence: 1})
	if response.Error != "" || response.Status.Paused || response.Status.Target != target || backend.checks != 3 {
		t.Fatalf("operator pane resume: response=%+v checks=%d", response, backend.checks)
	}
}

func TestOperatorPaneProtocolUnicodeFill(t *testing.T) {
	ctx := context.Background()
	backend := &guardedSemanticBackend{element: Element{Ref: "@e1", Enabled: true, Editable: true}}
	s := New(backend)
	if err := s.Start(ctx, Target{ID: "w"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	pane, err := NewPane(s, io.Discard, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	server, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeOperatorPane(ctx, server, pane) }()
	client := NewPaneClient(clientConn)
	response, err := client.Request(ctx, PaneRequest{Action: "observe", Sequence: 1})
	if err != nil || response.Error != "" {
		t.Fatalf("observe: response=%+v err=%v", response, err)
	}
	response, err = client.Request(ctx, PaneRequest{Action: "fill", Ref: "@e1", Text: "世界  two", Sequence: 2})
	if err != nil || response.Error != "" || backend.called != 1 || backend.value != "世界  two" {
		t.Fatalf("fill: response=%+v calls=%d value=%q err=%v", response, backend.called, backend.value, err)
	}
	_ = client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
