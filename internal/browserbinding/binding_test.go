package browserbinding

import (
	"sync"
	"testing"
)

func TestBindingIsolationAndCoalescedNotification(t *testing.T) {
	a, b := New("a"), New("b")
	before := a.Snapshot()
	first := Endpoint{Socket: SocketKey("/tmp/first.sock"), BrowserID: "first"}
	second := Endpoint{Socket: SocketKey("/tmp/second.sock"), BrowserID: "second"}
	a.Select(first)
	firstState := a.Snapshot()
	a.Select(second)
	select {
	case <-before.Changed:
	default:
		t.Fatal("selection did not notify its observers")
	}
	if got := a.Snapshot(); got.Endpoint != second || got.Revision != 2 || got.Reveal != 2 {
		t.Fatalf("latest binding = %+v", got)
	}
	if got := b.Snapshot(); got.Endpoint.Socket != "" || got.Revision != 0 {
		t.Fatalf("another conversation changed: %+v", got)
	}
	a.Present(firstState)
	a.Clear(firstState)
	if got := a.Snapshot(); got.Endpoint != second || got.Revision != 2 {
		t.Fatalf("late completion changed the binding: %+v", got)
	}
}

func TestClearedBindingNeverFallsThroughToAnotherBrowser(t *testing.T) {
	b := New("conversation")
	one := Endpoint{Socket: SocketKey("/tmp/one.sock"), BrowserID: "one"}
	two := Endpoint{Socket: SocketKey("/tmp/two.sock"), BrowserID: "two"}
	b.Restore(one)
	b.Clear(b.Snapshot())
	b.Restore(two)
	if got := b.Snapshot(); got.Endpoint.Socket != "" {
		t.Fatalf("closed selection fell through to another browser: %+v", got)
	}
	b.Select(two)
	if b.Snapshot().Endpoint != two {
		t.Fatal("explicit selection was refused after clear")
	}
}

func TestBindingConcurrentReadersAndWriters(t *testing.T) {
	b := New("conversation")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				b.Select(Endpoint{Socket: "/tmp/browser.sock", BrowserID: "browser"})
				state := b.Snapshot()
				b.Present(state)
			}
		})
	}
	wg.Wait()
	if got := b.Snapshot(); got.Revision < 400 || got.Revision > 800 || got.Reveal != got.Revision {
		t.Fatalf("lost updates: %+v", got)
	}
}
