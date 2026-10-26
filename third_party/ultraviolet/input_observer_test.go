//go:build darwin || linux

package uv

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInputObserverMetadataDoesNotExposeInput(t *testing.T) {
	var mu sync.Mutex
	var observations []InputObservation
	restore := SetInputObserver(func(e InputObservation) {
		mu.Lock()
		defer mu.Unlock()
		observations = append(observations, e)
	})
	defer restore()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	cr, err := NewCancelReader(r)
	if err != nil {
		t.Fatal(err)
	}
	defer cr.Close()
	d := NewTerminalReader(cr, "xterm")
	events := make(chan Event, 128)
	done := make(chan error, 1)
	go func() { done <- d.StreamEvents(context.Background(), events) }()
	const private = "private fixture"
	if _, err := w.Write([]byte(private)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		cr.Cancel()
		t.Fatal("observer changed input-reader completion")
	}
	close(events)
	var text strings.Builder
	for e := range events {
		if k, ok := e.(KeyPressEvent); ok {
			text.WriteString(k.Text)
		}
	}
	if text.String() != private {
		t.Fatalf("observer changed input: %q", text.String())
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, e := range observations {
		seen[e.Phase] = true
		if strings.Contains(fmt.Sprintf("%+v", e), private) {
			t.Fatal("metadata exposed key text")
		}
	}
	for _, phase := range []string{"cancel_reader.init.begin", "reader.init.end", "poll.begin", "poll.end", "decode.end", "deliver.begin", "deliver.end", "stream.end"} {
		if !seen[phase] {
			t.Errorf("missing reader/queue phase %s", phase)
		}
	}
}
