//go:build !windows
// +build !windows

package uv

import (
	"os"
	"testing"
	"time"
)

func TestReader(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	defer pr.Close()

	first, err := newPollReader(pr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	// Native backends cancel Poll, not an underlying syscall Read which
	// already passed its canceled check. Exercise the same Poll-then-Read
	// contract as the input loop. No input is written until polling ends.
	type pollResult struct {
		ready bool
		err   error
	}
	done := make(chan pollResult, 1)
	go func(reader pollReader) {
		ready, err := reader.Poll(-1)
		done <- pollResult{ready, err}
	}(first)

	if !first.Cancel() {
		t.Fatal("expected cancellation to be success")
	}

	select {
	case got := <-done:
		if got.ready || got.err != ErrCanceled {
			t.Fatalf("canceled poll = %t, %v; want false, ErrCanceled", got.ready, got.err)
		}
	case <-time.After(time.Second):
		// Never continue with an unfinished worker: the old test reassigned
		// its captured reader/n/err here, raced it and lost a byte to it.
		t.Fatal("expected cancellation to unblock poll")
	}
	if n, err := first.Read(make([]byte, 1)); n != 0 || err != ErrCanceled {
		t.Fatalf("read after cancel = %d, %v; want 0, ErrCanceled", n, err)
	}

	// A new reader can still use the borrowed pipe after cancellation.
	reader, err := newPollReader(pr)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	msg := "hello"
	n, err := pw.Write([]byte(msg))
	if n != 5 {
		t.Errorf("expected 5 bytes written but got %d", n)
	}
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := reader.Poll(time.Second); !ready || err != nil {
		t.Fatalf("poll after write = %t, %v; want true, nil", ready, err)
	}
	p := make([]byte, 5)
	n, err = reader.Read(p)
	if n != 5 {
		t.Errorf("expected 5 bytes written but got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, but got %s", err)
	}
	if string(p[:n]) != msg[:n] {
		t.Errorf("expected to read %q but got %q", msg[:n], string(p[:n]))
	}
}
